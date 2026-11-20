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

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/hub"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// streamEnv is a live server with one node, a user token and a sampler
// token. The hub keeps the legacy sample frames.
type streamEnv struct {
	t       *testing.T
	ts      *httptest.Server
	node    uuid.UUID
	user    string
	sampler string
}

func newStreamEnv(t *testing.T) *streamEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuer := auth.NewIssuer(st)
	srv := New(Deps{
		Config: &config.Config{Ingest: config.IngestConfig{MaxBodyBytes: 1 << 20}},
		Log:    log,
		Store:  st,
		Issuer: issuer,
		Hub:    hub.New(log, hub.Options{LegacySamples: true}),
	})
	e := &streamEnv{t: t, node: uuid.New()}
	if err := st.PutNode(ctx, store.ZeroOrg, store.Node{ID: e.node, DisplayName: "n", Kind: "ssh", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	issue := func(kind store.TokenKind, node *uuid.UUID) string {
		raw, _, err := issuer.Issue(ctx, store.ZeroOrg, kind, "test", node)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	e.user = issue(store.TokenKindUser, nil)
	e.sampler = issue(store.TokenKindSampler, &e.node)
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

func (e *streamEnv) dial(query string) (*websocket.Conn, context.Context) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	e.t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, strings.Replace(e.ts.URL, "http", "ws", 1)+"/v1/stream"+query, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + e.user}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = ws.CloseNow() })
	return ws, ctx
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
