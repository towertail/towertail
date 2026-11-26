package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/hub"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// streamEnv is a live server with one node, a user token and a sampler
// token. The hub keeps the legacy sample frames.
type streamEnv struct {
	t       *testing.T
	ts      *httptest.Server
	hub     *hub.Hub
	node    uuid.UUID
	user    string
	user2   string
	sampler string
}

func newStreamEnv(t *testing.T) *streamEnv {
	t.Helper()
	return newStreamEnvWith(t, hub.Options{LegacySamples: true})
}

func newStreamEnvWith(t *testing.T, o hub.Options) *streamEnv {
	t.Helper()
	return newStreamEnvCfg(t, o, "")
}

// newStreamEnvCfg sets stream.compression of the server config.
func newStreamEnvCfg(t *testing.T, o hub.Options, compression string) *streamEnv {
	t.Helper()
	te := newTestEnv(t)
	h := hub.New(slog.New(slog.DiscardHandler), o)
	srv := te.serve(Deps{
		Config: &config.Config{
			Ingest: config.IngestConfig{MaxBodyBytes: 1 << 20},
			Stream: config.StreamConfig{Compression: compression},
		},
		Hub: h,
	})
	e := &streamEnv{t: t, hub: h, node: uuid.New()}
	if err := te.st.PutNode(context.Background(), store.ZeroOrg, store.Node{ID: e.node, DisplayName: "n", Kind: "ssh", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.user = te.issue(store.ZeroOrg, store.TokenKindUser, nil)
	e.user2 = te.issue(store.ZeroOrg, store.TokenKindUser, nil)
	e.sampler = te.issue(store.ZeroOrg, store.TokenKindSampler, &e.node)
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

func (e *streamEnv) dial(query string) (*websocket.Conn, context.Context) {
	e.t.Helper()
	return e.dialAs(e.user, query)
}

func (e *streamEnv) dialAs(token, query string) (*websocket.Conn, context.Context) {
	e.t.Helper()
	ws, _, ctx := e.dialMode(token, query, websocket.CompressionDisabled)
	return ws, ctx
}

// dialMode also returns the upgrade response.
func (e *streamEnv) dialMode(token, query string, mode websocket.CompressionMode) (*websocket.Conn, *http.Response, context.Context) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	e.t.Cleanup(cancel)
	ws, res, err := websocket.Dial(ctx, strings.Replace(e.ts.URL, "http", "ws", 1)+"/v1/stream"+query, &websocket.DialOptions{
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + token}},
		CompressionMode: mode,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = ws.CloseNow() })
	return ws, res, ctx
}

// bigNode broadcasts a node_updated frame of about 20 KB.
func (e *streamEnv) bigNode() int {
	blob, _ := json.Marshal(map[string]string{"pad": strings.Repeat("abcd", 5000)})
	e.hub.BroadcastOrg(store.ZeroOrg, wire.WSMessage{Type: "node_updated", Node: blob})
	return len(blob)
}

func (e *streamEnv) ingest() {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/v1/ingest/samples", bytes.NewReader([]byte(sampleLine+"\n")))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.sampler)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		e.t.Fatalf("ingest: %d", res.StatusCode)
	}
}

