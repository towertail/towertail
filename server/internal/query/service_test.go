package query

import (
	"context"
	"errors"
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
	r := &fakeReader{procs: []clickhouse.TopProc{{NodeID: a, Name: "nginx", Cmd: "nginx -g", CPUPctAvg: 12, CPUPctMax: 30, RSSMax: 1 << 20}}}
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
