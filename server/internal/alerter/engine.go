package alerter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// reasonMuted is the reason of the change to ok of a muted alert.
const reasonMuted = "muted"

const (
	settingsTTL = 30 * time.Second
	// notifyTimeout covers the retries of the channel router.
	notifyTimeout   = 30 * time.Second
	defaultCooldown = 10 * time.Minute
	maxSent         = 4096
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
	Registrations(ctx context.Context, since time.Time) ([]clickhouse.NodeSeen, error)
	// OpenMarks gives the acks and snoozes of the alert log since the time.
	OpenMarks(ctx context.Context, since time.Time) ([]clickhouse.AlertMark, error)
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
	// expected holds the registration time of nodes with no sample yet.
	expected map[nodeKey]time.Time
	sent     map[sentKey]time.Time
	procs    map[nodeKey]*procTracker
	// open holds the alert (incident) of each key that is not ok.
	open map[stateKey]*openAlert
}

// openAlert is one alert from the change out of ok to the change back to
// ok. An ack or a snooze stops its notifications.
type openAlert struct {
	id           uuid.UUID
	tint         Tint
	ackedBy      *uuid.UUID
	snoozedUntil time.Time
	// snoozeDone is true after the reminder that follows the snooze.
	snoozeDone bool
	// last is the change that set the tint. The reminder reuses it.
	last change
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
		expected:  map[nodeKey]time.Time{},
		sent:      map[sentKey]time.Time{},
		procs:     map[nodeKey]*procTracker{},
		open:      map[stateKey]*openAlert{},
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

// Expect starts the never-reported timer of a node that just enrolled.
// The registered event lets Load restore the timer after a restart.
func (e *Engine) Expect(ctx context.Context, orgID, nodeID uuid.UUID, at time.Time) {
	nk := nodeKey{org: orgID, node: nodeID}
	e.stateMu.Lock()
	_, seen := e.seen[nk]
	if !seen {
		e.expected[nk] = at
	}
	e.stateMu.Unlock()
	if seen || e.store == nil {
		return
	}
	err := e.store.WriteEvent(ctx, clickhouse.EventRow{
		TS:      at,
		OrgID:   orgID,
		NodeID:  nodeID,
		EventID: uuid.New(),
		Kind:    "registered",
		Metric:  metricReachability,
		Tint:    string(TintOK),
	})
	if err != nil {
		e.log.Warn("alerter: write registered event", "node_id", nodeID, "err", err)
	}
}

// Reading is the part of one sample that the rules look at. CPU, Mem and
// Disk are ratios from 0 to 1. At is the sample time (the server clock
// when zero). Health is nil when the sample has no health part.
type Reading struct {
	At             time.Time
	CPU, Mem, Disk float64
	Health         *wire.HealthBrief
}

// Evaluate looks at one sample and emits events if tints change. Node
// is optional: nil means the org rules and no node flags.
func (e *Engine) Evaluate(ctx context.Context, orgID, nodeID uuid.UUID, node *store.Node, r Reading) {
	cfg := e.settings(ctx, orgID)
	var host store.HostRules
	if node != nil {
		host = node.Rules()
	}
	rules := cfg.Rules().For(host)
	now := e.clock.Now()
	at := r.At
	if at.IsZero() {
		at = now
	}
	debounce := time.Duration(cfg.NotifyDebounceSeconds) * time.Second
	tolerance := rules.Alerts.Tolerance

	var changes []change
	e.stateMu.Lock()
	if ch, ok := e.markSeen(orgID, nodeID, now); ok {
		changes = append(changes, ch)
	}
	for _, m := range []struct {
		metric store.ThresholdMetric
		value  float64
	}{
		{store.MetricCPU, r.CPU},
		{store.MetricMem, r.Mem},
		{store.MetricDisk, r.Disk},
	} {
		k := stateKey{org: orgID, node: nodeID, metric: string(m.metric)}
		if rules.Muted.Has(m.metric) {
			if ch, ok := e.muteMetric(k, at); ok {
				changes = append(changes, ch)
			}
			continue
		}
		rule, _ := rules.Alerts.For(string(m.metric))
		raw := rawTint(m.value, rules.Thresholds.Pair(m.metric))
		if ch, ok := e.evalMetric(k, m.value, raw, rule, tolerance, at, debounce); ok {
			changes = append(changes, ch)
		}
	}
	if r.Health != nil {
		k := stateKey{org: orgID, node: nodeID, metric: metricHealth}
		kernelOnly := host.Thresholds != nil && host.Thresholds.MemPressure != nil
		checks := rules.Thresholds.HealthChecks(*r.Health, rules.Muted, kernelOnly)
		if ch, ok := e.evalHealth(k, checks, healthMuteSig(rules.Muted), rules.Alerts.Health, tolerance, at, debounce); ok {
			changes = append(changes, ch)
		}
	}
	e.stateMu.Unlock()

	for _, ch := range changes {
		e.emit(ctx, cfg, node, ch)
	}
}

func rawTint(value float64, p store.ThresholdPair) Tint {
	switch {
	case value >= p.Critical:
		return TintCritical
	case value >= p.Warn:
		return TintWarn
	}
	return TintOK
}

// EvaluateProcs follows the processes of one sample and emits a
// long_process event when a process is newly flagged or cleared.
func (e *Engine) EvaluateProcs(ctx context.Context, orgID, nodeID uuid.UUID, node *store.Node, procs []clickhouse.ProcRow, at time.Time) {
	cfg := e.settings(ctx, orgID)
	rule := cfg.LongProc
	var nodeIgnore []string
	if node != nil {
		if node.CustomLongProc != nil {
			rule = *node.CustomLongProc
		}
		nodeIgnore = node.IgnoredProcs
	}
	ignored := rule.IgnoreSet(cfg.LongProc.Ignore, nodeIgnore)
	// A muted rule follows no process, so the open alerts close quietly.
	muted := cfg.MutedSet(node).Has(store.MetricLong)
	if muted {
		rule.Enabled = false
	}

	nk := nodeKey{org: orgID, node: nodeID}
	e.stateMu.Lock()
	t := e.procs[nk]
	if t == nil {
		if !rule.Enabled {
			e.stateMu.Unlock()
			return
		}
		t = newProcTracker()
		e.procs[nk] = t
	}
	pcs := t.observe(procs, at, rule, ignored)
	if t.empty() {
		delete(e.procs, nk)
	}
	e.stateMu.Unlock()

	for _, pc := range pcs {
		c := procEvent(nk, pc, at, !rule.Notifies())
		if muted {
			c.quiet, c.reason = true, reasonMuted
		}
		e.emit(ctx, cfg, node, c)
	}
}

func procEvent(nk nodeKey, pc procChange, at time.Time, quiet bool) change {
	c := change{
		key:     stateKey{org: nk.org, node: nk.node, metric: pc.key.metric()},
		from:    TintWarn,
		to:      TintOK,
		value:   float64(pc.proc.CPUPct),
		at:      at,
		detail:  pc.detail(at),
		payload: pc.payload(),
		quiet:   quiet,
	}
	if pc.flagged {
		c.from, c.to = TintOK, TintWarn
	}
	if pc.ignored {
		c.quiet = true
		c.payload["closed"] = "ignored"
	}
	return c
}

// muteMetric closes the alert of a muted metric. It runs under stateMu.
// A muted metric keeps an ok state, so the alert can open again after an
// unmute.
func (e *Engine) muteMetric(k stateKey, now time.Time) (change, bool) {
	st, ok := e.states[k]
	if !ok {
		e.states[k] = &metricState{tint: TintOK, changed: now}
		return change{}, false
	}
	if st.tint == TintOK {
		return change{}, false
	}
	c := st.set(k, TintOK, 0, now)
	c.quiet, c.reason = true, reasonMuted
	return c, true
}

// evalMetric runs under stateMu. The tint of the state follows the
// sustained level of the raw tint. With a sustain time of 0 the first sample
// sets the state and no event comes. With a longer sustain time the state
// starts at ok and the first event comes when the window is full.
func (e *Engine) evalMetric(k stateKey, value float64, raw Tint, rule store.AlertRule, tolerance float64, now time.Time, debounce time.Duration) (change, bool) {
	sustain := time.Duration(rule.SustainSeconds) * time.Second
	st, ok := e.states[k]
	if !ok {
		st = &metricState{tint: TintOK, changed: now}
		e.states[k] = st
		if sustain == 0 {
			st.tint = raw
			return change{}, false
		}
	}
	st.window.record(raw, now, sustain)
	newTint, ready := st.window.level(sustain, tolerance)
	if !ready || newTint == st.tint {
		return change{}, false
	}
	if e.debounced(st, newTint, now, debounce) {
		return change{}, false
	}
	return st.set(k, newTint, value, now), true
}

// debounced requires a sustained observation before the state flips back
// to ok. Escalations fire at once.
func (e *Engine) debounced(st *metricState, to Tint, now time.Time, debounce time.Duration) bool {
	return to == TintOK && debounce > 0 && now.Sub(st.changed) < debounce
}

// healthMuteSig names the muted health checks, so a change of the list is
// easy to see.
func healthMuteSig(m store.MutedSignals) string {
	var names []string
	for _, h := range store.HealthMetrics {
		if m.Has(h) {
			names = append(names, string(h))
		}
	}
	return strings.Join(names, ",")
}

// evalHealth runs under stateMu. The checks are the unmuted checks over
// their limits. The target is the worst of the sustained level of all
// checks but inodes, and the level of the inode check at once. A window that
// is not full lets only an inode escalation act. A change of the muted checks
// clears the window. It closes an open alert quietly when no check is over its
// limit any more.
func (e *Engine) evalHealth(k stateKey, checks []store.HealthCheck, muteSig string, rule store.AlertRule, tolerance float64, now time.Time, debounce time.Duration) (change, bool) {
	rest, inode := TintOK, TintOK
	for _, c := range checks {
		if c.Metric == store.MetricInodes {
			inode = maxTint(inode, c.Tint)
		} else {
			rest = maxTint(rest, c.Tint)
		}
	}
	sustain := time.Duration(rule.SustainSeconds) * time.Second
	st, ok := e.states[k]
	if !ok {
		st = &metricState{tint: TintOK, changed: now, muteSig: muteSig}
		e.states[k] = st
		if sustain == 0 {
			st.tint = maxTint(rest, inode)
			return change{}, false
		}
	}
	if st.muteSig != muteSig {
		st.muteSig = muteSig
		st.window = sustainWindow{}
		if st.tint != TintOK && maxTint(rest, inode) == TintOK {
			c := st.set(k, TintOK, 0, now)
			c.quiet, c.reason, c.detail = true, reasonMuted, healthOKText
			return c, true
		}
	}
	st.window.record(rest, now, sustain)
	target := inode
	if level, ready := st.window.level(sustain, tolerance); ready {
		target = maxTint(level, inode)
	} else if tintRank(inode) <= tintRank(st.tint) {
		return change{}, false
	}
	if target == st.tint || e.debounced(st, target, now, debounce) {
		return change{}, false
	}
	c := st.set(k, target, float64(len(checks)), now)
	c.detail = healthOKText
	if target != TintOK {
		c.detail = healthDetail(checks)
	}
	return c, true
}

const healthOKText = "All health checks are back to normal."

// healthDetail joins the texts of the checks, critical first.
func healthDetail(checks []store.HealthCheck) string {
	texts := make([]string, len(checks))
	for i, c := range checks {
		texts[i] = c.Text
	}
	return strings.Join(texts, " · ")
}

// change is one tint change, ready to emit outside the lock.
type change struct {
	key      stateKey
	from, to Tint
	value    float64
	at       time.Time
	// since and reason are for reachability changes.
	since  time.Time
	reason string
	// detail, payload and quiet are for long_proc changes. quiet sends
	// no notification.
	detail  string
	payload map[string]any
	quiet   bool
}

func (c change) kind() string {
	switch c.metric() {
	case metricReachability:
		return "reachability_changed"
	case metricLongProc:
		return "long_process"
	}
	return "threshold_crossed"
}

// metric is the metric of the key without the instance: "long_proc" for
// "long_proc/<pid>/<start_unix>".
func (c change) metric() string {
	m, _, _ := strings.Cut(c.key.metric, "/")
	return m
}

// instance is the part of the key after the metric, if any.
func (c change) instance() string {
	_, inst, _ := strings.Cut(c.key.metric, "/")
	return inst
}

// emit publishes, persists and notifies one change.
func (e *Engine) emit(ctx context.Context, cfg store.ServerSettings, node *store.Node, c change) {
	body := map[string]any{
		"metric":   c.metric(),
		"previous": c.from,
		"value":    c.value,
	}
	if c.reason != "" {
		body["reason"] = c.reason
	}
	if !c.since.IsZero() {
		body["since"] = c.since
	}
	maps.Copy(body, c.payload)
	payload, _ := json.Marshal(body)

	eventID := uuid.New()
	alertID := e.track(c, eventID)
	sendIt := e.shouldNotify(cfg, node, c)
	a := e.alertOf(c, node)
	a.EventID, a.AlertID = eventID, alertID
	evt := wire.Event{
		ID:      eventID,
		AlertID: alertID,
		TS:      c.at,
		Kind:    c.kind(),
		NodeID:  c.key.node,
		Metric:  c.metric(),
		Tint:    string(c.to),
		Notify:  sendIt,
		Title:   a.Title(),
		Text:    a.Text(),
	}
	if e.sink != nil {
		e.sink.Publish(c.key.org, evt)
	}
	if e.store != nil {
		err := e.store.WriteEvent(ctx, clickhouse.EventRow{
			TS:      c.at,
			OrgID:   c.key.org,
			NodeID:  c.key.node,
			EventID: eventID,
			AlertID: alertID,
			Kind:    evt.Kind,
			Metric:  c.key.metric,
			Tint:    string(c.to),
			Payload: string(payload),
		})
		if err != nil {
			e.log.Warn("alerter: write event", "err", err)
		}
	}
	if c.to == TintOK {
		e.stateMu.Lock()
		delete(e.open, c.key)
		e.stateMu.Unlock()
	}
	if sendIt {
		e.notify(a)
	}
}

// track gives the alert id of the change and keeps the open alert. A
// change out of ok opens a new alert, and its id is the id of the event.
func (e *Engine) track(c change, eventID uuid.UUID) uuid.UUID {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	o := e.open[c.key]
	if c.to == TintOK {
		if o != nil {
			return o.id
		}
		return eventID
	}
	if o == nil || c.from == TintOK {
		o = &openAlert{id: eventID}
		e.open[c.key] = o
	}
	o.tint, o.last = c.to, c
	return o.id
}

// shouldNotify applies the notify level of the metric, the org flags, the
// node flags, the node snooze, the ack and the snooze of the alert, and the
// cooldown. A change to ok
// uses the flags of the tint that it ends. An ack stops all changes except
// the change to ok. A snooze stops all changes until its time.
func (e *Engine) shouldNotify(cfg store.ServerSettings, node *store.Node, c change) bool {
	if c.quiet {
		return false
	}
	tint := c.to
	if tint == TintOK {
		tint = c.from
	}
	if !e.flagsAllow(cfg, node, c.metric(), tint, c.at) {
		return false
	}
	cooldown := time.Duration(cfg.NotifyCooldownSeconds) * time.Second
	if cooldown <= 0 {
		cooldown = defaultCooldown
	}
	k := sentKey{stateKey: c.key, tint: c.to}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if o := e.open[c.key]; o != nil {
		if o.snoozedUntil.After(c.at) || (o.ackedBy != nil && c.to != TintOK) {
			return false
		}
	}
	if last, ok := e.sent[k]; ok && c.at.Sub(last) < cooldown {
		return false
	}
	// Each process has its own keys, so drop the old ones.
	if len(e.sent) > maxSent {
		maps.DeleteFunc(e.sent, func(_ sentKey, t time.Time) bool { return c.at.Sub(t) >= cooldown })
	}
	e.sent[k] = c.at
	return true
}

// flagsAllow applies the notify level of the metric, the org flags, the
// node flags and the node snooze to a notification of the tint.
func (e *Engine) flagsAllow(cfg store.ServerSettings, node *store.Node, metric string, tint Tint, at time.Time) bool {
	if len(e.notifiers) == 0 || !cfg.NotificationsEnabled {
		return false
	}
	if !notifyLevelAllows(cfg, node, metric, tint) {
		return false
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
	return node == nil || node.SnoozedUntil == nil || !node.SnoozedUntil.After(at)
}

// notifyLevelAllows applies the notify level of cpu, mem, disk and health.
// Other metrics have no level.
func notifyLevelAllows(cfg store.ServerSettings, node *store.Node, metric string, tint Tint) bool {
	var host store.HostRules
	if node != nil {
		host = node.Rules()
	}
	rule, ok := cfg.Rules().For(host).Alerts.For(metric)
	return !ok || rule.Notify.Allows(tint)
}

// notify calls each notifier in its own goroutine with a timeout.
func (e *Engine) notify(a notify.Alert) {
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

// alertOf builds the notification of a change. The caller sets the ids.
func (e *Engine) alertOf(c change, node *store.Node) notify.Alert {
	a := notify.Alert{
		OrgID:    c.key.org,
		NodeID:   c.key.node,
		Metric:   c.metric(),
		Instance: c.instance(),
		From:     c.from,
		To:       c.to,
		Value:    c.value,
		Reason:   c.reason,
		Since:    c.since,
		Detail:   c.detail,
		At:       c.at,
	}
	if node != nil {
		a.NodeName = node.DisplayName
		a.Tags = node.Tags
		if node.SSHHost != nil && *node.SSHHost != "" {
			a.Host = *node.SSHHost
			if node.SSHUser != nil && *node.SSHUser != "" {
				a.Host = *node.SSHUser + "@" + a.Host
			}
		}
	}
	return a
}

// Acked sets the ack or the snooze of the open alert, then tells the
// notifiers that implement notify.AckObserver. An undo changes only the
// state. Each call has its own goroutine and timeout.
func (e *Engine) Acked(_ context.Context, a notify.Ack) {
	key := stateKey{org: a.OrgID, node: a.NodeID, metric: a.Metric}
	e.stateMu.Lock()
	if o := e.open[key]; o != nil && o.id == a.AlertID {
		switch {
		case a.Snooze && a.Undo:
			o.snoozedUntil, o.snoozeDone = time.Time{}, false
		case a.Snooze && a.Until != nil:
			o.snoozedUntil, o.snoozeDone = *a.Until, false
		case a.Undo:
			o.ackedBy = nil
		case !a.Snooze:
			by := a.By
			o.ackedBy = &by
		}
	}
	e.stateMu.Unlock()
	if a.Undo {
		return
	}
	for _, n := range e.notifiers {
		o, ok := n.(notify.AckObserver)
		if !ok {
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
			defer cancel()
			if err := o.AlertAcked(ctx, a); err != nil {
				e.log.Warn("alerter: ack", "notifier", n.Name(), "node_id", a.NodeID, "metric", a.Metric, "err", err)
			}
		}()
	}
}

// wakeSnoozed sends one reminder for each open alert whose snooze ended.
func (e *Engine) wakeSnoozed(ctx context.Context, now time.Time) {
	var due []openAlert
	e.stateMu.Lock()
	for _, o := range e.open {
		if o.snoozedUntil.IsZero() || o.snoozedUntil.After(now) || o.snoozeDone || o.ackedBy != nil {
			continue
		}
		o.snoozeDone = true
		due = append(due, *o)
	}
	e.stateMu.Unlock()

	for _, o := range due {
		c := o.last
		if c.quiet {
			continue
		}
		org := c.key.org
		var node *store.Node
		if n, err := e.config.GetNode(ctx, org, c.key.node); err == nil {
			node = &n
		} else if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if !e.flagsAllow(e.settings(ctx, org), node, c.metric(), o.tint, now) {
			continue
		}
		c.at = now
		a := e.alertOf(c, node)
		a.AlertID, a.Repeat = o.id, true
		e.notify(a)
	}
}
