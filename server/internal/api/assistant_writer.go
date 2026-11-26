package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/internal/assistant"
	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Suggester is the part of query.Service (B7) that the impact needs.
type Suggester interface {
	SuggestThresholds(ctx context.Context, r query.SuggestRequest) (query.Suggestion, error)
}

// HostResolver finds a host by ID or name.
type HostResolver interface {
	ResolveHost(ctx context.Context, orgID uuid.UUID, ref string) (store.Node, error)
}

// writerActions is the part of actions.Service that the writer uses.
type writerActions interface {
	Preview(ctx context.Context, a actions.Actor, ed actions.RulesEdit) (actions.RulesResult, error)
	EditRules(ctx context.Context, a actions.Actor, ed actions.RulesEdit) (actions.RulesResult, error)
	Restore(ctx context.Context, a actions.Actor, backupID uuid.UUID, mode actions.RestoreMode) (actions.RulesResult, error)
	MarkAlert(ctx context.Context, a actions.Actor, alertID uuid.UUID, act actions.AlertAction) (wire.Alert, error)
	SnoozeHost(ctx context.Context, a actions.Actor, nodeID uuid.UUID, until *time.Time) (store.Node, error)
	SetMute(ctx context.Context, a actions.Actor, metric store.ThresholdMetric, nodeID *uuid.UUID, muted bool) (actions.MuteResult, error)
	IgnoreAlert(ctx context.Context, a actions.Actor, alertID uuid.UUID, scope actions.IgnoreScope, undo bool) (wire.Alert, actions.RulesResult, error)
	IgnoreProcesses(ctx context.Context, a actions.Actor, items []actions.IgnoreItem, scope actions.IgnoreScope, undo bool) (actions.RulesResult, []error, error)
}

// writerTools is the part of *mcp.Server that the writer uses.
type writerTools interface {
	RulesEditFor(ctx context.Context, tool string, args json.RawMessage) (actions.RulesEdit, error)
	SnoozeUntil(until, forText string, undo bool) (*time.Time, error)
}

var errNoSplit = &codedError{
	Status: http.StatusBadRequest, Code: "bad_request",
	Message: "These rows cannot be applied one by one. Select all rows.",
}

const impactDays = 30

// assistantWriter plans, applies and undoes the write tools of the
// assistant with the actions service.
type assistantWriter struct {
	actions writerActions
	tools   writerTools
	suggest Suggester
	hosts   HostResolver
	now     func() time.Time
}

func newAssistantWriter(a writerActions, t writerTools, sg Suggester, h HostResolver) *assistantWriter {
	return &assistantWriter{actions: a, tools: t, suggest: sg, hosts: h, now: time.Now}
}

func assistantActor(a assistant.Actor) actions.Actor {
	return actions.Actor{OrgID: a.OrgID, By: a.UserID, Client: store.ClientAI, Device: "assistant"}
}

func decodeInput[T any](raw json.RawMessage) (T, error) {
	var in T
	if len(raw) == 0 {
		return in, nil
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("bad arguments: %w", err)
	}
	return in, nil
}

func parseUUID(field, text string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(text))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s must be an ID", field)
	}
	return id, nil
}

