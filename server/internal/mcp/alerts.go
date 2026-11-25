package mcp

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Limits of the alerts tool.
const (
	defaultAlertsLimit = 50
	maxAlertsLimit     = 100
	defaultAlertsRange = "7d"
)

// AlertsIn is the input of alerts.
type AlertsIn struct {
	State string   `json:"state,omitempty" jsonschema:"open (not resolved, default), action (open, not acked, not snoozed), resolved or all"`
	Range string   `json:"range,omitempty" jsonschema:"span up to now, for example 24h or 7d. Alerts that overlap the range count. At most 90d. Default 7d. Do not use with from."`
	From  string   `json:"from,omitempty" jsonschema:"start of the range in RFC 3339. Use it with to, instead of range."`
	To    string   `json:"to,omitempty" jsonschema:"end of the range in RFC 3339. Default now."`
	Host  string   `json:"host,omitempty" jsonschema:"host ID or name. Empty means the whole fleet."`
	Kind  []string `json:"kind,omitempty" jsonschema:"keep these kinds: cpu, mem, disk, health, long (long-running process) or nodata"`
	Tint  []string `json:"tint,omitempty" jsonschema:"keep these severities (the worst level of the alert): critical or warn"`
	Q     string   `json:"q,omitempty" jsonschema:"text to find in the host name, the metric and the process name or command"`
	Sort  string   `json:"sort,omitempty" jsonschema:"sev (default), start, peak, duration, title, host, kind or state. The number sorts give the highest first. The text sorts give A to Z."`
	Limit int      `json:"limit,omitempty" jsonschema:"most alerts to return, 1 to 100. Default 50."`
}

// AlertProcOut is the process of a long-process alert.
type AlertProcOut struct {
	Key       string  `json:"key"`
	PID       int32   `json:"pid"`
	Name      string  `json:"name"`
	Command   string  `json:"command"`
	Repo      string  `json:"repo,omitempty"`
	StartedAt string  `json:"started_at,omitempty"`
	HighSince string  `json:"high_since,omitempty"`
	CPUPct    float64 `json:"cpu_pct"`
	Cwd       string  `json:"cwd,omitempty"`
	Parent    string  `json:"parent,omitempty"`
}

// AlertItem is one alert. Severity is the worst level: warn or critical.
// State is open, acked, snoozed or resolved. Value is the value at the
// last change. Peak is the highest value.
type AlertItem struct {
	ID           string        `json:"id"`
	Host         string        `json:"host"`
	HostID       string        `json:"host_id"`
	Kind         string        `json:"kind"`
	Metric       string        `json:"metric"`
	Severity     string        `json:"severity"`
	State        string        `json:"state"`
	Value        float64       `json:"value"`
	Peak         *float64      `json:"peak,omitempty"`
	Start        string        `json:"start"`
	End          string        `json:"end,omitempty"`
	AckedBy      string        `json:"acked_by,omitempty"`
	SnoozedUntil string        `json:"snoozed_until,omitempty"`
	Proc         *AlertProcOut `json:"proc,omitempty"`
}

// newAlertItem converts an alert of the query service. An alert from the
// open list has no start, severity and state, so the item takes them from
// the last event.
func newAlertItem(a wire.Alert, host string, now time.Time) AlertItem {
	item := AlertItem{
		ID: a.ID.String(), Host: cmp.Or(host, a.NodeName), HostID: a.NodeID.String(), Kind: a.Kind, Metric: a.Metric,
		Severity: cmp.Or(string(a.Severity), string(a.Tint)), State: a.State, Value: a.Value, Peak: a.Peak,
		Start: ts(a.TS), End: tsPtr(a.End), SnoozedUntil: tsPtr(a.SnoozedUntil),
	}
	if a.Start != nil {
		item.Start = ts(*a.Start)
	}
	if a.AckedBy != nil {
		item.AckedBy = a.AckedBy.String()
	}
	if item.State == "" {
		switch {
		case a.AckedBy != nil:
			item.State = "acked"
		case a.SnoozedUntil != nil && a.SnoozedUntil.After(now):
			item.State = "snoozed"
		default:
			item.State = "open"
		}
	}
	if p := a.Proc; p != nil {
		item.Proc = &AlertProcOut{
			Key: p.Key.String(), PID: p.PID, Name: p.Name, Command: p.Cmd, Repo: p.Repo, StartedAt: tsPtr(p.StartTS),
			HighSince: tsPtr(p.HighSince), CPUPct: p.CPUPct, Cwd: p.Cwd, Parent: p.Parent,
		}
	}
	return item
}

