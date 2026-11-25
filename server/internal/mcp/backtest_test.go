package mcp

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/alerter"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
)

func TestSuggestThresholdsTool(t *testing.T) {
	f := newFake()
	web := f.nodes[0]
	f.suggest = &query.Suggestion{
		From: now.Add(-7 * 24 * time.Hour), To: now, Clamped: true, Approximate: true, Resolution: time.Minute,
		Metric: store.MetricCPU, Hosts: 1, ActualAlerts: 3, ActualSends: 2,
		Percentiles: map[string]float64{"p95": 0.6, "max": 0.9},
		Candidates: []query.Candidate{
			{Pair: store.ThresholdPair{Warn: 0.75, Critical: 0.9}, SustainSeconds: 300, Current: true, HostsAlerting: 1,
				Count: alerter.ReplayCount{Alerts: 4, Critical: 1, Notifications: 2, WarnTime: 10 * time.Minute, CriticalTime: 5 * time.Minute}},
			{Pair: store.ThresholdPair{Warn: 0.85, Critical: 0.95}, SustainSeconds: 300},
		},
	}
	cs := connect(t, f, tokenA())
	out := decode[SuggestThresholdsOut](t, call(t, cs, "suggest_thresholds", map[string]any{
		"metric": "cpu", "host": "web-1", "sustain_seconds": 0, "candidates": []map[string]any{{"warn": 0.85, "critical": 0.95}},
	}))

	req := f.gotSuggest
	if req.OrgID != orgA || req.NodeID != web.ID || req.Metric != store.MetricCPU || !req.From.Equal(now.Add(-7*24*time.Hour)) || !req.To.Equal(now) {
		t.Fatalf("request = %+v", req)
	}
	if req.SustainSeconds == nil || *req.SustainSeconds != 0 || len(req.Candidates) != 1 || req.Candidates[0] != (store.ThresholdPair{Warn: 0.85, Critical: 0.95}) {
		t.Fatalf("request options = %+v", req)
	}
	if !out.Clamped || !out.Approximate || out.Resolution != "1m0s" || out.Unit != "ratio 0 to 1" || out.Hosts != 1 || out.ActualAlerts != 3 || out.ActualSends != 2 {
		t.Fatalf("head = %+v", out)
	}
	if out.Percentiles["max"] != 0.9 || len(out.Candidates) != 2 {
		t.Fatalf("body = %+v", out)
	}
	c := out.Candidates[0]
	if !c.Current || c.Alerts != 4 || c.CriticalAlerts != 1 || c.Notifications != 2 || c.HostsAlerting != 1 || c.WarnSeconds != 600 || c.CriticalSeconds != 300 || c.SustainSeconds != 300 {
		t.Errorf("current = %+v", c)
	}
	if out.Candidates[1].Current || out.Candidates[1].Alerts != 0 {
		t.Errorf("second = %+v", out.Candidates[1])
	}
}

func TestSuggestThresholdsToolDefaults(t *testing.T) {
	f := newFake()
	cs := connect(t, f, tokenA())
	out := decode[SuggestThresholdsOut](t, call(t, cs, "suggest_thresholds", map[string]any{"metric": "mem"}))
	if f.gotSuggest.NodeID != uuid.Nil || f.gotSuggest.SustainSeconds != nil || !f.gotSuggest.From.Equal(now.Add(-7*24*time.Hour)) {
		t.Fatalf("defaults: %+v", f.gotSuggest)
	}
	if out.Percentiles == nil || out.Candidates == nil || out.Metric != "mem" {
		t.Fatalf("empty maps and lists must not be null: %+v", out)
	}
	f.suggestErr = query.ErrBadRange
	if res := call(t, cs, "suggest_thresholds", map[string]any{"metric": "mem"}); !res.IsError {
		t.Fatalf("want an error: %s", text(res))
	}
}
