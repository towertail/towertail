package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/alerter"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/hub"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

const sampleLine = `{"v":1,"ts":"2026-04-23T10:00:00.000Z","host":{"name":"x","os":"linux","arch":"amd64","kernel":"6.6","uptime_s":1,"sampler":"0.7.0+abc"},"cpu":{"pct":1,"load_1":0,"load_5":0,"load_15":0,"cores":2},"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},"errors":[]}`

type fixedInterval time.Duration

func (f fixedInterval) Interval(uuid.UUID, uuid.UUID) time.Duration { return time.Duration(f) }

type fakeLeases bool

func (f fakeLeases) Covered(uuid.UUID, uuid.UUID) bool { return bool(f) }

var streamIntervals = config.StreamConfig{LiveInterval: 5 * time.Second, IdleInterval: 30 * time.Second}

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
	raw, _, err := e.issuer.Issue(ctx, store.ZeroOrg, store.TokenKindSampler, "t", &n.ID, nil)
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

func TestIngestReplyCarriesProcCwd(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	_, on := e.node(nil)
	_, off := e.node(func(n *store.Node) { n.HideProcFolders = true })
	for tok, want := range map[string]bool{on: true, off: false} {
		rec := e.post(tok, []byte(sampleLine))
		wantCode(t, rec, http.StatusAccepted)
		if got, ok := bodyKeys(t, rec)["proc_cwd"]; !ok || got != want {
			t.Errorf("proc_cwd = %v (%v), want %v: %s", got, ok, want, rec.Body)
		}
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

// intervalOf posts one sample and gives the interval key of the reply.
func (e *ingestEnv) intervalOf(tok string) (string, bool) {
	e.t.Helper()
	rec := e.post(tok, []byte(sampleLine))
	wantCode(e.t, rec, http.StatusAccepted)
	v, ok := bodyKeys(e.t, rec)["interval"].(string)
	return v, ok
}

func TestIngestIntervalLiveWhenCovered(t *testing.T) {
	e := newIngestEnv(t, config.Config{Stream: streamIntervals}, nil, nil)
	e.srv.leases = fakeLeases(true)
	_, tok := e.node(nil)
	if got, _ := e.intervalOf(tok); got != "5s" {
		t.Fatalf("interval %q, want 5s", got)
	}
}

func TestIngestIntervalIdleWhenNotCovered(t *testing.T) {
	e := newIngestEnv(t, config.Config{Stream: streamIntervals}, nil, nil)
	e.srv.leases = fakeLeases(false)
	_, tok := e.node(nil)
	if got, _ := e.intervalOf(tok); got != "30s" {
		t.Fatalf("interval %q, want 30s", got)
	}
}

func TestIngestNoIntervalWithoutIndex(t *testing.T) {
	e := newIngestEnv(t, config.Config{Stream: streamIntervals}, nil, nil)
	_, tok := e.node(nil)
	if got, ok := e.intervalOf(tok); ok {
		t.Fatalf("interval %q, want no key", got)
	}
}

func TestIngestAdvisorWinsOverLease(t *testing.T) {
	e := newIngestEnv(t, config.Config{Stream: streamIntervals}, nil, fixedInterval(10*time.Second))
	e.srv.leases = fakeLeases(true)
	_, tok := e.node(nil)
	if got, _ := e.intervalOf(tok); got != "10s" {
		t.Fatalf("interval %q, want 10s", got)
	}
}

func TestIngestIntervalFollowsLease(t *testing.T) {
	now := time.Now()
	h := hub.New(slog.New(slog.NewTextHandler(io.Discard, nil)), hub.Options{Now: func() time.Time { return now }})
	e := newIngestEnv(t, config.Config{Stream: streamIntervals}, nil, nil)
	e.srv.leases = h
	n, tok := e.node(nil)
	c := h.Register(store.ZeroOrg, nil, hub.ClientOptions{Lease: true})
	view := func(ids ...uuid.UUID) {
		t.Helper()
		if err := h.SetView(c, wire.ClientMessage{Type: "view", Hosts: ids, TTL: 60}); err != nil {
			t.Fatal(err)
		}
	}
	want := func(w string) {
		t.Helper()
		if got, _ := e.intervalOf(tok); got != w {
			t.Fatalf("interval %q, want %s", got, w)
		}
	}

	want("30s")
	view(n.ID)
	want("5s")
	now = now.Add(61 * time.Second)
	want("30s")
	view(n.ID)
	want("5s")
	view()
	want("30s")
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
	user, _, err := e.issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindUser, "u", nil, nil)
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
	bundle, sample, err := decodeSample(line, org, node)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bundle.Sample.HostName != "x" {
		t.Errorf("host: %s", bundle.Sample.HostName)
	}
	if sample.CPU.Pct != 12.5 {
		t.Errorf("sample cpu: %v", sample.CPU.Pct)
	}
	if bundle.Sample.CPUPct != 12.5 {
		t.Errorf("cpu: %v", bundle.Sample.CPUPct)
	}
	if bundle.Sample.DiskWriteBps != 2 {
		t.Errorf("disk io: %v", bundle.Sample.DiskWriteBps)
	}
}

func TestDecodeSample_ProcFolders(t *testing.T) {
	line := []byte(`{"v":1,"ts":"2026-04-23T10:00:00.000Z",
		"host":{"name":"x","os":"linux","arch":"amd64","kernel":"6.6","uptime_s":1,"sampler":"1.5.0"},
		"cpu":{"pct":1,"load_1":0,"load_5":0,"load_15":0,"cores":2},
		"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},
		"procs":{"root":false,"top_n":2,"total":2,"visible":2,"items":[
			{"pid":10,"ppid":1,"name":"node","cpu_pct":50,"rss":5,"cwd":"/srv/app/api","repo":"/srv/app","parent":"zsh","read_bps":1024,"write_bps":0},
			{"pid":11,"name":"sh","cpu_pct":1,"rss":1}]},
		"errors":[]}`)
	b, _, err := decodeSample(line, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Procs) != 2 {
		t.Fatalf("procs = %d", len(b.Procs))
	}
	a, o := b.Procs[0], b.Procs[1]
	if a.Cwd != "/srv/app/api" || a.Repo != "/srv/app" || a.Parent != "zsh" {
		t.Errorf("folders = %q %q %q", a.Cwd, a.Repo, a.Parent)
	}
	if a.ReadBps == nil || *a.ReadBps != 1024 || a.WriteBps == nil || *a.WriteBps != 0 {
		t.Errorf("rates = %v %v", a.ReadBps, a.WriteBps)
	}
	if o.Cwd != "" || o.Repo != "" || o.Parent != "" || o.ReadBps != nil || o.WriteBps != nil {
		t.Errorf("a row without the fields has values: %+v", o)
	}
}

func TestDecodeSample_Ports(t *testing.T) {
	head := `{"v":1,"ts":"2026-04-23T10:00:05.000Z",
		"host":{"name":"x","os":"linux","arch":"amd64","kernel":"6.6","uptime_s":1,"sampler":"1.5.0"},
		"cpu":{"pct":1,"load_1":0,"load_5":0,"load_15":0,"cores":2},
		"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},`
	org, node := uuid.New(), uuid.New()

	b, _, err := decodeSample([]byte(head+`"ports":{"root":true,"collected_ts":"2026-04-23T10:00:00.000Z","items":[
			{"pid":10,"name":"nginx","user":"www","listen_tcp":[80,443],"listen_udp":[53],"est_in":3,"est_out":1},
			{"pid":11,"name":"curl","est_out":2},
			{"pid":12,"name":"odd","listen_tcp":[70000]}]},"errors":[]}`), org, node)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Ports) != 1 {
		t.Fatalf("ports = %+v, want 1 row", b.Ports)
	}
	p := b.Ports[0]
	if p.PID != 10 || p.Name != "nginx" || p.User != "www" || p.EstIn != 3 || p.EstOut != 1 ||
		!reflect.DeepEqual(p.ListenTCP, []uint16{80, 443}) || !reflect.DeepEqual(p.ListenUDP, []uint16{53}) || p.OrgID != org || p.NodeID != node {
		t.Errorf("row = %+v", p)
	}
	if want := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC); !p.TS.Equal(want) {
		t.Errorf("ts = %v, want the snapshot time %v", p.TS, want)
	}

	b, _, err = decodeSample([]byte(head+`"errors":[]}`), org, node)
	if err != nil || len(b.Ports) != 0 {
		t.Errorf("sample with no ports: %+v, %v", b.Ports, err)
	}
	// A snapshot time after the sample time is not valid.
	b, _, err = decodeSample([]byte(head+`"ports":{"collected_ts":"2026-04-23T11:00:00.000Z","items":[{"pid":1,"listen_tcp":[22]}]},"errors":[]}`), org, node)
	if err != nil || len(b.Ports) != 1 || !b.Ports[0].TS.Equal(time.Date(2026, 4, 23, 10, 0, 5, 0, time.UTC)) {
		t.Errorf("future snapshot: %+v, %v", b.Ports, err)
	}
}

