package mcp

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

func TestExplainChangeTool(t *testing.T) {
	f := newFake()
	web := f.nodes[0]
	cp := now.Add(-3 * time.Hour)
	start := cp.Add(5 * time.Minute)
	api := wire.ProcessKey{Name: "api", CmdHash: 7}
	f.change = &query.ChangeReport{
		From: now.Add(-24 * time.Hour), To: now, Bucket: 5 * time.Minute, Clamped: true, Metric: "mem", By: clickhouse.ProcByMem,
		Points: []query.ChangePoint{{TS: cp, Before: 0.3, After: 0.8, Delta: 0.5}},
		Before: wire.Window{From: cp.Add(-time.Hour), To: cp}, After: wire.Window{From: cp, To: cp.Add(time.Hour)},
		Processes: []query.ProcDelta{
			{Key: api, Name: "api", Cmd: "api --serve", Before: 1 << 30, After: 6 << 30, Delta: 5 << 30},
			{Key: wire.ProcessKey{Name: "batch", CmdHash: 9}, Name: "batch", Cmd: "batch", After: 1 << 30, Delta: 1 << 30, New: true},
		},
		Alerts: []wire.Alert{{ID: uuid.New(), NodeID: web.ID, Kind: "threshold_crossed", Metric: "mem", Tint: "warn", TS: start, Start: &start, State: "open", Value: 0.85}},
	}
	cs := connect(t, f, tokenA())
	out := decode[ExplainChangeOut](t, call(t, cs, "explain_change", map[string]any{"host": "web-1", "metric": "mem"}))

	req := f.gotChange
	if req.OrgID != orgA || req.NodeID != web.ID || req.Metric != "mem" || !req.From.Equal(now.Add(-24*time.Hour)) || !req.To.Equal(now) {
		t.Fatalf("request = %+v", req)
	}
	if out.Host != "web-1" || out.HostID != web.ID.String() || !out.Clamped || out.Resolution != "5m0s" || out.Unit != "ratio 0 to 1" || out.Metric != "mem" {
		t.Fatalf("head = %+v", out)
	}
	if len(out.Points) != 1 || out.Points[0].TS != "2026-11-20T09:00:00Z" || out.Points[0].Delta != 0.5 {
		t.Fatalf("points = %+v", out.Points)
	}
	if out.Before == nil || out.After == nil || out.Before.To != out.After.From || out.After.From != out.Points[0].TS {
		t.Fatalf("windows = %+v %+v", out.Before, out.After)
	}
	if out.ProcessesBy != "mem" || out.ProcessUnit != "bytes" || len(out.Processes) != 2 {
		t.Fatalf("processes = %q %q %+v", out.ProcessesBy, out.ProcessUnit, out.Processes)
	}
	if p := out.Processes[0]; p.Key != api.String() || p.Command != "api --serve" || p.Delta != 5<<30 || p.New {
		t.Errorf("first = %+v", p)
	}
	if p := out.Processes[1]; !p.New || p.Gone {
		t.Errorf("second = %+v", p)
	}
	if len(out.Alerts) != 1 || out.Alerts[0].Host != "web-1" || out.Alerts[0].Metric != "mem" || out.Alerts[0].Start != ts(start) {
		t.Errorf("alerts = %+v", out.Alerts)
	}
}

func TestExplainChangeToolDefaults(t *testing.T) {
	f := newFake()
	cs := connect(t, f, tokenA())
	out := decode[ExplainChangeOut](t, call(t, cs, "explain_change", map[string]any{"host": "web-1", "metric": "cpu"}))
	if !f.gotChange.From.Equal(now.Add(-24 * time.Hour)) {
		t.Fatalf("default range: from = %v", f.gotChange.From)
	}
	if out.Points == nil || out.Processes == nil || out.Alerts == nil || out.Before != nil || out.After != nil {
		t.Fatalf("no change point: lists must be empty and windows absent: %+v", out)
	}
	if out.ProcessesBy != "cpu" || out.ProcessUnit != "percent of one core" || out.Unit != "percent" {
		t.Errorf("units = %+v", out)
	}
	f.changeErr = query.ErrNoHistory
	if res := call(t, cs, "explain_change", map[string]any{"host": "web-1", "metric": "cpu"}); !res.IsError {
		t.Fatalf("want an error: %s", text(res))
	}
}
