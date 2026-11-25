package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Actions is the part of actions.Service that the write tools use.
type Actions interface {
	MarkAlert(ctx context.Context, a actions.Actor, alertID uuid.UUID, act actions.AlertAction) (wire.Alert, error)
	SnoozeHost(ctx context.Context, a actions.Actor, nodeID uuid.UUID, until *time.Time) (store.Node, error)
	SetMute(ctx context.Context, a actions.Actor, metric store.ThresholdMetric, nodeID *uuid.UUID, muted bool) (actions.MuteResult, error)
	EditRules(ctx context.Context, a actions.Actor, ed actions.RulesEdit) (actions.RulesResult, error)
	IgnoreProcesses(ctx context.Context, a actions.Actor, items []actions.IgnoreItem, scope actions.IgnoreScope, undo bool) (actions.RulesResult, []error, error)
	IgnoreAlert(ctx context.Context, a actions.Actor, alertID uuid.UUID, scope actions.IgnoreScope, undo bool) (wire.Alert, actions.RulesResult, error)
	Restore(ctx context.Context, a actions.Actor, backupID uuid.UUID, mode actions.RestoreMode) (actions.RulesResult, error)
	RestoreEdit(ctx context.Context, a actions.Actor, backupID uuid.UUID) (actions.RulesEdit, error)
}

// ErrUnknownTool means that the tool is not a write tool with a rules edit.
var ErrUnknownTool = errors.New("mcp: not a rules write tool")

// Limits of a snooze.
const (
	minSnooze = 5 * time.Minute
	maxSnooze = 30 * 24 * time.Hour
)

// Change is the diff of a write call. Each write tool puts it in the output
// key change. BackupID is the rules backup that the call made, if any.
type Change struct {
	Targets  []ChangeTarget `json:"targets"`
	Fields   []ChangeField  `json:"fields"`
	BackupID string         `json:"backup_id,omitempty"`
}

// ChangeTarget is a thing that the call changed. Kind is alert, host or org.
type ChangeTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// ChangeField is one field that changed. Target is the ID of a target, or
// org. Path is the snake_case path of the field. A missing After means that
// the field is not set now.
type ChangeField struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

var orgChangeTarget = ChangeTarget{Kind: "org", Name: "org"}

func hostChangeTarget(n store.Node) ChangeTarget {
	return ChangeTarget{Kind: "host", ID: n.ID.String(), Name: n.DisplayName}
}

func newChange(targets ...ChangeTarget) Change {
	return Change{Targets: targets, Fields: []ChangeField{}}
}

func jsonAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}

// snakePath turns the camelCase JSON path of a rule field into snake_case.
func snakePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if unicode.IsUpper(r) {
			b.WriteByte('_')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// rulesChange builds the change of a rules write. The backup and the diff
// come from the result.
func rulesChange(res actions.RulesResult, targets ...ChangeTarget) Change {
	ch := newChange(targets...)
	for _, f := range res.Diff {
		ch.Fields = append(ch.Fields, ChangeField{Target: f.Target, Path: snakePath(f.Path), Before: jsonAny(f.Before), After: jsonAny(f.After)})
	}
	if res.Backup.ID != uuid.Nil {
		ch.BackupID = res.Backup.ID.String()
	}
	return ch
}

func actorOf(c caller) actions.Actor {
	return actions.NewActor(c.Token, store.ClientAI, c.Token.Label)
}

// writeCall is one call of a write tool, after the right check.
type writeCall struct {
	s     *Server
	tool  string
	c     caller
	actor actions.Actor
}

// begin checks the right of the caller for the tool. A refused call is
// logged. It gives an error when the server has no write service.
func (s *Server) begin(ctx context.Context, tool string, n need) (*writeCall, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, err
	}
	g, err := s.grantOf(ctx, c)
	if err != nil {
		return nil, s.toolError(tool, err)
	}
	if err := g.allow(n); err != nil {
		s.auditLog(c, tool, nil, err)
		return nil, err
	}
	if s.actions == nil {
		return nil, badf("Changes are not available on this server.")
	}
	return &writeCall{s: s, tool: tool, c: c, actor: actorOf(c)}, nil
}

// done logs the call and gives the error that the model sees.
func (w *writeCall) done(ch Change, err error) error {
	if err != nil {
		w.s.auditLog(w.c, w.tool, nil, err)
		return w.s.writeError(w.tool, err)
	}
	w.s.auditLog(w.c, w.tool, &ch, nil)
	return nil
}

