package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/hub"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

func TestAckedBy(t *testing.T) {
	user := uuid.New()
	withUser := store.Token{ID: uuid.New(), UserID: &user}
	if got := ackedBy(withUser); got != user {
		t.Errorf("token with a user: %s, want %s", got, user)
	}
	plain := store.Token{ID: uuid.New()}
	if got := ackedBy(plain); got != plain.ID {
		t.Errorf("token with no user: %s, want %s", got, plain.ID)
	}
}

type fakeAlerts struct {
	rows map[uuid.UUID]clickhouse.AlertRow
}

func (fakeAlerts) Ready() bool { return true }

func (f fakeAlerts) Alert(_ context.Context, _, id uuid.UUID) (clickhouse.AlertRow, error) {
	if r, ok := f.rows[id]; ok {
		return r, nil
	}
	return clickhouse.AlertRow{}, clickhouse.ErrNoAlert
}

func (fakeAlerts) AlertMarks(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error) {
	return nil, nil
}

type fakeJournal struct {
	mu    sync.Mutex
	lines []notify.Line
}

func (j *fakeJournal) Write(_ context.Context, lines ...notify.Line) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, lines...)
	return nil
}

func (j *fakeJournal) kinds() []notify.LineKind {
	j.mu.Lock()
	defer j.mu.Unlock()
	var k []notify.LineKind
	for _, l := range j.lines {
		k = append(k, l.Kind)
	}
	return k
}

type mapRoles map[uuid.UUID]store.Role

func (m mapRoles) Role(_ context.Context, _, u uuid.UUID) (store.Role, error) { return m[u], nil }

// alertEnv is a live server with one host, a threshold alert, a
// long_process alert and one user for each role.
type alertEnv struct {
	t       *testing.T
	ts      *httptest.Server
	store   store.ConfigStore
	journal *fakeJournal
	node    uuid.UUID
	cpu     uuid.UUID
	long    uuid.UUID
	closed  uuid.UUID
	tokens  map[store.Role]string
}

