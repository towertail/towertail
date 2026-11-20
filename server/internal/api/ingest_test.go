package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

const sampleLine = `{"v":1,"ts":"2026-04-23T10:00:00.000Z","host":{"name":"x","os":"linux","arch":"amd64","kernel":"6.6","uptime_s":1,"sampler":"0.7.0+abc"},"cpu":{"pct":1,"load_1":0,"load_5":0,"load_15":0,"cores":2},"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},"errors":[]}`

type fixedInterval time.Duration

func (f fixedInterval) Interval(uuid.UUID, uuid.UUID) time.Duration { return time.Duration(f) }

// ingestEnv is a server with Bolt, no ClickHouse and real sampler tokens.
type ingestEnv struct {
	t      *testing.T
	srv    *Server
	st     *store.BoltStore
	issuer *auth.TokenIssuer
}

func newIngestEnv(t *testing.T, cfg config.Config, limits Limits, interval IntervalAdvisor) *ingestEnv {
	t.Helper()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	issuer := auth.NewIssuer(st)
	cfg.Ingest.MaxBodyBytes = 1 << 20
	d := Deps{
		Config:   &cfg,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:    st,
		Issuer:   issuer,
		Interval: interval,
	}
	if limits != nil {
		d.Limits = limits
	}
	return &ingestEnv{t: t, srv: New(d), st: st, issuer: issuer}
}

// node makes an enabled node and a sampler token bound to it.
func (e *ingestEnv) node(mod func(*store.Node)) (store.Node, string) {
	e.t.Helper()
	ctx := context.Background()
	n := store.Node{ID: uuid.New(), DisplayName: "n", Kind: "ssh", Enabled: true}
	if mod != nil {
		mod(&n)
	}
	if err := e.st.PutNode(ctx, store.ZeroOrg, n); err != nil {
		e.t.Fatal(err)
	}
	raw, _, err := e.issuer.Issue(ctx, store.ZeroOrg, store.TokenKindSampler, "t", &n.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // distinct CreatedAt
	return n, raw
}

func (e *ingestEnv) post(token string, body []byte, header ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest/samples", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func bodyKeys(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	decode(t, rec.Body.Bytes(), &m)
	return m
}

func TestIngest_AcceptedBody(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	_, tok := e.node(nil)
	rec := e.post(tok, []byte(sampleLine+"\n"))
	wantCode(t, rec, http.StatusAccepted)
	m := bodyKeys(t, rec)
	if m["accepted"] != float64(1) || m["rejected"] != float64(0) {
		t.Fatalf("body: %s", rec.Body)
	}
	for _, k := range []string{"interval", "update", "error"} {
		if _, ok := m[k]; ok {
			t.Errorf("key %q in %s", k, rec.Body)
		}
	}
}

func TestIngest_GoneHostRemoved(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	n, tok := e.node(nil)
	if err := e.st.DeleteNode(context.Background(), store.ZeroOrg, n.ID); err != nil {
		t.Fatal(err)
	}
	rec := e.post(tok, []byte(sampleLine))
	wantCode(t, rec, http.StatusGone)
	var g wire.IngestGone
	decode(t, rec.Body.Bytes(), &g)
	if g.Action != "exit" || g.Reason != wire.GoneHostRemoved {
		t.Fatalf("body: %s", rec.Body)
	}
}

func TestIngest_GoneCloudOff(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	_, tok := e.node(func(n *store.Node) { n.Enabled = false })
	rec := e.post(tok, []byte(sampleLine))
	wantCode(t, rec, http.StatusGone)
	var g wire.IngestGone
	decode(t, rec.Body.Bytes(), &g)
	if g.Action != "exit" || g.Reason != wire.GoneCloudOff {
		t.Fatalf("body: %s", rec.Body)
	}
}

func TestIngest_PlanFull(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, maxNodes(1), nil)
	_, first := e.node(func(n *store.Node) { n.DisplayName = "first" })
	_, second := e.node(func(n *store.Node) { n.DisplayName = "second" })
	wantCode(t, e.post(first, []byte(sampleLine)), http.StatusAccepted)
	rec := e.post(second, []byte(sampleLine))
	wantCode(t, rec, http.StatusPaymentRequired)
	var p wire.IngestPlanFull
	decode(t, rec.Body.Bytes(), &p)
	if p.RetryAfter != 3600 || rec.Header().Get("Retry-After") != "3600" {
		t.Fatalf("body: %s, header %q", rec.Body, rec.Header().Get("Retry-After"))
	}
}