// auditLog writes one log line for each write call, also a refused one. It
// keeps no metric values.
func (s *Server) auditLog(c caller, tool string, ch *Change, err error) {
	args := []any{
		"tool", tool, "client", c.Token.Label, "org_id", c.Token.OrgID, "actor_token", c.Token.ID,
		"actor_kind", c.Token.Kind, "role", c.Role,
	}
	if c.Token.UserID != nil {
		args = append(args, "user_id", *c.Token.UserID)
	}
	var right *rightError
	switch {
	case err == nil:
		args = append(args, "result", "ok")
	case errors.As(err, &right):
		args = append(args, "result", "denied", "reason", right.err.Error())
	default:
		args = append(args, "result", "error")
	}
	if ch != nil {
		targets := make([]string, len(ch.Targets))
		for i, t := range ch.Targets {
			targets[i] = t.Kind + ":" + cmpOr(t.ID, t.Name)
		}
		args = append(args, "targets", targets)
		if ch.BackupID != "" {
			args = append(args, "backup_id", ch.BackupID)
		}
	}
	s.log.Info("audit", args...)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// writeError gives the text of an error of a write call. Other errors go
// to toolError.
func (s *Server) writeError(tool string, err error) error {
	var conflict *actions.ConflictError
	switch {
	case errors.As(err, &conflict):
		return errors.New("The rules of " + strings.Join(conflict.Targets, ", ") + " changed after the backup. Nothing was restored.")
	case errors.Is(err, actions.ErrNotFound):
		return errors.New("alert not found")
	case errors.Is(err, actions.ErrNotOpen):
		return errors.New("alert is not open")
	case errors.Is(err, actions.ErrNotReady):
		return errors.New("alerts are not available now. Try again later.")
	case errors.Is(err, actions.ErrBadUntil), errors.Is(err, actions.ErrNoProcess), errors.Is(err, actions.ErrBadMetric):
		return err
	case errors.Is(err, actions.ErrBadInput), errors.Is(err, store.ErrInvalidRules):
		return &inputError{msg: err.Error()}
	case errors.Is(err, store.ErrNotFound) && tool == "undo_rules_change":
		return errors.New("rules backup not found")
	}
	return s.toolError(tool, err)
}

// hostName gives the display name of a host, or an empty text.
func (s *Server) hostName(ctx context.Context, org, id uuid.UUID) string {
	n, err := s.q.ResolveHost(ctx, org, id.String())
	if err != nil {
		return ""
	}
	return n.DisplayName
}

func parseAlertID(text string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(text))
	if err != nil {
		return uuid.Nil, badf("alert_id must be an alert ID from the alerts tool")
	}
	return id, nil
}

// requireHost resolves a host that the call must name.
func (s *Server) requireHost(ctx context.Context, org uuid.UUID, ref string) (store.Node, error) {
	if strings.TrimSpace(ref) == "" {
		return store.Node{}, badf("host is required")
	}
	return s.q.ResolveHost(ctx, org, ref)
}

// snoozeUntil gives the end of a snooze from until or for. An undo takes
// neither and gives nil.
func (s *Server) snoozeUntil(until, forText string, undo bool) (*time.Time, error) {
	switch {
	case undo && (until != "" || forText != ""):
		return nil, badf("undo takes neither until nor for")
	case undo:
		return nil, nil
	case until != "" && forText != "":
		return nil, badf("use until or for, but not both")
	case until == "" && forText == "":
		return nil, badf("give until (RFC 3339) or for (a duration from 5m to 30d)")
	}
	now := s.now().UTC()
	var end time.Time
	if forText != "" {
		d, err := parseSpan(forText)
		if err != nil {
			return nil, err
		}
		end = now.Add(d)
	} else {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(until))
		if err != nil {
			return nil, badf("until must be RFC 3339, for example 2026-11-20T18:00:00Z")
		}
		end = t.UTC()
	}
	if d := end.Sub(now); d < minSnooze || d > maxSnooze {
		return nil, badf("a snooze must last from 5m to 30d")
	}
	return &end, nil
}

// AckAlertIn is the input of ack_alert.
type AckAlertIn struct {
	AlertID string `json:"alert_id" jsonschema:"alert ID from the alerts tool"`
	Undo    bool   `json:"undo,omitempty" jsonschema:"take back the acknowledgement"`
}

// SnoozeAlertIn is the input of snooze_alert.
type SnoozeAlertIn struct {
	AlertID string `json:"alert_id" jsonschema:"alert ID from the alerts tool"`
	Until   string `json:"until,omitempty" jsonschema:"end of the snooze in RFC 3339, 5m to 30d from now. Do not use with for."`
	For     string `json:"for,omitempty" jsonschema:"length of the snooze, 5m to 30d, for example 2h or 3d. Do not use with until."`
	Undo    bool   `json:"undo,omitempty" jsonschema:"remove the snooze. Give no until and no for."`
}

// AlertChangeOut is the output of ack_alert and snooze_alert.
type AlertChangeOut struct {
	Alert  AlertItem `json:"alert"`
	Change Change    `json:"change"`
}

func (s *Server) ackAlert(ctx context.Context, _ *sdk.CallToolRequest, in AckAlertIn) (*sdk.CallToolResult, AlertChangeOut, error) {
	w, err := s.begin(ctx, "ack_alert", needAlerts)
	if err != nil {
		return nil, AlertChangeOut{}, err
	}
	out, err := w.markAlert(ctx, in.AlertID, actions.AlertAction{Kind: actions.ActAck, Undo: in.Undo})
	return nil, out, w.done(out.Change, err)
}

