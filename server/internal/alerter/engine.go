package alerter

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/server/internal/clickhouse"
	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/wire"
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

// Store persists events to the historical events table.
type Store interface {
	WriteEvent(ctx context.Context, e clickhouse.EventRow) error
}

type Tint string

const (
	TintOK       Tint = "ok"
	TintWarn     Tint = "warn"
	TintCritical Tint = "critical"
)

// nodeState tracks per-(org,node) threshold state for each metric.
type nodeState struct {
	cpu  metricState
	mem  metricState
	disk metricState
}

type metricState struct {
	tint     Tint
	changed  time.Time
	notified time.Time
}

// Engine evaluates thresholds on every ingested sample. Edge-triggered:
// only tint changes produce events. Debounce suppresses flapping.
type Engine struct {
	log     *slog.Logger
	clock   Clock
	sink    EventSink
	store   Store
	cfgMu   sync.RWMutex
	cfg     store.ServerSettings
	stateMu sync.Mutex
	states  map[uuid.UUID]*nodeState
}

func New(log *slog.Logger, clock Clock, sink EventSink, st Store, initial store.ServerSettings) *Engine {
	if clock == nil {
		clock = realClock{}
	}
	return &Engine{
		log:    log,
		clock:  clock,
		sink:   sink,
		store:  st,
		cfg:    initial,
		states: map[uuid.UUID]*nodeState{},
	}
}

// UpdateSettings is called when ServerSettings change at runtime so the
// alerter picks up new thresholds without a restart.
func (e *Engine) UpdateSettings(s store.ServerSettings) {
	e.cfgMu.Lock()
	e.cfg = s
	e.cfgMu.Unlock()
}

func (e *Engine) settings() store.ServerSettings {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.cfg
}

// Evaluate looks at one sample and emits events if tints change. Node
// is optional — passing nil falls back to global settings. OrgID is
// the event fan-out key.
func (e *Engine) Evaluate(ctx context.Context, orgID, nodeID uuid.UUID, node *store.Node, cpuPct, memRatio, maxDiskRatio float64) {
	cfg := e.settings()
	thresholds := cfg.Thresholds
	if node != nil && node.CustomThresholds != nil {
		thresholds = *node.CustomThresholds
	}

	e.stateMu.Lock()
	st, ok := e.states[nodeID]
	if !ok {
		st = &nodeState{}
		e.states[nodeID] = st
	}
	e.stateMu.Unlock()

	now := e.clock.Now()
	debounce := time.Duration(cfg.NotifyDebounceSeconds) * time.Second

	e.evalMetric(ctx, orgID, nodeID, "cpu", cpuPct, thresholds.CPUWarn, thresholds.CPUCritical, &st.cpu, now, debounce)
	e.evalMetric(ctx, orgID, nodeID, "mem", memRatio, thresholds.MemWarn, thresholds.MemCritical, &st.mem, now, debounce)
	e.evalMetric(ctx, orgID, nodeID, "disk", maxDiskRatio, thresholds.DiskWarn, thresholds.DiskCritical, &st.disk, now, debounce)
}

func (e *Engine) evalMetric(ctx context.Context, orgID, nodeID uuid.UUID, metric string, value, warn, critical float64, state *metricState, now time.Time, debounce time.Duration) {
	newTint := TintOK
	switch {
	case value >= critical:
		newTint = TintCritical
	case value >= warn:
		newTint = TintWarn
	}

	if state.tint == "" {
		state.tint = newTint
		state.changed = now
		return
	}
	if newTint == state.tint {
		return
	}
	// Debounce: require a sustained observation before flipping back to
	// OK; escalations fire immediately.
	if newTint == TintOK && debounce > 0 && now.Sub(state.changed) < debounce {
		return
	}
	prev := state.tint
	state.tint = newTint
	state.changed = now
	state.notified = now

	payload, _ := json.Marshal(map[string]any{
		"metric":   metric,
		"previous": prev,
		"value":    value,
	})
	evt := wire.Event{
		ID:     uuid.New(),
		TS:     now,
		Kind:   "threshold_crossed",
		NodeID: nodeID,
		Metric: metric,
		Tint:   string(newTint),
	}
	if e.sink != nil {
		e.sink.Publish(orgID, evt)
	}
	if e.store != nil {
		_ = e.store.WriteEvent(ctx, clickhouse.EventRow{
			TS:      now,
			OrgID:   orgID,
			NodeID:  nodeID,
			EventID: evt.ID,
			Kind:    "threshold_crossed",
			Metric:  metric,
			Tint:    string(newTint),
			Payload: string(payload),
		})
	}
}
