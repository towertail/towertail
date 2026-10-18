package alerter

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

const (
	settingsTTL     = 30 * time.Second
	notifyTimeout   = 10 * time.Second
	defaultCooldown = 10 * time.Minute
)

// Clock abstracts time.Now for deterministic tests.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

// EventSink receives BackendEvent payloads after the state machine
// commits a transition. The hub implements this to fan out to
// connected websockets.
type EventSink interface {
	Publish(orgID uuid.UUID, event wire.Event)
}

// Store persists events to the historical events table and gives the
// state back after a restart.
type Store interface {
	WriteEvent(ctx context.Context, e clickhouse.EventRow) error
	LastTints(ctx context.Context, since time.Time) ([]clickhouse.TintRow, error)
	LastSeen(ctx context.Context, since time.Time) ([]clickhouse.NodeSeen, error)
}

// Config gives the settings of each org and the nodes.
type Config interface {
	GetSettings(ctx context.Context, orgID uuid.UUID) (store.ServerSettings, error)
	GetNode(ctx context.Context, orgID, id uuid.UUID) (store.Node, error)
}

type Tint = wire.Tint

const (
	TintOK       = wire.TintOK
	TintWarn     = wire.TintWarn
	TintCritical = wire.TintCritical
)

// Engine evaluates thresholds on every ingested sample and the
// reachability of each node. Edge-triggered: only tint changes produce
// events and alerts. Debounce suppresses flapping back to ok.
type Engine struct {
	log       *slog.Logger
	clock     Clock
	sink      EventSink
	store     Store
	config    Config
	notifiers []notify.Notifier

	cacheMu sync.Mutex
	cache   map[uuid.UUID]cachedSettings

	stateMu sync.Mutex
	states  map[stateKey]*metricState
	seen    map[nodeKey]*nodeSeen
	sent    map[sentKey]time.Time
}

type cachedSettings struct {
	s  store.ServerSettings
	at time.Time
}

func New(log *slog.Logger, clock Clock, sink EventSink, st Store, cfg Config, notifiers ...notify.Notifier) *Engine {
	if log == nil {
		log = slog.Default()
	}
	if clock == nil {
		clock = realClock{}
	}
	return &Engine{
		log:       log,
		clock:     clock,
		sink:      sink,
		store:     st,
		config:    cfg,
		notifiers: notifiers,
		cache:     map[uuid.UUID]cachedSettings{},
		states:    map[stateKey]*metricState{},
		seen:      map[nodeKey]*nodeSeen{},
		sent:      map[sentKey]time.Time{},
	}
}

// InvalidateSettings drops the cached settings of the org, so the next
// sample uses the new thresholds.
func (e *Engine) InvalidateSettings(orgID uuid.UUID) {
	e.cacheMu.Lock()
	delete(e.cache, orgID)
	e.cacheMu.Unlock()
}

func (e *Engine) settings(ctx context.Context, orgID uuid.UUID) store.ServerSettings {
	now := e.clock.Now()
	e.cacheMu.Lock()
	c, ok := e.cache[orgID]
	e.cacheMu.Unlock()
	if ok && now.Sub(c.at) < settingsTTL {
		return c.s
	}
	s, err := e.config.GetSettings(ctx, orgID)
	if err != nil {
		e.log.Warn("alerter: get settings", "org_id", orgID, "err", err)
		if ok {
			return c.s
		}
		s = store.DefaultServerSettings()
	}
	e.cacheMu.Lock()
	e.cache[orgID] = cachedSettings{s: s, at: now}
	e.cacheMu.Unlock()
	return s
}

// Evaluate looks at one sample and emits events if tints change. Node
// is optional: nil means the org thresholds and no node flags.
func (e *Engine) Evaluate(ctx context.Context, orgID, nodeID uuid.UUID, node *store.Node, cpuPct, memRatio, maxDiskRatio float64) {
	cfg := e.settings(ctx, orgID)
	thresholds := cfg.Thresholds
	if node != nil && node.CustomThresholds != nil {
		thresholds = *node.CustomThresholds
	}
	now := e.clock.Now()
	debounce := time.Duration(cfg.NotifyDebounceSeconds) * time.Second

	var changes []change
	e.stateMu.Lock()
	if ch, ok := e.markSeen(orgID, nodeID, now); ok {
		changes = append(changes, ch)
	}
	for _, m := range []struct {
		metric         string
		value          float64
		warn, critical float64
	}{
		{"cpu", cpuPct, thresholds.CPUWarn, thresholds.CPUCritical},
		{"mem", memRatio, thresholds.MemWarn, thresholds.MemCritical},
		{"disk", maxDiskRatio, thresholds.DiskWarn, thresholds.DiskCritical},
	} {
		k := stateKey{org: orgID, node: nodeID, metric: m.metric}
		if ch, ok := e.evalMetric(k, m.value, m.warn, m.critical, now, debounce); ok {
			changes = append(changes, ch)
		}
	}
	e.stateMu.Unlock()

	for _, ch := range changes {
		e.emit(ctx, cfg, node, ch)
	}
}