func TestIngest_NoLimits(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	for range 3 {
		_, tok := e.node(nil)
		wantCode(t, e.post(tok, []byte(sampleLine)), http.StatusAccepted)
	}
}

func TestIngest_IntervalField(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, fixedInterval(5*time.Second))
	_, tok := e.node(nil)
	rec := e.post(tok, []byte(sampleLine))
	wantCode(t, rec, http.StatusAccepted)
	if bodyKeys(t, rec)["interval"] != "5s" {
		t.Fatalf("body: %s", rec.Body)
	}
}

func TestIngest_UpdateField(t *testing.T) {
	cfg := config.Config{Sampler: config.SamplerConfig{LatestVersion: "0.7.1"}}
	e := newIngestEnv(t, cfg, nil, nil)
	_, tok := e.node(nil)
	for _, c := range []struct {
		ua   string
		want string
	}{
		{"towertail-sampler/0.7.0", "0.7.1"},
		{"towertail-sampler/0.6.9 (linux)", "0.7.1"},
		{"towertail-sampler/0.7.1", ""},
		{"towertail-sampler/0.8.0", ""},
		{"towertail-sampler/dev", ""},
		{"curl/8", ""},
		{"", ""},
	} {
		rec := e.post(tok, []byte(sampleLine), "User-Agent", c.ua)
		wantCode(t, rec, http.StatusAccepted)
		got, _ := bodyKeys(t, rec)["update"].(string)
		if got != c.want {
			t.Errorf("ua %q: update %q, want %q", c.ua, got, c.want)
		}
	}
}

func TestIngest_GateSkipsBody(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	_, tok := e.node(func(n *store.Node) { n.Enabled = false })
	rec := e.post(tok, []byte("not gzip"), "Content-Encoding", "gzip")
	wantCode(t, rec, http.StatusGone)
}

func TestDeleteNodeKeepsSamplerToken(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	n, tok := e.node(nil)
	user, _, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindUser, "u", nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/nodes/"+n.ID.String(), strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+user)
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	wantCode(t, rec, http.StatusNoContent)
	wantCode(t, e.post(tok, []byte(sampleLine)), http.StatusGone)
}

func TestDecodeSample(t *testing.T) {
	line := []byte(`{
		"v": 1,
		"ts": "2026-04-23T10:00:00.000Z",
		"host": {"name":"x","os":"linux","arch":"amd64","kernel":"6.6","uptime_s":1,"sampler":"0.1.0"},
		"cpu": {"pct":12.5,"load_1":0.1,"load_5":0.2,"load_15":0.3,"cores":4},
		"mem": {"used":1,"total":100},
		"swap": {"used":0,"total":0},
		"disk_io": {"read_bps":1,"write_bps":2,"read_cum":10,"write_cum":20},
		"errors": []
	}`)
	org, node := uuid.New(), uuid.New()
	bundle, err := decodeSample(line, org, node)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bundle.Sample.HostName != "x" {
		t.Errorf("host: %s", bundle.Sample.HostName)
	}
	if bundle.Sample.CPUPct != 12.5 {
		t.Errorf("cpu: %v", bundle.Sample.CPUPct)
	}
	if bundle.Sample.DiskWriteBps != 2 {
		t.Errorf("disk io: %v", bundle.Sample.DiskWriteBps)
	}
}

func TestDecodeSample_RejectsUnknownVersion(t *testing.T) {
	line := []byte(`{"v":99,"ts":"2026-04-23T10:00:00Z","host":{"name":"x"},"cpu":{},"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},"errors":[]}`)
	if _, err := decodeSample(line, uuid.New(), uuid.New()); err == nil {
		t.Error("expected error")
	}
}