func (s *Server) snoozeAlert(ctx context.Context, _ *sdk.CallToolRequest, in SnoozeAlertIn) (*sdk.CallToolResult, AlertChangeOut, error) {
	w, err := s.begin(ctx, "snooze_alert", needAlerts)
	if err != nil {
		return nil, AlertChangeOut{}, err
	}
	until, err := s.snoozeUntil(in.Until, in.For, in.Undo)
	if err != nil {
		return nil, AlertChangeOut{}, w.done(Change{}, err)
	}
	out, err := w.markAlert(ctx, in.AlertID, actions.AlertAction{Kind: actions.ActSnooze, Until: until, Undo: in.Undo})
	return nil, out, w.done(out.Change, err)
}

func (w *writeCall) markAlert(ctx context.Context, alertID string, act actions.AlertAction) (AlertChangeOut, error) {
	id, err := parseAlertID(alertID)
	if err != nil {
		return AlertChangeOut{}, err
	}
	al, err := w.s.actions.MarkAlert(ctx, w.actor, id, act)
	if err != nil {
		return AlertChangeOut{}, err
	}
	host := w.s.hostName(ctx, w.actor.OrgID, al.NodeID)
	out := AlertChangeOut{Alert: newAlertItem(al, host, w.s.now().UTC())}
	out.Change = newChange(ChangeTarget{Kind: "alert", ID: id.String(), Name: al.Metric + " on " + host})
	f := ChangeField{Target: id.String()}
	switch act.Kind {
	case actions.ActAck:
		f.Path, f.After = "acked", !act.Undo
	case actions.ActSnooze:
		f.Path = "snoozed_until"
		if al.SnoozedUntil != nil {
			f.After = ts(*al.SnoozedUntil)
		}
	}
	out.Change.Fields = append(out.Change.Fields, f)
	return out, nil
}

// SnoozeHostIn is the input of snooze_host.
type SnoozeHostIn struct {
	Host  string `json:"host" jsonschema:"host ID or name"`
	Until string `json:"until,omitempty" jsonschema:"end of the snooze in RFC 3339, 5m to 30d from now. Do not use with for."`
	For   string `json:"for,omitempty" jsonschema:"length of the snooze, 5m to 30d, for example 2h or 3d. Do not use with until."`
	Undo  bool   `json:"undo,omitempty" jsonschema:"remove the snooze. Give no until and no for."`
}

// SnoozeHostOut is the output of snooze_host. SnoozedUntil is missing when
// the host has no snooze.
type SnoozeHostOut struct {
	Host         string `json:"host"`
	HostID       string `json:"host_id"`
	SnoozedUntil string `json:"snoozed_until,omitempty"`
	Change       Change `json:"change"`
}

func (s *Server) snoozeHost(ctx context.Context, _ *sdk.CallToolRequest, in SnoozeHostIn) (*sdk.CallToolResult, SnoozeHostOut, error) {
	w, err := s.begin(ctx, "snooze_host", needAlerts)
	if err != nil {
		return nil, SnoozeHostOut{}, err
	}
	out, err := w.snoozeHost(ctx, in)
	return nil, out, w.done(out.Change, err)
}

func (w *writeCall) snoozeHost(ctx context.Context, in SnoozeHostIn) (SnoozeHostOut, error) {
	until, err := w.s.snoozeUntil(in.Until, in.For, in.Undo)
	if err != nil {
		return SnoozeHostOut{}, err
	}
	old, err := w.s.requireHost(ctx, w.actor.OrgID, in.Host)
	if err != nil {
		return SnoozeHostOut{}, err
	}
	n, err := w.s.actions.SnoozeHost(ctx, w.actor, old.ID, until)
	if err != nil {
		return SnoozeHostOut{}, err
	}
	out := SnoozeHostOut{Host: n.DisplayName, HostID: n.ID.String(), SnoozedUntil: tsPtr(n.SnoozedUntil), Change: newChange(hostChangeTarget(n))}
	f := ChangeField{Target: n.ID.String(), Path: "snoozed_until"}
	if old.SnoozedUntil != nil {
		f.Before = ts(*old.SnoozedUntil)
	}
	if n.SnoozedUntil != nil {
		f.After = ts(*n.SnoozedUntil)
	}
	out.Change.Fields = append(out.Change.Fields, f)
	return out, nil
}

// metricNames lists the metrics that can be muted.
func metricNames() string {
	names := make([]string, 0, len(ruleMetrics)+1)
	for _, m := range ruleMetrics {
		names = append(names, string(m))
	}
	return strings.Join(append(names, string(store.MetricLong)), ", ")
}

