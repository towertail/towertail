package mcp

import (
	"slices"
	"strings"
	"testing"

	"github.com/towertail/towertail/server/internal/clickhouse"
)

func TestReferenceTopics(t *testing.T) {
	cs := connect(t, newFake(), tokenA())
	for _, topic := range referenceTopics {
		out := decode[ReferenceOut](t, call(t, cs, "reference", map[string]any{"topic": topic}))
		if out.Topic != topic || len(out.Facts) == 0 {
			t.Errorf("%s: out = %+v", topic, out)
		}
		for _, f := range out.Facts {
			if f.Name == "" || f.Text == "" {
				t.Errorf("%s: fact %+v has no name or text", topic, f)
			}
		}
	}
	facts := func(topic string) map[string]ReferenceFact {
		out := decode[ReferenceOut](t, call(t, cs, "reference", map[string]any{"topic": topic}))
		m := map[string]ReferenceFact{}
		for _, f := range out.Facts {
			m[f.Name] = f
		}
		return m
	}
	metrics := facts("metrics")
	for _, name := range clickhouse.MetricNames() {
		if f, ok := metrics[name]; !ok || f.Unit == "" {
			t.Errorf("metrics: %s has no fact with a unit: %+v", name, f)
		}
	}
	th := facts("thresholds")
	if f := th["cooldown"]; f.Value == nil || *f.Value != 600 || f.Unit != "seconds" {
		t.Errorf("cooldown = %+v, want 600 seconds", f)
	}
	if f := th["cpu.warn"]; f.Value == nil || *f.Value != 0.75 || f.Unit != "ratio 0 to 1" {
		t.Errorf("cpu.warn = %+v", f)
	}
	if f := th["memPressure.critical"]; f.Value == nil || *f.Value != 20 || f.Unit != "percent" {
		t.Errorf("memPressure.critical = %+v", f)
	}
	if f := th["procs.warn"]; f.Value == nil || *f.Value != 5000 || f.Unit != "count" {
		t.Errorf("procs.warn = %+v", f)
	}
	lp := facts("long_process")
	if f := lp["cpu"]; f.Value == nil || *f.Value != 50 {
		t.Errorf("long_process cpu = %+v", f)
	}
	if f := lp["cpu.max"]; f.Value == nil || *f.Value != 800 {
		t.Errorf("long_process cpu.max = %+v", f)
	}
	if f := lp["sustain.min"]; f.Value == nil || *f.Value != 60 {
		t.Errorf("long_process sustain.min = %+v", f)
	}
}

func TestReferencePlan(t *testing.T) {
	info := &fakeOrgInfo{facts: OrgFacts{Plan: "team", HistoryDays: 30, MaxHosts: 25}}
	cs := connectOpts(t, newFake(), tokenA(), func(o *Options) { o.OrgInfo = info })
	out := decode[ReferenceOut](t, call(t, cs, "reference", map[string]any{"topic": "plan"}))
	got := map[string]float64{}
	for _, f := range out.Facts {
		if f.Value != nil {
			got[f.Name] = *f.Value
		}
	}
	if got["history_days"] != 30 || got["max_hosts"] != 25 {
		t.Fatalf("facts = %+v", out.Facts)
	}
}

func TestReferenceNoInstructions(t *testing.T) {
	deny := []string{"Call ", "Use ", "Ask ", "Tell "}
	for _, topic := range referenceTopics {
		out, err := NewReference().Topic(topic, OrgFacts{Plan: "self-hosted"})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range out.Facts {
			if slices.ContainsFunc(deny, func(v string) bool { return strings.HasPrefix(f.Text, v) }) {
				t.Errorf("%s/%s: text %q starts with an imperative verb", topic, f.Name, f.Text)
			}
		}
	}
}

func TestReferenceUnknownTopic(t *testing.T) {
	if _, err := NewReference().Topic("bogus", OrgFacts{}); err == nil || !strings.Contains(err.Error(), "topic must be one of") {
		t.Fatalf("err = %v, want an input error", err)
	}
}
