package mcp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

func TestAlertsTool(t *testing.T) {
	f := newFake()
	cs := connect(t, f, tokenA())

	out := decode[AlertsOut](t, call(t, cs, "alerts", map[string]any{
		"state": "action", "range": "24h", "kind": []string{"cpu"}, "tint": []string{"critical"}, "q": "web", "sort": "start", "limit": 10,
	}))
	q := f.gotAlertQ
	if q.State != "action" || !q.From.Equal(now.Add(-24*time.Hour)) || !q.To.Equal(now) || q.Limit != 10 || q.Sort != "start" || !q.Desc || q.Q != "web" {
		t.Fatalf("query = %+v", q)
	}
	if len(q.Kinds) != 1 || q.Kinds[0] != "cpu" || len(q.Tints) != 1 || q.Tints[0] != "critical" {
		t.Fatalf("filters = %+v", q)
	}
	if out.Total != 1 || out.Counts.All != 3 || out.Counts.Open != 1 || !out.Clamped || len(out.Alerts) != 1 {
		t.Fatalf("out = %+v", out)
	}
	a := out.Alerts[0]
	if a.Peak == nil || *a.Peak != 0.99 || a.State != "open" || a.HostID != f.nodes[0].ID.String() {
		t.Fatalf("alert = %+v", a)
	}

	decode[AlertsOut](t, call(t, cs, "alerts", nil))
	q = f.gotAlertQ
	if q.State != "open" || q.Limit != 50 || q.Sort != "sev" || !q.From.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("default query = %+v", q)
	}

	decode[AlertsOut](t, call(t, cs, "alerts", map[string]any{"state": "recent"}))
	if q = f.gotAlertQ; q.State != "all" || !q.From.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("recent query = %+v, want all over 7d", q)
	}

	decode[AlertsOut](t, call(t, cs, "alerts", map[string]any{"sort": "host"}))
	if f.gotAlertQ.Desc {
		t.Error("a text sort must give A to Z")
	}

	for name, args := range map[string]map[string]any{
		"limit": {"limit": 101},
		"kind":  {"kind": []string{"bogus"}},
		"tint":  {"tint": []string{"ok"}},
		"sort":  {"sort": "bogus"},
	} {
		res := call(t, cs, "alerts", args)
		if !res.IsError || !strings.Contains(text(res), name) {
			t.Errorf("%s: IsError = %v, text = %q; want an input error", name, res.IsError, text(res))
		}
	}
}

func TestAlertItemFromOpenList(t *testing.T) {
	by := uuid.New()
	a := wire.Alert{ID: uuid.New(), NodeID: uuid.New(), Kind: "threshold_crossed", Metric: "cpu", Tint: wire.TintWarn, TS: now, AckedBy: &by}
	got := newAlertItem(a, "web-1", now)
	if got.Severity != "warn" || got.State != "acked" || got.Start != ts(now) || got.AckedBy != by.String() {
		t.Fatalf("item = %+v", got)
	}
	until := now.Add(time.Hour)
	a.AckedBy, a.SnoozedUntil = nil, &until
	if got = newAlertItem(a, "web-1", now); got.State != "snoozed" {
		t.Errorf("state = %q, want snoozed", got.State)
	}
	if got = newAlertItem(a, "web-1", now.Add(2*time.Hour)); got.State != "open" {
		t.Errorf("state = %q, want open after the snooze", got.State)
	}
}

