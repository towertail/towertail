package query

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

type fixedLimits int

func (d fixedLimits) HistoryDays(context.Context, uuid.UUID) int { return int(d) }

type fakeNodes []store.Node

func (f fakeNodes) ListNodes(context.Context, uuid.UUID) ([]store.Node, error) { return f, nil }

// fakeReader gives stats by window start, so a test sees which windows
// the service asks for.
type fakeReader struct {
	latest  []clickhouse.NodeLatest
	disks   map[uuid.UUID]float64
	procs   []clickhouse.TopProc
	stats   func(q clickhouse.StatsQuery) []clickhouse.NodeStat
	gotProc clickhouse.RangeQuery
	gotBy   clickhouse.ProcOrder
	gotN    int
	asked   []clickhouse.StatsQuery
	since   time.Time

	history     []clickhouse.HistoryPoint
	gotHistory  clickhouse.HistoryQuery
	disk        []clickhouse.DiskPoint
	gotDisk     clickhouse.RangeQuery
	open, rows  []clickhouse.AlertRow
	gotSince    time.Time
	gotRowLimit int
	marks       map[uuid.UUID]clickhouse.AlertMark
}

func (f *fakeReader) LatestByNode(_ context.Context, _ uuid.UUID, since time.Time) ([]clickhouse.NodeLatest, error) {
	f.since = since
	return f.latest, nil
}

func (f *fakeReader) LatestDiskRatio(context.Context, uuid.UUID, time.Time) (map[uuid.UUID]float64, error) {
	return f.disks, nil
}

func (f *fakeReader) TopProcesses(_ context.Context, q clickhouse.RangeQuery, by clickhouse.ProcOrder, n int) ([]clickhouse.TopProc, error) {
	f.gotProc, f.gotBy, f.gotN = q, by, n
	return f.procs, nil
}

func (f *fakeReader) WindowStats(_ context.Context, q clickhouse.StatsQuery) ([]clickhouse.NodeStat, error) {
	f.asked = append(f.asked, q)
	if f.stats == nil {
		return nil, nil
	}
	return f.stats(q), nil
}

func (f *fakeReader) QueryHistory(_ context.Context, q clickhouse.HistoryQuery) ([]clickhouse.HistoryPoint, time.Duration, error) {
	f.gotHistory = q
	return f.history, time.Minute, nil
}

func (f *fakeReader) DiskHistory(_ context.Context, q clickhouse.RangeQuery) ([]clickhouse.DiskPoint, time.Duration, error) {
	f.gotDisk = q
	return f.disk, time.Minute, nil
}

func (f *fakeReader) OpenAlerts(context.Context, uuid.UUID) ([]clickhouse.AlertRow, error) {
	return f.open, nil
}

func (f *fakeReader) RecentAlerts(_ context.Context, _ uuid.UUID, since time.Time, limit int) ([]clickhouse.AlertRow, error) {
	f.gotSince, f.gotRowLimit = since, limit
	return f.rows, nil
}

func (f *fakeReader) AlertMarks(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error) {
	return f.marks, nil
}

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func newService(r Reader, nodes []store.Node, days int) *Service {
	s := NewService(r, fakeNodes(nodes), fixedLimits(days))
	s.now = func() time.Time { return now }
	return s
}

func TestClamp(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		name        string
		limits      Limits
		from        time.Time
		want        time.Time
		wantClamped bool
	}{
		{"30 days", fixedLimits(30), now.Add(-90 * day), now.Add(-30 * day), true},
		{"inside plan", fixedLimits(30), now.Add(-7 * day), now.Add(-7 * day), false},
		{"no limits", nil, now.Add(-90 * day), now.Add(-90 * day), false},
		{"0 days", fixedLimits(0), now.Add(-90 * day), now.Add(-90 * day), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped := NewService(nil, nil, tc.limits).Clamp(context.Background(), uuid.New(), tc.from, now)
			if !got.Equal(tc.want) || clamped != tc.wantClamped {
				t.Fatalf("Clamp = %v, %v; want %v, %v", got, clamped, tc.want, tc.wantClamped)
			}
		})
	}
}

func TestNoHistory(t *testing.T) {
	s := NewService(nil, fakeNodes(nil), nil)
	ctx := context.Background()
	if _, err := s.Hosts(ctx, uuid.New(), SortCPU, 10); !errors.Is(err, ErrNoHistory) {
		t.Errorf("Hosts: %v", err)
	}
	if _, err := s.TopProcesses(ctx, ProcQuery{}); !errors.Is(err, ErrNoHistory) {
		t.Errorf("TopProcesses: %v", err)
	}
	if _, err := s.Compare(ctx, CompareQuery{Metric: "cpu", Window: time.Hour}); !errors.Is(err, ErrNoHistory) {
		t.Errorf("Compare: %v", err)
	}
}