// Plan makes the change that a write tool asks for. It writes nothing.
func (w *assistantWriter) Plan(ctx context.Context, a assistant.Actor, tool string, input json.RawMessage) (assistant.Change, error) {
	switch tool {
	case "ack_alert":
		in, err := decodeInput[mcp.AckAlertIn](input)
		if err != nil {
			return assistant.Change{}, err
		}
		id, err := parseUUID("alert_id", in.AlertID)
		if err != nil {
			return assistant.Change{}, err
		}
		row := assistant.ChangeRow{Target: "alert " + id.String(), Field: "ack", Old: "open", New: "acknowledged"}
		if in.Undo {
			row.Old, row.New = "acknowledged", "open"
		}
		return alertChange(tool, input, row), nil
	case "snooze_alert":
		in, err := decodeInput[mcp.SnoozeAlertIn](input)
		if err != nil {
			return assistant.Change{}, err
		}
		id, err := parseUUID("alert_id", in.AlertID)
		if err != nil {
			return assistant.Change{}, err
		}
		if _, err := w.tools.SnoozeUntil(in.Until, in.For, in.Undo); err != nil {
			return assistant.Change{}, err
		}
		return alertChange(tool, input, assistant.ChangeRow{
			Target: "alert " + id.String(), Field: "snooze", Old: "not snoozed", New: snoozeText(in.Until, in.For, in.Undo),
		}), nil
	case "snooze_host":
		in, err := decodeInput[mcp.SnoozeHostIn](input)
		if err != nil {
			return assistant.Change{}, err
		}
		if _, err := w.tools.SnoozeUntil(in.Until, in.For, in.Undo); err != nil {
			return assistant.Change{}, err
		}
		n, err := w.hosts.ResolveHost(ctx, a.OrgID, in.Host)
		if err != nil {
			return assistant.Change{}, err
		}
		old := "not snoozed"
		if n.SnoozedUntil != nil {
			old = "snoozed until " + n.SnoozedUntil.UTC().Format(time.RFC3339)
		}
		return alertChange(tool, input, assistant.ChangeRow{
			Target: n.DisplayName, Field: "snooze", Old: old, New: snoozeText(in.Until, in.For, in.Undo),
		}), nil
	case "mute_metric":
		in, err := decodeInput[mcp.MuteMetricIn](input)
		if err != nil {
			return assistant.Change{}, err
		}
		if strings.TrimSpace(in.Host) != "" {
			return w.planHostMute(ctx, a, input, in)
		}
		return w.planRules(ctx, a, tool, input)
	case "set_thresholds", "ignore_process", "undo_rules_change":
		return w.planRules(ctx, a, tool, input)
	}
	return assistant.Change{}, fmt.Errorf("%s is not a write tool", tool)
}

func alertChange(tool string, input json.RawMessage, row assistant.ChangeRow) assistant.Change {
	row.ID, row.Checked = "r1", true
	return assistant.Change{Tool: tool, Kind: "alerts", MinRole: store.RoleMember, Rows: []assistant.ChangeRow{row}, Input: input}
}

func snoozeText(until, forText string, undo bool) string {
	switch {
	case undo:
		return "not snoozed"
	case forText != "":
		return "snoozed for " + forText
	}
	return "snoozed until " + until
}

func (w *assistantWriter) planHostMute(ctx context.Context, a assistant.Actor, input json.RawMessage, in mcp.MuteMetricIn) (assistant.Change, error) {
	metric := store.ThresholdMetric(strings.TrimSpace(in.Metric))
	if !metric.Valid() {
		return assistant.Change{}, fmt.Errorf("unknown metric %q", in.Metric)
	}
	n, err := w.hosts.ResolveHost(ctx, a.OrgID, in.Host)
	if err != nil {
		return assistant.Change{}, err
	}
	state := func(m bool) string {
		if m {
			return "muted"
		}
		return "unmuted"
	}
	return alertChange("mute_metric", input, assistant.ChangeRow{
		Target: n.DisplayName, Field: "mute " + string(metric), Old: state(n.MutedSignals.Has(metric)), New: state(in.Muted == nil || *in.Muted),
	}), nil
}

func (w *assistantWriter) planRules(ctx context.Context, a assistant.Actor, tool string, input json.RawMessage) (assistant.Change, error) {
	edit, err := w.tools.RulesEditFor(ctx, tool, input)
	if err != nil {
		return assistant.Change{}, err
	}
	res, err := w.actions.Preview(ctx, assistantActor(a), edit)
	if err != nil {
		return assistant.Change{}, err
	}
	if len(res.Diff) == 0 {
		return assistant.Change{}, errors.New("no rule would change: the rules already have these values")
	}
	names := map[string]string{}
	name := func(target string) string {
		if target == "org" {
			return "Organization"
		}
		if n, ok := names[target]; ok {
			return n
		}
		names[target] = target
		if n, err := w.hosts.ResolveHost(ctx, a.OrgID, target); err == nil {
			names[target] = n.DisplayName
		}
		return names[target]
	}
	ch := assistant.Change{Tool: tool, Kind: "rules", MinRole: store.RoleAdmin, Backup: true, Input: input}
	for i, f := range res.Diff {
		ch.Rows = append(ch.Rows, assistant.ChangeRow{
			ID: fmt.Sprintf("r%d", i+1), Target: name(f.Target), Field: f.Path,
			Old: jsonText(f.Before), New: jsonText(f.After), Checked: true,
		})
	}
	if tool == "set_thresholds" {
		ch.Impact = w.impact(ctx, a, input)
	}
	return ch, nil
}

