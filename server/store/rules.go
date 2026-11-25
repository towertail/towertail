package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// ErrInvalidRules wraps the error of a rule that has a bad value.
var ErrInvalidRules = errors.New("invalid rules")

func invalidRules(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRules, fmt.Sprintf(format, args...))
}

// ThresholdMetric names a signal. Ten names have a warn and critical pair
// (the keys of the Mac ThresholdOverrides). All names can be muted.
type ThresholdMetric string

const (
	MetricCPU         ThresholdMetric = "cpu"
	MetricMem         ThresholdMetric = "mem"
	MetricDisk        ThresholdMetric = "disk"
	MetricProcs       ThresholdMetric = "procs"
	MetricZombies     ThresholdMetric = "zombies"
	MetricPIDs        ThresholdMetric = "pids"
	MetricFiles       ThresholdMetric = "files"
	MetricInodes      ThresholdMetric = "inodes"
	MetricMemPressure ThresholdMetric = "memPressure"
	MetricIOPressure  ThresholdMetric = "ioPressure"
	// MetricLong is the long-running process alert. It has no pair.
	MetricLong ThresholdMetric = "long"
)

var (
	// UsageMetrics are the metrics with their own alert rule.
	UsageMetrics = []ThresholdMetric{MetricCPU, MetricMem, MetricDisk}
	// HealthMetrics are the checks of the health alert.
	HealthMetrics = []ThresholdMetric{
		MetricProcs, MetricZombies, MetricPIDs, MetricFiles, MetricInodes, MetricMemPressure, MetricIOPressure,
	}
)

// Valid reports whether m is a known name.
func (m ThresholdMetric) Valid() bool {
	return m == MetricLong || m.hasPair()
}

func (m ThresholdMetric) hasPair() bool {
	return slices.Contains(UsageMetrics, m) || slices.Contains(HealthMetrics, m)
}

// Fraction reports whether the pair of m is a ratio from 0 to 1.
func (m ThresholdMetric) Fraction() bool {
	switch m {
	case MetricCPU, MetricMem, MetricDisk, MetricPIDs, MetricFiles, MetricInodes:
		return true
	}
	return false
}

func (m ThresholdMetric) percent() bool { return m == MetricMemPressure || m == MetricIOPressure }

// Default counts of the health checks.
const (
	DefaultProcsWarn       = 5000
	DefaultProcsCritical   = 20000
	DefaultZombiesWarn     = 200
	DefaultZombiesCritical = 2000
)

// ThresholdPair is the warn and the critical level of one metric.
type ThresholdPair struct {
	Warn     float64 `json:"warn"`
	Critical float64 `json:"critical"`
}

// Validate checks 0 < warn <= critical, and critical <= 1 for a fraction.
func (p ThresholdPair) Validate(fraction bool) error {
	switch {
	case p.Warn <= 0 || math.IsNaN(p.Warn) || math.IsNaN(p.Critical):
		return invalidRules("warn must be more than 0")
	case p.Warn > p.Critical:
		return invalidRules("warn must not be more than critical")
	case fraction && p.Critical > 1:
		return invalidRules("critical must not be more than 1")
	}
	return nil
}

func (p ThresholdPair) validate(m ThresholdMetric) error {
	if err := p.Validate(m.Fraction()); err != nil {
		return fmt.Errorf("%s: %w", m, err)
	}
	if m.percent() && p.Critical > 100 {
		return fmt.Errorf("%s: %w", m, invalidRules("critical must not be more than 100"))
	}
	return nil
}

// HealthLimits are the limits of the health checks that are not counts.
// Pids, files and inodes are fractions. Pressure values are percents.
type HealthLimits struct {
	PIDs           ThresholdPair `json:"pids"`
	Files          ThresholdPair `json:"files"`
	Inodes         ThresholdPair `json:"inodes"`
	MemPressure    ThresholdPair `json:"memPressure"`
	IOPressure     ThresholdPair `json:"ioPressure"`
	MacMemPressure ThresholdPair `json:"macMemPressure"`
}