func TestGetAlert(t *testing.T) {
	f := newFake()
	node := f.nodes[0]
	node.AlertOverrides = &store.AlertOverrides{CPU: &store.AlertRule{SustainSeconds: 60, Notify: store.NotifyAll}}
	f.nodes[0] = node
	id, user := uuid.New(), uuid.New()
	start := now.Add(-time.Hour)
	f.alertDetail = wire.AlertDetail{
		Alert:  wire.Alert{ID: id, NodeID: node.ID, NodeName: "web-1", Kind: "threshold_crossed", Metric: "cpu", Severity: wire.TintWarn, State: "acked", Start: &start, TS: now, Value: 0.8},
		Limits: &wire.AlertLimits{Warn: 0.75, Critical: 0.9},
		Timeline: []wire.AlertLine{
			{TS: start, Kind: "opened", Tint: wire.TintWarn},
			{TS: start.Add(5 * time.Minute), Kind: "notified", Text: "Email ops"},
			{TS: start.Add(10 * time.Minute), Kind: "acked", User: &user},
		},
	}
	cs := connect(t, f, tokenA())
	out := decode[GetAlertOut](t, call(t, cs, "get_alert", map[string]any{"alert_id": id.String()}))
	if f.gotAlertID != id || f.gotRulesFor != node.ID {
		t.Fatalf("asked for alert %s and rules of %s", f.gotAlertID, f.gotRulesFor)
	}
	if out.Alert.ID != id.String() || out.Alert.Host != "web-1" || out.Notifications != 1 || len(out.Timeline) != 3 || out.Timeline[2].User != user.String() {
		t.Fatalf("out = %+v", out)
	}
	if out.Limits == nil || out.Limits.Warn != 0.75 || out.Limits.Unit != "ratio 0 to 1" {
		t.Fatalf("limits = %+v", out.Limits)
	}
	r := out.RuleNow
	if r == nil || r.Kind != "cpu" || r.Warn == nil || *r.Warn != 0.75 || r.SustainSeconds != 60 || r.Notify != "all" || r.Source != "host" {
		t.Fatalf("rule_now = %+v", r)
	}

	t.Run("host gone", func(t *testing.T) {
		f.rulesErr = store.ErrNotFound
		out := decode[GetAlertOut](t, call(t, cs, "get_alert", map[string]any{"alert_id": id.String()}))
		if out.RuleNow != nil || out.Alert.ID != id.String() {
			t.Fatalf("out = %+v, want the alert and no rule_now", out)
		}
		f.rulesErr = nil
	})
	t.Run("unknown id", func(t *testing.T) {
		f.alertErr = clickhouse.ErrNoAlert
		res := call(t, cs, "get_alert", map[string]any{"alert_id": uuid.NewString()})
		if !res.IsError || text(res) != "alert not found" {
			t.Fatalf("IsError = %v, text = %q", res.IsError, text(res))
		}
		f.alertErr = errors.New("boom")
		if res := call(t, cs, "get_alert", map[string]any{"alert_id": uuid.NewString()}); !res.IsError || strings.Contains(text(res), "boom") {
			t.Fatalf("text = %q, want the error hidden", text(res))
		}
	})
	t.Run("bad id", func(t *testing.T) {
		if res := call(t, cs, "get_alert", map[string]any{"alert_id": "x"}); !res.IsError || !strings.Contains(text(res), "alert_id must be") {
			t.Fatalf("text = %q", text(res))
		}
	})
}

func TestRuleNowKinds(t *testing.T) {
	eff := store.DefaultServerSettings().Rules().For(store.HostRules{})
	r := queryRules(eff)
	if got := ruleNow(wire.Alert{Kind: "reachability_changed", Metric: "reachability"}, r); got != nil {
		t.Errorf("nodata rule = %+v, want none", got)
	}
	long := ruleNow(wire.Alert{Kind: "long_process", Metric: "long_proc"}, r)
	if long == nil || long.Kind != "long" || long.CPUPct == nil || *long.CPUPct != 50 || long.SustainSeconds != 1800 {
		t.Errorf("long rule = %+v", long)
	}
	health := ruleNow(wire.Alert{Kind: "threshold_crossed", Metric: "health"}, r)
	if health == nil || health.Kind != "health" || health.Warn != nil || health.SustainSeconds != 300 || health.Source != "org" {
		t.Errorf("health rule = %+v", health)
	}
	disk := ruleNow(wire.Alert{Kind: "threshold_crossed", Metric: "disk"}, r)
	if disk == nil || disk.Warn == nil || disk.Unit != "ratio 0 to 1" {
		t.Errorf("disk rule = %+v", disk)
	}
}