// MuteMetricIn is the input of mute_metric.
type MuteMetricIn struct {
	Metric string `json:"metric" jsonschema:"metric to mute: cpu, mem, disk, procs, zombies, pids, files, inodes, memPressure, ioPressure or long (long-running process)"`
	Host   string `json:"host,omitempty" jsonschema:"host ID or name. Empty mutes the metric for the whole organization."`
	Muted  *bool  `json:"muted,omitempty" jsonschema:"true mutes (default). false unmutes."`
}

// MuteMetricOut is the output of mute_metric. Target is the host name or
// org. Muted lists the muted metrics of the target now.
type MuteMetricOut struct {
	Target string   `json:"target"`
	Muted  []string `json:"muted"`
	Change Change   `json:"change"`
}

func (s *Server) muteMetric(ctx context.Context, _ *sdk.CallToolRequest, in MuteMetricIn) (*sdk.CallToolResult, MuteMetricOut, error) {
	n := needRules
	if strings.TrimSpace(in.Host) != "" {
		n = needAlerts
	}
	w, err := s.begin(ctx, "mute_metric", n)
	if err != nil {
		return nil, MuteMetricOut{}, err
	}
	out, err := w.muteMetric(ctx, in)
	return nil, out, w.done(out.Change, err)
}

func (w *writeCall) muteMetric(ctx context.Context, in MuteMetricIn) (MuteMetricOut, error) {
	metric := store.ThresholdMetric(strings.TrimSpace(in.Metric))
	if !metric.Valid() {
		return MuteMetricOut{}, badf("metric must be one of %s", metricNames())
	}
	muted := in.Muted == nil || *in.Muted
	if strings.TrimSpace(in.Host) == "" {
		res, err := w.s.actions.SetMute(ctx, w.actor, metric, nil, muted)
		if err != nil {
			return MuteMetricOut{}, err
		}
		out := MuteMetricOut{Target: "org", Muted: []string{}}
		if res.Settings != nil {
			out.Muted = mutedOut(res.Settings.Muted)
		}
		if res.Rules == nil {
			res.Rules = &actions.RulesResult{}
		}
		out.Change = rulesChange(*res.Rules, orgChangeTarget)
		return out, nil
	}
	old, err := w.s.q.ResolveHost(ctx, w.actor.OrgID, in.Host)
	if err != nil {
		return MuteMetricOut{}, err
	}
	res, err := w.s.actions.SetMute(ctx, w.actor, metric, &old.ID, muted)
	if err != nil {
		return MuteMetricOut{}, err
	}
	n := old
	if res.Node != nil {
		n = *res.Node
	}
	out := MuteMetricOut{Target: n.DisplayName, Muted: mutedOut(n.MutedSignals), Change: newChange(hostChangeTarget(n))}
	if !slices.Equal(old.MutedSignals, n.MutedSignals) {
		out.Change.Fields = append(out.Change.Fields, ChangeField{
			Target: n.ID.String(), Path: "muted", Before: mutedOut(old.MutedSignals), After: mutedOut(n.MutedSignals),
		})
	}
	return out, nil
}

// SetThresholdsIn is the input of set_thresholds.
type SetThresholdsIn struct {
	Host            string   `json:"host,omitempty" jsonschema:"host ID or name. Empty changes the limits of the organization."`
	Metric          string   `json:"metric,omitempty" jsonschema:"metric with limits: cpu, mem, disk, procs, zombies, pids, files, inodes, memPressure or ioPressure. health sets only sustain_seconds and notify of the health checks. Not needed when only cooldown_seconds is set."`
	Warn            *float64 `json:"warn,omitempty" jsonschema:"warn limit in the unit of the metric. cpu, mem, disk, pids, files and inodes are ratios from 0 to 1 (0.9 is 90 %). memPressure and ioPressure are percents. procs and zombies are counts."`
	Critical        *float64 `json:"critical,omitempty" jsonschema:"critical limit, in the same unit as warn. It must not be less than warn."`
	SustainSeconds  *int     `json:"sustain_seconds,omitempty" jsonschema:"seconds that the value must stay over the limit before the alert starts, 0 to 3600. For cpu, mem, disk and the health checks."`
	Notify          string   `json:"notify,omitempty" jsonschema:"which levels send a notification: off, critical or all. For cpu, mem, disk and the health checks."`
	Clear           bool     `json:"clear,omitempty" jsonschema:"for a host: remove its override of the metric, so the host follows the organization again. Do not use with other values."`
	CooldownSeconds *int     `json:"cooldown_seconds,omitempty" jsonschema:"organization only: the least seconds between two notifications of the same host, metric and level, 0 to 86400. 0 means the default of 600."`
}

// EffectiveOut is the limit that applies after a set_thresholds call.
type EffectiveOut struct {
	Metric          string   `json:"metric,omitempty"`
	Warn            *float64 `json:"warn,omitempty"`
	Critical        *float64 `json:"critical,omitempty"`
	Unit            string   `json:"unit,omitempty"`
	SustainSeconds  *int     `json:"sustain_seconds,omitempty"`
	Notify          string   `json:"notify,omitempty"`
	CooldownSeconds *int     `json:"cooldown_seconds,omitempty"`
}

