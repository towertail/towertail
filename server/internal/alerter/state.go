package alerter

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
)

const (
	metricReachability = "reachability"
	metricHealth       = "health"
	// restoreWindow is how far back Load reads the state.
	restoreWindow = 24 * time.Hour
	// defaultInterval is the sample interval until a node sends two
	// samples. A node is unreachable after 3 intervals.
	defaultInterval = 10 * time.Second
	maxInterval     = 5 * time.Minute
	// neverReportedAfter is the wait for the first sample of a node
	// that just enrolled.
	neverReportedAfter = 10 * time.Minute
)

type stateKey struct {
	org, node uuid.UUID
	metric    string
}

type nodeKey struct{ org, node uuid.UUID }

type sentKey struct {
	stateKey
	tint Tint
}

type metricState struct {
	levelMachine
	// muteSig is the list of muted health checks of the last sample.
	muteSig string
}

// newMetricState makes the state of a metric that has already been seen.
func newMetricState(tint Tint, changed time.Time) *metricState {
	return &metricState{levelMachine: levelMachine{tint: tint, changed: changed, started: true}}
}

// levelMachine follows the sustained level of one metric. The zero value
// is a metric with no sample yet.
type levelMachine struct {
	window  sustainWindow
	tint    Tint
	changed time.Time
	started bool
}

// step takes one raw level and gives the tint of the metric and whether it
// changed. With a sustain time of 0, the first sample sets the tint and no
// change comes. With a longer sustain time, the tint starts at ok and the
// first change comes when the window is full.
func (m *levelMachine) step(raw Tint, at time.Time, sustain time.Duration, tolerance float64, debounce time.Duration) (Tint, bool) {
	if !m.started {
		m.started, m.tint, m.changed = true, TintOK, at
		if sustain == 0 {
			m.tint = raw
			return m.tint, false
		}
	}
	m.window.record(raw, at, sustain)
	level, ready := m.window.level(sustain, tolerance)
	if !ready || level == m.tint || m.debounced(level, at, debounce) {
		return m.tint, false
	}
	m.tint, m.changed = level, at
	return m.tint, true
}

// debounced requires a sustained observation before the tint flips back
// to ok. Escalations come at once.
func (m *levelMachine) debounced(to Tint, now time.Time, debounce time.Duration) bool {
	return to == TintOK && debounce > 0 && now.Sub(m.changed) < debounce
}

// sustainWindow holds the recent raw levels of one metric. Each point
// holds its level until the next point.
type sustainWindow struct{ points []levelPoint }

type levelPoint struct {
	at   time.Time
	tint Tint
}

func tintRank(t Tint) int {
	switch t {
	case TintWarn:
		return 1
	case TintCritical:
		return 2
	}
	return 0
}

func maxTint(a, b Tint) Tint {
	if tintRank(b) > tintRank(a) {
		return b
	}
	return a
}

// record adds a level and drops the points that the last "keep" does not
// need. One point before the cutoff stays, so the start of the window is
// covered. A time that goes back clears the window first.
func (w *sustainWindow) record(t Tint, at time.Time, keep time.Duration) {
	if n := len(w.points); n > 0 && at.Before(w.points[n-1].at) {
		w.points = w.points[:0]
	}
	w.points = append(w.points, levelPoint{at: at, tint: t})
	cutoff := at.Add(-keep)
	for len(w.points) > 1 && !w.points[1].at.After(cutoff) {
		w.points = slices.Delete(w.points, 0, 1)
	}
}

