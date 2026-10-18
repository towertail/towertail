package alerter

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	metricReachability = "reachability"
	// restoreWindow is how far back Load reads the state.
	restoreWindow = 24 * time.Hour
	// defaultInterval is the sample interval until a node sends two
	// samples. A node is unreachable after 3 intervals.
	defaultInterval = 10 * time.Second
	maxInterval     = 5 * time.Minute
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
// reachable, if any. It runs under stateMu.
func (e *Engine) markSeen(org, node uuid.UUID, now time.Time) (change, bool) {
	nk := nodeKey{org: org, node: node}
	s, ok := e.seen[nk]
	if !ok {
		s = &nodeSeen{interval: defaultInterval}
		e.seen[nk] = s
	} else if gap := now.Sub(s.at); gap > 0 && gap <= s.timeout() {
		// A gap longer than the timeout is an outage, not the interval.
		s.interval = min(max(gap, defaultInterval), maxInterval)
	}
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
	return st.set(k, TintOK, 0, now), true
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
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	for _, r := range tints {
		k := stateKey{org: r.OrgID, node: r.NodeID, metric: r.Metric}
		if _, ok := e.states[k]; !ok {
			e.states[k] = &metricState{tint: Tint(r.Tint), changed: r.TS}
		}
	}
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
	e.log.Info("alerter: state loaded", "tints", len(tints), "nodes", len(seen))
	return nil
}
