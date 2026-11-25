package actions

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/wire"
)

// AlertActionKind is the kind of a change of an alert.
type AlertActionKind string

const (
	ActAck    AlertActionKind = "ack"
	ActSnooze AlertActionKind = "snooze"
	// ActIgnore is for MarkAlerts only. IgnoreAlert is the single form.
	ActIgnore AlertActionKind = "ignore"
)

// AlertAction is one change of an alert. Undo takes it back. Until is for
// a snooze that is not an undo.
type AlertAction struct {
	Kind  AlertActionKind
	Until *time.Time
	Undo  bool
}

// LineKind is the kind of the alert log line of an ack or a snooze.
func (a AlertAction) LineKind() notify.LineKind {
	switch {
	case a.Kind == ActAck && a.Undo:
		return notify.LineUnacked
	case a.Kind == ActAck:
		return notify.LineAcked
	case a.Kind == ActSnooze && a.Undo:
		return notify.LineUnsnoozed
	case a.Kind == ActSnooze:
		return notify.LineSnoozed
	}
	return ""
}

// BulkResult is the result of one id of MarkAlerts: the new alert, or the
// error.
type BulkResult struct {
	ID    uuid.UUID
	Alert *wire.Alert
	Err   error
}

// future checks that an until is after now and gives it in UTC.
func (s *Service) future(until *time.Time) (*time.Time, error) {
	if until == nil || !until.After(s.now()) {
		return nil, ErrBadUntil
	}
	u := until.UTC()
	return &u, nil
}

// MarkAlert sets or takes back the ack or the snooze of an open alert. It
// writes the log line, tells the engine and sends alert_updated.
func (s *Service) MarkAlert(ctx context.Context, a Actor, alertID uuid.UUID, act AlertAction) (wire.Alert, error) {
	switch {
	case act.Kind != ActAck && act.Kind != ActSnooze:
		return wire.Alert{}, badInput(errors.New("bad action"))
	case act.Kind == ActSnooze && !act.Undo:
		u, err := s.future(act.Until)
		if err != nil {
			return wire.Alert{}, err
		}
		act.Until = u
	}
	row, mark, err := s.findAlert(ctx, a.OrgID, alertID, true)
	if err != nil {
		return wire.Alert{}, err
	}
	line, al, err := s.applyAlertAction(ctx, a, row, mark, act, s.now())
	if err != nil {
		return wire.Alert{}, err
	}
	s.writeJournal(ctx, line)
	s.broadcastAlert(a.OrgID, al)
	return al, nil
}

// MarkAlerts applies one action to many alerts. It gives one result for
// each id, also when some ids fail. It writes one log batch, and one list
// edit for each node (or one for the org) for an ignore.
func (s *Service) MarkAlerts(ctx context.Context, a Actor, ids []uuid.UUID, act AlertAction, scope IgnoreScope) ([]BulkResult, error) {
	now := s.now()
	switch {
	case act.Kind == ActSnooze && !act.Undo:
		u, err := s.future(act.Until)
		if err != nil {
			return nil, err
		}
		act.Until = u
	case act.Kind == ActIgnore && !scope.Valid():
		return nil, badInput(errScope)
	}
	if s.alerts != nil && !s.alerts.Ready() {
		return nil, ErrNotReady
	}
	order := dedupeIDs(ids)
	var rows map[uuid.UUID]clickhouse.AlertRow
	marks := map[uuid.UUID]clickhouse.AlertMark{}
	if s.alerts != nil {
		var err error
		if rows, err = s.alerts.AlertsByID(ctx, a.OrgID, order); err != nil {
			return nil, err
		}
		aids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			aids = append(aids, alertIDOf(r))
		}
		if marks, err = s.alerts.AlertMarks(ctx, a.OrgID, aids); err != nil {
			return nil, err
		}
	}

	results := make([]BulkResult, len(order))
	var (
		lines   []notify.Line
		items   []IgnoreItem
		itemAt  []int
		ignored []notify.Line
	)
	for i, id := range order {
		results[i].ID = id
		row, found := rows[id]
		switch {
		case !found:
			results[i].Err = ErrNotFound
			continue
		case act.Kind != ActIgnore && row.Tint == "ok":
			results[i].Err = ErrNotOpen
			continue
		}
		line, al, err := s.applyAlertAction(ctx, a, row, marks[alertIDOf(row)], act, now)
		if err != nil {
			results[i].Err = err
			continue
		}
		results[i].Alert = &al
		if act.Kind == ActIgnore {
			items, itemAt = append(items, IgnoreItem{NodeID: al.NodeID, Name: al.Proc.Name}), append(itemAt, i)
			ignored = append(ignored, line)
			continue
		}
		lines = append(lines, line)
	}
	if len(items) > 0 {
		text, errs, _ := s.writeIgnores(ctx, a.OrgID, scope, act.Undo, items)
		for n, i := range itemAt {
			if errs[n] != nil {
				results[i].Alert, results[i].Err = nil, errs[n]
				continue
			}
			if !act.Undo {
				ignored[n].Text = text[n]
				lines = append(lines, ignored[n])
			}
		}
	}
	s.writeJournal(ctx, lines...)
	for _, r := range results {
		if r.Alert != nil {
			s.broadcastAlert(a.OrgID, *r.Alert)
		}
	}
	return results, nil
}

