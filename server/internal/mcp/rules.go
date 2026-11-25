package mcp

import (
	"context"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/store"
)

// HostRulesIn is the input of host_rules.
type HostRulesIn struct {
	Host string `json:"host,omitempty" jsonschema:"host ID or name. Empty gives the rules of the organization only."`
}

// ThresholdOut is the warn and critical limit of a metric. The unit is in
// the units map of the output.
type ThresholdOut struct {
	Warn     float64 `json:"warn"`
	Critical float64 `json:"critical"`
}

// AlertRuleOut is the sustain time and the notify level of a rule.
type AlertRuleOut struct {
	SustainSeconds int    `json:"sustain_seconds"`
	Notify         string `json:"notify"`
}

// AlertRulesOut holds the rules of cpu, mem, disk and health, and the
// tolerance: the part of the sustain window that must be over the limit.
type AlertRulesOut struct {
	CPU       AlertRuleOut `json:"cpu"`
	Mem       AlertRuleOut `json:"mem"`
	Disk      AlertRuleOut `json:"disk"`
	Health    AlertRuleOut `json:"health"`
	Tolerance float64      `json:"tolerance"`
}

// AlertOverridesOut holds the alert rules that a host replaces.
type AlertOverridesOut struct {
	CPU       *AlertRuleOut `json:"cpu,omitempty"`
	Mem       *AlertRuleOut `json:"mem,omitempty"`
	Disk      *AlertRuleOut `json:"disk,omitempty"`
	Health    *AlertRuleOut `json:"health,omitempty"`
	Tolerance *float64      `json:"tolerance,omitempty"`
}

// LongProcOut is the long-process rule. CPUPct is a percent of one core.
type LongProcOut struct {
	Enabled        bool     `json:"enabled"`
	CPUPct         float64  `json:"cpu_pct"`
	SustainSeconds int      `json:"sustain_seconds"`
	Notify         string   `json:"notify"`
	Ignore         []string `json:"ignore"`
}

// NotifyFlagsOut are the org switches of the notifications.
type NotifyFlagsOut struct {
	Enabled  bool `json:"enabled"`
	Warn     bool `json:"warn"`
	Critical bool `json:"critical"`
}

// OrgRulesOut are the rules of the organization.
type OrgRulesOut struct {
	Thresholds      map[string]ThresholdOut `json:"thresholds"`
	Alerts          AlertRulesOut           `json:"alerts"`
	Muted           []string                `json:"muted"`
	LongProc        LongProcOut             `json:"long_proc"`
	CooldownSeconds int                     `json:"cooldown_seconds"`
	DebounceSeconds int                     `json:"debounce_seconds"`
	NotifyFlags     NotifyFlagsOut          `json:"notify_flags"`
}

// HostOverridesOut are the rules that a host replaces.
type HostOverridesOut struct {
	Thresholds   map[string]ThresholdOut `json:"thresholds"`
	Alerts       *AlertOverridesOut      `json:"alerts,omitempty"`
	Muted        []string                `json:"muted"`
	LongProc     *LongProcOut            `json:"long_proc,omitempty"`
	IgnoredProcs []string                `json:"ignored_procs"`
}

// HostRulesOut are the rules of one host. NotifyWarn and NotifyCritical
// are the switches of the host. A notification needs the org switch and
// the host switch, and the host must not be snoozed.
type HostRulesOut struct {
	Overrides      HostOverridesOut `json:"overrides"`
	NotifyWarn     bool             `json:"notify_warn"`
	NotifyCritical bool             `json:"notify_critical"`
	SnoozedUntil   string           `json:"snoozed_until,omitempty"`
}

// SourcesOut tells for each effective rule if it comes from the org or
// from the host.
type SourcesOut struct {
	Thresholds map[string]string `json:"thresholds"`
	Alerts     map[string]string `json:"alerts"`
	Muted      map[string]string `json:"muted"`
	LongProc   string            `json:"long_proc"`
}

// EffectiveRulesOut are the rules that apply to a host.
type EffectiveRulesOut struct {
	Thresholds map[string]ThresholdOut `json:"thresholds"`
	Alerts     AlertRulesOut           `json:"alerts"`
	Muted      []string                `json:"muted"`
	LongProc   LongProcOut             `json:"long_proc"`
	Ignore     []string                `json:"ignore"`
	Sources    SourcesOut              `json:"sources"`
}

// HostRulesResult is the output of host_rules. Units gives the unit of the
// limits of each metric. Host and Effective are missing when the call has
// no host.
type HostRulesResult struct {
	Org       OrgRulesOut        `json:"org"`
	Host      *HostRulesOut      `json:"host,omitempty"`
	Effective *EffectiveRulesOut `json:"effective,omitempty"`
	Units     map[string]string  `json:"units"`
}

var ruleMetrics = slices.Concat(store.UsageMetrics, store.HealthMetrics)