// jsonText shows a JSON value as plain text.
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "unset"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// impact replays the last 30 days with the current and the proposed limits.
// It gives nil when the replay is not possible.
func (w *assistantWriter) impact(ctx context.Context, a assistant.Actor, input json.RawMessage) *assistant.Impact {
	in, err := decodeInput[mcp.SetThresholdsIn](input)
	m := store.ThresholdMetric(strings.TrimSpace(in.Metric))
	if err != nil || in.Clear || (in.Warn == nil && in.Critical == nil) || !slices.Contains(store.UsageMetrics, m) {
		return nil
	}
	now := w.now().UTC()
	req := query.SuggestRequest{OrgID: a.OrgID, Metric: m, From: now.AddDate(0, 0, -impactDays), To: now}
	if strings.TrimSpace(in.Host) != "" {
		n, err := w.hosts.ResolveHost(ctx, a.OrgID, in.Host)
		if err != nil {
			return nil
		}
		req.NodeID = n.ID
	}
	var pair store.ThresholdPair
	if in.Warn != nil && in.Critical != nil {
		pair = store.ThresholdPair{Warn: *in.Warn, Critical: *in.Critical}
	} else {
		cur, err := w.suggest.SuggestThresholds(ctx, req)
		if err != nil {
			return nil
		}
		i := slices.IndexFunc(cur.Candidates, func(c query.Candidate) bool { return c.Current })
		if i < 0 {
			return nil
		}
		pair = cur.Candidates[i].Pair
		if in.Warn != nil {
			pair.Warn = *in.Warn
		}
		if in.Critical != nil {
			pair.Critical = *in.Critical
		}
	}
	req.Candidates = []store.ThresholdPair{pair}
	sug, err := w.suggest.SuggestThresholds(ctx, req)
	if err != nil {
		return nil
	}
	// The service rounds the pairs to 3 decimals.
	round := func(v float64) float64 { return math.Round(v*1000) / 1000 }
	before := slices.IndexFunc(sug.Candidates, func(c query.Candidate) bool { return c.Current })
	if before < 0 {
		return nil
	}
	after := slices.IndexFunc(sug.Candidates, func(c query.Candidate) bool {
		return !c.Current && c.Pair.Warn == round(pair.Warn) && c.Pair.Critical == round(pair.Critical)
	})
	if after < 0 {
		after = before
	}
	im := &assistant.Impact{
		Before: sug.Candidates[before].Count.Notifications,
		After:  sug.Candidates[after].Count.Notifications,
		Window: fmt.Sprintf("%dd", int(math.Round(sug.To.Sub(sug.From).Hours()/24))),
	}
	if sug.Clamped {
		im.Note = "The plan cut the range, so the window is shorter."
	}
	return im
}