func newAlertEnv(t *testing.T) *alertEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuer := auth.NewIssuer(st)
	e := &alertEnv{t: t, store: st, journal: &fakeJournal{}, node: uuid.New(), cpu: uuid.New(), long: uuid.New(), closed: uuid.New(), tokens: map[store.Role]string{}}
	if err := st.PutNode(ctx, store.ZeroOrg, store.Node{ID: e.node, DisplayName: "web1", Kind: "ssh", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	row := func(id uuid.UUID, kind, metric, tint, payload string) clickhouse.AlertRow {
		return clickhouse.AlertRow{EventID: id, AlertID: id, NodeID: e.node, Kind: kind, Metric: metric, Tint: tint, TS: time.Now(), Payload: payload}
	}
	alerts := fakeAlerts{rows: map[uuid.UUID]clickhouse.AlertRow{
		e.cpu:    row(e.cpu, "threshold_crossed", "cpu", "warn", `{"value":0.9}`),
		e.long:   row(e.long, "long_process", "long_proc/812/1", "warn", `{"name":"python3","pid":812,"cpu_pct":90}`),
		e.closed: row(e.closed, "threshold_crossed", "mem", "ok", `{"value":0.1}`),
	}}
	roles := mapRoles{}
	for _, r := range []store.Role{store.RoleViewer, store.RoleMember, store.RoleAdmin} {
		u := uuid.New()
		roles[u] = r
		raw, _, err := issuer.Issue(ctx, store.ZeroOrg, store.TokenKindUser, "test", nil, &u)
		if err != nil {
			t.Fatal(err)
		}
		e.tokens[r] = raw
	}
	srv := New(Deps{
		Config:  &config.Config{},
		Log:     log,
		Store:   st,
		Issuer:  issuer,
		Hub:     hub.New(log, hub.Options{}),
		Roles:   roles,
		Journal: e.journal,
	})
	srv.alerts = alerts
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

func (e *alertEnv) do(role store.Role, method, path, body string) (int, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.tokens[role])
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func (e *alertEnv) nodeNow() store.Node {
	e.t.Helper()
	n, err := e.store.GetNode(context.Background(), store.ZeroOrg, e.node)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *alertEnv) settings() store.ServerSettings {
	e.t.Helper()
	s, err := e.store.GetSettings(context.Background(), store.ZeroOrg)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func TestAlertActionRoutes(t *testing.T) {
	e := newAlertEnv(t)
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(e.ts.URL, "http", "ws", 1)+"/v1/stream", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + e.tokens[store.RoleViewer]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	cpu, long, closed, node := "/v1/alerts/"+e.cpu.String(), "/v1/alerts/"+e.long.String(), "/v1/alerts/"+e.closed.String(), "/v1/nodes/"+e.node.String()
	member, admin, viewer := store.RoleMember, store.RoleAdmin, store.RoleViewer

	for _, tc := range []struct {
		name       string
		role       store.Role
		method     string
		path, body string
		want       int
		frame      string
		check      func(t *testing.T, body []byte)
	}{
		{name: "ack as viewer", role: viewer, method: "POST", path: cpu + "/ack", want: 403},
		{name: "ack", role: member, method: "POST", path: cpu + "/ack", want: 200, frame: "alert_updated", check: func(t *testing.T, b []byte) {
			var a wire.Alert
			if err := json.Unmarshal(b, &a); err != nil || a.ID != e.cpu || a.AckedBy == nil {
				t.Fatalf("alert = %s", b)
			}
		}},
		{name: "ack closed alert", role: member, method: "POST", path: closed + "/ack", want: 404},
		{name: "ack unknown alert", role: member, method: "POST", path: "/v1/alerts/" + uuid.NewString() + "/ack", want: 404},
		{name: "unack", role: member, method: "DELETE", path: cpu + "/ack", want: 200, frame: "alert_updated", check: func(t *testing.T, b []byte) {
			var a wire.Alert
			if err := json.Unmarshal(b, &a); err != nil || a.AckedBy != nil {
				t.Fatalf("alert = %s", b)
			}
		}},
		{name: "snooze without until", role: member, method: "POST", path: cpu + "/snooze", want: 400},
		{name: "snooze in the past", role: member, method: "POST", path: cpu + "/snooze?until=" + past, want: 400},
		{name: "snooze", role: member, method: "POST", path: cpu + "/snooze?until=" + until, want: 200, frame: "alert_updated", check: func(t *testing.T, b []byte) {
			var a wire.Alert
			if err := json.Unmarshal(b, &a); err != nil || a.SnoozedUntil == nil {
				t.Fatalf("alert = %s", b)
			}
		}},
		{name: "unsnooze", role: member, method: "DELETE", path: cpu + "/snooze", want: 200, frame: "alert_updated"},
		{name: "ignore as member", role: member, method: "POST", path: long + "/ignore?scope=host", want: 403},
		{name: "ignore bad scope", role: admin, method: "POST", path: long + "/ignore?scope=x", want: 400},
		{name: "ignore host", role: admin, method: "POST", path: long + "/ignore?scope=host", want: 200, frame: "alert_updated", check: func(t *testing.T, _ []byte) {
			if n := e.nodeNow(); !slices.Contains(n.IgnoredProcs, "python3") {
				t.Fatalf("ignored = %v", n.IgnoredProcs)
			}
		}},
		{name: "unignore host", role: admin, method: "DELETE", path: long + "/ignore?scope=host", want: 200, frame: "alert_updated", check: func(t *testing.T, _ []byte) {
			if n := e.nodeNow(); len(n.IgnoredProcs) != 0 {
				t.Fatalf("ignored = %v", n.IgnoredProcs)
			}
		}},
		{name: "ignore all", role: admin, method: "POST", path: long + "/ignore?scope=all", want: 200, frame: "alert_updated", check: func(t *testing.T, _ []byte) {
			if s := e.settings(); !slices.Contains(s.LongProc.Ignore, "python3") {
				t.Fatalf("ignore = %v", s.LongProc.Ignore)
			}
		}},
		{name: "unignore all", role: admin, method: "DELETE", path: long + "/ignore?scope=all", want: 200, frame: "alert_updated", check: func(t *testing.T, _ []byte) {
			if s := e.settings(); len(s.LongProc.Ignore) != 0 {
				t.Fatalf("ignore = %v", s.LongProc.Ignore)
			}
		}},
		{name: "mute host as viewer", role: viewer, method: "PUT", path: "/v1/mutes", body: `{"metric":"cpu","nodeId":"` + e.node.String() + `","muted":true}`, want: 403},
		{name: "mute host", role: member, method: "PUT", path: "/v1/mutes", body: `{"metric":"cpu","nodeId":"` + e.node.String() + `","muted":true}`, want: 200, frame: "node_updated", check: func(t *testing.T, _ []byte) {
			if n := e.nodeNow(); !n.MutedSignals.Has(store.MetricCPU) {
				t.Fatalf("muted = %v", n.MutedSignals)
			}
		}},
		{name: "unmute host", role: member, method: "PUT", path: "/v1/mutes", body: `{"metric":"cpu","nodeId":"` + e.node.String() + `","muted":false}`, want: 200, frame: "node_updated", check: func(t *testing.T, _ []byte) {
			if n := e.nodeNow(); n.MutedSignals.Has(store.MetricCPU) {
				t.Fatalf("muted = %v", n.MutedSignals)
			}
		}},
		{name: "mute org as member", role: member, method: "PUT", path: "/v1/mutes", body: `{"metric":"mem","muted":true}`, want: 403},
		{name: "mute org", role: admin, method: "PUT", path: "/v1/mutes", body: `{"metric":"mem","muted":true}`, want: 200, frame: "settings_updated", check: func(t *testing.T, _ []byte) {
			if s := e.settings(); !s.Muted.Has(store.MetricMem) {
				t.Fatalf("muted = %v", s.Muted)
			}
		}},
		{name: "unmute org", role: admin, method: "PUT", path: "/v1/mutes", body: `{"metric":"mem","muted":false}`, want: 200, frame: "settings_updated", check: func(t *testing.T, _ []byte) {
			if s := e.settings(); s.Muted.Has(store.MetricMem) {
				t.Fatalf("muted = %v", s.Muted)
			}
		}},
		{name: "mute unknown metric", role: admin, method: "PUT", path: "/v1/mutes", body: `{"metric":"gpu","muted":true}`, want: 400},
		{name: "mute unknown host", role: member, method: "PUT", path: "/v1/mutes", body: `{"metric":"cpu","nodeId":"` + uuid.NewString() + `","muted":true}`, want: 404},
		{name: "snooze host in the past", role: member, method: "POST", path: node + "/snooze?until=" + past, want: 400},
		{name: "snooze host", role: member, method: "POST", path: node + "/snooze?until=" + until, want: 200, frame: "node_updated", check: func(t *testing.T, _ []byte) {
			if n := e.nodeNow(); n.SnoozedUntil == nil {
				t.Fatal("no snooze")
			}
		}},
		{name: "unsnooze host", role: member, method: "DELETE", path: node + "/snooze", want: 200, frame: "node_updated", check: func(t *testing.T, _ []byte) {
			if n := e.nodeNow(); n.SnoozedUntil != nil {
				t.Fatal("snooze kept")
			}
		}},
		{name: "snooze unknown host", role: member, method: "DELETE", path: "/v1/nodes/" + uuid.NewString() + "/snooze", want: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := e.do(tc.role, tc.method, tc.path, tc.body)
			if code != tc.want {
				t.Fatalf("status = %d, want %d: %s", code, tc.want, body)
			}
			if tc.check != nil {
				tc.check(t, body)
			}
			for tc.frame != "" {
				var m wire.WSMessage
				if err := json.Unmarshal(readRaw(t, ctx, ws), &m); err != nil {
					t.Fatal(err)
				}
				if m.Type == tc.frame {
					if m.Type == "alert_updated" && (m.Alert == nil || m.NodeID == nil || *m.NodeID != e.node) {
						t.Fatalf("frame = %+v", m)
					}
					break
				}
			}
		})
	}

	want := []notify.LineKind{notify.LineAcked, notify.LineUnacked, notify.LineSnoozed, notify.LineUnsnoozed, notify.LineClosed}
	got := e.journal.kinds()
	if !slices.Equal(got[:len(want)], want) {
		t.Fatalf("journal = %v, want a start of %v", got, want)
	}
	if l := e.journal.lines[4]; l.Text != "python3 ignored on web1" {
		t.Fatalf("closed line = %q", l.Text)
	}
	if l := e.journal.lines[5]; l.Text != "python3 ignored on all hosts" {
		t.Fatalf("closed line = %q", l.Text)
	}
}

func readRaw(t *testing.T, ctx context.Context, ws *websocket.Conn) []byte {
	t.Helper()
	_, b, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIgnoreAlertWrongKind(t *testing.T) {
	e := newAlertEnv(t)
	if code, b := e.do(store.RoleAdmin, "POST", "/v1/alerts/"+e.cpu.String()+"/ignore?scope=host", ""); code != http.StatusBadRequest {
		t.Fatalf("ignore on a threshold alert: %d %s", code, b)
	}
}

func TestUpdateNodeKeepsMutedSignals(t *testing.T) {
	e := newAlertEnv(t)
	body := `{"metric":"disk","nodeId":"` + e.node.String() + `","muted":true}`
	if code, b := e.do(store.RoleMember, "PUT", "/v1/mutes", body); code != http.StatusOK {
		t.Fatalf("mute: %d %s", code, b)
	}
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String(), `{"displayName":"web2"}`); code != http.StatusOK {
		t.Fatalf("update node: %d %s", code, b)
	}
	if n := e.nodeNow(); n.DisplayName != "web2" || !n.MutedSignals.Has(store.MetricDisk) {
		t.Fatalf("node = %+v", n)
	}
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String(), `{"displayName":"web2","mutedSignals":[]}`); code != http.StatusOK {
		t.Fatalf("update node: %d %s", code, b)
	}
	if n := e.nodeNow(); n.MutedSignals.Has(store.MetricDisk) {
		t.Fatalf("an explicit empty list must clear: %v", n.MutedSignals)
	}

	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/mutes", `{"metric":"mem","muted":true}`); code != http.StatusOK {
		t.Fatalf("mute org: %d %s", code, b)
	}
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/settings", `{"name":"x"}`); code != http.StatusOK {
		t.Fatalf("put settings: %d %s", code, b)
	}
	if s := e.settings(); !s.Muted.Has(store.MetricMem) {
		t.Fatalf("settings lost the mute: %v", s.Muted)
	}
}