// AlertsOut is the output of alerts. Total counts all alerts that match
// the filters, and Counts counts each state with the other filters on.
// Truncated is true when the server read only the newest alerts of a big
// range, so the counts are not complete.
type AlertsOut struct {
	From      string           `json:"from"`
	To        string           `json:"to"`
	Clamped   bool             `json:"clamped,omitempty"`
	Truncated bool             `json:"truncated,omitempty"`
	Total     int              `json:"total"`
	Counts    wire.AlertCounts `json:"counts"`
	Alerts    []AlertItem      `json:"alerts"`
}

// oneOf checks that every value is in the list.
func oneOf(field string, got, ok []string) error {
	for _, v := range got {
		if !slices.Contains(ok, v) {
			return badf("%s must be one of %s", field, strings.Join(ok, ", "))
		}
	}
	return nil
}

func (s *Server) alerts(ctx context.Context, _ *sdk.CallToolRequest, in AlertsIn) (*sdk.CallToolResult, AlertsOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, AlertsOut{}, err
	}
	orgID := c.Token.OrgID
	state := cmp.Or(in.State, "open")
	// recent was the old state of this tool. It gives all alerts of 7 days.
	if state == "recent" {
		state = "all"
	}
	sort := cmp.Or(in.Sort, "sev")
	for _, f := range []struct {
		field string
		got   []string
		ok    []string
	}{
		{"state", []string{state}, query.AlertStates},
		{"kind", in.Kind, query.AlertKinds},
		{"tint", in.Tint, query.AlertTints},
		{"sort", []string{sort}, query.AlertSorts},
	} {
		if err := oneOf(f.field, f.got, f.ok); err != nil {
			return nil, AlertsOut{}, err
		}
	}
	if in.Limit < 0 || in.Limit > maxAlertsLimit {
		return nil, AlertsOut{}, badf("limit must be 1 to %d", maxAlertsLimit)
	}
	rangeText := in.Range
	if rangeText == "" && in.From == "" {
		rangeText = defaultAlertsRange
	}
	now := s.now().UTC()
	from, to, err := parseRange(rangeText, in.From, in.To, now)
	if err != nil {
		return nil, AlertsOut{}, err
	}
	var hosts []uuid.UUID
	if id, err := s.hostID(ctx, orgID, in.Host); err != nil {
		return nil, AlertsOut{}, s.toolError("alerts", err)
	} else if id != uuid.Nil {
		hosts = []uuid.UUID{id}
	}
	page, err := s.q.ListAlerts(ctx, query.AlertQuery{
		OrgID: orgID, State: state, Tints: in.Tint, Kinds: in.Kind, Hosts: hosts, Q: in.Q, From: from, To: to,
		Sort: sort, Desc: !slices.Contains([]string{"title", "host", "kind", "state"}, sort),
		Limit: cmp.Or(in.Limit, defaultAlertsLimit), Now: now,
	})
	if err != nil {
		return nil, AlertsOut{}, s.toolError("alerts", err)
	}
	out := AlertsOut{
		From: ts(page.From), To: ts(page.To), Clamped: page.Clamped, Truncated: page.Truncated, Total: page.Total,
		Counts: page.Counts, Alerts: make([]AlertItem, 0, len(page.Alerts)),
	}
	for _, a := range page.Alerts {
		out.Alerts = append(out.Alerts, newAlertItem(a, "", now))
	}
	return nil, out, nil
}

// GetAlertIn is the input of get_alert.
type GetAlertIn struct {
	AlertID string `json:"alert_id" jsonschema:"alert ID, from the alerts tool"`
}

// AlertLimits are the limits that the alert had when it opened.
type AlertLimits struct {
	Warn     float64 `json:"warn"`
	Critical float64 `json:"critical"`
	Unit     string  `json:"unit,omitempty"`
}

// RuleNow is the rule that applies to the host of the alert now. It can
// differ from the limits that the alert had when it opened. Warn and
// Critical are in Unit. A long-process rule has CPUPct and no Warn.
type RuleNow struct {
	Kind           string   `json:"kind"`
	Warn           *float64 `json:"warn,omitempty"`
	Critical       *float64 `json:"critical,omitempty"`
	Unit           string   `json:"unit,omitempty"`
	CPUPct         *float64 `json:"cpu_pct,omitempty"`
	SustainSeconds int      `json:"sustain_seconds"`
	Notify         string   `json:"notify"`
	Muted          bool     `json:"muted,omitempty"`
	// Source is host when the host replaces the org value.
	Source string `json:"source"`
}

