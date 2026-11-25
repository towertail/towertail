package query

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/wire"
)

func TestProcessesAt(t *testing.T) {
	start := now.Add(-48 * time.Hour)
	r := &fakeReader{snap: clickhouse.Snapshot{
		TS: now.Add(-time.Minute), Tier: "raw", Total: 90,
		Procs: []clickhouse.SnapshotProc{{CmdHash: 7, PID: 4, Name: "api", Cmd: "api -x", CPUPct: 12, StartTS: &start}},
	}}
	s := newService(r, nil, 7)
	org, node := uuid.New(), uuid.New()

	got, err := s.ProcessesAt(context.Background(), SnapshotRequest{OrgID: org, NodeID: node, At: now.Add(-time.Minute), By: clickhouse.ProcByMem, Limit: 99})
	if err != nil {
		t.Fatal(err)
	}
	if r.gotSnap.Limit != MaxProcs || r.gotSnap.By != clickhouse.ProcByMem || r.gotSnap.NodeID != node {
		t.Errorf("snapshot query = %+v", r.gotSnap)
	}
	if got.Tier != "raw" || got.Total != 90 || got.By != "mem" || len(got.Processes) != 1 || got.Processes[0].PID != 4 ||
		got.Processes[0].Key != (wire.ProcessKey{Name: "api", CmdHash: 7}) || got.Processes[0].StartTS == nil {
		t.Errorf("snapshot = %+v", got)
	}

	// An empty bucket keeps the asked time.
	r.snap = clickhouse.Snapshot{}
	at := now.Add(-time.Hour)
	if got, _ = s.ProcessesAt(context.Background(), SnapshotRequest{OrgID: org, NodeID: node, At: at}); !got.TS.Equal(at) || got.By != "cpu" || len(got.Processes) != 0 {
		t.Errorf("empty snapshot = %+v", got)
	}

	// A time older than the plan reads nothing.
	r.gotSnap = clickhouse.SnapshotQuery{}
	got, err = s.ProcessesAt(context.Background(), SnapshotRequest{OrgID: org, NodeID: node, At: now.Add(-30 * 24 * time.Hour)})
	if err != nil || !got.Clamped || len(got.Processes) != 0 || r.gotSnap.NodeID != uuid.Nil {
		t.Errorf("clamped = %+v, %v", got, err)
	}
	if _, err := s.ProcessesAt(context.Background(), SnapshotRequest{By: "disk"}); !errors.Is(err, ErrBadOrder) {
		t.Errorf("bad order: %v", err)
	}
	if _, err := NewService(nil, fakeNodes(nil), nil, nil).ProcessesAt(context.Background(), SnapshotRequest{}); !errors.Is(err, ErrNoHistory) {
		t.Errorf("no ClickHouse: %v", err)
	}
}

func TestServiceProcessStarts(t *testing.T) {
	node := uuid.New()
	exact := clickhouse.ProcStartKey{NodeID: node, Key: wire.NewProcessKey("a", "a")}
	nostart := clickhouse.ProcStartKey{NodeID: node, Key: wire.NewProcessKey("b", "b")}
	rollup := clickhouse.ProcStartKey{NodeID: node, Key: wire.NewProcessKey("c", "c")}
	missing := clickhouse.ProcStartKey{NodeID: node, Key: wire.NewProcessKey("d", "d")}
	born, first := now.Add(-3*time.Hour), now.Add(-5*24*time.Hour)
	r := &fakeReader{
		starts: map[clickhouse.ProcStartKey]clickhouse.ProcStart{
			exact:   {PID: 5, StartTS: &born},
			nostart: {PID: 6},
		},
		firstSeen: map[clickhouse.ProcStartKey]time.Time{nostart: first, exact: first, rollup: first},
	}
	s := newService(r, nil, 10)
	keys := []clickhouse.ProcStartKey{exact, nostart, rollup, missing}

	got, err := s.ProcessStarts(context.Background(), uuid.New(), keys, now)
	if err != nil {
		t.Fatal(err)
	}
	if e := got[exact]; !e.Exact || e.PID != 5 || e.StartedAt == nil || !e.StartedAt.Equal(born) || e.FirstSeen != nil {
		t.Errorf("exact = %+v, want the raw start to win over FirstSeen", e)
	}
	if e := got[nostart]; e.Exact || e.PID != 6 || e.StartedAt != nil || e.FirstSeen == nil || !e.FirstSeen.Equal(first) {
		t.Errorf("no start = %+v", e)
	}
	if e := got[rollup]; e.Exact || e.FirstSeen == nil {
		t.Errorf("rollup = %+v", e)
	}
	if _, ok := got[missing]; ok {
		t.Error("a key with no rows has an entry")
	}
	// The fallback starts at the plan limit: 10 days.
	if !r.gotSeenSince.Equal(now.Add(-10 * 24 * time.Hour)) {
		t.Errorf("first seen since = %v", r.gotSeenSince)
	}

	// Older than 1 hour: no raw read.
	r.gotStarts = nil
	got, _ = s.ProcessStarts(context.Background(), uuid.New(), []clickhouse.ProcStartKey{exact}, now.Add(-2*time.Hour))
	if r.gotStarts != nil || got[exact].Exact || got[exact].FirstSeen == nil {
		t.Errorf("old time: %+v, raw keys %v", got[exact], r.gotStarts)
	}
}