func TestHosts(t *testing.T) {
	a, b, c, off := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nodes := []store.Node{
		{ID: a, DisplayName: "a", Tags: []string{"prod"}},
		{ID: b, DisplayName: "b"},
		{ID: c, DisplayName: "c"},
		{ID: off, DisplayName: "off"},
	}
	r := &fakeReader{
		latest: []clickhouse.NodeLatest{
			{NodeID: a, TS: now, CPUPct: 90, MemRatio: 0.2, Load1: 1.5, NetRxBps: 10, NetTxBps: 10},
			{NodeID: b, TS: now, CPUPct: 10, MemRatio: 0.9, NetRxBps: 500, NetTxBps: 5},
			{NodeID: c, TS: now, CPUPct: 50, MemRatio: 0.5},
		},
		disks: map[uuid.UUID]float64{a: 0.3, c: 0.8},
	}
	s := newService(r, nodes, 0)
	ctx := context.Background()

	got, err := s.Hosts(ctx, uuid.New(), SortCPU, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !r.since.Equal(now.Add(-10 * time.Minute)) {
		t.Errorf("since = %v, want 10 minutes ago", r.since)
	}
	order := func() (out []string) {
		for _, h := range got.Hosts {
			out = append(out, h.Name)
		}
		return
	}
	if o := order(); len(o) != 4 || o[0] != "a" || o[1] != "c" || o[2] != "b" || o[3] != "off" {
		t.Fatalf("cpu order = %v, want a c b off", o)
	}
	h := got.Hosts[0]
	if !h.Online || *h.CPU != 90 || *h.Disk != 0.3 || *h.Load1 != 1.5 || len(h.Tags) != 1 {
		t.Errorf("host a = %+v", h)
	}
	if h := got.Hosts[2]; h.Disk != nil {
		t.Errorf("host b has a disk value: %v", *h.Disk)
	}
	offline := got.Hosts[3]
	if offline.Online || offline.CPU != nil || offline.Mem != nil || offline.LastSeen != nil {
		t.Errorf("offline host = %+v, want no values", offline)
	}

	for sort, want := range map[HostSort]string{SortMem: "b", SortDisk: "c", SortNet: "b"} {
		got, err := s.Hosts(ctx, uuid.New(), sort, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Hosts) != 1 || got.Hosts[0].Name != want {
			t.Errorf("sort %s limit 1 = %+v, want %s", sort, got.Hosts, want)
		}
	}
	if _, err := s.Hosts(ctx, uuid.New(), "bogus", 1); !errors.Is(err, ErrBadSort) {
		t.Errorf("bad sort: %v", err)
	}
}

func TestCompare(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	nodes := []store.Node{{ID: a, DisplayName: "a"}, {ID: b, DisplayName: "b"}}
	stats := func(q clickhouse.StatsQuery) []clickhouse.NodeStat {
		if q.To.Equal(now) {
			return []clickhouse.NodeStat{{NodeID: a, Avg: 80, Max: 95, Points: 10}, {NodeID: b, Avg: 21, Max: 30, Points: 10}}
		}
		return []clickhouse.NodeStat{{NodeID: a, Avg: 40, Max: 50, Points: 10}, {NodeID: b, Avg: 20, Max: 25, Points: 10}}
	}
	ctx := context.Background()

	t.Run("two windows", func(t *testing.T) {
		r := &fakeReader{stats: stats}
		got, err := newService(r, nodes, 30).Compare(ctx, CompareQuery{OrgID: uuid.New(), Metric: "cpu", Window: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Current.From.Equal(now.Add(-time.Hour)) || !got.Current.To.Equal(now) ||
			!got.Previous.From.Equal(now.Add(-2*time.Hour)) || !got.Previous.To.Equal(now.Add(-time.Hour)) || got.Clamped {
			t.Fatalf("windows = %+v %+v clamped %v", got.Current, got.Previous, got.Clamped)
		}
		if len(r.asked) != 2 || len(got.Hosts) != 2 {
			t.Fatalf("asked %d windows, %d hosts", len(r.asked), len(got.Hosts))
		}
		h := got.Hosts[0]
		if h.Name != "a" || h.Previous == nil || h.Previous.Avg != 40 || h.DeltaAvg == nil || *h.DeltaAvg != 40 || h.Current.Max != 95 {
			t.Fatalf("host 0 = %+v, want a with delta 40 first", h)
		}
	})

	t.Run("previous outside the plan", func(t *testing.T) {
		r := &fakeReader{stats: stats}
		got, err := newService(r, nodes, 1).Compare(ctx, CompareQuery{Metric: "cpu", Window: 24 * time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Clamped || !got.Previous.From.Equal(got.Previous.To) {
			t.Fatalf("previous = %+v clamped %v, want an empty clamped window", got.Previous, got.Clamped)
		}
		if len(r.asked) != 1 {
			t.Fatalf("asked %d windows, want 1", len(r.asked))
		}
		for _, h := range got.Hosts {
			if h.Previous != nil || h.DeltaAvg != nil {
				t.Fatalf("host %s has a previous value", h.Name)
			}
		}
	})

	t.Run("previous cut by the plan", func(t *testing.T) {
		r := &fakeReader{stats: stats}
		got, err := newService(r, nodes, 2).Compare(ctx, CompareQuery{Metric: "cpu", Window: 36 * time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Clamped || !got.Previous.From.Equal(now.Add(-48*time.Hour)) || !got.Previous.To.Equal(now.Add(-36*time.Hour)) {
			t.Fatalf("previous = %+v clamped %v", got.Previous, got.Clamped)
		}
	})

	t.Run("bad arguments", func(t *testing.T) {
		s := newService(&fakeReader{}, nodes, 30)
		for _, w := range []time.Duration{5 * time.Minute, 721 * time.Hour} {
			if _, err := s.Compare(ctx, CompareQuery{Metric: "cpu", Window: w}); !errors.Is(err, ErrBadWindow) {
				t.Errorf("window %v: %v", w, err)
			}
		}
		for _, m := range []string{"bogus", "procs", ""} {
			if _, err := s.Compare(ctx, CompareQuery{Metric: m, Window: time.Hour}); !errors.Is(err, clickhouse.ErrUnknownMetric) {
				t.Errorf("metric %q: %v", m, err)
			}
		}
	})
}

func TestTopProcessesFleet(t *testing.T) {
	a := uuid.New()
	r := &fakeReader{procs: []clickhouse.TopProc{{NodeID: a, Name: "nginx", Cmd: "nginx -g", CmdHash: 42, CPUPctAvg: 12, CPUPctMax: 30, RSSMax: 1 << 20}}}
	s := newService(r, []store.Node{{ID: a, DisplayName: "web-1"}}, 7)
	got, err := s.TopProcesses(context.Background(), ProcQuery{
		OrgID: uuid.New(), From: now.Add(-30 * 24 * time.Hour), To: now, By: clickhouse.ProcByMem, Limit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Processes) != 1 || got.Processes[0].NodeName != "web-1" || got.Processes[0].RSSMax != 1<<20 || got.By != "mem" {
		t.Fatalf("processes = %+v", got)
	}
	if k := got.Processes[0].Key; k.Name != "nginx" || k.CmdHash != 42 {
		t.Fatalf("key = %+v", k)
	}
	if !got.Clamped || !got.From.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("from = %v clamped %v, want the plan limit", got.From, got.Clamped)
	}
	if !r.gotProc.From.Equal(got.From) || r.gotBy != clickhouse.ProcByMem || r.gotN != MaxProcs {
		t.Fatalf("reader got %+v %s %d", r.gotProc, r.gotBy, r.gotN)
	}
	if _, err := s.TopProcesses(context.Background(), ProcQuery{By: "disk"}); !errors.Is(err, ErrBadOrder) {
		t.Fatalf("bad order: %v", err)
	}
}

func TestResolveHost(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	s := newService(nil, []store.Node{
		{ID: a, DisplayName: "Web-1"},
		{ID: b, DisplayName: "db"},
		{ID: c, DisplayName: "DB"},
	}, 0)
	ctx := context.Background()

	if n, err := s.ResolveHost(ctx, uuid.New(), a.String()); err != nil || n.ID != a {
		t.Errorf("by ID: %v, %v", n.ID, err)
	}
	if n, err := s.ResolveHost(ctx, uuid.New(), "web-1"); err != nil || n.ID != a {
		t.Errorf("by name: %v, %v", n.ID, err)
	}
	if _, err := s.ResolveHost(ctx, uuid.New(), "nope"); !errors.Is(err, ErrHostNotFound) {
		t.Errorf("unknown name: %v", err)
	}
	if _, err := s.ResolveHost(ctx, uuid.New(), uuid.NewString()); !errors.Is(err, ErrHostNotFound) {
		t.Errorf("unknown ID: %v", err)
	}
	if _, err := s.ResolveHost(ctx, uuid.New(), ""); !errors.Is(err, ErrHostNotFound) {
		t.Errorf("empty ref: %v", err)
	}
	_, err := s.ResolveHost(ctx, uuid.New(), "Db")
	if !errors.Is(err, ErrHostAmbiguous) || !strings.Contains(err.Error(), b.String()) || !strings.Contains(err.Error(), c.String()) {
		t.Errorf("same name: %v, want both IDs", err)
	}
}

func TestServiceHistoryClamp(t *testing.T) {
	day := 24 * time.Hour
	r := &fakeReader{}
	s := newService(r, nil, 1)
	_, _, clamped, err := s.History(context.Background(), HistoryRequest{
		OrgID: uuid.New(), NodeID: uuid.New(), Metric: "cpu", From: now.Add(-7 * day), To: now, Step: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !clamped || !r.gotHistory.From.Equal(now.Add(-day)) {
		t.Fatalf("clamped = %v, from = %v; want true, %v", clamped, r.gotHistory.From, now.Add(-day))
	}
	_, _, clamped, _ = s.History(context.Background(), HistoryRequest{Metric: "cpu", From: now.Add(-time.Hour)})
	if clamped || !r.gotHistory.To.Equal(now) {
		t.Fatalf("inside plan: clamped = %v, to = %v", clamped, r.gotHistory.To)
	}
	if _, _, _, err := NewService(nil, fakeNodes(nil), nil).History(context.Background(), HistoryRequest{}); !errors.Is(err, ErrNoHistory) {
		t.Errorf("no ClickHouse: %v", err)
	}
}

func TestServiceDisksClamp(t *testing.T) {
	day := 24 * time.Hour
	r := &fakeReader{disk: []clickhouse.DiskPoint{{Mount: "/"}}}
	got, err := newService(r, nil, 1).Disks(context.Background(), uuid.New(), uuid.New(), now.Add(-7*day), now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Clamped || !got.From.Equal(now.Add(-day)) || len(got.Points) != 1 || !r.gotDisk.From.Equal(got.From) {
		t.Fatalf("Disks = %+v", got)
	}
}

func TestServiceAlerts(t *testing.T) {
	day := 24 * time.Hour
	a, b := uuid.New(), uuid.New()
	rows := []clickhouse.AlertRow{{NodeID: a, Metric: "cpu", Tint: "crit"}, {NodeID: b, Metric: "mem", Tint: "warn"}}
	ctx := context.Background()
	org := uuid.New()

	for _, tc := range []struct {
		name        string
		limits      int
		req         AlertsRequest
		wantSince   time.Time
		wantClamped bool
		wantN       int
		wantErr     error
	}{
		{"clamped", 1, AlertsRequest{Days: 7}, now.Add(-day), true, 2, nil},
		{"inside plan", 30, AlertsRequest{Days: 7}, now.Add(-7 * day), false, 2, nil},
		{"no limit", 0, AlertsRequest{Days: 7}, now.Add(-7 * day), false, 2, nil},
		{"default days", 30, AlertsRequest{}, now.Add(-7 * day), false, 2, nil},
		{"one host", 30, AlertsRequest{NodeID: b}, now.Add(-7 * day), false, 1, nil},
		{"days 91", 0, AlertsRequest{Days: 91}, time.Time{}, false, 0, ErrBadDays},
		{"days -1", 0, AlertsRequest{Days: -1}, time.Time{}, false, 0, ErrBadDays},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeReader{rows: rows}
			tc.req.OrgID = org
			got, err := newService(r, nil, tc.limits).Alerts(ctx, tc.req)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if !r.gotSince.Equal(tc.wantSince) || got.Clamped != tc.wantClamped || len(got.Alerts) != tc.wantN || r.gotRowLimit != RecentAlertsLimit {
				t.Fatalf("since = %v, clamped = %v, n = %d", r.gotSince, got.Clamped, len(got.Alerts))
			}
		})
	}

	open := &fakeReader{open: rows}
	got, err := newService(open, nil, 1).Alerts(ctx, AlertsRequest{OrgID: org, Open: true, NodeID: a})
	if err != nil || len(got.Alerts) != 1 || got.Alerts[0].NodeID != a || got.Clamped {
		t.Fatalf("open alerts = %+v, %v", got, err)
	}
	empty, err := NewService(nil, fakeNodes(nil), nil).Alerts(ctx, AlertsRequest{Open: true})
	if err != nil || empty.Alerts == nil || len(empty.Alerts) != 0 {
		t.Fatalf("no ClickHouse = %+v, %v; want an empty list", empty, err)
	}
}

func TestAlertFromRowLongProc(t *testing.T) {
	start := time.Date(2026, 10, 18, 11, 0, 0, 0, time.UTC)
	high := start.Add(90 * time.Minute)
	payload, err := json.Marshal(map[string]any{
		"metric":     "long_proc/812/1799000000",
		"previous":   "ok",
		"value":      95,
		"pid":        812,
		"name":       "python3",
		"cmd":        "python3 etl/backfill.py",
		"cpu_pct":    95,
		"high_since": high,
		"start_ts":   start,
		"cwd":        "/srv/etl",
	})
	if err != nil {
		t.Fatal(err)
	}
	a := AlertFromRow(clickhouse.AlertRow{Kind: "long_process", Metric: "long_proc/812/1799000000", Tint: "warn", Payload: string(payload)}, clickhouse.AlertMark{})
	p := a.Proc
	if a.Value != 95 || a.Metric != "long_proc/812/1799000000" || p == nil {
		t.Fatalf("alert = %+v", a)
	}
	if p.PID != 812 || p.Name != "python3" || p.Cmd != "python3 etl/backfill.py" || p.CPUPct != 95 || p.Cwd != "/srv/etl" {
		t.Fatalf("proc = %+v", p)
	}
	if p.StartTS == nil || !p.StartTS.Equal(start) || p.HighSince == nil || !p.HighSince.Equal(high) {
		t.Fatalf("times = %v, %v", p.StartTS, p.HighSince)
	}
}

func TestAlertFromRowLongProcNoStart(t *testing.T) {
	payload := `{"value":95,"pid":812,"name":"python3","cmd":"python3","cpu_pct":95,"start_ts":null}`
	a := AlertFromRow(clickhouse.AlertRow{Kind: "long_process", Payload: payload}, clickhouse.AlertMark{})
	if a.Proc == nil || a.Proc.StartTS != nil {
		t.Fatalf("proc = %+v", a.Proc)
	}
}

func TestAlertFromRowThreshold(t *testing.T) {
	a := AlertFromRow(clickhouse.AlertRow{Kind: "threshold_crossed", Metric: "cpu", Payload: `{"metric":"cpu","value":0.92}`}, clickhouse.AlertMark{})
	if a.Value != 0.92 || a.Proc != nil {
		t.Fatalf("alert = %+v", a)
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"proc"`) {
		t.Fatalf("json has proc: %s", b)
	}
}

func TestAlertFromRowBadPayload(t *testing.T) {
	a := AlertFromRow(clickhouse.AlertRow{Kind: "long_process", Payload: ""}, clickhouse.AlertMark{})
	if a.Value != 0 || a.Proc != nil {
		t.Fatalf("alert = %+v", a)
	}
}

func TestAlertsUseMarks(t *testing.T) {
	org, node, alert, event := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	by := uuid.New()
	r := &fakeReader{
		open:  []clickhouse.AlertRow{{EventID: event, AlertID: alert, NodeID: node, Kind: "threshold_crossed", Metric: "cpu", Tint: "warn", Payload: `{"value":0.9}`}},
		marks: map[uuid.UUID]clickhouse.AlertMark{alert: {AckedBy: &by}},
	}
	got, err := newService(r, nil, 1).Alerts(context.Background(), AlertsRequest{OrgID: org, Open: true})
	if err != nil || len(got.Alerts) != 1 {
		t.Fatalf("alerts = %+v, %v", got, err)
	}
	if a := got.Alerts[0]; a.ID != alert || a.AckedBy == nil || *a.AckedBy != by {
		t.Fatalf("alert = %+v, want id %s acked by %s", a, alert, by)
	}
}

func TestAlertFromRowOldRowHasEventID(t *testing.T) {
	event := uuid.New()
	if a := AlertFromRow(clickhouse.AlertRow{EventID: event}, clickhouse.AlertMark{}); a.ID != event {
		t.Fatalf("id = %s, want %s", a.ID, event)
	}
}

func TestParseProcOrderIO(t *testing.T) {
	if by, err := ParseProcOrder("io"); err != nil || by != clickhouse.ProcByIO {
		t.Fatalf("io: %q, %v", by, err)
	}
	if _, err := ParseProcOrder("disk"); !errors.Is(err, ErrBadOrder) {
		t.Fatalf("disk: %v", err)
	}
}