// Apply runs the change with the rows that the user checked.
func (w *assistantWriter) Apply(ctx context.Context, a assistant.Actor, c assistant.Change, rows []string) (assistant.Applied, error) {
	input, err := splitInput(c, rows)
	if err != nil {
		return assistant.Applied{}, err
	}
	ap := assistant.Applied{Rows: rows}
	actor := assistantActor(a)
	switch c.Tool {
	case "set_thresholds":
		edit, err := w.tools.RulesEditFor(ctx, c.Tool, input)
		if err != nil {
			return ap, writerError(err)
		}
		res, err := w.actions.EditRules(ctx, actor, edit)
		ap.BackupID = res.Backup.ID
		return ap, writerError(err)
	case "undo_rules_change":
		in, err := decodeInput[mcp.UndoRulesChangeIn](input)
		if err != nil {
			return ap, writerError(err)
		}
		id, err := parseUUID("backup_id", in.BackupID)
		if err != nil {
			return ap, writerError(err)
		}
		res, err := w.actions.Restore(ctx, actor, id, actions.RestoreIfUnchanged)
		ap.BackupID = res.Backup.ID
		return ap, writerError(err)
	case "ignore_process":
		res, err := w.ignore(ctx, actor, input)
		ap.BackupID = res.Backup.ID
		return ap, writerError(err)
	case "mute_metric":
		in, err := decodeInput[mcp.MuteMetricIn](input)
		if err != nil {
			return ap, writerError(err)
		}
		if strings.TrimSpace(in.Host) == "" {
			res, err := w.actions.SetMute(ctx, actor, store.ThresholdMetric(strings.TrimSpace(in.Metric)), nil, in.Muted == nil || *in.Muted)
			if res.Rules != nil {
				ap.BackupID = res.Rules.Backup.ID
			}
			return ap, writerError(err)
		}
	}
	if err := w.alertAction(ctx, actor, c.Tool, input); err != nil {
		return ap, writerError(err)
	}
	ap.Inverse = inverseInput(c.Tool, input)
	return ap, nil
}

// Undo takes back an applied change.
func (w *assistantWriter) Undo(ctx context.Context, a assistant.Actor, c assistant.Change, ap assistant.Applied) error {
	actor := assistantActor(a)
	if c.Kind == "rules" {
		if ap.BackupID == uuid.Nil {
			return nil // no rule changed, so nothing to restore
		}
		_, err := w.actions.Restore(ctx, actor, ap.BackupID, actions.RestoreIfUnchanged)
		return writerError(err)
	}
	if len(ap.Inverse) == 0 {
		return &codedError{Status: http.StatusConflict, Code: "conflict", Message: "This change cannot be undone."}
	}
	return writerError(w.alertAction(ctx, actor, c.Tool, ap.Inverse))
}

// alertAction runs an alert tool or a host mute with the C service.
func (w *assistantWriter) alertAction(ctx context.Context, actor actions.Actor, tool string, input json.RawMessage) error {
	switch tool {
	case "ack_alert":
		in, err := decodeInput[mcp.AckAlertIn](input)
		if err != nil {
			return err
		}
		id, err := parseUUID("alert_id", in.AlertID)
		if err != nil {
			return err
		}
		_, err = w.actions.MarkAlert(ctx, actor, id, actions.AlertAction{Kind: actions.ActAck, Undo: in.Undo})
		return err
	case "snooze_alert":
		in, err := decodeInput[mcp.SnoozeAlertIn](input)
		if err != nil {
			return err
		}
		id, err := parseUUID("alert_id", in.AlertID)
		if err != nil {
			return err
		}
		until, err := w.tools.SnoozeUntil(in.Until, in.For, in.Undo)
		if err != nil {
			return err
		}
		_, err = w.actions.MarkAlert(ctx, actor, id, actions.AlertAction{Kind: actions.ActSnooze, Until: until, Undo: in.Undo})
		return err
	case "snooze_host":
		in, err := decodeInput[mcp.SnoozeHostIn](input)
		if err != nil {
			return err
		}
		until, err := w.tools.SnoozeUntil(in.Until, in.For, in.Undo)
		if err != nil {
			return err
		}
		n, err := w.hosts.ResolveHost(ctx, actor.OrgID, in.Host)
		if err != nil {
			return err
		}
		_, err = w.actions.SnoozeHost(ctx, actor, n.ID, until)
		return err
	case "mute_metric":
		in, err := decodeInput[mcp.MuteMetricIn](input)
		if err != nil {
			return err
		}
		n, err := w.hosts.ResolveHost(ctx, actor.OrgID, in.Host)
		if err != nil {
			return err
		}
		_, err = w.actions.SetMute(ctx, actor, store.ThresholdMetric(strings.TrimSpace(in.Metric)), &n.ID, in.Muted == nil || *in.Muted)
		return err
	}
	return fmt.Errorf("%s is not an alert tool", tool)
}

