package mcp

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

func TestFleetSummaryTool(t *testing.T) {
	f := newFake()
	cpu := 88.0
	web := wire.FleetHost{NodeID: f.nodes[0].ID, Name: "web-1", Online: true, CPU: &cpu}
	f.summary = query.FleetSummary{
		Hosts: 3, Online: 2, Offline: 1,
		OpenAlerts: map[wire.Tint]int{wire.TintCritical: 1, wire.TintWarn: 2},
		Over:       []query.HostOver{{NodeID: web.NodeID, Name: "web-1", Metric: "cpu", Value: 88, Tint: wire.TintWarn}},
		TopCPU:     []wire.FleetHost{web},
		Alerts24h:  4, Notifications24h: 9,
		Tags: map[string]int{"prod": 2},
	}
	cs := connect(t, f, tokenA())
	out := decode[FleetSummaryOut](t, call(t, cs, "fleet_summary", nil))
	if out.Hosts != 3 || out.Online != 2 || out.Offline != 1 || out.OpenAlerts.Critical != 1 || out.OpenAlerts.Warn != 2 {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Over) != 1 || out.Over[0].Host != "web-1" || out.Over[0].HostID != web.NodeID.String() || out.Over[0].Value != 88 || out.Over[0].Tint != "warn" {
		t.Fatalf("over = %+v", out.Over)
	}
	if len(out.TopCPU) != 1 || out.TopCPU[0].CPUPct == nil || len(out.TopMem) != 0 || out.Alerts24h != 4 || out.Notifications24h != 9 || out.Tags["prod"] != 2 {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetHostTool(t *testing.T) {
	f := newFake()
	node := f.nodes[0]
	node.Kind, node.Enabled, node.Tags = "ssh", true, []string{"web"}
	snooze := now.Add(time.Hour)
	node.SnoozedUntil = &snooze
	node.ThresholdOverrides = &store.ThresholdOverrides{CPU: &store.ThresholdPair{Warn: 0.5, Critical: 0.6}}
	cpu := 42.5
	f.detail = &query.HostDetail{
		Node:   node,
		Latest: &wire.FleetHost{NodeID: node.ID, Name: node.DisplayName, Tags: node.Tags, Online: true, LastSeen: &now, CPU: &cpu},
		Facts: &clickhouse.HostFacts{
			SeenAt: now, OS: "linux", Arch: "arm64", Kernel: "6.8.0", Sampler: "1.0+abc", Cores: 8, MemTotal: 16 << 30, UptimeS: 3600,
		},
		OpenAlerts: []wire.Alert{{ID: uuid.New(), NodeID: node.ID, Kind: "threshold_crossed", Metric: "cpu", Tint: wire.TintWarn, TS: now, Value: 0.55}},
		Effective:  store.DefaultServerSettings().Rules().For(node.Rules()),
	}
	cs := connect(t, f, tokenA())
	out := decode[GetHostOut](t, call(t, cs, "get_host", map[string]any{"host": "web-1"}))
	if out.ID != node.ID.String() || out.Name != "web-1" || out.Kind != "ssh" || !out.Enabled || !out.Online || out.CPUPct == nil || *out.CPUPct != 42.5 {
		t.Fatalf("out = %+v", out)
	}
	if out.Facts == nil || out.Facts.OS != "linux" || out.Facts.Cores != 8 || out.Facts.MemTotalBytes != 16<<30 || out.Facts.UptimeS != 3600 {
		t.Fatalf("facts = %+v", out.Facts)
	}
	if out.SnoozedUntil != ts(snooze) {
		t.Errorf("snoozed_until = %q", out.SnoozedUntil)
	}
	if len(out.OpenAlerts) != 1 || out.OpenAlerts[0].Host != "web-1" || out.OpenAlerts[0].Metric != "cpu" {
		t.Fatalf("open alerts = %+v", out.OpenAlerts)
	}
	cpuLimit := out.EffectiveLimits["cpu"]
	if cpuLimit.Warn != 0.5 || cpuLimit.Critical != 0.6 || cpuLimit.Unit != "ratio 0 to 1" || cpuLimit.Source != "host" {
		t.Errorf("cpu limit = %+v", cpuLimit)
	}
	if m := out.EffectiveLimits["mem"]; m.Warn != 0.75 || m.Source != "org" {
		t.Errorf("mem limit = %+v", m)
	}
	if p := out.EffectiveLimits["procs"]; p.Unit != "count" || p.Warn != 5000 {
		t.Errorf("procs limit = %+v", p)
	}
	if len(out.EffectiveLimits) != 10 {
		t.Errorf("limits = %d metrics, want 10", len(out.EffectiveLimits))
	}
}

func TestGetHostOffline(t *testing.T) {
	f := newFake()
	cs := connect(t, f, tokenA())
	out := decode[GetHostOut](t, call(t, cs, "get_host", map[string]any{"host": f.nodes[1].ID.String()}))
	if out.Name != "db-1" || out.Online || out.CPUPct != nil || out.Facts != nil || len(out.OpenAlerts) != 0 {
		t.Fatalf("out = %+v", out)
	}
}
