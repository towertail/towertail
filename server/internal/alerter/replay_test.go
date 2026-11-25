package alerter

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

var replayStart = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

// minutes gives one point each minute. The values come in runs: n values
// of v.
func minutes(runs ...[2]float64) []ReplayPoint {
	var out []ReplayPoint
	for _, r := range runs {
		for range int(r[0]) {
			out = append(out, ReplayPoint{At: replayStart.Add(time.Duration(len(out)) * time.Minute), Value: r[1]})
		}
	}
	return out
}

var replayLimits = store.ThresholdPair{Warn: 0.7, Critical: 0.9}

func TestReplayCounts(t *testing.T) {
	sustain := store.AlertRule{SustainSeconds: 300, Notify: store.NotifyAll}
	got := NewReplay(sustain, 0.8, 0, 0).Run(minutes([2]float64{10, 0.1}, [2]float64{6, 0.8}, [2]float64{10, 0.1}), replayLimits)
	if got.Alerts != 1 || got.Critical != 0 {
		t.Errorf("6 min over warn: %+v, want 1 alert", got)
	}
	got = NewReplay(sustain, 0.8, 0, 0).Run(minutes([2]float64{10, 0.1}, [2]float64{3, 0.8}, [2]float64{10, 0.1}), replayLimits)
	if got.Alerts != 0 {
		t.Errorf("3 min over warn: %+v, want no alert", got)
	}
}

func TestReplayCooldown(t *testing.T) {
	instant := store.AlertRule{Notify: store.NotifyAll}
	pts := minutes([2]float64{1, 0.1}, [2]float64{1, 0.8}, [2]float64{1, 0.1}, [2]float64{1, 0.8}, [2]float64{1, 0.1})
	if got := NewReplay(instant, 0.8, 0, 600*time.Second).Run(pts, replayLimits); got.Alerts != 2 || got.Notifications != 2 {
		t.Errorf("cooldown 600 s: %+v, want 2 alerts and 2 notifications", got)
	}
	if got := NewReplay(instant, 0.8, 0, 60*time.Second).Run(pts, replayLimits); got.Notifications != 4 {
		t.Errorf("cooldown 60 s: %+v, want 4 notifications", got)
	}
}

func TestReplayNotifyLevel(t *testing.T) {
	warnOnly := minutes([2]float64{1, 0.1}, [2]float64{3, 0.8}, [2]float64{2, 0.1})
	got := NewReplay(store.AlertRule{Notify: store.NotifyCritical}, 0.8, 0, 0).Run(warnOnly, replayLimits)
	if got.Alerts != 1 || got.Notifications != 0 {
		t.Errorf("notify critical, warn alert: %+v, want 1 alert and no notification", got)
	}
	rise := minutes([2]float64{1, 0.1}, [2]float64{2, 0.8}, [2]float64{2, 0.95}, [2]float64{2, 0.1})
	got = NewReplay(store.AlertRule{Notify: store.NotifyCritical}, 0.8, 0, 0).Run(rise, replayLimits)
	if got.Alerts != 1 || got.Critical != 1 || got.Notifications != 2 {
		t.Errorf("notify critical, critical alert: %+v, want 1 alert, 1 critical, the critical and the recovery", got)
	}
	if got := NewReplay(store.AlertRule{Notify: store.NotifyOff}, 0.8, 0, 0).Run(rise, replayLimits); got.Notifications != 0 {
		t.Errorf("notify off: %+v", got)
	}
}

func TestReplayTime(t *testing.T) {
	pts := minutes([2]float64{1, 0.1}, [2]float64{2, 0.8}, [2]float64{3, 0.95}, [2]float64{2, 0.1})
	got := NewReplay(store.AlertRule{Notify: store.NotifyAll}, 0.8, 0, 0).Run(pts, replayLimits)
	if got.WarnTime != 2*time.Minute || got.CriticalTime != 3*time.Minute {
		t.Errorf("times = %v warn, %v critical; want 2m and 3m", got.WarnTime, got.CriticalTime)
	}
}

func TestReplayHigherLimitsGiveFewerAlerts(t *testing.T) {
	pts := minutes([2]float64{5, 0.1}, [2]float64{5, 0.75}, [2]float64{5, 0.1}, [2]float64{5, 0.95}, [2]float64{5, 0.1})
	r := NewReplay(store.AlertRule{Notify: store.NotifyAll}, 0.8, 0, 0)
	low, high := r.Run(pts, replayLimits), r.Run(pts, store.ThresholdPair{Warn: 0.96, Critical: 0.99})
	if low.Alerts != 2 || high.Alerts != 0 {
		t.Errorf("alerts = %d and %d, want 2 and 0", low.Alerts, high.Alerts)
	}
}

// The same samples give the same tint changes through the engine and the
// level machine.
func TestLevelMachineMatchesEngine(t *testing.T) {
	cfg := instantSettings()
	cfg.Alerts.CPU = store.AlertRule{SustainSeconds: 120, Notify: store.NotifyAll}
	cfg.NotifyDebounceSeconds = 60
	rules := cfg.Rules()
	limits, tolerance := rules.Thresholds.Pair(store.MetricCPU), rules.Alerts.Tolerance

	var values []float64
	for _, run := range [][2]float64{{10, 0.1}, {20, 0.95}, {5, 0.1}, {20, 0.95}, {30, 0.1}, {20, 0.8}, {30, 0.1}} {
		for range int(run[0]) {
			values = append(values, run[1])
		}
	}

	clock := &testClock{t: time.Unix(1700000000, 0).UTC()}
	sink := &capturingSink{}
	e := New(nil, clock, sink, nopStore{}, newFakeConfig(cfg))
	org, node := uuid.New(), uuid.New()
	var m levelMachine
	var want, got []Tint
	for _, v := range values {
		e.Evaluate(context.Background(), org, node, nil, Reading{CPU: v, Mem: 0.1, Disk: 0.1})
		if to, changed := m.step(rawTint(v, limits), clock.Now(), 120*time.Second, tolerance, 60*time.Second); changed {
			want = append(want, to)
		}
		clock.advance(10 * time.Second)
	}
	for _, ev := range sink.snapshot() {
		if ev.Metric == "cpu" {
			got = append(got, Tint(ev.Tint))
		}
	}
	if len(want) < 4 || !slices.Equal(got, want) {
		t.Fatalf("engine changes %v, level machine changes %v", got, want)
	}
}
