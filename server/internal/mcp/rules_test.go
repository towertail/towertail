package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
)

func queryRules(eff store.EffectiveRules) query.RulesResult {
	return query.RulesResult{Effective: &eff}
}

func TestHostRules(t *testing.T) {
	f := newFake()
	node := f.nodes[0]
	snooze := now.Add(time.Hour)
	node.SnoozedUntil = &snooze
	node.NotifyOnWarn = true
	node.ThresholdOverrides = &store.ThresholdOverrides{CPU: &store.ThresholdPair{Warn: 0.5, Critical: 0.6}}
	node.AlertOverrides = &store.AlertOverrides{Disk: &store.AlertRule{SustainSeconds: 30, Notify: store.NotifyCritical}}
	node.MutedSignals = store.MutedSignals{store.MetricZombies}
	node.IgnoredProcs = []string{"backup"}
	f.nodes[0] = node
	cs := connect(t, f, tokenA())

	org := decode[HostRulesResult](t, call(t, cs, "host_rules", nil))
	if org.Host != nil || org.Effective != nil || f.gotRulesFor.String() != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("org call = %+v", org)
	}
	if org.Org.Thresholds["cpu"].Warn != 0.75 || org.Org.CooldownSeconds != 600 || org.Org.Alerts.CPU.SustainSeconds != 300 || org.Org.LongProc.CPUPct != 50 {
		t.Fatalf("org = %+v", org.Org)
	}
	if org.Units["cpu"] != "ratio 0 to 1" || org.Units["procs"] != "count" || org.Units["memPressure"] != "percent" || len(org.Units) != 10 || len(org.Org.Thresholds) != 10 {
		t.Fatalf("units = %v", org.Units)
	}

	got := decode[HostRulesResult](t, call(t, cs, "host_rules", map[string]any{"host": "web-1"}))
	if f.gotRulesFor != node.ID || got.Host == nil || got.Effective == nil {
		t.Fatalf("host call = %+v", got)
	}
	h := got.Host
	if cpu := h.Overrides.Thresholds["cpu"]; cpu.Warn != 0.5 || len(h.Overrides.Thresholds) != 1 {
		t.Errorf("overrides = %+v", h.Overrides.Thresholds)
	}
	if a := h.Overrides.Alerts; a == nil || a.Disk == nil || a.Disk.SustainSeconds != 30 || a.CPU != nil {
		t.Errorf("alert overrides = %+v", h.Overrides.Alerts)
	}
	if !h.NotifyWarn || h.NotifyCritical || h.SnoozedUntil != ts(snooze) || len(h.Overrides.IgnoredProcs) != 1 || h.Overrides.Muted[0] != "zombies" {
		t.Errorf("host = %+v", h)
	}
	e := got.Effective
	if e.Thresholds["cpu"].Warn != 0.5 || e.Sources.Thresholds["cpu"] != "host" || e.Thresholds["mem"].Warn != 0.75 || e.Sources.Thresholds["mem"] != "org" {
		t.Errorf("effective thresholds = %+v, sources = %+v", e.Thresholds, e.Sources.Thresholds)
	}
	if e.Alerts.Disk.Notify != "critical" || e.Sources.Alerts["disk"] != "host" || e.Sources.Muted["zombies"] != "host" || e.Sources.LongProc != "org" {
		t.Errorf("effective = %+v", e)
	}
}

func TestHostRulesUnknownHost(t *testing.T) {
	cs := connect(t, newFake(), tokenA())
	if res := call(t, cs, "host_rules", map[string]any{"host": "ghost"}); !res.IsError || !strings.Contains(text(res), "host not found") {
		t.Fatalf("IsError = %v, text = %q", res.IsError, text(res))
	}
}