func TestDecodeSample_Health(t *testing.T) {
	head := `{"v":1,"ts":"2026-04-23T10:00:00.000Z",
		"host":{"name":"x","os":"linux","arch":"amd64","kernel":"6.6","uptime_s":1,"sampler":"1.5.0"},
		"cpu":{"pct":1,"load_1":0,"load_5":0,"load_15":0,"cores":2},
		"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},`
	org, node := uuid.New(), uuid.New()

	b, _, err := decodeSample([]byte(head+`"disks":[
			{"mount":"/","fs":"ext4","used":50,"total":100,"inodes_used":10,"inodes_total":100},
			{"mount":"/data","fs":"xfs","used":90,"total":100,"inodes_used":60,"inodes_total":80},
			{"mount":"/boot","fs":"btrfs","used":1,"total":100}],
		"health":{"procs":200,"zombies":2,"pids_used":50,"pids_max":100,"files_used":7,"files_max":70,
			"psi":{"cpu_some":1.5,"mem_some":2.5,"mem_full":3.5,"io_some":4.5,"io_full":5.5},"mem_pressure_pct":0},
		"errors":[]}`), org, node)
	if err != nil {
		t.Fatal(err)
	}
	s := b.Sample
	if s.Zombies == nil || *s.Zombies != 2 || *s.PidsUsed != 50 || *s.PidsMax != 100 || *s.FilesUsed != 7 || *s.FilesMax != 70 {
		t.Errorf("counts = %v %v %v %v %v", s.Zombies, s.PidsUsed, s.PidsMax, s.FilesUsed, s.FilesMax)
	}
	if s.PSICPUSome == nil || *s.PSICPUSome != 1.5 || *s.PSIMemSome != 2.5 || *s.PSIMemFull != 3.5 || *s.PSIIOSome != 4.5 || *s.PSIIOFull != 5.5 {
		t.Errorf("psi = %v %v %v %v %v", s.PSICPUSome, s.PSIMemSome, s.PSIMemFull, s.PSIIOSome, s.PSIIOFull)
	}
	if s.MemPressurePct == nil || *s.MemPressurePct != 0 {
		t.Errorf("mem pressure = %v, want 0 (not nil)", s.MemPressurePct)
	}
	if s.DiskRatioMax == nil || *s.DiskRatioMax != 0.9 || s.InodesRatioMax == nil || *s.InodesRatioMax != 0.75 {
		t.Errorf("worst mount = %v %v, want 0.9 and 0.75", s.DiskRatioMax, s.InodesRatioMax)
	}
	if len(b.Disks) != 3 || b.Disks[0].InodesUsed == nil || *b.Disks[0].InodesUsed != 10 || *b.Disks[0].InodesTotal != 100 || b.Disks[2].InodesUsed != nil || b.Disks[2].InodesTotal != nil {
		t.Errorf("disk inodes = %+v", b.Disks)
	}

	// A Mac sample has a pressure and no PSI. No disks gives no ratios.
	b, _, err = decodeSample([]byte(head+`"health":{"procs":300,"mem_pressure_pct":42},"errors":[]}`), org, node)
	if err != nil {
		t.Fatal(err)
	}
	s = b.Sample
	if s.MemPressurePct == nil || *s.MemPressurePct != 42 || s.PSICPUSome != nil || s.PSIIOFull != nil || s.Zombies != nil || s.PidsUsed != nil ||
		s.DiskRatioMax != nil || s.InodesRatioMax != nil {
		t.Errorf("mac sample = %+v", s)
	}

	// A sample with no health, and disks with no inodes.
	b, _, err = decodeSample([]byte(head+`"disks":[{"mount":"/","fs":"ntfs","used":25,"total":100}],"errors":[]}`), org, node)
	if err != nil {
		t.Fatal(err)
	}
	s = b.Sample
	if s.Zombies != nil || s.MemPressurePct != nil || s.InodesRatioMax != nil || s.DiskRatioMax == nil || *s.DiskRatioMax != 0.25 {
		t.Errorf("no health = %+v", s)
	}
}