// evalMetric runs under stateMu.
func (e *Engine) evalMetric(k stateKey, value, warn, critical float64, now time.Time, debounce time.Duration) (change, bool) {
	newTint := TintOK
	switch {
	case value >= critical:
		newTint = TintCritical
	case value >= warn:
		newTint = TintWarn
	}
	st, ok := e.states[k]
	if !ok {
		e.states[k] = &metricState{tint: newTint, changed: now}
		return change{}, false
	}
	if newTint == st.tint {
		return change{}, false
	}
	// Debounce: require a sustained observation before flipping back to
	// OK; escalations fire immediately.
	if newTint == TintOK && debounce > 0 && now.Sub(st.changed) < debounce {
		return change{}, false
	}
	return st.set(k, newTint, value, now), true
}

// change is one tint change, ready to emit outside the lock.
type change struct {
	key      stateKey
	from, to Tint
	value    float64
	at       time.Time
}

func (c change) kind() string {
	if c.key.metric == metricReachability {
		return "reachability_changed"
	}
	return "threshold_crossed"
}

// emit publishes, persists and notifies one change.
func (e *Engine) emit(ctx context.Context, cfg store.ServerSettings, node *store.Node, c change) {
	payload, _ := json.Marshal(map[string]any{
		"metric":   c.key.metric,
		"previous": c.from,
		"value":    c.value,
	})
	evt := wire.Event{
		ID:     uuid.New(),
		TS:     c.at,
		Kind:   c.kind(),
		NodeID: c.key.node,
		Metric: c.key.metric,
		Tint:   string(c.to),
	}
	if e.sink != nil {
		e.sink.Publish(c.key.org, evt)
	}
	if e.store != nil {
		err := e.store.WriteEvent(ctx, clickhouse.EventRow{
			TS:      c.at,
			OrgID:   c.key.org,
			NodeID:  c.key.node,
			EventID: evt.ID,
			Kind:    evt.Kind,
			Metric:  c.key.metric,
			Tint:    string(c.to),
			Payload: string(payload),
		})
		if err != nil {
			e.log.Warn("alerter: write event", "err", err)
		}
	}
	if e.shouldNotify(cfg, node, c) {
		e.notify(c, node)
	}
}

// shouldNotify applies the org flags, the node flags, the node snooze
// and the cooldown. A change to ok uses the flags of the tint that it
// ends.
func (e *Engine) shouldNotify(cfg store.ServerSettings, node *store.Node, c change) bool {
	if len(e.notifiers) == 0 || !cfg.NotificationsEnabled {
		return false
	}
	tint := c.to
	if tint == TintOK {
		tint = c.from
	}
	switch tint {
	case TintWarn:
		if !cfg.NotifyWarn || (node != nil && !node.NotifyOnWarn) {
			return false
		}
	case TintCritical:
		if !cfg.NotifyCritical || (node != nil && !node.NotifyOnCritical) {
			return false
		}
	default:
		return false
	}
	if node != nil && node.SnoozedUntil != nil && node.SnoozedUntil.After(c.at) {
		return false
	}
	cooldown := time.Duration(cfg.NotifyCooldownSeconds) * time.Second
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	k := sentKey{stateKey: c.key, tint: c.to}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if last, ok := e.sent[k]; ok && c.at.Sub(last) < cooldown {
		return false
	}
	e.sent[k] = c.at
	return true
}

// notify calls each notifier in its own goroutine with a timeout.
func (e *Engine) notify(c change, node *store.Node) {
	a := notify.Alert{
		OrgID:  c.key.org,
		NodeID: c.key.node,
		Metric: c.key.metric,
		From:   c.from,
		To:     c.to,
		Value:  c.value,
		At:     c.at,
	}
	if node != nil {
		a.NodeName = node.DisplayName
	}
	for _, n := range e.notifiers {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
			defer cancel()
			if err := n.Notify(ctx, a); err != nil {
				e.log.Warn("alerter: notify", "notifier", n.Name(), "node_id", a.NodeID, "metric", a.Metric, "err", err)
			}
		}()
	}
}