// SetThresholdsOut is the output of set_thresholds. Target is the host name
// or org.
type SetThresholdsOut struct {
	Target    string       `json:"target"`
	Effective EffectiveOut `json:"effective"`
	Change    Change       `json:"change"`
}

// rulesCall is a rules write that the tool arguments describe. Host is nil
// for a call on the org.
type rulesCall struct {
	edit    actions.RulesEdit
	targets []ChangeTarget
	host    *store.Node
}

const healthRule = "health"

// ruleKey gives the alert rule of a metric: cpu, mem, disk or health.
func ruleKey(metric string) string {
	switch store.ThresholdMetric(metric) {
	case store.MetricCPU, store.MetricMem, store.MetricDisk:
		return metric
	}
	return healthRule
}

func ruleSlot(a *store.AlertRules, key string) *store.AlertRule {
	switch key {
	case "cpu":
		return &a.CPU
	case "mem":
		return &a.Mem
	case "disk":
		return &a.Disk
	}
	return &a.Health
}

func overrideSlot(o *store.AlertOverrides, key string) **store.AlertRule {
	switch key {
	case "cpu":
		return &o.CPU
	case "mem":
		return &o.Mem
	case "disk":
		return &o.Disk
	}
	return &o.Health
}

func checkValue(m store.ThresholdMetric, name string, v float64) error {
	switch {
	case v <= 0:
		return badf("%s must be more than 0", name)
	case m.Fraction() && v > 1:
		return badf("%s limits are ratios from 0 to 1 (0.9 is 90 %%).", m)
	case (m == store.MetricMemPressure || m == store.MetricIOPressure) && v > 100:
		return badf("%s limits are percents from 0 to 100.", m)
	}
	return nil
}

