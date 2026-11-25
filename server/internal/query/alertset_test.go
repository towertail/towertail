package query

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

var (
	hostA, hostB = uuid.MustParse("00000000-0000-0000-0000-00000000000a"), uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	user         = uuid.MustParse("00000000-0000-0000-0000-0000000000f1")
)

type alertOpt func(*wire.Alert)

// mkAlert builds an alert that started h hours before now.
func mkAlert(host uuid.UUID, name, kind, metric string, sev wire.Tint, h float64, opts ...alertOpt) wire.Alert {
	start := now.Add(-time.Duration(h * float64(time.Hour)))
	a := wire.Alert{
		ID: uuid.New(), NodeID: host, NodeName: name, Kind: kind, Metric: metric,
		Tint: sev, Severity: sev, Start: &start, State: alertOpen,
	}
	for _, o := range opts {
		o(&a)
	}
	return a
}

func ended(h float64) alertOpt {
	return func(a *wire.Alert) {
		end := now.Add(-time.Duration(h * float64(time.Hour)))
		a.End, a.State = &end, alertResolved
	}
}
func acked(a *wire.Alert) { a.AckedBy, a.State = &user, alertAcked }
func proc(name, cmd string) alertOpt {
	return func(a *wire.Alert) { a.Proc = &wire.AlertProc{Name: name, Cmd: cmd} }
}
func peak(v float64) alertOpt { return func(a *wire.Alert) { a.Peak = &v } }

func testSet() alertSet {
	return alertSet{
		mkAlert(hostA, "web-1", "threshold_crossed", "cpu", wire.TintCritical, 2, peak(0.99)),
		mkAlert(hostA, "web-1", "threshold_crossed", "disk", wire.TintWarn, 30, acked),
		mkAlert(hostB, "db-2", "threshold_crossed", "disk", wire.TintCritical, 50, ended(48)),
		mkAlert(hostB, "db-2", "long_process", "long_proc/9/1", wire.TintWarn, 5, proc("python3", "python3 etl/backfill.py")),
		mkAlert(hostB, "db-2", "reachability_changed", "reachability", wire.TintWarn, 80, ended(79)),
	}
}

func queryOf(mod func(*AlertQuery)) AlertQuery {
	q := AlertQuery{State: "all", From: now.Add(-7 * 24 * time.Hour), To: now, Limit: 500, Now: now}
	if mod != nil {
		mod(&q)
	}
	return q
}

