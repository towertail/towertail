// Package actions holds the writes that the REST routes and the MCP tools
// share. A method checks its input, writes to the store, tells the alerter
// and sends the events of the dashboards.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Errors of the service. The text of each one is the text that REST sends.
var (
	ErrNotFound  = errors.New("alert not found")
	ErrNotOpen   = errors.New("alert is not open")
	ErrNotReady  = errors.New("clickhouse not ready")
	ErrBadUntil  = errors.New("until must be in the future")
	ErrNoProcess = errors.New("only a long_process alert has a process to ignore")
	ErrBadMetric = errors.New("unknown metric")
	ErrBadInput  = errors.New("bad input")
)

// inputError is a bad value. It keeps the text of its cause and matches
// ErrBadInput and the cause.
type inputError struct{ err error }

func badInput(err error) error { return &inputError{err} }

func (e *inputError) Error() string   { return e.err.Error() }
func (e *inputError) Unwrap() []error { return []error{ErrBadInput, e.err} }

// Store is the part of the config store that the service uses.
type Store interface {
	GetNode(ctx context.Context, orgID, id uuid.UUID) (store.Node, error)
	ListNodes(ctx context.Context, orgID uuid.UUID) ([]store.Node, error)
	GetSettings(ctx context.Context, orgID uuid.UUID) (store.ServerSettings, error)
	EditNode(ctx context.Context, orgID, id uuid.UUID, f func(*store.Node) error) (store.Node, error)
	EditSettings(ctx context.Context, orgID uuid.UUID, f func(*store.ServerSettings) error) (store.ServerSettings, error)
	EditRules(ctx context.Context, orgID uuid.UUID, ed store.RulesEdit) (store.RulesBackup, error)
	ReplaceRules(ctx context.Context, orgID uuid.UUID, ch store.RulesChange) (store.RulesBackup, error)
	GetRulesBackup(ctx context.Context, orgID, id uuid.UUID) (store.RulesBackup, error)
}

// AlertReader is the part of ClickHouse that the alert writes read.
type AlertReader interface {
	Ready() bool
	Alert(ctx context.Context, orgID, id uuid.UUID) (clickhouse.AlertRow, error)
	AlertsByID(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertRow, error)
	AlertMarks(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error)
}

// Engine is the part of the alerter that the service tells.
type Engine interface {
	Acked(ctx context.Context, a notify.Ack)
	InvalidateSettings(orgID uuid.UUID)
}

// Broadcaster sends an event to the dashboards of an org.
type Broadcaster interface {
	BroadcastOrg(orgID uuid.UUID, msg wire.WSMessage)
}

// Service runs the writes. A nil alerts, journal, engine or events is
// allowed: the service then skips that part.
type Service struct {
	st      Store
	alerts  AlertReader
	journal notify.Journal
	engine  Engine
	events  Broadcaster
	log     *slog.Logger
	now     func() time.Time
}

// NewService makes a service. A nil log gives the default logger.
func NewService(st Store, alerts AlertReader, journal notify.Journal, engine Engine, events Broadcaster, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		st: st, alerts: alerts, journal: journal, engine: engine, events: events, log: log,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Actor is who makes a change. Client and Device name the app of a rules
// change.
type Actor struct {
	OrgID, By      uuid.UUID
	Client, Device string
}

// NewActor makes the actor of a token. By is the user of the token when it
// has one, else the token.
func NewActor(t store.Token, client, device string) Actor {
	by := t.ID
	if t.UserID != nil {
		by = *t.UserID
	}
	return Actor{OrgID: t.OrgID, By: by, Client: client, Device: device}
}

// Origin names the client of a rules change.
func (a Actor) Origin(at time.Time) store.RulesOrigin {
	return store.RulesOrigin{
		Client: store.NormalizeClient(a.Client),
		Device: a.Device,
		UserID: a.By,
		At:     at.UTC(),
	}
}

// RulesResult is the outcome of a write of rules. Backup is zero when no
// backup was made. Diff lists the fields that changed, with the target of
// each (see store.FieldChange). Settings and Nodes are the new values of the
// targets.
type RulesResult struct {
	Backup   store.RulesBackup
	Diff     []store.FieldChange
	Settings *store.ServerSettings
	Nodes    []store.Node
}

func (s *Service) writeJournal(ctx context.Context, lines ...notify.Line) {
	if s.journal == nil {
		return
	}
	if err := s.journal.Write(ctx, lines...); err != nil {
		s.log.Warn("alert log", "err", err)
	}
}

func (s *Service) invalidateSettings(orgID uuid.UUID) {
	if s.engine != nil {
		s.engine.InvalidateSettings(orgID)
	}
}

func (s *Service) broadcastAlert(orgID uuid.UUID, a wire.Alert) {
	if s.events == nil {
		return
	}
	s.events.BroadcastOrg(orgID, wire.WSMessage{Type: "alert_updated", NodeID: &a.NodeID, Alert: &a})
}

func (s *Service) broadcastNode(orgID uuid.UUID, n store.Node) {
	if s.events == nil {
		return
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return
	}
	s.events.BroadcastOrg(orgID, wire.WSMessage{Type: "node_updated", Node: raw})
}

func (s *Service) broadcastSettings(orgID uuid.UUID, cfg store.ServerSettings) {
	if s.events == nil {
		return
	}
	if raw, err := json.Marshal(cfg); err == nil {
		s.events.BroadcastOrg(orgID, wire.WSMessage{Type: "settings_updated", Setting: raw})
	}
}