// thresholdsCall checks the arguments of set_thresholds and builds the edit.
func (s *Server) thresholdsCall(ctx context.Context, org uuid.UUID, in SetThresholdsIn) (rulesCall, error) {
	name := strings.TrimSpace(in.Metric)
	metric := store.ThresholdMetric(name)
	hasPair := in.Warn != nil || in.Critical != nil
	hasRule := in.SustainSeconds != nil || in.Notify != ""
	hasHost := strings.TrimSpace(in.Host) != ""
	switch {
	case in.Clear && (hasPair || hasRule || in.CooldownSeconds != nil):
		return rulesCall{}, badf("clear cannot be used with other values")
	case in.Clear && !hasHost:
		return rulesCall{}, badf("clear works on a host. Set host.")
	case in.CooldownSeconds != nil && hasHost:
		return rulesCall{}, badf("cooldown_seconds is for the organization. Do not set host.")
	case in.CooldownSeconds != nil && (*in.CooldownSeconds < 0 || *in.CooldownSeconds > store.MaxNotifyCooldownSeconds):
		return rulesCall{}, badf("cooldown_seconds must be from 0 to %d", store.MaxNotifyCooldownSeconds)
	case !hasPair && !hasRule && !in.Clear && in.CooldownSeconds == nil:
		return rulesCall{}, badf("set one of warn, critical, sustain_seconds, notify, clear or cooldown_seconds")
	case name != "" && !hasPair && !hasRule && !in.Clear:
		return rulesCall{}, badf("metric needs warn, critical, sustain_seconds, notify or clear")
	}
	if hasPair || hasRule || in.Clear {
		switch {
		case name == "":
			return rulesCall{}, badf("metric is required")
		case metric == store.MetricLong:
			return rulesCall{}, badf("long has no limits. Use mute_metric to mute it.")
		case name == healthRule && hasPair:
			return rulesCall{}, badf("health has no warn and critical. Set the metric of the check: procs, zombies, pids, files, inodes, memPressure or ioPressure.")
		case name != healthRule && !metric.Valid():
			return rulesCall{}, badf("metric must be one of %s", strings.TrimSuffix(metricNames(), ", long")+", health")
		}
	}
	for label, v := range map[string]*float64{"warn": in.Warn, "critical": in.Critical} {
		if v != nil {
			if err := checkValue(metric, label, *v); err != nil {
				return rulesCall{}, err
			}
		}
	}
	if in.Warn != nil && in.Critical != nil && *in.Warn > *in.Critical {
		return rulesCall{}, badf("warn must not be more than critical")
	}
	if in.SustainSeconds != nil && (*in.SustainSeconds < 0 || *in.SustainSeconds > store.MaxSustainSeconds) {
		return rulesCall{}, badf("sustain_seconds must be from 0 to %d", store.MaxSustainSeconds)
	}
	if n := store.AlertNotify(in.Notify); in.Notify != "" && n != store.NotifyOff && n != store.NotifyCritical && n != store.NotifyAll {
		return rulesCall{}, badf("notify must be off, critical or all")
	}

	key := ruleKey(name)
	apply := func(base store.ThresholdPair) (store.ThresholdPair, error) {
		if in.Warn != nil {
			base.Warn = *in.Warn
		}
		if in.Critical != nil {
			base.Critical = *in.Critical
		}
		if base.Warn > base.Critical {
			return base, badf("warn must not be more than critical. The current %s limits are warn %g and critical %g.", metric, base.Warn, base.Critical)
		}
		return base, nil
	}
	rule := func(base store.AlertRule) store.AlertRule {
		if in.SustainSeconds != nil {
			base.SustainSeconds = *in.SustainSeconds
		}
		if in.Notify != "" {
			base.Notify = store.AlertNotify(in.Notify)
		}
		return base
	}

	if !hasHost {
		return rulesCall{targets: []ChangeTarget{orgChangeTarget}, edit: actions.RulesEdit{
			Org: true,
			Apply: func(cfg *store.ServerSettings, _ map[uuid.UUID]*store.Node) error {
				if hasPair {
					p, err := apply(cfg.Thresholds.Pair(metric))
					if err != nil {
						return err
					}
					cfg.Thresholds.SetPair(metric, p)
				}
				if hasRule {
					r := ruleSlot(&cfg.Alerts, key)
					*r = rule(*r)
				}
				if in.CooldownSeconds != nil {
					cfg.NotifyCooldownSeconds = *in.CooldownSeconds
				}
				return nil
			},
		}}, nil
	}

	host, err := s.q.ResolveHost(ctx, org, in.Host)
	if err != nil {
		return rulesCall{}, err
	}
	// A value that the call leaves out comes from the org rules.
	partial := (hasPair && (in.Warn == nil || in.Critical == nil)) || (hasRule && (in.SustainSeconds == nil || in.Notify == ""))
	return rulesCall{host: &host, targets: []ChangeTarget{hostChangeTarget(host)}, edit: actions.RulesEdit{
		Org: partial, Hosts: []uuid.UUID{host.ID},
		Apply: func(cfg *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error {
			n := nodes[host.ID]
			var th store.ThresholdOverrides
			if n.ThresholdOverrides != nil {
				th = *n.ThresholdOverrides
			}
			var al store.AlertOverrides
			if n.AlertOverrides != nil {
				al = *n.AlertOverrides
			}
			switch {
			case in.Clear:
				th.SetPair(metric, nil)
				if slices.Contains([]string{"cpu", "mem", "disk", healthRule}, name) {
					*overrideSlot(&al, key) = nil
				}
			default:
				if hasPair {
					var base store.ThresholdPair
					if cur := th.Pair(metric); cur != nil {
						base = *cur
					} else {
						base = cfg.Thresholds.Pair(metric)
					}
					p, err := apply(base)
					if err != nil {
						return err
					}
					th.SetPair(metric, &p)
				}
				if hasRule {
					slot := overrideSlot(&al, key)
					var base store.AlertRule
					if *slot != nil {
						base = **slot
					} else {
						base = *ruleSlot(&cfg.Alerts, key)
					}
					r := rule(base)
					*slot = &r
				}
			}
			n.ThresholdOverrides, n.AlertOverrides = &th, &al
			if th.IsEmpty() {
				n.ThresholdOverrides = nil
			}
			if al.IsEmpty() {
				n.AlertOverrides = nil
			}
			return nil
		},
	}}, nil
}

func (s *Server) setThresholds(ctx context.Context, _ *sdk.CallToolRequest, in SetThresholdsIn) (*sdk.CallToolResult, SetThresholdsOut, error) {
	w, err := s.begin(ctx, "set_thresholds", needRules)
	if err != nil {
		return nil, SetThresholdsOut{}, err
	}
	out, err := w.setThresholds(ctx, in)
	return nil, out, w.done(out.Change, err)
}

func (w *writeCall) setThresholds(ctx context.Context, in SetThresholdsIn) (SetThresholdsOut, error) {
	call, err := w.s.thresholdsCall(ctx, w.actor.OrgID, in)
	if err != nil {
		return SetThresholdsOut{}, err
	}
	res, err := w.s.actions.EditRules(ctx, w.actor, call.edit)
	if err != nil {
		return SetThresholdsOut{}, err
	}
	out := SetThresholdsOut{Target: "org", Change: rulesChange(res, call.targets...)}
	var host uuid.UUID
	if call.host != nil {
		out.Target, host = call.host.DisplayName, call.host.ID
	}
	out.Effective, err = w.s.effective(ctx, w.actor.OrgID, host, in)
	return out, err
}

// effective reads the rules that apply after a write.
func (s *Server) effective(ctx context.Context, org, host uuid.UUID, in SetThresholdsIn) (EffectiveOut, error) {
	r, err := s.q.Rules(ctx, org, host)
	if err != nil {
		return EffectiveOut{}, err
	}
	th, alerts := r.Org.Thresholds, r.Org.Alerts
	if r.Effective != nil {
		th, alerts = r.Effective.Thresholds, r.Effective.Alerts
	}
	var out EffectiveOut
	if name := strings.TrimSpace(in.Metric); name != "" {
		out.Metric = name
		if m := store.ThresholdMetric(name); m.Valid() && name != string(store.MetricLong) {
			p := th.Pair(m)
			out.Warn, out.Critical, out.Unit = &p.Warn, &p.Critical, thresholdUnit(m)
		}
		if rl, ok := alerts.For(ruleKey(name)); ok {
			out.SustainSeconds, out.Notify = &rl.SustainSeconds, string(rl.Notify)
		}
	}
	if in.CooldownSeconds != nil {
		c := int(r.Cooldown.Seconds())
		out.CooldownSeconds = &c
	}
	return out, nil
}

// IgnoreProcessIn is the input of ignore_process.
type IgnoreProcessIn struct {
	Name    string `json:"name,omitempty" jsonschema:"process name to ignore. Give name or alert_id."`
	AlertID string `json:"alert_id,omitempty" jsonschema:"ID of a long_process alert. The tool ignores the process of the alert on the host of the alert."`
	Host    string `json:"host,omitempty" jsonschema:"host ID or name, with name. Empty ignores the name on all hosts."`
	Undo    bool   `json:"undo,omitempty" jsonschema:"remove the name from the ignore list"`
}

// IgnoreProcessOut is the output of ignore_process. Scope is host or all.
// Alert is set when the call has an alert_id.
type IgnoreProcessOut struct {
	Name   string     `json:"name"`
	Scope  string     `json:"scope"`
	Alert  *AlertItem `json:"alert,omitempty"`
	Change Change     `json:"change"`
}

// ignoreCall is an ignore write that the tool arguments describe. Alert is
// set when the call has an alert_id.
type ignoreCall struct {
	rulesCall
	name  string
	scope actions.IgnoreScope
	alert uuid.UUID
}

// ignoreCallFor checks the arguments of ignore_process and builds the edit.
func (s *Server) ignoreCallFor(ctx context.Context, org uuid.UUID, in IgnoreProcessIn) (ignoreCall, error) {
	name, host := strings.TrimSpace(in.Name), strings.TrimSpace(in.Host)
	var c ignoreCall
	switch {
	case name != "" && in.AlertID != "":
		return c, badf("give name or alert_id, but not both")
	case name == "" && in.AlertID == "":
		return c, badf("give name or alert_id")
	case in.AlertID != "" && host != "":
		return c, badf("host is not used with alert_id. The process is ignored on the host of the alert.")
	}
	var node store.Node
	switch {
	case in.AlertID != "":
		id, err := parseAlertID(in.AlertID)
		if err != nil {
			return c, err
		}
		d, err := s.q.AlertDetail(ctx, org, id)
		if err != nil {
			return c, err
		}
		p := d.Alert.Proc
		if d.Alert.Kind != "long_process" || p == nil || p.Name == "" {
			return c, actions.ErrNoProcess
		}
		name = p.Name
		if node, err = s.q.ResolveHost(ctx, org, d.Alert.NodeID.String()); err != nil {
			return c, err
		}
		c.alert, c.scope = id, actions.ScopeHost
	case host != "":
		var err error
		if node, err = s.q.ResolveHost(ctx, org, host); err != nil {
			return c, err
		}
		c.scope = actions.ScopeHost
	default:
		c.scope = actions.ScopeAll
	}
	c.name = name
	item := actions.IgnoreItem{NodeID: node.ID, Name: name}
	c.edit = actions.IgnoreEdit(c.scope, []actions.IgnoreItem{item}, in.Undo)
	if c.scope == actions.ScopeAll {
		c.targets = []ChangeTarget{orgChangeTarget}
	} else {
		c.host, c.targets = &node, []ChangeTarget{hostChangeTarget(node)}
	}
	return c, nil
}

func (s *Server) ignoreProcess(ctx context.Context, _ *sdk.CallToolRequest, in IgnoreProcessIn) (*sdk.CallToolResult, IgnoreProcessOut, error) {
	w, err := s.begin(ctx, "ignore_process", needRules)
	if err != nil {
		return nil, IgnoreProcessOut{}, err
	}
	out, err := w.ignoreProcess(ctx, in)
	return nil, out, w.done(out.Change, err)
}

func (w *writeCall) ignoreProcess(ctx context.Context, in IgnoreProcessIn) (IgnoreProcessOut, error) {
	call, err := w.s.ignoreCallFor(ctx, w.actor.OrgID, in)
	if err != nil {
		return IgnoreProcessOut{}, err
	}
	out := IgnoreProcessOut{Name: call.name, Scope: string(call.scope)}
	var res actions.RulesResult
	if call.alert != uuid.Nil {
		var al wire.Alert
		if al, res, err = w.s.actions.IgnoreAlert(ctx, w.actor, call.alert, call.scope, in.Undo); err != nil {
			return IgnoreProcessOut{}, err
		}
		item := newAlertItem(al, call.host.DisplayName, w.s.now().UTC())
		out.Alert = &item
	} else {
		var errs []error
		items := []actions.IgnoreItem{{Name: call.name}}
		if call.host != nil {
			items[0].NodeID = call.host.ID
		}
		if res, errs, err = w.s.actions.IgnoreProcesses(ctx, w.actor, items, call.scope, in.Undo); err != nil {
			return IgnoreProcessOut{}, err
		}
		if errs[0] != nil {
			return IgnoreProcessOut{}, errs[0]
		}
	}
	out.Change = rulesChange(res, call.targets...)
	return out, nil
}

// UndoRulesChangeIn is the input of undo_rules_change.
type UndoRulesChangeIn struct {
	BackupID string `json:"backup_id" jsonschema:"backup ID from change.backup_id of a rules tool"`
}

// UndoRulesChangeOut is the output of undo_rules_change. The restore makes
// its own backup, in change.backup_id.
type UndoRulesChangeOut struct {
	RestoredBackupID string `json:"restored_backup_id"`
	Change           Change `json:"change"`
}

func (s *Server) undoRulesChange(ctx context.Context, _ *sdk.CallToolRequest, in UndoRulesChangeIn) (*sdk.CallToolResult, UndoRulesChangeOut, error) {
	w, err := s.begin(ctx, "undo_rules_change", needRules)
	if err != nil {
		return nil, UndoRulesChangeOut{}, err
	}
	out, err := w.undoRulesChange(ctx, in)
	return nil, out, w.done(out.Change, err)
}

func parseBackupID(text string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(text))
	if err != nil {
		return uuid.Nil, badf("backup_id must be a backup ID from change.backup_id")
	}
	return id, nil
}

