package mcp

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

func TestTopProcessesAt(t *testing.T) {
	f := newFake()
	web := f.nodes[0]
	start := now.Add(-26 * time.Hour)
	key := wire.ProcessKey{Name: "api", CmdHash: 5}
	f.snap = wire.ProcessSnapshot{NodeID: web.ID, TS: now.Add(-2 * time.Hour), Tier: "raw", By: "io", Processes: []wire.SnapshotProcess{
		{Key: key, PID: 77, User: "app", State: "S", Name: "api", Cmd: "api -x", CPUPct: 12, RSS: 2048, IOBps: 900, StartTS: &start},
	}}
	cs := connect(t, f, tokenA())

	out := decode[TopProcessesOut](t, call(t, cs, "top_processes", map[string]any{"host": "web-1", "at": "now-2h", "by": "io", "limit": 3}))
	if f.gotSnap.NodeID != web.ID || !f.gotSnap.At.Equal(now.Add(-2*time.Hour)) || f.gotSnap.By != clickhouse.ProcByIO || f.gotSnap.Limit != 3 {
		t.Fatalf("request = %+v", f.gotSnap)
	}
	if out.At != "2026-11-20T10:00:00Z" || out.Tier != "raw" || out.By != "io" || out.From != "" || len(out.Processes) != 1 {
		t.Fatalf("out = %+v", out)
	}
	p := out.Processes[0]
	if p.PID != 77 || p.User != "app" || p.Host != "web-1" || p.IOBpsAvg != 900 || p.Key != key.String() ||
		p.StartedAt != "2026-11-19T10:00:00Z" || !p.StartExact || p.RunSeconds != 24*3600 {
		t.Fatalf("process = %+v", p)
	}
	if f.gotStarts != nil {
		t.Errorf("a raw start time needs no start query: %v", f.gotStarts)
	}

	// An RFC 3339 time works too, and a time older than the plan is flagged.
	f.snap = wire.ProcessSnapshot{NodeID: web.ID, TS: now.Add(-90 * 24 * time.Hour), By: "cpu", Clamped: true, Processes: []wire.SnapshotProcess{}}
	out = decode[TopProcessesOut](t, call(t, cs, "top_processes", map[string]any{"host": "web-1", "at": "2026-08-22T12:00:00Z"}))
	if !out.Clamped || len(out.Processes) != 0 || !f.gotSnap.At.Equal(time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("clamped out = %+v, request = %+v", out, f.gotSnap)
	}
}

func TestTopProcessesRunTime(t *testing.T) {
	f := newFake()
	web := f.nodes[0]
	born, first := now.Add(-3*time.Hour), now.Add(-20*24*time.Hour)
	cs := connect(t, f, tokenA())

	// No entry for the process: no start facts.
	out := decode[TopProcessesOut](t, call(t, cs, "top_processes", map[string]any{"host": "web-1"}))
	if p := out.Processes[0]; p.StartExact || p.StartedAt != "" || p.FirstSeen != "" || p.RunSeconds != 0 {
		t.Fatalf("no start facts: %+v", p)
	}
	if !f.gotStartsAt.Equal(now) || len(f.gotStarts) != 1 {
		t.Fatalf("start query at %v for %v", f.gotStartsAt, f.gotStarts)
	}

	// With the key of the process, the raw start gives an exact run time.
	f.starts = map[clickhouse.ProcStartKey]query.ProcStart{{NodeID: web.ID, Key: wire.ProcessKey{Name: "postgres", CmdHash: 7}}: {PID: 9, StartedAt: &born, Exact: true}}
	out = decode[TopProcessesOut](t, call(t, cs, "top_processes", map[string]any{"host": "web-1"}))
	p := out.Processes[0]
	if !p.StartExact || p.StartedAt != "2026-11-20T09:00:00Z" || p.RunSeconds != 3*3600 || p.PID != 9 || p.FirstSeen != "" {
		t.Fatalf("exact: %+v", p)
	}

	// With rollup rows only, the oldest stored time is a lower bound.
	f.starts = map[clickhouse.ProcStartKey]query.ProcStart{{NodeID: web.ID, Key: wire.ProcessKey{Name: "postgres", CmdHash: 7}}: {FirstSeen: &first}}
	out = decode[TopProcessesOut](t, call(t, cs, "top_processes", map[string]any{"host": "web-1"}))
	p = out.Processes[0]
	if p.StartExact || p.StartedAt != "" || p.FirstSeen != "2026-10-31T12:00:00Z" || p.RunSeconds != 0 {
		t.Fatalf("rollup only: %+v", p)
	}
}