func (w *assistantWriter) ignore(ctx context.Context, actor actions.Actor, input json.RawMessage) (actions.RulesResult, error) {
	in, err := decodeInput[mcp.IgnoreProcessIn](input)
	if err != nil {
		return actions.RulesResult{}, err
	}
	name := strings.TrimSpace(in.Name)
	if in.AlertID != "" {
		id, err := parseUUID("alert_id", in.AlertID)
		if err != nil {
			return actions.RulesResult{}, err
		}
		_, res, err := w.actions.IgnoreAlert(ctx, actor, id, actions.ScopeHost, in.Undo)
		return res, err
	}
	item, scope := actions.IgnoreItem{Name: name}, actions.ScopeAll
	if host := strings.TrimSpace(in.Host); host != "" {
		n, err := w.hosts.ResolveHost(ctx, actor.OrgID, host)
		if err != nil {
			return actions.RulesResult{}, err
		}
		item.NodeID, scope = n.ID, actions.ScopeHost
	}
	res, errs, err := w.actions.IgnoreProcesses(ctx, actor, []actions.IgnoreItem{item}, scope, in.Undo)
	if err != nil {
		return res, err
	}
	if len(errs) > 0 && errs[0] != nil {
		return res, errs[0]
	}
	return res, nil
}

// inverseInput gives the input that takes back an alert tool. It is empty
// when the tool call already was an undo that has no inverse.
func inverseInput(tool string, input json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return nil
	}
	undo, _ := m["undo"].(bool)
	switch tool {
	case "ack_alert":
		m["undo"] = !undo
	case "snooze_alert", "snooze_host":
		if undo {
			return nil
		}
		delete(m, "until")
		delete(m, "for")
		m["undo"] = true
	case "mute_metric":
		muted, ok := m["muted"].(bool)
		m["muted"] = ok && !muted
	default:
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

// splitInput gives the tool input for the checked rows. Only set_thresholds
// can drop a value (one row for each value). Other changes need all rows.
func splitInput(c assistant.Change, rows []string) (json.RawMessage, error) {
	var dropped []assistant.ChangeRow
	for _, r := range c.Rows {
		if !slices.Contains(rows, r.ID) {
			dropped = append(dropped, r)
		}
	}
	if len(dropped) == 0 {
		return c.Input, nil
	}
	if c.Tool != "set_thresholds" {
		return nil, errNoSplit
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(c.Input, &m); err != nil {
		return nil, err
	}
	if _, ok := m["clear"]; ok {
		return nil, errNoSplit
	}
	for _, r := range dropped {
		f := thresholdField(r.Field)
		if f == "" {
			return nil, errNoSplit
		}
		delete(m, f)
	}
	return json.Marshal(m)
}

// thresholdField maps the path of a rule field to the input of set_thresholds.
func thresholdField(path string) string {
	switch {
	case path == "notifyCooldownSeconds":
		return "cooldown_seconds"
	case strings.HasPrefix(path, "thresholds.") && strings.HasSuffix(path, "Warn"):
		return "warn"
	case strings.HasPrefix(path, "thresholds.") && strings.HasSuffix(path, "Critical"):
		return "critical"
	case strings.HasPrefix(path, "alerts.") && strings.HasSuffix(path, ".sustainSeconds"):
		return "sustain_seconds"
	case strings.HasPrefix(path, "alerts.") && strings.HasSuffix(path, ".notify"):
		return "notify"
	}
	return ""
}

// writerError maps the errors of Apply and Undo to HTTP errors.
func writerError(err error) error {
	switch {
	case err == nil:
		return nil
	case mcp.IsInputError(err):
		return &codedError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	case errors.Is(err, query.ErrHostNotFound):
		return &codedError{Status: http.StatusNotFound, Code: "not_found", Message: err.Error()}
	case errors.Is(err, query.ErrHostAmbiguous):
		return &codedError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	return actionError(err)
}
