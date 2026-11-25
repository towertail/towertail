package alerter

import (
	"time"

	"github.com/towertail/towertail/server/store"
)

// ReplayPoint is one value of a metric. Cpu, mem and disk are ratios from
// 0 to 1, as in a rule.
type ReplayPoint struct {
	At    time.Time
	Value float64
}

// ReplayCount is what a replay of points under a pair of limits gives.
// Critical counts the alerts that reached critical. WarnTime and
// CriticalTime are the time that the state spent at each level.
type ReplayCount struct {
	Alerts, Critical, Notifications int
	WarnTime, CriticalTime          time.Duration
}

// Replay runs stored values through the same rules as the live engine, to
// count the alerts that other limits would have made.
type Replay struct {
	rule      store.AlertRule
	tolerance float64
	debounce  time.Duration
	cooldown  time.Duration
}

// NewReplay makes a replay for one metric. A cooldown of 0 or less gives
// the engine default.
func NewReplay(rule store.AlertRule, tolerance float64, debounce, cooldown time.Duration) *Replay {
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	return &Replay{rule: rule, tolerance: tolerance, debounce: debounce, cooldown: cooldown}
}

// Run replays the points, oldest first, under the limits. It does not
// check the notify flags of the org and of the node. A level holds until
// the next point.
func (r *Replay) Run(points []ReplayPoint, limits store.ThresholdPair) ReplayCount {
	var (
		out     ReplayCount
		m       levelMachine
		lastTo  = map[Tint]time.Time{}
		reached bool
		sustain = time.Duration(r.rule.SustainSeconds) * time.Second
	)
	for i, p := range points {
		from := m.tint
		to, changed := m.step(rawTint(p.Value, limits), p.At, sustain, r.tolerance, r.debounce)
		if changed {
			if from == TintOK {
				out.Alerts++
				reached = false
			}
			if to == TintCritical && !reached {
				out.Critical++
				reached = true
			}
			if r.notifies(lastTo, from, to, p.At) {
				out.Notifications++
			}
		}
		if i+1 < len(points) {
			switch d := points[i+1].At.Sub(p.At); m.tint {
			case TintWarn:
				out.WarnTime += d
			case TintCritical:
				out.CriticalTime += d
			}
		}
	}
	return out
}

// notifies applies the notify level and the cooldown of the engine to a
// change. A change to ok uses the tint that it leaves.
func (r *Replay) notifies(last map[Tint]time.Time, from, to Tint, at time.Time) bool {
	tint := to
	if tint == TintOK {
		tint = from
	}
	if !r.rule.Notify.Allows(tint) {
		return false
	}
	if t, ok := last[to]; ok && at.Sub(t) < r.cooldown {
		return false
	}
	last[to] = at
	return true
}
