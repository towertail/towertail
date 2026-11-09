package alerter

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

const metricLongProc = "long_proc"

// procKey is the identity of one process across samples. A reused PID
// with a new start time is a new key. The name is the key only when the
// start time is not known.
type procKey struct {
	pid   int32
	start int64
	name  string
}

func newProcKey(p clickhouse.ProcRow) procKey {
	if p.StartTS.IsZero() {
		return procKey{pid: p.PID, name: p.Name}
	}
	return procKey{pid: p.PID, start: p.StartTS.Unix()}
}

// metric is the state key of the process: "long_proc/<pid>/<start_unix>".
func (k procKey) metric() string {
	id := strconv.FormatInt(k.start, 10)
	if k.name != "" {
		id = k.name
	}
	return fmt.Sprintf("%s/%d/%s", metricLongProc, k.pid, id)
}

type procPoint struct {
	t    time.Time
	high bool
}

type procEntry struct {
	proc      clickhouse.ProcRow
	points    []procPoint
	lastAt    time.Time
	highSince time.Time
	flagged   bool
	// present is false when the latest sample did not list the key. The
	// key keeps its history but is not reported: it can be killed.
	present bool
}

// procChange is one process that is newly flagged (flagged true) or no
// longer flagged.
type procChange struct {
	key       procKey
	proc      clickhouse.ProcRow
	highSince time.Time
	flagged   bool
}

// procTracker follows the processes of one node, with the same rules as
// the apps (LongProcTracker). It stores only level changes, so a 24 h
// window stays small.
type procTracker struct {
	entries  map[procKey]*procEntry
	reported map[procKey]procChange
}

func newProcTracker() *procTracker {
	return &procTracker{entries: map[procKey]*procEntry{}, reported: map[procKey]procChange{}}
}

func (t *procTracker) empty() bool { return len(t.entries) == 0 && len(t.reported) == 0 }

// observe feeds one sample and gives the changes of the flagged set.
// ignored holds lowercase names.
func (t *procTracker) observe(items []clickhouse.ProcRow, at time.Time, rule store.LongProcRule, ignored map[string]bool) []procChange {
	if !rule.Enabled {
		clear(t.entries)
		return t.diff(at)
	}
	for _, e := range t.entries {
		if at.Before(e.lastAt) {
			clear(t.entries)
			break
		}
	}

	seen := map[procKey]bool{}
	for _, p := range items {
		if ignored[strings.ToLower(p.Name)] {
			continue
		}
		k := newProcKey(p)
		if seen[k] {
			continue
		}
		seen[k] = true
		high := float64(p.CPUPct) >= rule.CPUPct
		if e, ok := t.entries[k]; ok {
			e.proc = p
			e.present = true
			e.record(high, at, rule)
		} else if high {
			e := &procEntry{proc: p, lastAt: at, present: true}
			e.record(true, at, rule)
			t.entries[k] = e
		}
	}
	for k, e := range t.entries {
		if seen[k] {
			continue
		}
		if ignored[strings.ToLower(e.proc.Name)] {
			delete(t.entries, k)
			continue
		}
		e.present = false
		e.record(false, at, rule)
	}
	for k, e := range t.entries {
		if !slices.ContainsFunc(e.points, func(p procPoint) bool { return p.high }) {
			delete(t.entries, k)
		}
	}
	return t.diff(at)
}

// drop forgets all processes and gives a clear change for each
// reported one.
func (t *procTracker) drop(at time.Time) []procChange {
	clear(t.entries)
	return t.diff(at)
}

// diff compares the flagged set with the reported set and updates the
// reported set. New keys come first, by CPU.
func (t *procTracker) diff(at time.Time) []procChange {
	var added, removed []procChange
	now := map[procKey]bool{}
	for k, e := range t.entries {
		if !e.flagged || !e.present || e.highSince.IsZero() {
			continue
		}
		now[k] = true
		c := procChange{key: k, proc: e.proc, highSince: e.highSince, flagged: true}
		if _, ok := t.reported[k]; !ok {
			added = append(added, c)
		}
		t.reported[k] = c
	}
	for k, c := range t.reported {
		if now[k] {
			continue
		}
		delete(t.reported, k)
		c.flagged = false
		removed = append(removed, c)
	}
	slices.SortFunc(added, func(a, b procChange) int { return cmp.Compare(b.proc.CPUPct, a.proc.CPUPct) })
	slices.SortFunc(removed, func(a, b procChange) int { return cmp.Compare(a.key.pid, b.key.pid) })
	return append(added, removed...)
}

func (e *procEntry) record(high bool, at time.Time, rule store.LongProcRule) {
	if n := len(e.points); n == 0 || e.points[n-1].high != high {
		e.points = append(e.points, procPoint{t: at, high: high})
	}
	e.lastAt = at
	if high && e.highSince.IsZero() {
		e.highSince = at
	}
	start := at.Add(-time.Duration(rule.SustainSeconds) * time.Second)
	for len(e.points) > 1 && !e.points[1].t.After(start) {
		e.points = e.points[1:]
	}
	full := len(e.points) > 0 && !e.points[0].t.After(start)
	e.flagged = full && highFraction(e.points, start, at) >= store.LongProcTolerance
	// The run ends when its own high fraction falls below the tolerance.
	if !e.flagged && !e.highSince.IsZero() {
		from := e.highSince
		if start.After(from) {
			from = start
		}
		if at.After(from) && highFraction(e.points, from, at) < store.LongProcTolerance {
			e.highSince = time.Time{}
			if high {
				e.highSince = at
			}
		}
	}
}

// highFraction is the time-weighted high fraction over start..now. Each
// point holds its level until the next point.
func highFraction(points []procPoint, start, now time.Time) float64 {
	span := now.Sub(start)
	if span <= 0 {
		return 0
	}
	var high time.Duration
	for i, p := range points {
		if !p.high {
			continue
		}
		end := now
		if i+1 < len(points) {
			end = points[i+1].t
		}
		from := p.t
		if start.After(from) {
			from = start
		}
		if d := end.Sub(from); d > 0 {
			high += d
		}
	}
	return float64(high) / float64(span)
}

// shortName is the name cut at 24 characters, the same as the apps.
func (c procChange) shortName() string {
	n := c.proc.Name
	if n == "" {
		return fmt.Sprintf("pid %d", c.proc.PID)
	}
	if r := []rune(n); len(r) > 24 {
		return string(r[:23]) + "…"
	}
	return n
}

// detail is the alert text, for example
// "OrbStack Helper (pid 812) at 85% CPU for 32m".
func (c procChange) detail(at time.Time) string {
	if !c.flagged {
		return fmt.Sprintf("%s (pid %d) is no longer over the CPU limit.", c.shortName(), c.proc.PID)
	}
	return fmt.Sprintf("%s (pid %d) at %.0f%% CPU for %s.", c.shortName(), c.proc.PID, c.proc.CPUPct, shortDuration(at.Sub(c.highSince)))
}

func (c procChange) payload() map[string]any {
	p := map[string]any{
		"pid":        c.proc.PID,
		"name":       c.proc.Name,
		"cmd":        c.proc.Cmd,
		"cpu_pct":    c.proc.CPUPct,
		"high_since": c.highSince,
		"start_ts":   nil,
	}
	if !c.proc.StartTS.IsZero() {
		p["start_ts"] = c.proc.StartTS
	}
	return p
}

// shortDuration gives "30s", "2m" or "68h", the same as the apps.
func shortDuration(d time.Duration) string {
	s := max(int(d.Seconds()), 0)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	default:
		return fmt.Sprintf("%dh", s/3600)
	}
}