func TestResolveProcess(t *testing.T) {
	node := uuid.New()
	r := &fakeReader{procs: []clickhouse.TopProc{
		{Name: "postgres", Cmd: "postgres -D /a", CmdHash: 1},
		{Name: "postgres", Cmd: "postgres -D /a", CmdHash: 1, Repo: "/srv"},
		{Name: "node", Cmd: "node a.js", CmdHash: 2},
		{Name: "node", Cmd: "node b.js", CmdHash: 3},
		{Name: "redis", Cmd: "redis-server", CmdHash: 4},
	}}
	s := newService(r, nil, 0)
	ctx := context.Background()
	from, to := now.Add(-time.Hour), now

	key := wire.NewProcessKey("python", "python x.py")
	if got, err := s.ResolveProcess(ctx, uuid.New(), node, key.String(), from, to); err != nil || got != key {
		t.Errorf("key text: %+v, %v", got, err)
	}
	if got, err := s.ResolveProcess(ctx, uuid.New(), node, "POSTGRES", from, to); err != nil || got != (wire.ProcessKey{Name: "postgres", CmdHash: 1}) {
		t.Errorf("unique name: %+v, %v", got, err)
	}
	if r.gotN != 50 || r.gotBy != clickhouse.ProcByCPU || r.gotProc.NodeID != node {
		t.Errorf("search = %+v, %v, %d", r.gotProc, r.gotBy, r.gotN)
	}
	_, err := s.ResolveProcess(ctx, uuid.New(), node, "node", from, to)
	if !errors.Is(err, ErrProcessAmbiguous) || !strings.Contains(err.Error(), wire.ProcessKey{Name: "node", CmdHash: 2}.String()) ||
		!strings.Contains(err.Error(), wire.ProcessKey{Name: "node", CmdHash: 3}.String()) || !strings.Contains(err.Error(), "node b.js") {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := s.ResolveProcess(ctx, uuid.New(), node, "ghost", from, to); !errors.Is(err, ErrProcessNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestProcessHistoryService(t *testing.T) {
	node := uuid.New()
	key := wire.ProcessKey{Name: "api", CmdHash: 9}
	born := now.Add(-time.Hour)
	r := &fakeReader{
		procInfo:  func(wire.ProcessKey) (string, string, error) { return "api -x", "/srv", nil },
		procPts:   map[wire.ProcessKey][]clickhouse.ProcPoint{key: {{TS: now.Add(-time.Minute), CPUPct: 3}}},
		procFacts: &clickhouse.ProcFacts{PID: 4},
		starts:    map[clickhouse.ProcStartKey]clickhouse.ProcStart{{NodeID: node, Key: key}: {PID: 4, StartTS: &born}},
	}
	s := newService(r, nil, 7)
	req := ProcessHistoryRequest{OrgID: uuid.New(), NodeID: node, Key: key, From: now.Add(-2 * time.Hour), To: now}

	got, err := s.ProcessHistory(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmd != "api -x" || got.Repo != "/srv" || len(got.Points) != 1 || got.Facts == nil || got.Facts.PID != 4 || !got.Start.Exact {
		t.Errorf("history = %+v", got)
	}
	if got.Step != time.Minute || r.gotSeries.Step != got.Step || r.gotSeries.Keys[0] != key {
		t.Errorf("step = %v, series query = %+v", got.Step, r.gotSeries)
	}

	// To is older than 1 hour: no facts.
	req.From, req.To = now.Add(-5*time.Hour), now.Add(-3*time.Hour)
	if got, _ = s.ProcessHistory(context.Background(), req); got.Facts != nil {
		t.Errorf("old range has facts: %+v", got.Facts)
	}

	// The plan clamps the range.
	req.From, req.To = now.Add(-30*24*time.Hour), now
	if got, _ = s.ProcessHistory(context.Background(), req); !got.Clamped || !got.From.Equal(now.Add(-7*24*time.Hour)) {
		t.Errorf("clamp: %+v", got)
	}

	r.procInfo = nil
	if _, err := s.ProcessHistory(context.Background(), req); !errors.Is(err, ErrProcessNotFound) {
		t.Errorf("no process: %v", err)
	}
}