func TestAlertSetFilter(t *testing.T) {
	set := testSet()
	for _, tc := range []struct {
		name string
		mod  func(*AlertQuery)
		want int
	}{
		{"none", nil, 5},
		{"open", func(q *AlertQuery) { q.State = "open" }, 3},
		{"action", func(q *AlertQuery) { q.State = "action" }, 2},
		{"resolved", func(q *AlertQuery) { q.State = "resolved" }, 2},
		{"tint", func(q *AlertQuery) { q.Tints = []string{"critical"} }, 2},
		{"kind", func(q *AlertQuery) { q.Kinds = []string{"disk", "long"} }, 3},
		{"host", func(q *AlertQuery) { q.Hosts = []uuid.UUID{hostA} }, 2},
		{"q host", func(q *AlertQuery) { q.Q = "DB-2" }, 3},
		{"q process name", func(q *AlertQuery) { q.Q = "python" }, 1},
		{"q command", func(q *AlertQuery) { q.Q = "etl/back" }, 1},
		{"q metric", func(q *AlertQuery) { q.Q = "reach" }, 1},
		{"bin", func(q *AlertQuery) {
			from, to := now.Add(-6*time.Hour), now.Add(-time.Hour)
			q.StartedFrom, q.StartedTo = &from, &to
		}, 2},
		{"all together", func(q *AlertQuery) {
			q.State, q.Tints, q.Kinds, q.Hosts, q.Q = "open", []string{"warn"}, []string{"long"}, []uuid.UUID{hostB}, "backfill"
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(set.filter(queryOf(tc.mod), "")); got != tc.want {
				t.Fatalf("matches = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAlertSetFacetsLeaveOwnFilterOut(t *testing.T) {
	q := queryOf(func(q *AlertQuery) {
		q.Tints = []string{"critical"}
		q.Facets = []string{"tint", "kind", "state", "host"}
	})
	f := testSet().facets(q)
	if f.Tint["warn"] != 3 || f.Tint["critical"] != 2 {
		t.Errorf("tint facet = %v, want the warn alerts too", f.Tint)
	}
	if f.Kind["cpu"] != 1 || f.Kind["disk"] != 1 || f.Kind["long"] != 0 || len(f.Kind) != 2 {
		t.Errorf("kind facet = %v, want only the critical alerts", f.Kind)
	}
	if f.State["open"] != 1 || f.State["resolved"] != 1 {
		t.Errorf("state facet = %v", f.State)
	}
	if f.Host[hostA.String()] != 1 || f.Host[hostB.String()] != 1 {
		t.Errorf("host facet = %v", f.Host)
	}
	if got := testSet().facets(queryOf(nil)); got.Tint != nil || got.Kind != nil {
		t.Errorf("no facets asked, got %+v", got)
	}
}

func TestAlertSetCounts(t *testing.T) {
	q := queryOf(func(q *AlertQuery) { q.State = "resolved"; q.Kinds = []string{"disk"} })
	got := testSet().counts(q)
	if want := (wire.AlertCounts{Open: 1, Action: 0, Resolved: 1, All: 2}); got != want {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
	if got := testSet().counts(queryOf(nil)); got != (wire.AlertCounts{Open: 3, Action: 2, Resolved: 2, All: 5}) {
		t.Fatalf("counts = %+v", got)
	}
}

func TestAlertSetBins(t *testing.T) {
	week := 7 * 24 * time.Hour
	q := queryOf(func(q *AlertQuery) { q.Bins = 28 })
	if got := len(testSet().bins(q)); got != 28 {
		t.Fatalf("bins = %d", got)
	}
	edge := mkAlert(hostA, "web-1", "threshold_crossed", "cpu", wire.TintCritical, 0)
	// The start is on the edge of bins 3 and 4 (6 h each): it goes to bin 4.
	start := q.From.Add(4 * 6 * time.Hour)
	edge.Start = &start
	first := mkAlert(hostA, "web-1", "threshold_crossed", "mem", wire.TintWarn, 0)
	s0 := q.From
	first.Start = &s0
	out := alertSet{edge, first}.bins(q)
	if out[4].Critical != 1 || out[3].Critical != 0 || out[0].Warn != 1 {
		t.Fatalf("bins = %+v", out[:5])
	}
	if !out[0].Start.Equal(q.From) || out[27].End != q.To || out[1].Start.Sub(out[0].Start) != week/28 {
		t.Fatalf("bin edges: %v %v", out[0], out[27])
	}
	// A bar filter keeps the bars.
	from, to := q.From.Add(4*6*time.Hour), q.From.Add(5*6*time.Hour)
	q.StartedFrom, q.StartedTo = &from, &to
	if again := (alertSet{edge, first}).bins(q); again[0].Warn != 1 || again[4].Critical != 1 {
		t.Fatalf("the bin filter changed the bins: %+v", again[:5])
	}
	if (alertSet{}).bins(queryOf(nil)) != nil {
		t.Fatal("0 bins gives a list")
	}
}

func TestAlertSetSorted(t *testing.T) {
	set := testSet()
	names := func(q AlertQuery) []string {
		var out []string
		for _, a := range set.sorted(q) {
			out = append(out, a.NodeName+"/"+a.Metric)
		}
		return out
	}
	for _, tc := range []struct {
		sort string
		desc bool
		want []string
	}{
		{"sev", true, []string{"web-1/cpu", "db-2/disk", "db-2/long_proc/9/1", "web-1/disk", "db-2/reachability"}},
		{"sev", false, []string{"db-2/long_proc/9/1", "web-1/disk", "db-2/reachability", "web-1/cpu", "db-2/disk"}},
		{"host", false, []string{"db-2/long_proc/9/1", "db-2/disk", "db-2/reachability", "web-1/cpu", "web-1/disk"}},
		{"kind", false, []string{"web-1/cpu", "web-1/disk", "db-2/disk", "db-2/long_proc/9/1", "db-2/reachability"}},
		{"peak", true, []string{"web-1/cpu", "db-2/long_proc/9/1", "web-1/disk", "db-2/disk", "db-2/reachability"}},
		{"start", true, []string{"web-1/cpu", "db-2/long_proc/9/1", "web-1/disk", "db-2/disk", "db-2/reachability"}},
		{"title", false, []string{"web-1/cpu", "web-1/disk", "db-2/disk", "db-2/long_proc/9/1", "db-2/reachability"}},
		{"duration", true, []string{"web-1/disk", "db-2/long_proc/9/1", "web-1/cpu", "db-2/disk", "db-2/reachability"}},
		{"state", false, []string{"web-1/cpu", "db-2/long_proc/9/1", "web-1/disk", "db-2/disk", "db-2/reachability"}},
	} {
		t.Run(tc.sort, func(t *testing.T) {
			got := names(queryOf(func(q *AlertQuery) { q.Sort, q.Desc = tc.sort, tc.desc }))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("order = %v\nwant    %v", got, tc.want)
			}
		})
	}
}

func TestAlertSetCloseSuperseded(t *testing.T) {
	first := mkAlert(hostA, "web-1", "threshold_crossed", "cpu", wire.TintWarn, 10)
	second := mkAlert(hostA, "web-1", "threshold_crossed", "cpu", wire.TintCritical, 4)
	other := mkAlert(hostA, "web-1", "threshold_crossed", "mem", wire.TintWarn, 8)
	done := mkAlert(hostB, "db-2", "threshold_crossed", "cpu", wire.TintWarn, 9, ended(8))
	later := mkAlert(hostB, "db-2", "threshold_crossed", "cpu", wire.TintWarn, 1)
	out := alertSet{second, first, other, done, later}.closeSuperseded()

	byID := map[uuid.UUID]wire.Alert{}
	for _, a := range out {
		byID[a.ID] = a
	}
	if a := byID[first.ID]; a.End == nil || !a.End.Equal(*second.Start) || a.State != alertResolved {
		t.Errorf("first = %+v, want an end at the start of the second", a)
	}
	if a := byID[second.ID]; a.End != nil || a.State != alertOpen {
		t.Errorf("second = %+v, want open", a)
	}
	if a := byID[other.ID]; a.End != nil {
		t.Errorf("an alert of another metric closed: %+v", a)
	}
	if a := byID[done.ID]; !a.End.Equal(now.Add(-8 * time.Hour)) {
		t.Errorf("a resolved alert changed: %+v", a)
	}
	if first.End != nil {
		t.Error("the input changed")
	}
}

func TestAlertQueryValidate(t *testing.T) {
	good := func() AlertQuery { return queryOf(nil) }
	for _, tc := range []struct {
		name    string
		mod     func(*AlertQuery)
		wantErr string
		clamped bool
	}{
		{name: "good"},
		{name: "bad state", mod: func(q *AlertQuery) { q.State = "x" }, wantErr: "state"},
		{name: "bad tint", mod: func(q *AlertQuery) { q.Tints = []string{"ok"} }, wantErr: "tint"},
		{name: "bad kind", mod: func(q *AlertQuery) { q.Kinds = []string{"gpu"} }, wantErr: "kind"},
		{name: "bad facet", mod: func(q *AlertQuery) { q.Facets = []string{"x"} }, wantErr: "facets"},
		{name: "bad sort", mod: func(q *AlertQuery) { q.Sort = "x" }, wantErr: "sort"},
		{name: "from after to", mod: func(q *AlertQuery) { q.From = q.To.Add(time.Hour) }, wantErr: "from"},
		{name: "120 days", mod: func(q *AlertQuery) { q.From = q.To.Add(-120 * 24 * time.Hour) }, clamped: true},
		{name: "60 bins", mod: func(q *AlertQuery) { q.Bins = 60 }},
		{name: "61 bins", mod: func(q *AlertQuery) { q.Bins = 61 }, wantErr: "bins"},
		{name: "limit 0", mod: func(q *AlertQuery) { q.Limit = 0 }, wantErr: "limit"},
		{name: "limit 1001", mod: func(q *AlertQuery) { q.Limit = 1001 }, wantErr: "limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := good()
			if tc.mod != nil {
				tc.mod(&q)
			}
			clamped, err := q.Validate()
			if tc.wantErr != "" {
				if !errors.Is(err, ErrBadAlertQuery) || err.Error() != "query: bad alert query: "+tc.wantErr {
					t.Fatalf("err = %v, want a bad %s", err, tc.wantErr)
				}
				return
			}
			if err != nil || clamped != tc.clamped {
				t.Fatalf("clamped = %v, err = %v", clamped, err)
			}
			if tc.clamped && !q.From.Equal(q.To.Add(-MaxAlertDays*24*time.Hour)) {
				t.Fatalf("from = %v", q.From)
			}
		})
	}
	q := queryOf(func(q *AlertQuery) { q.State, q.Sort = "", "" })
	if _, err := q.Validate(); err != nil || q.State != "open" || q.Sort != "sev" {
		t.Fatalf("defaults: %+v, %v", q, err)
	}
}

func incident(id, node uuid.UUID, kind, metric, sev string, startH float64, mod func(*clickhouse.IncidentRow)) clickhouse.IncidentRow {
	start := now.Add(-time.Duration(startH * float64(time.Hour)))
	r := clickhouse.IncidentRow{
		AlertID: id, NodeID: node, Kind: kind, Metric: metric, Tint: sev, Severity: sev,
		Start: start, LastTS: start, Value: 0.9, Peak: 0.9, Payload: `{"value":0.9}`,
	}
	if mod != nil {
		mod(&r)
	}
	return r
}

type fixedPeaks map[uuid.UUID]float64

func (f fixedPeaks) Peaks(uuid.UUID) map[uuid.UUID]float64 { return f }

func TestListAlertsLivePeak(t *testing.T) {
	open, closed, old1, old2, okOnly := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	end := now.Add(-time.Hour)
	r := &fakeReader{incidents: []clickhouse.IncidentRow{
		incident(open, hostA, "threshold_crossed", "cpu", "warn", 3, nil),
		incident(closed, hostA, "threshold_crossed", "mem", "critical", 5, func(r *clickhouse.IncidentRow) { r.End, r.Tint = &end, "ok"; r.Peak = 0.97 }),
		incident(old1, hostB, "threshold_crossed", "disk", "warn", 20, nil),
		incident(old2, hostB, "threshold_crossed", "disk", "warn", 10, nil),
		incident(okOnly, hostB, "threshold_crossed", "mem", "ok", 15, func(r *clickhouse.IncidentRow) { r.End = &end }),
	}, truncated: true}
	nodes := []store.Node{{ID: hostA, DisplayName: "web-1"}, {ID: hostB, DisplayName: "db-2"}}
	s := newService(r, nodes, 0)
	s.peaks = fixedPeaks{open: 0.99, closed: 0.5}

	page, err := s.ListAlerts(context.Background(), queryOf(func(q *AlertQuery) {
		q.State = "all"
		q.Bins = 7
		q.Facets = []string{"tint"}
		q.Hosts = []uuid.UUID{hostA}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r.gotIncs.Limit != MaxIncidents || !r.gotIncs.From.Equal(now.Add(-7*24*time.Hour)) || len(r.gotIncs.NodeIDs) != 1 {
		t.Fatalf("incident query = %+v", r.gotIncs)
	}
	if !page.Truncated || page.Total != 2 || len(page.Alerts) != 2 {
		t.Fatalf("page = %+v", page)
	}
	got := map[uuid.UUID]wire.Alert{}
	for _, a := range page.Alerts {
		got[a.ID] = a
	}
	if p := got[open].Peak; p == nil || *p != 0.99 || got[open].NodeName != "web-1" || got[open].State != alertOpen {
		t.Fatalf("open alert = %+v, want the live peak 0.99", got[open])
	}
	if p := got[closed].Peak; p == nil || *p != 0.97 || got[closed].State != alertResolved {
		t.Fatalf("closed alert = %+v, want the stored peak", got[closed])
	}
	if page.Counts != (wire.AlertCounts{Open: 1, Action: 1, Resolved: 1, All: 2}) {
		t.Fatalf("counts = %+v", page.Counts)
	}
	if page.Facets == nil || page.Facets.Tint["critical"] != 1 || page.Facets.Tint["warn"] != 1 {
		t.Fatalf("facets = %+v", page.Facets)
	}
	if len(page.Bins) != 7 {
		t.Fatalf("bins = %d", len(page.Bins))
	}
}

func TestListAlertsClosesOldAlerts(t *testing.T) {
	old1, old2, okOnly := uuid.New(), uuid.New(), uuid.New()
	end := now.Add(-15 * time.Hour)
	r := &fakeReader{incidents: []clickhouse.IncidentRow{
		incident(old1, hostB, "threshold_crossed", "disk", "warn", 20, nil),
		incident(old2, hostB, "threshold_crossed", "disk", "warn", 10, nil),
		incident(okOnly, hostB, "threshold_crossed", "disk", "ok", 15, func(r *clickhouse.IncidentRow) { r.End = &end }),
	}}
	page, err := newService(r, nil, 0).ListAlerts(context.Background(), queryOf(func(q *AlertQuery) { q.Sort, q.Desc = "start", false }))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Alerts) != 2 || page.Alerts[0].ID != old1 || page.Alerts[0].State != alertResolved ||
		!page.Alerts[0].End.Equal(end) || page.Alerts[1].State != alertOpen {
		t.Fatalf("alerts = %+v", page.Alerts)
	}
}

func TestListAlertsNoClickHouse(t *testing.T) {
	page, err := NewService(nil, fakeNodes(nil), nil, nil).ListAlerts(context.Background(), queryOf(nil))
	if err != nil || page.Alerts == nil || len(page.Alerts) != 0 {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if _, err := NewService(nil, nil, nil, nil).ListAlerts(context.Background(), AlertQuery{}); !errors.Is(err, ErrBadAlertQuery) {
		t.Fatalf("empty query: %v", err)
	}
}

func TestAlertDetailTimeline(t *testing.T) {
	alert, node, by := uuid.New(), hostA, user
	at := func(m int) time.Time { return now.Add(time.Duration(m) * time.Minute) }
	ev := func(m int, tint, payload string) clickhouse.EventRow {
		return clickhouse.EventRow{TS: at(m), NodeID: node, EventID: uuid.New(), AlertID: alert, Kind: "threshold_crossed", Metric: "cpu", Tint: tint, Payload: payload}
	}
	until := at(40)
	r := &fakeReader{
		events: []clickhouse.EventRow{
			ev(0, "warn", `{"value":0.85,"warn":0.8,"critical":0.95}`),
			ev(10, "critical", `{"value":0.97,"warn":0.8,"critical":0.95}`),
			ev(60, "ok", `{"value":0.2,"peak":0.97,"closed":"ignored"}`),
		},
		lines: []clickhouse.AlertLine{
			{TS: at(1), Kind: "notified", Text: "Slack #ops, email"},
			{TS: at(5), Kind: "acked", User: &by},
			{TS: at(6), Kind: "snoozed", User: &by, Until: &until},
			{TS: at(61), Kind: "closed", User: &by, Text: "cpu ignored"},
		},
	}
	d, err := newService(r, []store.Node{{ID: node, DisplayName: "web-1"}}, 0).AlertDetail(context.Background(), uuid.New(), alert)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, l := range d.Timeline {
		kinds = append(kinds, l.Kind)
	}
	want := []string{"opened", "notified", "acked", "snoozed", "changed", "closed"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("timeline = %v, want %v", kinds, want)
	}
	if l := d.Timeline[5]; l.Text != "cpu ignored" || l.User == nil {
		t.Fatalf("closed line = %+v, want the line of the log, not the event", l)
	}
	if d.Limits == nil || d.Limits.Warn != 0.8 || d.Limits.Critical != 0.95 {
		t.Fatalf("limits = %+v", d.Limits)
	}
	a := d.Alert
	if a.State != alertResolved || a.End == nil || !a.End.Equal(at(60)) || a.Severity != wire.TintCritical ||
		a.Peak == nil || *a.Peak != 0.97 || a.NodeName != "web-1" || a.ID != alert || !a.Start.Equal(at(0)) {
		t.Fatalf("alert = %+v", a)
	}

	// A change back to ok with no `closed` is a recovery; a mute closes quietly.
	r.events[2] = ev(60, "ok", `{"value":0.2}`)
	r.lines = nil
	d, _ = newService(r, nil, 0).AlertDetail(context.Background(), uuid.New(), alert)
	if l := d.Timeline[len(d.Timeline)-1]; l.Kind != "recovered" || l.Tint != wire.TintOK {
		t.Fatalf("last line = %+v", l)
	}
	r.events[2] = ev(60, "ok", `{"reason":"muted"}`)
	d, _ = newService(r, nil, 0).AlertDetail(context.Background(), uuid.New(), alert)
	if l := d.Timeline[len(d.Timeline)-1]; l.Kind != "closed" || l.Text != "Muted" {
		t.Fatalf("last line = %+v", l)
	}

	// An old alert that a later alert replaced is resolved at its last event.
	r.events = r.events[:2]
	d, _ = newService(r, nil, 0).AlertDetail(context.Background(), uuid.New(), alert)
	if d.Alert.State != alertResolved || !d.Alert.End.Equal(at(10)) {
		t.Fatalf("replaced alert = %+v", d.Alert)
	}
	r.byID = map[uuid.UUID]clickhouse.AlertRow{alert: {AlertID: alert}}
	d, _ = newService(r, nil, 0).AlertDetail(context.Background(), uuid.New(), alert)
	if d.Alert.State != alertOpen || d.Alert.End != nil {
		t.Fatalf("open alert = %+v", d.Alert)
	}

	r.events = nil
	if _, err := newService(r, nil, 0).AlertDetail(context.Background(), uuid.New(), alert); !errors.Is(err, clickhouse.ErrNoAlert) {
		t.Fatalf("unknown alert: %v", err)
	}
}