// TimelineEntry is one line of the history of an alert. Kind is opened,
// changed, notified, escalated, acked, unacked, snoozed, unsnoozed,
// recovered or closed.
type TimelineEntry struct {
	TS    string `json:"ts"`
	Kind  string `json:"kind"`
	Tint  string `json:"tint,omitempty"`
	User  string `json:"user,omitempty"`
	Until string `json:"until,omitempty"`
	Text  string `json:"text,omitempty"`
}

// GetAlertOut is the output of get_alert. Notifications is the number of
// notified lines in the timeline. RuleNow is missing for a host that is
// gone and for an alert with no rule (nodata).
type GetAlertOut struct {
	Alert         AlertItem       `json:"alert"`
	Limits        *AlertLimits    `json:"limits,omitempty"`
	RuleNow       *RuleNow        `json:"rule_now,omitempty"`
	Notifications int             `json:"notifications"`
	Timeline      []TimelineEntry `json:"timeline"`
}

func (s *Server) getAlert(ctx context.Context, _ *sdk.CallToolRequest, in GetAlertIn) (*sdk.CallToolResult, GetAlertOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, GetAlertOut{}, err
	}
	orgID := c.Token.OrgID
	id, err := uuid.Parse(strings.TrimSpace(in.AlertID))
	if err != nil {
		return nil, GetAlertOut{}, badf("alert_id must be an alert ID from the alerts tool")
	}
	d, err := s.q.AlertDetail(ctx, orgID, id)
	if err != nil {
		return nil, GetAlertOut{}, s.toolError("get_alert", err)
	}
	out := GetAlertOut{Alert: newAlertItem(d.Alert, "", s.now().UTC()), Timeline: make([]TimelineEntry, 0, len(d.Timeline))}
	if l := d.Limits; l != nil {
		out.Limits = &AlertLimits{Warn: l.Warn, Critical: l.Critical, Unit: pairUnit(d.Alert.Metric)}
	}
	for _, l := range d.Timeline {
		e := TimelineEntry{TS: ts(l.TS), Kind: l.Kind, Tint: string(l.Tint), Until: tsPtr(l.Until), Text: l.Text}
		if l.User != nil {
			e.User = l.User.String()
		}
		if l.Kind == "notified" {
			out.Notifications++
		}
		out.Timeline = append(out.Timeline, e)
	}
	rules, err := s.q.Rules(ctx, orgID, d.Alert.NodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, GetAlertOut{}, s.toolError("get_alert", err)
	}
	if err == nil {
		out.RuleNow = ruleNow(d.Alert, rules)
	}
	return nil, out, nil
}

// pairUnit is the unit of the limits of a rule metric, or empty when the
// metric has no limits.
func pairUnit(metric string) string {
	m := store.ThresholdMetric(metric)
	if m == store.MetricLong || !m.Valid() {
		return ""
	}
	return thresholdUnit(m)
}

// ruleNow gives the rule that applies now to the metric of the alert.
func ruleNow(a wire.Alert, r query.RulesResult) *RuleNow {
	eff := r.Effective
	if eff == nil {
		return nil
	}
	switch {
	case a.Kind == "long_process":
		lp := eff.LongProc
		return &RuleNow{
			Kind: "long", CPUPct: &lp.CPUPct, Unit: "percent of one core", SustainSeconds: lp.SustainSeconds, Notify: lp.Notify,
			Muted: eff.Muted.Has(store.MetricLong), Source: string(eff.Sources.LongProc),
		}
	case a.Kind == "reachability_changed":
		return nil
	}
	m := store.ThresholdMetric(a.Metric)
	kind := a.Metric
	if !slices.Contains(store.UsageMetrics, m) {
		kind = "health"
	}
	rule, ok := eff.Alerts.For(kind)
	if !ok {
		return nil
	}
	out := &RuleNow{
		Kind: kind, SustainSeconds: rule.SustainSeconds, Notify: string(rule.Notify), Muted: eff.Muted.Has(m),
		Source: string(eff.Sources.Alerts[kind]),
	}
	if kind != "health" {
		p := eff.Thresholds.Pair(m)
		out.Warn, out.Critical, out.Unit = &p.Warn, &p.Critical, thresholdUnit(m)
		if eff.Sources.Thresholds[m] == store.SourceHost {
			out.Source = string(store.SourceHost)
		}
	}
	return out
}