func DefaultHealthLimits() HealthLimits {
	return HealthLimits{
		PIDs:           ThresholdPair{0.70, 0.90},
		Files:          ThresholdPair{0.80, 0.95},
		Inodes:         ThresholdPair{0.85, 0.95},
		MemPressure:    ThresholdPair{10, 20},
		IOPressure:     ThresholdPair{20, 40},
		MacMemPressure: ThresholdPair{80, 90},
	}
}

// UnmarshalJSON decodes onto the receiver, so a missing key keeps its
// value. A zero receiver gets the defaults first.
func (h *HealthLimits) UnmarshalJSON(b []byte) error {
	type alias HealthLimits
	a := alias(*h)
	if *h == (HealthLimits{}) {
		a = alias(DefaultHealthLimits())
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*h = HealthLimits(a)
	return nil
}

// MetricThresholds mirrors the Swift MetricThresholds struct. CPU, memory,
// disk, PID use, open files and inodes are 0..1 ratios. Pressure values are
// percents. Processes and zombies are counts.
type MetricThresholds struct {
	CPUWarn         float64      `json:"cpuWarn"`
	CPUCritical     float64      `json:"cpuCritical"`
	MemWarn         float64      `json:"memWarn"`
	MemCritical     float64      `json:"memCritical"`
	DiskWarn        float64      `json:"diskWarn"`
	DiskCritical    float64      `json:"diskCritical"`
	ProcsWarn       int          `json:"procsWarn"`
	ProcsCritical   int          `json:"procsCritical"`
	ZombiesWarn     int          `json:"zombiesWarn"`
	ZombiesCritical int          `json:"zombiesCritical"`
	Health          HealthLimits `json:"health"`
}

// UnmarshalJSON decodes onto the receiver, so a missing key keeps the
// stored value. A zero receiver gets the default counts and health limits
// first. A client that sends only the six usage values stays valid.
func (t *MetricThresholds) UnmarshalJSON(b []byte) error {
	type alias MetricThresholds
	a := alias(*t)
	if a.ProcsWarn == 0 && a.ProcsCritical == 0 {
		a.ProcsWarn, a.ProcsCritical = DefaultProcsWarn, DefaultProcsCritical
	}
	if a.ZombiesWarn == 0 && a.ZombiesCritical == 0 {
		a.ZombiesWarn, a.ZombiesCritical = DefaultZombiesWarn, DefaultZombiesCritical
	}
	if a.Health == (HealthLimits{}) {
		a.Health = DefaultHealthLimits()
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*t = MetricThresholds(a)
	return nil
}

// Pair gives the warn and critical level of a metric. A metric with no pair
// (long) gives the zero pair.
func (t MetricThresholds) Pair(m ThresholdMetric) ThresholdPair {
	switch m {
	case MetricCPU:
		return ThresholdPair{t.CPUWarn, t.CPUCritical}
	case MetricMem:
		return ThresholdPair{t.MemWarn, t.MemCritical}
	case MetricDisk:
		return ThresholdPair{t.DiskWarn, t.DiskCritical}
	case MetricProcs:
		return ThresholdPair{float64(t.ProcsWarn), float64(t.ProcsCritical)}
	case MetricZombies:
		return ThresholdPair{float64(t.ZombiesWarn), float64(t.ZombiesCritical)}
	case MetricPIDs:
		return t.Health.PIDs
	case MetricFiles:
		return t.Health.Files
	case MetricInodes:
		return t.Health.Inodes
	case MetricMemPressure:
		return t.Health.MemPressure
	case MetricIOPressure:
		return t.Health.IOPressure
	}
	return ThresholdPair{}
}

func (t *MetricThresholds) setPair(m ThresholdMetric, p ThresholdPair) {
	switch m {
	case MetricCPU:
		t.CPUWarn, t.CPUCritical = p.Warn, p.Critical
	case MetricMem:
		t.MemWarn, t.MemCritical = p.Warn, p.Critical
	case MetricDisk:
		t.DiskWarn, t.DiskCritical = p.Warn, p.Critical
	case MetricProcs:
		t.ProcsWarn, t.ProcsCritical = int(math.Round(p.Warn)), int(math.Round(p.Critical))
	case MetricZombies:
		t.ZombiesWarn, t.ZombiesCritical = int(math.Round(p.Warn)), int(math.Round(p.Critical))
	case MetricPIDs:
		t.Health.PIDs = p
	case MetricFiles:
		t.Health.Files = p
	case MetricInodes:
		t.Health.Inodes = p
	case MetricMemPressure:
		t.Health.MemPressure = p
	case MetricIOPressure:
		t.Health.IOPressure = p
	}
}

// Validate checks each pair.
func (t MetricThresholds) Validate() error {
	for _, m := range append(slices.Clone(UsageMetrics), HealthMetrics...) {
		if err := t.Pair(m).validate(m); err != nil {
			return err
		}
	}
	if err := t.Health.MacMemPressure.validate(MetricMemPressure); err != nil {
		return fmt.Errorf("macMemPressure: %w", err)
	}
	return nil
}

// ThresholdOverrides holds the pairs that a host replaces. A nil pair uses
// the org value.
type ThresholdOverrides struct {
	CPU         *ThresholdPair `json:"cpu,omitempty"`
	Mem         *ThresholdPair `json:"mem,omitempty"`
	Disk        *ThresholdPair `json:"disk,omitempty"`
	Procs       *ThresholdPair `json:"procs,omitempty"`
	Zombies     *ThresholdPair `json:"zombies,omitempty"`
	PIDs        *ThresholdPair `json:"pids,omitempty"`
	Files       *ThresholdPair `json:"files,omitempty"`
	Inodes      *ThresholdPair `json:"inodes,omitempty"`
	MemPressure *ThresholdPair `json:"memPressure,omitempty"`
	IOPressure  *ThresholdPair `json:"ioPressure,omitempty"`
}

func (o *ThresholdOverrides) slot(m ThresholdMetric) **ThresholdPair {
	switch m {
	case MetricCPU:
		return &o.CPU
	case MetricMem:
		return &o.Mem
	case MetricDisk:
		return &o.Disk
	case MetricProcs:
		return &o.Procs
	case MetricZombies:
		return &o.Zombies
	case MetricPIDs:
		return &o.PIDs
	case MetricFiles:
		return &o.Files
	case MetricInodes:
		return &o.Inodes
	case MetricMemPressure:
		return &o.MemPressure
	case MetricIOPressure:
		return &o.IOPressure
	}
	return nil
}

// Pair gives the override of m, or nil.
func (o ThresholdOverrides) Pair(m ThresholdMetric) *ThresholdPair {
	if s := o.slot(m); s != nil {
		return *s
	}
	return nil
}

func allPairMetrics() []ThresholdMetric {
	return append(slices.Clone(UsageMetrics), HealthMetrics...)
}

// Applied gives the global values with each set pair on top. A memPressure
// pair also sets the macOS pair, because one host has one OS.
func (o ThresholdOverrides) Applied(global MetricThresholds) MetricThresholds {
	t := global
	for _, m := range allPairMetrics() {
		if p := o.Pair(m); p != nil {
			t.setPair(m, *p)
		}
	}
	if o.MemPressure != nil {
		t.Health.MacMemPressure = *o.MemPressure
	}
	return t
}

// Merge gives the overrides of o with the set pairs of top on top.
func (o ThresholdOverrides) Merge(top ThresholdOverrides) ThresholdOverrides {
	out := o
	for _, m := range allPairMetrics() {
		if p := top.Pair(m); p != nil {
			c := *p
			*out.slot(m) = &c
		}
	}
	return out
}

func (o ThresholdOverrides) IsEmpty() bool {
	return !slices.ContainsFunc(allPairMetrics(), func(m ThresholdMetric) bool { return o.Pair(m) != nil })
}

func (o ThresholdOverrides) Validate() error {
	for _, m := range allPairMetrics() {
		if p := o.Pair(m); p != nil {
			if err := p.validate(m); err != nil {
				return err
			}
		}
	}
	return nil
}

// LegacyOverrides converts the old customThresholds (cpu, mem and disk).
func LegacyOverrides(t MetricThresholds) ThresholdOverrides {
	var o ThresholdOverrides
	for _, m := range UsageMetrics {
		p := t.Pair(m)
		*o.slot(m) = &p
	}
	return o
}

// AlertNotify tells which tints of a metric send a notification.
type AlertNotify string

const (
	NotifyOff      AlertNotify = "off"
	NotifyCritical AlertNotify = "critical"
	NotifyAll      AlertNotify = "all"
)

func (n AlertNotify) valid() bool { return n == NotifyOff || n == NotifyCritical || n == NotifyAll }

// Allows reports whether a notification of the tint can go out.
func (n AlertNotify) Allows(t wire.Tint) bool {
	switch n {
	case NotifyAll:
		return t == wire.TintWarn || t == wire.TintCritical
	case NotifyCritical:
		return t == wire.TintCritical
	}
	return false
}

// MaxSustainSeconds is the longest sustain time.
const MaxSustainSeconds = 3600

// AlertRule is the sustain time and the notify level of one metric.
type AlertRule struct {
	SustainSeconds int         `json:"sustainSeconds"`
	Notify         AlertNotify `json:"notify"`
}

func (r AlertRule) Validate() error {
	if r.SustainSeconds < 0 || r.SustainSeconds > MaxSustainSeconds {
		return invalidRules("sustainSeconds must be from 0 to %d", MaxSustainSeconds)
	}
	if !r.Notify.valid() {
		return invalidRules("unknown notify %q", r.Notify)
	}
	return nil
}

// Tolerance limits.
const (
	MinTolerance = 0.5
	MaxTolerance = 1.0
)

func validTolerance(v float64) bool { return v >= MinTolerance && v <= MaxTolerance }

// AlertRules holds the timing of the metrics that alert after a sustain
// time. Tolerance is the part of the window that must be over the line.
type AlertRules struct {
	CPU       AlertRule `json:"cpu"`
	Mem       AlertRule `json:"mem"`
	Disk      AlertRule `json:"disk"`
	Health    AlertRule `json:"health"`
	Tolerance float64   `json:"tolerance"`
}

// DefaultAlertRules are the Mac defaults.
func DefaultAlertRules() AlertRules {
	return AlertRules{
		CPU:       AlertRule{300, NotifyCritical},
		Mem:       AlertRule{120, NotifyAll},
		Disk:      AlertRule{0, NotifyAll},
		Health:    AlertRule{300, NotifyAll},
		Tolerance: 0.8,
	}
}

// For gives the rule of cpu, mem, disk or health.
func (r AlertRules) For(metric string) (AlertRule, bool) {
	switch metric {
	case "cpu":
		return r.CPU, true
	case "mem":
		return r.Mem, true
	case "disk":
		return r.Disk, true
	case "health":
		return r.Health, true
	}
	return AlertRule{}, false
}

func (r AlertRules) Validate() error {
	for _, m := range []string{"cpu", "mem", "disk", "health"} {
		rule, _ := r.For(m)
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("%s: %w", m, err)
		}
	}
	if !validTolerance(r.Tolerance) {
		return invalidRules("tolerance must be from %.1f to %.1f", MinTolerance, MaxTolerance)
	}
	return nil
}

// UnmarshalJSON decodes onto the receiver. A zero receiver gets the
// defaults first, so a missing key has the default value.
func (r *AlertRules) UnmarshalJSON(b []byte) error {
	type alias AlertRules
	a := alias(*r)
	if *r == (AlertRules{}) {
		a = alias(DefaultAlertRules())
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*r = AlertRules(a)
	return nil
}

// AlertOverrides holds the rules that a host replaces.
type AlertOverrides struct {
	CPU       *AlertRule `json:"cpu,omitempty"`
	Mem       *AlertRule `json:"mem,omitempty"`
	Disk      *AlertRule `json:"disk,omitempty"`
	Health    *AlertRule `json:"health,omitempty"`
	Tolerance *float64   `json:"tolerance,omitempty"`
}

func (o AlertOverrides) Applied(global AlertRules) AlertRules {
	out := global
	if o.CPU != nil {
		out.CPU = *o.CPU
	}
	if o.Mem != nil {
		out.Mem = *o.Mem
	}
	if o.Disk != nil {
		out.Disk = *o.Disk
	}
	if o.Health != nil {
		out.Health = *o.Health
	}
	if o.Tolerance != nil {
		out.Tolerance = *o.Tolerance
	}
	return out
}

func (o AlertOverrides) IsEmpty() bool { return o == AlertOverrides{} }

func (o AlertOverrides) Validate() error {
	for name, r := range map[string]*AlertRule{"cpu": o.CPU, "mem": o.Mem, "disk": o.Disk, "health": o.Health} {
		if r == nil {
			continue
		}
		if err := r.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if o.Tolerance != nil && !validTolerance(*o.Tolerance) {
		return invalidRules("tolerance must be from %.1f to %.1f", MinTolerance, MaxTolerance)
	}
	return nil
}

// RulesOrigin tells which client set rules last.
type RulesOrigin struct {
	// Client is web, mac, windows or api.
	Client string    `json:"client"`
	Device string    `json:"device,omitempty"`
	UserID uuid.UUID `json:"userId"`
	At     time.Time `json:"at"`
}

// Clients that set rules.
const (
	ClientWeb     = "web"
	ClientMac     = "mac"
	ClientWindows = "windows"
	ClientAPI     = "api"
)

// NormalizeClient gives a known client name, or api.
func NormalizeClient(c string) string {
	switch c {
	case ClientWeb, ClientMac, ClientWindows:
		return c
	}
	return ClientAPI
}

// OrgRules are the rules of the org in one value.
type OrgRules struct {
	Thresholds MetricThresholds `json:"thresholds"`
	Alerts     AlertRules       `json:"alerts"`
	Muted      MutedSignals     `json:"muted"`
	LongProc   LongProcRule     `json:"longProc"`
	Origin     *RulesOrigin     `json:"origin,omitempty"`
}

// UnmarshalJSON starts from the default rules, so a missing key has the
// default value.
func (r *OrgRules) UnmarshalJSON(b []byte) error {
	type alias OrgRules
	a := alias(DefaultServerSettings().Rules())
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*r = OrgRules(a)
	return nil
}

func (r OrgRules) Validate() error {
	if err := r.Thresholds.Validate(); err != nil {
		return err
	}
	return r.Alerts.Validate()
}

// HostRules are the rules that one host sets. A nil field uses the org rule.
type HostRules struct {
	Thresholds   *ThresholdOverrides `json:"thresholds,omitempty"`
	Alerts       *AlertOverrides     `json:"alerts,omitempty"`
	Muted        MutedSignals        `json:"muted"`
	LongProc     *LongProcRule       `json:"longProc,omitempty"`
	IgnoredProcs []string            `json:"ignoredProcs,omitempty"`
	Origin       *RulesOrigin        `json:"origin,omitempty"`
}

func (r HostRules) Validate() error {
	if r.Thresholds != nil {
		if err := r.Thresholds.Validate(); err != nil {
			return err
		}
	}
	if r.Alerts != nil {
		return r.Alerts.Validate()
	}
	return nil
}

// Rules gives the rules of the org.
func (s ServerSettings) Rules() OrgRules {
	return OrgRules{Thresholds: s.Thresholds, Alerts: s.Alerts, Muted: s.Muted, LongProc: s.LongProc, Origin: s.RulesOrigin}
}

// SetRules replaces the rule fields. It leaves the other fields.
func (s *ServerSettings) SetRules(r OrgRules) {
	s.Thresholds, s.Alerts, s.Muted, s.LongProc, s.RulesOrigin = r.Thresholds, r.Alerts, r.Muted, r.LongProc, r.Origin
}

// Rules gives the rules of the host. Empty overrides are nil.
func (n Node) Rules() HostRules {
	return HostRules{
		Thresholds: n.ThresholdOverrides, Alerts: n.AlertOverrides, Muted: n.MutedSignals,
		LongProc: n.CustomLongProc, IgnoredProcs: n.IgnoredProcs, Origin: n.RulesOrigin,
	}
}

// SetRules replaces the rule fields. It leaves the other fields.
func (n *Node) SetRules(r HostRules) {
	if r.Thresholds != nil && r.Thresholds.IsEmpty() {
		r.Thresholds = nil
	}
	if r.Alerts != nil && r.Alerts.IsEmpty() {
		r.Alerts = nil
	}
	n.ThresholdOverrides, n.AlertOverrides, n.MutedSignals = r.Thresholds, r.Alerts, r.Muted
	n.CustomLongProc, n.IgnoredProcs, n.RulesOrigin = r.LongProc, r.IgnoredProcs, r.Origin
}

// RuleSource tells where a rule comes from.
type RuleSource string

const (
	SourceOrg  RuleSource = "org"
	SourceHost RuleSource = "host"
)

// RuleSources tells the source of each effective rule. Muted has the
// muted metrics only.
type RuleSources struct {
	Thresholds map[ThresholdMetric]RuleSource `json:"thresholds"`
	Alerts     map[string]RuleSource          `json:"alerts"`
	Muted      map[ThresholdMetric]RuleSource `json:"muted"`
	LongProc   RuleSource                     `json:"longProc"`
}

// EffectiveRules are the rules that the engine applies to one host.
type EffectiveRules struct {
	Thresholds MetricThresholds `json:"thresholds"`
	Alerts     AlertRules       `json:"alerts"`
	Muted      MutedSignals     `json:"muted"`
	LongProc   LongProcRule     `json:"longProc"`
	Ignore     []string         `json:"ignore"`
	Sources    RuleSources      `json:"sources"`
}

// For gives the rules of one host: the org rules with the host rules on top.
func (r OrgRules) For(h HostRules) EffectiveRules {
	var to ThresholdOverrides
	if h.Thresholds != nil {
		to = *h.Thresholds
	}
	var ao AlertOverrides
	if h.Alerts != nil {
		ao = *h.Alerts
	}
	out := EffectiveRules{
		Thresholds: to.Applied(r.Thresholds),
		Alerts:     ao.Applied(r.Alerts),
		Muted:      r.Muted.Union(h.Muted),
		LongProc:   r.LongProc,
		Sources: RuleSources{
			Thresholds: map[ThresholdMetric]RuleSource{},
			Alerts:     map[string]RuleSource{},
			Muted:      map[ThresholdMetric]RuleSource{},
			LongProc:   SourceOrg,
		},
	}
	for _, m := range allPairMetrics() {
		out.Sources.Thresholds[m] = sourceOf(to.Pair(m) != nil)
	}
	out.Sources.Alerts["cpu"] = sourceOf(ao.CPU != nil)
	out.Sources.Alerts["mem"] = sourceOf(ao.Mem != nil)
	out.Sources.Alerts["disk"] = sourceOf(ao.Disk != nil)
	out.Sources.Alerts["health"] = sourceOf(ao.Health != nil)
	out.Sources.Alerts["tolerance"] = sourceOf(ao.Tolerance != nil)
	for _, m := range out.Muted {
		out.Sources.Muted[m] = sourceOf(!r.Muted.Has(m))
	}
	ignore := slices.Concat(r.LongProc.Ignore, h.IgnoredProcs)
	if h.LongProc != nil {
		out.LongProc = *h.LongProc
		out.Sources.LongProc = SourceHost
		ignore = slices.Concat(ignore, h.LongProc.Ignore)
	}
	out.Ignore = uniqueFold(ignore)
	return out
}

func sourceOf(host bool) RuleSource {
	if host {
		return SourceHost
	}
	return SourceOrg
}

// uniqueFold gives the names without duplicates (case does not count), sorted.
func uniqueFold(names []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range names {
		if l := strings.ToLower(n); !seen[l] {
			seen[l] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// HealthCheck is one health check that is over its warn level.
type HealthCheck struct {
	Metric ThresholdMetric
	Tint   wire.Tint
	Text   string
}

// HealthChecks gives the checks of the health brief that are over their
// limits, critical first. Muted checks are left out. It follows the Mac
// rules. kernelCriticalOnly is true when the host overrides memory
// pressure: with no pressure number, only the kernel critical level counts.
// The brief has the worst inode ratio of all mounts, so inodes have one check
// with no mount name.
func (t MetricThresholds) HealthChecks(h wire.HealthBrief, muted MutedSignals, kernelCriticalOnly bool) []HealthCheck {
	var out []HealthCheck
	add := func(m ThresholdMetric, tint wire.Tint, text string) {
		if tint != wire.TintOK && !muted.Has(m) {
			out = append(out, HealthCheck{Metric: m, Tint: tint, Text: text})
		}
	}
	level := func(v float64, p ThresholdPair) wire.Tint {
		switch {
		case v >= p.Critical:
			return wire.TintCritical
		case v >= p.Warn:
			return wire.TintWarn
		}
		return wire.TintOK
	}
	pct := func(f float64) string { return strconv.Itoa(int(math.Round(f*100))) + "%" }
	whole := func(v float64) string { return strconv.Itoa(int(math.Round(v))) + "%" }

	add(MetricProcs, level(float64(h.Procs), t.Pair(MetricProcs)), groupInt(h.Procs)+" processes")
	if h.Zombies != nil {
		add(MetricZombies, level(float64(*h.Zombies), t.Pair(MetricZombies)), groupInt(*h.Zombies)+" zombies")
	}
	if f, ok := fraction(h.PidsUsed, h.PidsMax); ok {
		add(MetricPIDs, level(f, t.Health.PIDs), "PIDs "+pct(f)+" of limit")
	}
	if f, ok := fraction(h.FilesUsed, h.FilesMax); ok {
		add(MetricFiles, level(f, t.Health.Files), "Open files "+pct(f)+" of limit")
	}
	switch {
	case h.MemPressurePct != nil:
		v := float64(*h.MemPressurePct)
		add(MetricMemPressure, level(v, t.Health.MacMemPressure), "Memory pressure "+whole(v))
	case h.PSI != nil:
		p := t.Health.MemPressure
		if h.PSI.MemFull >= p.Critical {
			add(MetricMemPressure, wire.TintCritical, "Memory pressure "+whole(h.PSI.MemFull))
		} else if h.PSI.MemSome >= p.Warn {
			add(MetricMemPressure, wire.TintWarn, "Memory pressure "+whole(h.PSI.MemSome))
		}
	case h.MemPressure != nil:
		switch {
		case *h.MemPressure == 4:
			add(MetricMemPressure, wire.TintCritical, "Memory pressure critical")
		case *h.MemPressure == 2 && !kernelCriticalOnly:
			add(MetricMemPressure, wire.TintWarn, "Memory pressure high")
		}
	}
	if h.PSI != nil {
		add(MetricIOPressure, level(h.PSI.IOFull, t.Health.IOPressure), "I/O pressure "+whole(h.PSI.IOFull))
	}
	if h.Inodes > 0 {
		add(MetricInodes, level(h.Inodes, t.Health.Inodes), "Inodes "+pct(h.Inodes))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Tint == wire.TintCritical && out[j].Tint != wire.TintCritical
	})
	return out
}

func fraction(used, max *int64) (float64, bool) {
	if used == nil || max == nil || *max <= 0 {
		return 0, false
	}
	return float64(*used) / float64(*max), true
}

// groupInt writes n with a comma between each group of three digits.
func groupInt(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