func TestDecodeSample_RejectsWithValues(t *testing.T) {
	const tail = `"host":{"name":"x"},"cpu":{},"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},"errors":[]}`
	tests := []struct {
		name string
		line string
		want string
	}{
		{"unknown version", `{"v":99,"ts":"2026-04-23T10:00:00Z",` + tail, "unsupported schema v=99"},
		{"bad ts", `{"v":1,"ts":"nope",` + tail, `invalid ts: "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := decodeSample([]byte(tt.line), uuid.New(), uuid.New())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestIngestPassesHealthToAlerter(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil, nil)
	rs := &registeredStore{}
	e.srv.alerter = alerter.New(nil, nil, nil, rs, e.st)
	s := store.DefaultServerSettings()
	s.Alerts.Health = store.AlertRule{Notify: store.NotifyAll}
	if err := e.st.PutSettings(context.Background(), store.ZeroOrg, s); err != nil {
		t.Fatal(err)
	}
	_, tok := e.node(nil)

	withHealth := func(ts, health string) string {
		line := strings.Replace(sampleLine, "2026-04-23T10:00:00.000Z", ts, 1)
		return strings.Replace(line, `"errors":[]`, `"errors":[],"health":`+health, 1)
	}
	body := withHealth("2026-04-23T10:00:00.000Z", `{"procs":10,"zombies":0}`) + "\n" +
		withHealth("2026-04-23T10:00:05.000Z", `{"procs":10,"zombies":5000}`) + "\n"
	wantCode(t, e.post(tok, []byte(body)), http.StatusAccepted)

	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.events) != 1 {
		t.Fatalf("events = %+v", rs.events)
	}
	ev := rs.events[0]
	if ev.Metric != "health" || ev.Tint != "critical" {
		t.Fatalf("event = %+v", ev)
	}
}
