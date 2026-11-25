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
	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/hub"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

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

func (f fakeAlerts) AlertsByID(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertRow, error) {
	out := map[uuid.UUID]clickhouse.AlertRow{}
	for _, id := range ids {
		if r, ok := f.rows[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (fakeAlerts) AlertMarks(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error) {
	return nil, nil
}

type fakeJournal struct {
	mu     sync.Mutex
	lines  []notify.Line
	writes int
}

func (j *fakeJournal) Write(_ context.Context, lines ...notify.Line) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.writes++
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
	srv     *Server
	rows    map[uuid.UUID]clickhouse.AlertRow
	hub     *hub.Hub
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
	e.hub = hub.New(log, hub.Options{})
	srv := New(Deps{
		Config:  &config.Config{},
		Log:     log,
		Store:   st,
		Issuer:  issuer,
		Hub:     e.hub,
		Roles:   roles,
		Journal: e.journal,
	})
	srv.alerts = alerts
	e.srv, e.rows = srv, alerts.rows
	e.rewire(st)
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

// rewire gives the actions service the store and the fake alerts of the
// env. New built it before the test set them.
func (e *alertEnv) rewire(st store.ConfigStore) {
	e.srv.store = st
	e.srv.actions = actions.NewService(st, e.srv.alerts, e.journal, nil, e.hub, nil)
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

// pageReader gives the incidents and events of the alert page routes. The
// other methods of query.Reader panic.
type pageReader struct {
	query.Reader
	incidents []clickhouse.IncidentRow
	got       clickhouse.IncidentQuery
	events    []clickhouse.EventRow
}

func (r *pageReader) Incidents(_ context.Context, q clickhouse.IncidentQuery) ([]clickhouse.IncidentRow, bool, error) {
	r.got = q
	return r.incidents, false, nil
}

func (r *pageReader) AlertEvents(context.Context, uuid.UUID, uuid.UUID) ([]clickhouse.EventRow, error) {
	if len(r.events) == 0 {
		return nil, clickhouse.ErrNoAlert
	}
	return r.events, nil
}

func (r *pageReader) AlertLines(context.Context, uuid.UUID, uuid.UUID) ([]clickhouse.AlertLine, error) {
	return nil, nil
}

func (r *pageReader) AlertMarks(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error) {
	return nil, nil
}

func (r *pageReader) OpenAlerts(context.Context, uuid.UUID) ([]clickhouse.AlertRow, error) {
	return nil, nil
}

func (r *pageReader) RecentAlerts(context.Context, uuid.UUID, time.Time, int) ([]clickhouse.AlertRow, error) {
	return nil, nil
}

func (r *pageReader) AlertsByID(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]clickhouse.AlertRow, error) {
	return map[uuid.UUID]clickhouse.AlertRow{}, nil
}

func (e *alertEnv) withPages(r *pageReader) {
	e.srv.query = query.NewService(r, e.store, nil, nil)
}

func TestListAlertsRoute(t *testing.T) {
	e := newAlertEnv(t)
	start := time.Now().Add(-2 * time.Hour).UTC()
	r := &pageReader{incidents: []clickhouse.IncidentRow{
		{AlertID: e.cpu, NodeID: e.node, Kind: "threshold_crossed", Metric: "cpu", Tint: "critical", Severity: "critical", Start: start, LastTS: start, Value: 0.97, Peak: 0.97, Payload: `{}`},
		{AlertID: e.closed, NodeID: e.node, Kind: "threshold_crossed", Metric: "mem", Tint: "ok", Severity: "warn", Start: start, LastTS: start, End: &start, Payload: `{}`},
	}}
	e.withPages(r)

	// No state gives the old shape: the open alerts of the old reader.
	code, body := e.do(store.RoleViewer, "GET", "/v1/alerts", "")
	if code != 200 {
		t.Fatalf("old route: %d %s", code, body)
	}
	var old map[string]json.RawMessage
	if err := json.Unmarshal(body, &old); err != nil || old["counts"] != nil || old["total"] != nil {
		t.Fatalf("old route has the page shape: %s", body)
	}

	code, body = e.do(store.RoleViewer, "GET", "/v1/alerts?state=all&facets=tint,kind&bins=4&tint=critical&host="+e.node.String()+"&sort=peak&dir=asc&limit=10&from=now-48h", "")
	if code != 200 {
		t.Fatalf("page: %d %s", code, body)
	}
	var page wire.AlertPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Alerts) != 1 || page.Alerts[0].ID != e.cpu || page.Alerts[0].State != "open" || page.Alerts[0].NodeName != "web1" {
		t.Fatalf("alerts = %+v", page.Alerts)
	}
	if page.Counts.All != 1 || page.Facets == nil || page.Facets.Tint["warn"] != 1 || page.Facets.Tint["critical"] != 1 || len(page.Bins) != 4 {
		t.Fatalf("counts = %+v, facets = %+v, bins = %d", page.Counts, page.Facets, len(page.Bins))
	}
	if d := time.Since(r.got.From); d < 47*time.Hour || d > 49*time.Hour || r.got.OrgID != store.ZeroOrg {
		t.Fatalf("incident query = %+v", r.got)
	}

	for _, q := range []string{"state=x", "state=all&tint=ok", "state=all&host=nope", "state=all&bins=99", "state=all&bins=x",
		"state=all&limit=0", "state=all&dir=up", "state=all&started_from=yesterday", "state=all&from=now&to=now-1h"} {
		if code, body := e.do(store.RoleViewer, "GET", "/v1/alerts?"+q, ""); code != 400 {
			t.Errorf("%s: %d %s, want 400", q, code, body)
		}
	}
}

func TestGetAlertRoute(t *testing.T) {
	e := newAlertEnv(t)
	at := time.Now().Add(-time.Hour).UTC()
	e.withPages(&pageReader{events: []clickhouse.EventRow{
		{TS: at, NodeID: e.node, EventID: e.cpu, AlertID: e.cpu, Kind: "threshold_crossed", Metric: "cpu", Tint: "warn", Payload: `{"value":0.9,"warn":0.8,"critical":0.95}`},
	}})
	code, body := e.do(store.RoleViewer, "GET", "/v1/alerts/"+e.cpu.String(), "")
	var d wire.AlertDetail
	if err := json.Unmarshal(body, &d); code != 200 || err != nil || d.Alert.ID != e.cpu || d.Limits == nil || len(d.Timeline) != 1 || d.Timeline[0].Kind != "opened" {
		t.Fatalf("detail: %d %s", code, body)
	}
	if code, _ := e.do(store.RoleViewer, "GET", "/v1/alerts/nope", ""); code != 400 {
		t.Errorf("bad id: %d", code)
	}
	if code, _ := e.do(store.RoleViewer, "GET", "/v1/alerts/"+uuid.NewString(), ""); code != 200 {
		// The fake gives the same events for each id.
		t.Errorf("other id: %d", code)
	}
	e.withPages(&pageReader{})
	if code, _ := e.do(store.RoleViewer, "GET", "/v1/alerts/"+e.cpu.String(), ""); code != 404 {
		t.Errorf("unknown alert: %d, want 404", code)
	}
	// /v1/alerts/recent keeps its own route.
	if code, body := e.do(store.RoleViewer, "GET", "/v1/alerts/recent", ""); code != 200 {
		t.Errorf("recent: %d %s", code, body)
	}
}

// countingStore counts the rules edits.
type countingStore struct {
	store.ConfigStore
	ruleEdits int
}

func (c *countingStore) EditRules(ctx context.Context, org uuid.UUID, ed store.RulesEdit) (store.RulesBackup, error) {
	c.ruleEdits++
	return c.ConfigStore.EditRules(ctx, org, ed)
}

func TestBulkAlerts(t *testing.T) {
	e := newAlertEnv(t)
	cs := &countingStore{ConfigStore: e.srv.store}
	e.rewire(cs)
	long2, other := uuid.New(), uuid.New()
	e.rows[long2] = clickhouse.AlertRow{EventID: long2, AlertID: long2, NodeID: e.node, Kind: "long_process", Metric: "long_proc/813/1", Tint: "warn", TS: time.Now(), Payload: `{"name":"node","pid":813,"cpu_pct":90}`}
	e.rows[other] = clickhouse.AlertRow{EventID: other, AlertID: other, NodeID: e.node, Kind: "threshold_crossed", Metric: "disk", Tint: "warn", TS: time.Now(), Payload: `{"value":0.9}`}
	post := func(role store.Role, body string) (int, []bulkResult) {
		t.Helper()
		code, b := e.do(role, "POST", "/v1/alerts/bulk", body)
		var out struct{ Results []bulkResult }
		if code == 200 {
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
		}
		return code, out.Results
	}
	ids := func(l ...uuid.UUID) string {
		b, _ := json.Marshal(l)
		return string(b)
	}
	unknown := uuid.New()

	code, res := post(store.RoleMember, `{"action":"ack","ids":`+ids(e.cpu, other, unknown)+`}`)
	if code != 200 || len(res) != 3 {
		t.Fatalf("ack: %d %+v", code, res)
	}
	if res[0].Alert == nil || res[0].Alert.AckedBy == nil || res[1].Alert == nil || res[2].Alert != nil || res[2].Error != "alert not found" || res[2].ID != unknown {
		t.Fatalf("ack results = %+v", res)
	}
	if e.journal.writes != 1 || !slices.Equal(e.journal.kinds(), []notify.LineKind{notify.LineAcked, notify.LineAcked}) {
		t.Fatalf("journal: %d writes, %v", e.journal.writes, e.journal.kinds())
	}

	// A closed alert is not open; an id twice gives one result.
	code, res = post(store.RoleMember, `{"action":"unack","ids":`+ids(e.closed, e.cpu, e.cpu)+`}`)
	if code != 200 || len(res) != 2 || res[0].Error != "alert is not open" || res[1].Alert == nil || res[1].Alert.AckedBy != nil {
		t.Fatalf("unack: %d %+v", code, res)
	}

	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	code, res = post(store.RoleMember, `{"action":"snooze","until":"`+until+`","ids":`+ids(e.cpu)+`}`)
	if code != 200 || res[0].Alert == nil || res[0].Alert.SnoozedUntil == nil {
		t.Fatalf("snooze: %d %+v", code, res)
	}
	if code, _ := post(store.RoleMember, `{"action":"snooze","ids":`+ids(e.cpu)+`}`); code != 400 {
		t.Errorf("snooze with no until: %d", code)
	}
	if code, _ := post(store.RoleMember, `{"action":"unsnooze","ids":`+ids(e.cpu)+`}`); code != 200 {
		t.Errorf("unsnooze: %d", code)
	}

	many := make([]uuid.UUID, MaxBulkAlerts+1)
	for i := range many {
		many[i] = uuid.New()
	}
	for name, body := range map[string]string{
		"201 ids":    `{"action":"ack","ids":` + ids(many...) + `}`,
		"no ids":     `{"action":"ack","ids":[]}`,
		"bad action": `{"action":"nope","ids":` + ids(e.cpu) + `}`,
		"bad body":   `{`,
	} {
		if code, _ := post(store.RoleMember, body); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, _ := post(store.RoleMember, `{"action":"ack","ids":`+ids(many[:200]...)+`}`); code != 200 {
		t.Errorf("200 ids: %d", code)
	}
	if code, _ := post(store.RoleViewer, `{"action":"ack","ids":`+ids(e.cpu)+`}`); code != 403 {
		t.Errorf("ack as viewer: %d", code)
	}

	// Ignore is for an admin, and edits the rules one time.
	body := `{"action":"ignore","scope":"host","ids":` + ids(e.long, long2, e.cpu) + `}`
	if code, _ := post(store.RoleMember, body); code != 403 {
		t.Fatalf("ignore as member: %d, want 403", code)
	}
	if code, _ := post(store.RoleAdmin, `{"action":"ignore","scope":"x","ids":`+ids(e.long)+`}`); code != 400 {
		t.Errorf("ignore with a bad scope: %d", code)
	}
	before, writes := cs.ruleEdits, e.journal.writes
	code, res = post(store.RoleAdmin, body)
	if code != 200 || len(res) != 3 || res[0].Alert == nil || res[1].Alert == nil || res[2].Alert != nil || res[2].Error == "" {
		t.Fatalf("ignore: %d %+v", code, res)
	}
	if cs.ruleEdits-before != 1 || e.journal.writes-writes != 1 {
		t.Fatalf("rules edits = %d, journal writes = %d, want 1 and 1", cs.ruleEdits-before, e.journal.writes-writes)
	}
	if n := e.nodeNow(); !slices.Contains(n.IgnoredProcs, "python3") || !slices.Contains(n.IgnoredProcs, "node") || len(n.IgnoredProcs) != 2 {
		t.Fatalf("ignored = %v", n.IgnoredProcs)
	}
	if l := e.journal.lines[len(e.journal.lines)-1]; l.Kind != notify.LineClosed || l.Text != "node ignored on web1" {
		t.Fatalf("last line = %+v", l)
	}

	before = cs.ruleEdits
	code, res = post(store.RoleAdmin, `{"action":"ignore","scope":"all","ids":`+ids(e.long, long2)+`}`)
	if code != 200 || res[0].Alert == nil || cs.ruleEdits-before != 1 {
		t.Fatalf("ignore all: %d %+v, %d rules edits", code, res, cs.ruleEdits-before)
	}
	code, res = post(store.RoleAdmin, `{"action":"unignore","scope":"host","ids":`+ids(e.long, long2)+`}`)
	if code != 200 || res[0].Alert == nil || len(e.nodeNow().IgnoredProcs) != 0 {
		t.Fatalf("unignore: %d %+v", code, res)
	}
}