// level gives the highest level that holds for at least tolerance of the
// last sustain time. It gives false when the points do not cover that time.
// A zero sustain time gives the last level.
func (w *sustainWindow) level(sustain time.Duration, tolerance float64) (Tint, bool) {
	if len(w.points) == 0 {
		return TintOK, false
	}
	last := w.points[len(w.points)-1]
	if sustain <= 0 {
		return last.tint, true
	}
	start := last.at.Add(-sustain)
	if w.points[0].at.After(start) {
		return TintOK, false
	}
	var warn, critical time.Duration
	for i := 0; i+1 < len(w.points); i++ {
		a, b := w.points[i], w.points[i+1]
		d := b.at.Sub(maxTime(a.at, start))
		if d <= 0 {
			continue
		}
		if tintRank(a.tint) >= 1 {
			warn += d
		}
		if a.tint == TintCritical {
			critical += d
		}
	}
	switch {
	case float64(critical)/float64(sustain) >= tolerance:
		return TintCritical, true
	case float64(warn)/float64(sustain) >= tolerance:
		return TintWarn, true
	}
	return TintOK, true
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (s *metricState) set(k stateKey, to Tint, value float64, now time.Time) change {
	c := change{key: k, from: s.tint, to: to, value: value, at: now}
	s.tint = to
	s.changed = now
	return c
}

type nodeSeen struct {
	at       time.Time
	interval time.Duration
}

func (s *nodeSeen) timeout() time.Duration { return 3 * s.interval }

// markSeen records a sample of the node and gives the change back to
// reachable, if any. The change carries the down time and its start. It runs under stateMu.
func (e *Engine) markSeen(org, node uuid.UUID, now time.Time) (change, bool) {
	nk := nodeKey{org: org, node: node}
	reg := e.expected[nk]
	delete(e.expected, nk)
	s, ok := e.seen[nk]
	if !ok {
		s = &nodeSeen{at: reg, interval: defaultInterval}
		e.seen[nk] = s
	} else if gap := now.Sub(s.at); gap > 0 && gap <= s.timeout() {
		// A gap longer than the timeout is an outage, not the interval.
		s.interval = min(max(gap, defaultInterval), maxInterval)
	}
	last := s.at
	s.at = now

	k := stateKey{org: org, node: node, metric: metricReachability}
	st, ok := e.states[k]
	if !ok {
		e.states[k] = newMetricState(TintOK, now)
		return change{}, false
	}
	if st.tint == TintOK {
		return change{}, false
	}
	var down float64
	if !last.IsZero() {
		down = now.Sub(last).Seconds()
	}
	c := st.set(k, TintOK, down, now)
	c.since = last
	return c, true
}

// Load restores the last tints and the last sample times from the
// store, so a restart sends no duplicate alerts. It keeps the state of
// samples that came before it.
func (e *Engine) Load(ctx context.Context) error {
	if e.store == nil {
		return nil
	}
	since := e.clock.Now().Add(-restoreWindow)
	tints, err := e.store.LastTints(ctx, since)
	if err != nil {
		return err
	}
	seen, err := e.store.LastSeen(ctx, since)
	if err != nil {
		return err
	}
	regs, err := e.store.Registrations(ctx, since)
	if err != nil {
		return err
	}
	marks, err := e.store.OpenMarks(ctx, since)
	if err != nil {
		return err
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	for _, r := range tints {
		// The process trackers start empty, so their states are not kept.
		if strings.HasPrefix(r.Metric, metricLongProc+"/") {
			continue
		}
		k := stateKey{org: r.OrgID, node: r.NodeID, metric: r.Metric}
		if _, ok := e.states[k]; !ok {
			e.states[k] = newMetricState(Tint(r.Tint), r.TS)
		}
		if _, ok := e.open[k]; !ok && Tint(r.Tint) != TintOK {
			e.open[k] = &openAlert{id: r.AlertID, tint: Tint(r.Tint), last: restoredChange(k, Tint(r.Tint), r.TS)}
		}
	}
	e.restoreMarks(marks)
	for _, r := range seen {
		nk := nodeKey{org: r.OrgID, node: r.NodeID}
		if _, ok := e.seen[nk]; !ok {
			e.seen[nk] = &nodeSeen{at: r.TS, interval: defaultInterval}
		}
		k := stateKey{org: r.OrgID, node: r.NodeID, metric: metricReachability}
		if _, ok := e.states[k]; !ok {
			e.states[k] = newMetricState(TintOK, r.TS)
		}
	}
	for _, r := range regs {
		nk := nodeKey{org: r.OrgID, node: r.NodeID}
		if _, ok := e.seen[nk]; !ok {
			e.expected[nk] = r.TS
		}
	}
	e.log.Info("alerter: state loaded", "tints", len(tints), "nodes", len(seen), "registrations", len(regs))
	return nil
}

// restoredChange is the change of an alert that Load restored. The value
// is not known, so the text only says when the alert opened.
func restoredChange(k stateKey, tint Tint, at time.Time) change {
	return change{
		key: k, from: TintWarn, to: tint, at: at,
		detail: "Open since " + at.UTC().Format("15:04") + " UTC.",
	}
}

// restoreMarks sets the ack and the snooze of the restored alerts. It runs
// under stateMu. A snooze that ended before the restart sends no reminder.
func (e *Engine) restoreMarks(marks []clickhouse.AlertMark) {
	now := e.clock.Now()
	byID := make(map[markKey]*openAlert, len(e.open))
	for k, o := range e.open {
		byID[markKey{org: k.org, alert: o.id}] = o
	}
	for _, m := range marks {
		o := byID[markKey{org: m.OrgID, alert: m.AlertID}]
		if o == nil {
			continue
		}
		o.ackedBy = m.AckedBy
		if m.SnoozedUntil != nil {
			o.snoozedUntil = *m.SnoozedUntil
			o.snoozeDone = !m.SnoozedUntil.After(now)
		}
	}
}

type markKey struct{ org, alert uuid.UUID }