func TestProcessHistory(t *testing.T) {
	f := newFake()
	key := wire.ProcessKey{Name: "api", CmdHash: 5}
	f.procKey = key
	var pts []clickhouse.ProcPoint
	for i := range 1000 {
		pts = append(pts, clickhouse.ProcPoint{TS: now.Add(-time.Duration(1000-i) * time.Minute), CPUPct: float64(i % 10), RSS: uint64(i), IOBps: 2})
	}
	born := now.Add(-2 * time.Hour)
	f.procHist = query.ProcessHistory{
		From: now.Add(-24 * time.Hour), To: now, Step: time.Minute, Cmd: "api -x", Repo: "/srv",
		Points: pts,
		Facts:  &clickhouse.ProcFacts{PID: 3, User: "app", ListenTCP: []uint16{8080}},
		Start:  query.ProcStart{PID: 3, StartedAt: &born, Exact: true},
	}
	cs := connect(t, f, tokenA())

	out := decode[ProcessHistoryOut](t, call(t, cs, "process_history", map[string]any{"host": "web-1", "process": "api", "range": "24h"}))
	if f.gotRef != "api" || f.gotProcHist.Key != key || f.gotProcHist.NodeID != f.nodes[0].ID || f.gotProcHist.MaxPoints != 300 {
		t.Fatalf("requests: ref %q, %+v", f.gotRef, f.gotProcHist)
	}
	if len(out.Points) == 0 || len(out.Points) > 300 {
		t.Fatalf("%d points, want 1 to 300", len(out.Points))
	}
	if out.Name != "api" || out.Command != "api -x" || out.Key != key.String() || out.Resolution != "1m0s" || out.Stats.RSSMaxBytes != 999 || out.Stats.CPUPctMax != 9 || out.Stats.IOBpsAvg < 1.99 || out.Stats.IOBpsAvg > 2.01 {
		t.Fatalf("out = %+v", out)
	}
	if out.Stats.CPUPctAvg < 4.4 || out.Stats.CPUPctAvg > 4.6 {
		t.Errorf("cpu avg = %v, want 4.5", out.Stats.CPUPctAvg)
	}
	if out.Facts == nil || out.Facts.PID != 3 || len(out.Facts.ListenTCP) != 1 || !out.StartExact || out.RunSeconds != 7200 || out.StartedAt != "2026-11-20T10:00:00Z" {
		t.Fatalf("facts or start: %+v", out)
	}

	// Without facts (the range ended more than 1 hour ago) the field is missing.
	f.procHist.Facts = nil
	out = decode[ProcessHistoryOut](t, call(t, cs, "process_history", map[string]any{"host": "web-1", "process": "api"}))
	if out.Facts != nil {
		t.Errorf("facts = %+v, want none", out.Facts)
	}
}

func TestProcessHistoryErrors(t *testing.T) {
	f := newFake()
	cs := connect(t, f, tokenA())
	for _, tc := range []struct {
		name     string
		resolve  error
		history  error
		wantText string
	}{
		{"not found", fmt.Errorf("%w: %q", query.ErrProcessNotFound, "ghost"), nil, "process not found"},
		{"no process in range", nil, query.ErrProcessNotFound, "process not found"},
		{"ambiguous", fmt.Errorf("%w: %q matches k1 (a), k2 (b)", query.ErrProcessAmbiguous, "node"), nil, "k1 (a), k2 (b)"},
		{"other", errors.New("boom"), nil, "the query failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.procErr, f.procHistErr = tc.resolve, tc.history
			res := call(t, cs, "process_history", map[string]any{"host": "web-1", "process": "x"})
			if !res.IsError || !strings.Contains(text(res), tc.wantText) {
				t.Fatalf("IsError = %v, text = %q; want %q", res.IsError, text(res), tc.wantText)
			}
		})
	}
	f.procErr, f.procHistErr = nil, nil
}

func TestParseAt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Time
		bad  bool
	}{
		{"now", now, false},
		{"now-90m", now.Add(-90 * time.Minute), false},
		{"now-2d", now.Add(-48 * time.Hour), false},
		{"2026-11-20T08:00:00+01:00", time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC), false},
		{"2027-01-01T00:00:00Z", now, false},
		{"now+1h", time.Time{}, true},
		{"yesterday", time.Time{}, true},
		{"now-x", time.Time{}, true},
	} {
		got, err := parseAt(tc.in, now)
		if (err != nil) != tc.bad || (!tc.bad && !got.Equal(tc.want)) {
			t.Errorf("parseAt(%q) = %v, %v; want %v, bad %v", tc.in, got, err, tc.want, tc.bad)
		}
	}
}
