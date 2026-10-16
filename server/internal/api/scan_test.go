package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/towertail/sampler/pkg/schema"

	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/config"
	"github.com/towertail/towertail/server/internal/control"
	"github.com/towertail/towertail/server/internal/hub"
	"github.com/towertail/towertail/server/internal/scan"
	"github.com/towertail/towertail/server/internal/store"
	"github.com/towertail/towertail/server/internal/wire"
)

const progressLine = `{"t":"progress","v":1,"files":10,"dirs":2,"bytes":100,"no_access_dirs":0,"current":"/var","elapsed_ms":2000,"used_bytes":0,"top":[]}`

// scanEnv is a server with two nodes: on (allowDiskScan) and off.
type scanEnv struct {
	t          *testing.T
	srv        *Server
	user       string
	otherUser  string
	on, off    uuid.UUID
	samplerOn  string
	samplerOff string
}

func newScanEnv(t *testing.T) *scanEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	scanStore, err := scan.NewStore(st.DB())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(log)
	ctrl := control.New(st)
	issuer := auth.NewIssuer(st)
	srv := New(Deps{
		Config:  &config.Config{},
		Log:     log,
		Store:   st,
		Issuer:  issuer,
		Hub:     h,
		Control: ctrl,
		Scans:   scan.NewManager(scanStore, ctrl, h, log, nil),
	})

	e := &scanEnv{t: t, srv: srv, on: uuid.New(), off: uuid.New()}
	for _, n := range []store.Node{
		{ID: e.on, DisplayName: "on", Kind: "ssh", AllowDiskScan: true},
		{ID: e.off, DisplayName: "off", Kind: "ssh"},
	} {
		if err := st.PutNode(ctx, store.ZeroOrg, n); err != nil {
			t.Fatal(err)
		}
	}
	issue := func(org uuid.UUID, kind store.TokenKind, node *uuid.UUID) string {
		raw, _, err := issuer.Issue(ctx, org, kind, "test", node)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	e.user = issue(store.ZeroOrg, store.TokenKindUser, nil)
	e.otherUser = issue(uuid.New(), store.TokenKindUser, nil)
	e.samplerOn = issue(store.ZeroOrg, store.TokenKindSampler, &e.on)
	e.samplerOff = issue(store.ZeroOrg, store.TokenKindSampler, &e.off)
	return e
}

func (e *scanEnv) do(method, path, token string, body []byte, header ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *scanEnv) request(node uuid.UUID) wire.DiskScanJob {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/v1/nodes/"+node.String()+"/disk-scans", e.user, []byte(`{"root":"/","max_depth":6}`))
	if rec.Code != http.StatusAccepted {
		e.t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	var j wire.DiskScanJob
	decode(e.t, rec.Body.Bytes(), &j)
	return j
}

func decode(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
}

func wantCode(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("want %d, got %d: %s", code, rec.Code, rec.Body)
	}
}

func resultGzip(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../scan/testdata/scan-result.json")
	if err != nil {
		t.Fatal(err)
	}
	return gzipBytes(data)
}

func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

func TestDiskScanRequestEndpoint(t *testing.T) {
	e := newScanEnv(t)
	j := e.request(e.on)
	if j.State != "queued" || j.NodeID != e.on || j.Root != "/" || j.MaxDepth != 6 {
		t.Fatalf("job: %+v", j)
	}

	rec := e.do(http.MethodGet, "/v1/control/next", e.samplerOn, nil)
	wantCode(t, rec, http.StatusOK)
	var msg wire.ControlNextResponse
	decode(t, rec.Body.Bytes(), &msg)
	var p wire.DiskScanPayload
	decode(t, msg.Payload, &p)
	if msg.Kind != "disk_scan" || p.JobID != j.ID || p.Root != "/" || p.MaxDepth != 6 {
		t.Fatalf("control message: %+v %+v", msg, p)
	}

	rec = e.do(http.MethodPost, "/v1/nodes/"+e.on.String()+"/disk-scans", e.user, nil)
	wantCode(t, rec, http.StatusConflict)
	var env wire.APIErrorEnvelope
	decode(t, rec.Body.Bytes(), &env)
	if env.Error.Code != "conflict" || env.Job == nil || env.Job.ID != j.ID {
		t.Fatalf("409 envelope: %s", rec.Body)
	}

	rec = e.do(http.MethodPost, "/v1/nodes/"+e.off.String()+"/disk-scans", e.user, nil)
	wantCode(t, rec, http.StatusForbidden)
	decode(t, rec.Body.Bytes(), &env)
	if env.Error.Message != "disk scans are off for this node" {
		t.Fatalf("403 message: %q", env.Error.Message)
	}

	wantCode(t, e.do(http.MethodPost, "/v1/nodes/"+uuid.NewString()+"/disk-scans", e.user, nil), http.StatusNotFound)
	wantCode(t, e.do(http.MethodPost, "/v1/nodes/"+e.on.String()+"/disk-scans", e.otherUser, nil), http.StatusNotFound)
	wantCode(t, e.do(http.MethodPost, "/v1/nodes/"+e.on.String()+"/disk-scans", e.samplerOn, nil), http.StatusForbidden)
}