func readFrame(t *testing.T, ctx context.Context, ws *websocket.Conn) wire.WSMessage {
	t.Helper()
	var m wire.WSMessage
	if err := wsjson.Read(ctx, ws, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStreamLeaseModeHello(t *testing.T) {
	e := newStreamEnv(t)
	ws, ctx := e.dial("?lease=1")
	m := readFrame(t, ctx, ws)
	if m.Type != "hello" || m.Hello == nil || !m.Hello.Lease || m.Hello.RenewS != 30 || m.Hello.MaxTTLS != 120 {
		t.Fatalf("frame: %+v", m)
	}
}

func TestStreamLegacyModeReplay(t *testing.T) {
	e := newStreamEnv(t)
	e.ingest()
	ws, ctx := e.dial("")
	m := readFrame(t, ctx, ws)
	if m.Type != "sample" || m.NodeID == nil || *m.NodeID != e.node {
		t.Fatalf("frame: %+v", m)
	}
	// No hello follows: the next frame is the answer to the ping.
	if err := wsjson.Write(ctx, ws, wire.ClientMessage{Type: "ping"}); err != nil {
		t.Fatal(err)
	}
	if m := readFrame(t, ctx, ws); m.Type != "pong" {
		t.Fatalf("frame: %+v", m)
	}
}

func TestStreamViewThenIngest(t *testing.T) {
	e := newStreamEnv(t)
	ws, ctx := e.dial("?lease=1")
	if m := readFrame(t, ctx, ws); m.Type != "hello" {
		t.Fatalf("frame: %+v", m)
	}
	if err := wsjson.Write(ctx, ws, wire.ClientMessage{Type: "view", Hosts: []uuid.UUID{e.node}, TTL: 60}); err != nil {
		t.Fatal(err)
	}
	// The ping answer shows that the hub has handled the view frame.
	if err := wsjson.Write(ctx, ws, wire.ClientMessage{Type: "ping"}); err != nil {
		t.Fatal(err)
	}
	if m := readFrame(t, ctx, ws); m.Type != "pong" {
		t.Fatalf("frame: %+v", m)
	}
	e.ingest()
	m := readFrame(t, ctx, ws)
	if m.Type != "summary" || *m.NodeID != e.node || m.Summary == nil || m.Summary.CPU != 1 || m.Sample != nil {
		t.Fatalf("frame: %+v", m)
	}
	if err := wsjson.Write(ctx, ws, wire.ClientMessage{Type: "ping"}); err != nil {
		t.Fatal(err)
	}
	if m := readFrame(t, ctx, ws); m.Type != "pong" {
		t.Fatalf("no sample frame expected, got: %+v", m)
	}
}

// sendView sends a view and waits until the hub has handled it.
func sendView(t *testing.T, ctx context.Context, ws *websocket.Conn, node uuid.UUID) {
	t.Helper()
	if err := wsjson.Write(ctx, ws, wire.ClientMessage{Type: "view", Hosts: []uuid.UUID{node}, TTL: 60}); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, ws, wire.ClientMessage{Type: "ping"}); err != nil {
		t.Fatal(err)
	}
}

func TestStreamUserKeyFallsBackToTokenID(t *testing.T) {
	e := newStreamEnvWith(t, hub.Options{MaxLeasesPerUser: 1})
	a, actx := e.dialAs(e.user, "?lease=1")
	b, bctx := e.dialAs(e.user2, "?lease=1")
	for _, x := range []struct {
		ws  *websocket.Conn
		ctx context.Context
	}{{a, actx}, {b, bctx}} {
		if m := readFrame(t, x.ctx, x.ws); m.Type != "hello" {
			t.Fatalf("frame: %+v", m)
		}
		sendView(t, x.ctx, x.ws, e.node)
		if m := readFrame(t, x.ctx, x.ws); m.Type != "pong" {
			t.Fatalf("frame: %+v", m)
		}
	}

	// Two streams of one token share one key.
	c, cctx := e.dialAs(e.user, "?lease=1")
	if m := readFrame(t, cctx, c); m.Type != "hello" {
		t.Fatalf("frame: %+v", m)
	}
	sendView(t, cctx, c, e.node)
	if m := readFrame(t, cctx, c); m.Type != "pong" {
		t.Fatalf("frame: %+v", m)
	}
	m := readFrame(t, actx, a)
	if m.Type != "lease" || m.Lease == nil || m.Lease.State != "ended" || m.Lease.Reason != "limit" {
		t.Fatalf("frame: %+v", m)
	}
}

func checkBigNode(t *testing.T, ctx context.Context, ws *websocket.Conn, size int) {
	t.Helper()
	m := readFrame(t, ctx, ws)
	if m.Type != "node_updated" || len(m.Node) != size {
		t.Fatalf("frame: %s, %d bytes, want %d", m.Type, len(m.Node), size)
	}
}

func TestStreamNegotiatesCompression(t *testing.T) {
	e := newStreamEnvCfg(t, hub.Options{}, "no_context_takeover")
	ws, res, ctx := e.dialMode(e.user, "?lease=1", websocket.CompressionNoContextTakeover)
	if h := res.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(h, "permessage-deflate") {
		t.Fatalf("extensions header %q", h)
	}
	if m := readFrame(t, ctx, ws); m.Type != "hello" {
		t.Fatalf("frame: %+v", m)
	}
	checkBigNode(t, ctx, ws, e.bigNode())
}

func TestStreamCompressionOff(t *testing.T) {
	e := newStreamEnvCfg(t, hub.Options{}, "off")
	ws, res, ctx := e.dialMode(e.user, "?lease=1", websocket.CompressionNoContextTakeover)
	if h := res.Header.Get("Sec-WebSocket-Extensions"); h != "" {
		t.Fatalf("extensions header %q", h)
	}
	if m := readFrame(t, ctx, ws); m.Type != "hello" {
		t.Fatalf("frame: %+v", m)
	}
	checkBigNode(t, ctx, ws, e.bigNode())
}

func TestStreamPlainClientWithCompressionOn(t *testing.T) {
	e := newStreamEnvCfg(t, hub.Options{}, "no_context_takeover")
	ws, res, ctx := e.dialMode(e.user, "?lease=1", websocket.CompressionDisabled)
	if h := res.Header.Get("Sec-WebSocket-Extensions"); h != "" {
		t.Fatalf("extensions header %q", h)
	}
	if m := readFrame(t, ctx, ws); m.Type != "hello" {
		t.Fatalf("frame: %+v", m)
	}
	checkBigNode(t, ctx, ws, e.bigNode())
}