func (w *writeCall) undoRulesChange(ctx context.Context, in UndoRulesChangeIn) (UndoRulesChangeOut, error) {
	id, err := parseBackupID(in.BackupID)
	if err != nil {
		return UndoRulesChangeOut{}, err
	}
	res, err := w.s.actions.Restore(ctx, w.actor, id, actions.RestoreIfUnchanged)
	var conflict *actions.ConflictError
	if errors.As(err, &conflict) {
		return UndoRulesChangeOut{}, badf("The rules of %s changed after backup %s. Nothing was restored.", strings.Join(conflict.Targets, ", "), id)
	}
	if err != nil {
		return UndoRulesChangeOut{}, err
	}
	var targets []ChangeTarget
	if res.Backup.Org != nil {
		targets = append(targets, orgChangeTarget)
	}
	for _, n := range res.Nodes {
		if _, ok := res.Backup.Hosts[n.ID]; ok {
			targets = append(targets, hostChangeTarget(n))
		}
	}
	return UndoRulesChangeOut{RestoredBackupID: id.String(), Change: rulesChange(res, targets...)}, nil
}

// RulesEditFor builds the rules edit of a rules write tool from its
// arguments: set_thresholds, ignore_process, an org mute_metric or
// undo_rules_change. The tool handlers use the same code, so a preview and
// the write share one input path. It does not check the rights of the
// caller. The caller must be on the context.
func (s *Server) RulesEditFor(ctx context.Context, tool string, args json.RawMessage) (actions.RulesEdit, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return actions.RulesEdit{}, err
	}
	org := c.Token.OrgID
	switch tool {
	case "set_thresholds":
		in, err := decodeArgs[SetThresholdsIn](args)
		if err != nil {
			return actions.RulesEdit{}, err
		}
		call, err := s.thresholdsCall(ctx, org, in)
		return call.edit, err
	case "ignore_process":
		in, err := decodeArgs[IgnoreProcessIn](args)
		if err != nil {
			return actions.RulesEdit{}, err
		}
		call, err := s.ignoreCallFor(ctx, org, in)
		return call.edit, err
	case "mute_metric":
		in, err := decodeArgs[MuteMetricIn](args)
		if err != nil {
			return actions.RulesEdit{}, err
		}
		metric := store.ThresholdMetric(strings.TrimSpace(in.Metric))
		switch {
		case strings.TrimSpace(in.Host) != "":
			return actions.RulesEdit{}, badf("a host mute is an alert action, not a rules change")
		case !metric.Valid():
			return actions.RulesEdit{}, badf("metric must be one of %s", metricNames())
		}
		return actions.MuteEdit(metric, in.Muted == nil || *in.Muted), nil
	case "undo_rules_change":
		in, err := decodeArgs[UndoRulesChangeIn](args)
		if err != nil {
			return actions.RulesEdit{}, err
		}
		id, err := parseBackupID(in.BackupID)
		if err != nil {
			return actions.RulesEdit{}, err
		}
		if s.actions == nil {
			return actions.RulesEdit{}, badf("Changes are not available on this server.")
		}
		return s.actions.RestoreEdit(ctx, actorOf(c), id)
	}
	return actions.RulesEdit{}, ErrUnknownTool
}

func decodeArgs[T any](args json.RawMessage) (T, error) {
	var in T
	if len(args) == 0 {
		return in, nil
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return in, badf("bad arguments: %v", err)
	}
	return in, nil
}
