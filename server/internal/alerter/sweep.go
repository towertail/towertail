package alerter

import (
	"context"
	"errors"
	"time"

	"github.com/towertail/towertail/server/store"
)

// loadRetry is the wait between two Load attempts, for example while
// ClickHouse starts.
const loadRetry = 5 * time.Second

// Run loads the state, then sweeps for unreachable nodes each interval
// until ctx is done.
func (e *Engine) Run(ctx context.Context, every time.Duration) {
	for {
		err := e.Load(ctx)
		if err == nil {
			break
		}
		e.log.Warn("alerter: load state", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(loadRetry):
		}
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Sweep(ctx)
		}
	}
}

// Sweep sets nodes with no sample for 3 intervals to unreachable.
func (e *Engine) Sweep(ctx context.Context) {
	now := e.clock.Now()
	var stale []nodeKey
	e.stateMu.Lock()
	for nk, s := range e.seen {
		if now.Sub(s.at) <= s.timeout() {
			continue
		}
		if st := e.states[stateKey{org: nk.org, node: nk.node, metric: metricReachability}]; st != nil && st.tint == TintCritical {
			continue
		}
		stale = append(stale, nk)
	}
	e.stateMu.Unlock()

	for _, nk := range stale {
		node, err := e.config.GetNode(ctx, nk.org, nk.node)
		if errors.Is(err, store.ErrNotFound) {
			e.stateMu.Lock()
			delete(e.seen, nk)
			e.stateMu.Unlock()
			continue
		}
		if err != nil {
			e.log.Warn("alerter: sweep get node", "node_id", nk.node, "err", err)
			continue
		}
		if !node.Enabled {
			continue
		}

		k := stateKey{org: nk.org, node: nk.node, metric: metricReachability}
		e.stateMu.Lock()
		s := e.seen[nk]
		if s == nil || now.Sub(s.at) <= s.timeout() {
			// A sample came while the node was read.
			e.stateMu.Unlock()
			continue
		}
		st := e.states[k]
		if st == nil {
			st = &metricState{tint: TintOK, changed: s.at}
			e.states[k] = st
		}
		if st.tint == TintCritical {
			e.stateMu.Unlock()
			continue
		}
		c := st.set(k, TintCritical, now.Sub(s.at).Seconds(), now)
		e.stateMu.Unlock()
		e.emit(ctx, e.settings(ctx, nk.org), &node, c)
	}
}