// IgnoreAlert adds the process name of a long_process alert to the ignore
// list of the host or of the org, or removes it. The engine closes the open
// alert with no notification.
func (s *Service) IgnoreAlert(ctx context.Context, a Actor, alertID uuid.UUID, scope IgnoreScope, undo bool) (wire.Alert, RulesResult, error) {
	if !scope.Valid() {
		return wire.Alert{}, RulesResult{}, badInput(errScope)
	}
	row, mark, err := s.findAlert(ctx, a.OrgID, alertID, false)
	if err != nil {
		return wire.Alert{}, RulesResult{}, err
	}
	line, al, err := s.applyAlertAction(ctx, a, row, mark, AlertAction{Kind: ActIgnore, Undo: undo}, s.now())
	if err != nil {
		return wire.Alert{}, RulesResult{}, err
	}
	text, errs, res := s.writeIgnores(ctx, a.OrgID, scope, undo, []IgnoreItem{{NodeID: al.NodeID, Name: al.Proc.Name}})
	if errs[0] != nil {
		return wire.Alert{}, RulesResult{}, errs[0]
	}
	if !undo {
		line.Text = text[0]
		s.writeJournal(ctx, line)
	}
	s.broadcastAlert(a.OrgID, al)
	return al, res, nil
}

// applyAlertAction sets or takes back the ack or the snooze of an alert,
// tells the engine, and gives the log line and the new alert. For ignore it
// only checks that the alert has a process: the caller writes the lists,
// and the line has no text yet. An undo of an ignore has no line. The
// caller writes the line, and sends the event.
func (s *Service) applyAlertAction(ctx context.Context, a Actor, row clickhouse.AlertRow, mark clickhouse.AlertMark, act AlertAction, now time.Time) (notify.Line, wire.Alert, error) {
	by := a.By
	al := query.AlertFromRow(row, mark)
	line := notify.Line{OrgID: a.OrgID, NodeID: al.NodeID, AlertID: al.ID, At: now, User: &by}
	ack := notify.Ack{OrgID: a.OrgID, EventID: al.ID, AlertID: al.ID, NodeID: al.NodeID, Metric: al.Metric, By: by, At: now, Undo: act.Undo}
	switch act.Kind {
	case ActAck:
		line.Kind = act.LineKind()
		al.AckedBy, al.AckedAt = &by, &now
		if act.Undo {
			al.AckedBy, al.AckedAt = nil, nil
		}
	case ActSnooze:
		ack.Snooze = true
		line.Kind, line.Until = act.LineKind(), act.Until
		al.SnoozedUntil, ack.Until = act.Until, act.Until
		if act.Undo {
			line.Until = nil
			al.SnoozedUntil = nil
		}
	case ActIgnore:
		if al.Kind != "long_process" || al.Proc == nil || al.Proc.Name == "" {
			return notify.Line{}, al, ErrNoProcess
		}
		if act.Undo {
			return notify.Line{}, al, nil
		}
		line.Kind = notify.LineClosed
		return line, al, nil
	}
	if s.engine != nil {
		s.engine.Acked(ctx, ack)
	}
	return line, al, nil
}

// findAlert finds an alert of the org. With open set, an alert that is
// closed gives ErrNotFound.
func (s *Service) findAlert(ctx context.Context, org, id uuid.UUID, open bool) (clickhouse.AlertRow, clickhouse.AlertMark, error) {
	if s.alerts == nil {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, ErrNotFound
	}
	if !s.alerts.Ready() {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, ErrNotReady
	}
	r, err := s.alerts.Alert(ctx, org, id)
	if errors.Is(err, clickhouse.ErrNoAlert) || (err == nil && open && r.Tint == "ok") {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, ErrNotFound
	}
	if err != nil {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, err
	}
	aid := id
	if r.AlertID != uuid.Nil {
		aid = r.AlertID
	}
	marks, err := s.alerts.AlertMarks(ctx, org, []uuid.UUID{aid})
	if err != nil {
		return clickhouse.AlertRow{}, clickhouse.AlertMark{}, err
	}
	return r, marks[aid], nil
}

// dedupeIDs drops the repeated ids and keeps the order.
func dedupeIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// alertIDOf is the alert id of the row. An old row has the id of its event.
func alertIDOf(r clickhouse.AlertRow) uuid.UUID {
	if r.AlertID != uuid.Nil {
		return r.AlertID
	}
	return r.EventID
}