func TestDiskScanReadEndpoints(t *testing.T) {
	e := newScanEnv(t)
	j := e.request(e.on)

	rec := e.do(http.MethodGet, "/v1/nodes/"+e.on.String()+"/disk-scans", e.user, nil)
	wantCode(t, rec, http.StatusOK)
	var list wire.DiskScanJobList
	decode(t, rec.Body.Bytes(), &list)
	if len(list.Jobs) != 1 || list.Jobs[0].ID != j.ID {
		t.Fatalf("list: %s", rec.Body)
	}
	rec = e.do(http.MethodGet, "/v1/nodes/"+e.off.String()+"/disk-scans", e.user, nil)
	wantCode(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"jobs":[]`) {
		t.Fatalf("empty list: %s", rec.Body)
	}
	wantCode(t, e.do(http.MethodGet, "/v1/nodes/"+uuid.NewString()+"/disk-scans", e.user, nil), http.StatusNotFound)

	rec = e.do(http.MethodGet, "/v1/disk-scans/"+j.ID.String(), e.user, nil)
	wantCode(t, rec, http.StatusOK)
	var got wire.DiskScanJob
	decode(t, rec.Body.Bytes(), &got)
	if got.ID != j.ID || got.State != "queued" {
		t.Fatalf("get: %s", rec.Body)
	}
	wantCode(t, e.do(http.MethodGet, "/v1/disk-scans/"+uuid.NewString(), e.user, nil), http.StatusNotFound)
	wantCode(t, e.do(http.MethodGet, "/v1/disk-scans/"+j.ID.String(), e.otherUser, nil), http.StatusNotFound)
	wantCode(t, e.do(http.MethodGet, "/v1/disk-scans/"+j.ID.String()+"/result", e.user, nil), http.StatusNotFound)
}

func TestDiskScanCancelEndpoint(t *testing.T) {
	e := newScanEnv(t)
	j := e.request(e.on)
	path := "/v1/disk-scans/" + j.ID.String() + "/cancel"

	wantCode(t, e.do(http.MethodPost, path, e.otherUser, nil), http.StatusNotFound)
	wantCode(t, e.do(http.MethodPost, "/v1/disk-scans/"+uuid.NewString()+"/cancel", e.user, nil), http.StatusNotFound)

	rec := e.do(http.MethodPost, path, e.user, nil)
	wantCode(t, rec, http.StatusOK)
	var got wire.DiskScanJob
	decode(t, rec.Body.Bytes(), &got)
	if got.State != "cancelled" || got.FinishedTS == nil {
		t.Fatalf("cancel: %s", rec.Body)
	}
	// A final job does not change.
	rec = e.do(http.MethodPost, path, e.user, nil)
	wantCode(t, rec, http.StatusOK)
	decode(t, rec.Body.Bytes(), &got)
	if got.State != "cancelled" {
		t.Fatalf("second cancel: %s", rec.Body)
	}
	// The node is free again.
	e.request(e.on)
}

func TestDiskScanProgressEndpoint(t *testing.T) {
	e := newScanEnv(t)
	j := e.request(e.on)
	path := "/v1/disk-scans/" + j.ID.String() + "/progress"

	wantCode(t, e.do(http.MethodPost, path, e.samplerOff, []byte(progressLine)), http.StatusForbidden)
	wantCode(t, e.do(http.MethodPost, path, e.user, []byte(progressLine)), http.StatusForbidden)
	wantCode(t, e.do(http.MethodPost, "/v1/disk-scans/"+uuid.NewString()+"/progress", e.samplerOn, []byte(progressLine)), http.StatusNotFound)
	wantCode(t, e.do(http.MethodPost, path, e.samplerOn, []byte(`{"t":"result","v":1}`)), http.StatusBadRequest)
	wantCode(t, e.do(http.MethodPost, path, e.samplerOn, []byte(`not json`)), http.StatusBadRequest)
	wantCode(t, e.do(http.MethodPost, path, e.samplerOn, bytes.Repeat([]byte(" "), 2<<20)), http.StatusRequestEntityTooLarge)

	rec := e.do(http.MethodPost, path, e.samplerOn, []byte(progressLine))
	wantCode(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != `{"cancel":false}` {
		t.Fatalf("progress: %s", rec.Body)
	}
	rec = e.do(http.MethodGet, "/v1/disk-scans/"+j.ID.String(), e.user, nil)
	var got wire.DiskScanJob
	decode(t, rec.Body.Bytes(), &got)
	if got.State != "running" || got.StartedTS == nil || got.Progress == nil || got.Progress.Current != "/var" {
		t.Fatalf("job: %s", rec.Body)
	}
}

func TestDiskScanResultEndpoint(t *testing.T) {
	e := newScanEnv(t)
	j := e.request(e.on)
	path := "/v1/disk-scans/" + j.ID.String() + "/result"
	gz := resultGzip(t)
	ce := []string{"Content-Encoding", "gzip"}

	wantCode(t, e.do(http.MethodPut, path, e.samplerOff, gz, ce...), http.StatusForbidden)
	wantCode(t, e.do(http.MethodPut, "/v1/disk-scans/"+uuid.NewString()+"/result", e.samplerOn, gz, ce...), http.StatusNotFound)
	wantCode(t, e.do(http.MethodPut, path, e.samplerOn, make([]byte, scan.MaxResultBytes+1), ce...), http.StatusRequestEntityTooLarge)
	wantCode(t, e.do(http.MethodPut, path, e.samplerOn, []byte("plain"), ce...), http.StatusBadRequest)
	wantCode(t, e.do(http.MethodPut, path, e.samplerOn, gzipBytes([]byte(progressLine)), ce...), http.StatusBadRequest)
	wantCode(t, e.do(http.MethodPut, path, e.samplerOn, gzipBytes([]byte(`{"t":"result"`)), ce...), http.StatusBadRequest)

	wantCode(t, e.do(http.MethodPut, path, e.samplerOn, gz, ce...), http.StatusNoContent)
	wantCode(t, e.do(http.MethodPut, path, e.samplerOn, gz, ce...), http.StatusConflict)

	rec := e.do(http.MethodGet, path, e.user, nil, "Accept-Encoding", "gzip")
	wantCode(t, rec, http.StatusOK)
	if rec.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("headers: %v", rec.Header())
	}
	if !bytes.Equal(rec.Body.Bytes(), gz) {
		t.Fatalf("body is not the stored gzip (%d bytes, want %d)", rec.Body.Len(), len(gz))
	}
	wantCode(t, e.do(http.MethodGet, path, e.otherUser, nil), http.StatusNotFound)

	rec = e.do(http.MethodGet, "/v1/disk-scans/"+j.ID.String(), e.user, nil)
	var got wire.DiskScanJob
	decode(t, rec.Body.Bytes(), &got)
	if got.State != "done" || got.ResultBytes != int64(len(gz)) {
		t.Fatalf("job: %s", rec.Body)
	}
}

func TestUpdateNodeKeepsAllowDiskScan(t *testing.T) {
	e := newScanEnv(t)
	path := "/v1/nodes/" + e.on.String()
	rec := e.do(http.MethodPut, path, e.user, []byte(`{"displayName":"renamed","kind":"ssh"}`))
	wantCode(t, rec, http.StatusOK)
	var n store.Node
	decode(t, e.do(http.MethodGet, path, e.user, nil).Body.Bytes(), &n)
	if !n.AllowDiskScan || n.DisplayName != "renamed" {
		t.Fatalf("node: %+v", n)
	}
	rec = e.do(http.MethodPut, path, e.user, []byte(`{"displayName":"renamed","kind":"ssh","allowDiskScan":false}`))
	wantCode(t, rec, http.StatusOK)
	decode(t, e.do(http.MethodGet, path, e.user, nil).Body.Bytes(), &n)
	if n.AllowDiskScan {
		t.Fatal("allowDiskScan is still on")
	}
}

// fakeSampler acts as a sampler over real HTTP.
type fakeSampler struct {
	t     *testing.T
	base  string
	token string
}

func (s *fakeSampler) send(method, path string, body []byte, header ...string) *http.Response {
	s.t.Helper()
	req, err := http.NewRequest(method, s.base+path, bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func (s *fakeSampler) claim() wire.DiskScanPayload {
	s.t.Helper()
	res := s.send(http.MethodGet, "/v1/control/next", nil)
	if res.StatusCode != http.StatusOK {
		s.t.Fatalf("claim: %d", res.StatusCode)
	}
	var msg wire.ControlNextResponse
	if err := json.NewDecoder(res.Body).Decode(&msg); err != nil || msg.Kind != control.KindDiskScan {
		s.t.Fatalf("claim: %+v %v", msg, err)
	}
	var p wire.DiskScanPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.t.Fatal(err)
	}
	return p
}

func (s *fakeSampler) post(job uuid.UUID, line string) bool {
	s.t.Helper()
	res := s.send(http.MethodPost, "/v1/disk-scans/"+job.String()+"/progress", []byte(line))
	if res.StatusCode != http.StatusOK {
		s.t.Fatalf("progress: %d", res.StatusCode)
	}
	var r wire.DiskScanProgressResponse
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		s.t.Fatal(err)
	}
	return r.Cancel
}

func TestDiskScanEndToEnd(t *testing.T) {
	e := newScanEnv(t)
	ts := httptest.NewServer(e.srv.Handler())
	defer ts.Close()
	sampler := &fakeSampler{t: t, base: ts.URL, token: e.samplerOn}
	app := &fakeSampler{t: t, base: ts.URL, token: e.user}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(ts.URL, "http", "ws", 1)+"/v1/stream", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + e.user}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	// Wait until the hub has the client, then each update reaches it.
	if err := wsjson.Write(ctx, ws, wire.SubscribeMessage{Type: "ping"}); err != nil {
		t.Fatal(err)
	}
	var pong wire.WSMessage
	if err := wsjson.Read(ctx, ws, &pong); err != nil || pong.Type != "pong" {
		t.Fatalf("pong: %+v %v", pong, err)
	}
	nextState := func() string {
		t.Helper()
		var msg wire.WSMessage
		if err := wsjson.Read(ctx, ws, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type != "disk_scan_updated" || msg.Job == nil || *msg.NodeID != e.on {
			t.Fatalf("frame: %+v", msg)
		}
		return msg.Job.State
	}

	t.Run("done", func(t *testing.T) {
		res := app.send(http.MethodPost, "/v1/nodes/"+e.on.String()+"/disk-scans", []byte(`{"root":"/"}`))
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("request: %d", res.StatusCode)
		}
		if s := nextState(); s != "queued" {
			t.Fatalf("state %s", s)
		}
		p := sampler.claim()
		if sampler.post(p.JobID, progressLine) {
			t.Fatal("cancel on first progress")
		}
		if s := nextState(); s != "running" {
			t.Fatalf("state %s", s)
		}
		res = sampler.send(http.MethodPut, "/v1/disk-scans/"+p.JobID.String()+"/result", resultGzip(t), "Content-Encoding", "gzip")
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("result: %d", res.StatusCode)
		}
		if s := nextState(); s != "done" {
			t.Fatalf("state %s", s)
		}
		// The Go client asks for gzip and gunzips the body.
		res = app.send(http.MethodGet, "/v1/disk-scans/"+p.JobID.String()+"/result", nil)
		if res.StatusCode != http.StatusOK || !res.Uncompressed {
			t.Fatalf("result get: %d, uncompressed=%v", res.StatusCode, res.Uncompressed)
		}
		var r schema.ScanResult
		if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.T != "result" || r.Tree.B == 0 || len(r.Recs) == 0 {
			t.Fatalf("result: %+v", r)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		res := app.send(http.MethodPost, "/v1/nodes/"+e.on.String()+"/disk-scans", nil)
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("request: %d", res.StatusCode)
		}
		nextState()
		p := sampler.claim()
		sampler.post(p.JobID, progressLine)
		nextState()
		res = app.send(http.MethodPost, "/v1/disk-scans/"+p.JobID.String()+"/cancel", nil)
		var j wire.DiskScanJob
		if err := json.NewDecoder(res.Body).Decode(&j); err != nil || j.State != "running" || !j.CancelRequested {
			t.Fatalf("cancel: %+v %v", j, err)
		}
		if s := nextState(); s != "running" {
			t.Fatalf("state %s", s)
		}
		if !sampler.post(p.JobID, progressLine) {
			t.Fatal("no cancel for the sampler")
		}
		sampler.post(p.JobID, `{"t":"cancelled","v":1}`)
		if s := nextState(); s != "cancelled" {
			t.Fatalf("state %s", s)
		}
		// The sampler stops when it posts to a final job.
		if !sampler.post(p.JobID, progressLine) {
			t.Fatal("no cancel on a final job")
		}
	})
}
