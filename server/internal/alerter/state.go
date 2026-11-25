package alerter

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
)

const (
	metricReachability = "reachability"
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
	tint    Tint
	changed time.Time
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
		e.states[k] = &metricState{tint: TintOK, changed: now}
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
			e.states[k] = &metricState{tint: Tint(r.Tint), changed: r.TS}
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
			e.states[k] = &metricState{tint: TintOK, changed: r.TS}
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