func thresholdsOut(t store.MetricThresholds) map[string]ThresholdOut {
	out := make(map[string]ThresholdOut, len(ruleMetrics))
	for _, m := range ruleMetrics {
		p := t.Pair(m)
		out[string(m)] = ThresholdOut{Warn: p.Warn, Critical: p.Critical}
	}
	return out
}

func alertRuleOut(r store.AlertRule) AlertRuleOut {
	return AlertRuleOut{SustainSeconds: r.SustainSeconds, Notify: string(r.Notify)}
}

func alertRulesOut(a store.AlertRules) AlertRulesOut {
	return AlertRulesOut{
		CPU: alertRuleOut(a.CPU), Mem: alertRuleOut(a.Mem), Disk: alertRuleOut(a.Disk), Health: alertRuleOut(a.Health),
		Tolerance: a.Tolerance,
	}
}

func mutedOut(m store.MutedSignals) []string {
	out := make([]string, 0, len(m))
	for _, x := range m {
		out = append(out, string(x))
	}
	return out
}

func longProcOut(r store.LongProcRule) LongProcOut {
	return LongProcOut{
		Enabled: r.Enabled, CPUPct: r.CPUPct, SustainSeconds: r.SustainSeconds, Notify: r.Notify,
		Ignore: append([]string{}, r.Ignore...),
	}
}

func sourcesOut[K ~string](m map[K]store.RuleSource) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[string(k)] = string(v)
	}
	return out
}

func (s *Server) hostRules(ctx context.Context, _ *sdk.CallToolRequest, in HostRulesIn) (*sdk.CallToolResult, HostRulesResult, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, HostRulesResult{}, err
	}
	orgID := c.Token.OrgID
	node, err := s.hostID(ctx, orgID, in.Host)
	if err != nil {
		return nil, HostRulesResult{}, s.toolError("host_rules", err)
	}
	r, err := s.q.Rules(ctx, orgID, node)
	if err != nil {
		return nil, HostRulesResult{}, s.toolError("host_rules", err)
	}
	out := HostRulesResult{
		Org: OrgRulesOut{
			Thresholds: thresholdsOut(r.Org.Thresholds), Alerts: alertRulesOut(r.Org.Alerts), Muted: mutedOut(r.Org.Muted),
			LongProc: longProcOut(r.Org.LongProc), CooldownSeconds: int(r.Cooldown.Seconds()), DebounceSeconds: int(r.Debounce.Seconds()),
			NotifyFlags: NotifyFlagsOut{Enabled: r.Notify.Enabled, Warn: r.Notify.Warn, Critical: r.Notify.Critical},
		},
		Units: make(map[string]string, len(ruleMetrics)),
	}
	for _, m := range ruleMetrics {
		out.Units[string(m)] = thresholdUnit(m)
	}
	if r.Host != nil {
		h := r.Host
		o := HostOverridesOut{
			Thresholds: map[string]ThresholdOut{}, Muted: mutedOut(h.Muted), IgnoredProcs: append([]string{}, h.IgnoredProcs...),
		}
		if h.Thresholds != nil {
			for _, m := range ruleMetrics {
				if p := h.Thresholds.Pair(m); p != nil {
					o.Thresholds[string(m)] = ThresholdOut{Warn: p.Warn, Critical: p.Critical}
				}
			}
		}
		if a := h.Alerts; a != nil {
			ao := &AlertOverridesOut{Tolerance: a.Tolerance}
			for dst, src := range map[**AlertRuleOut]*store.AlertRule{&ao.CPU: a.CPU, &ao.Mem: a.Mem, &ao.Disk: a.Disk, &ao.Health: a.Health} {
				if src != nil {
					v := alertRuleOut(*src)
					*dst = &v
				}
			}
			o.Alerts = ao
		}
		if h.LongProc != nil {
			lp := longProcOut(*h.LongProc)
			o.LongProc = &lp
		}
		out.Host = &HostRulesOut{
			Overrides: o, NotifyWarn: derefBool(r.Notify.HostWarn), NotifyCritical: derefBool(r.Notify.HostCritical),
			SnoozedUntil: tsPtr(r.Notify.HostSnoozedUntil),
		}
	}
	if e := r.Effective; e != nil {
		out.Effective = &EffectiveRulesOut{
			Thresholds: thresholdsOut(e.Thresholds), Alerts: alertRulesOut(e.Alerts), Muted: mutedOut(e.Muted),
			LongProc: longProcOut(e.LongProc), Ignore: append([]string{}, e.Ignore...),
			Sources: SourcesOut{
				Thresholds: sourcesOut(e.Sources.Thresholds), Alerts: sourcesOut(e.Sources.Alerts),
				Muted: sourcesOut(e.Sources.Muted), LongProc: string(e.Sources.LongProc),
			},
		}
	}
	return nil, out, nil
}

func derefBool(b *bool) bool { return b != nil && *b }
