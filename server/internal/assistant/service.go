package assistant

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/store"
)

// Tools is the part of *mcp.Server that the loop uses.
type Tools interface {
	ToolSpecs() []mcp.ToolSpec
	Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

// Writer plans, applies and undoes the write tools. The implementation is
// the adapter assistantWriter in internal/api over actions.Service.
type Writer interface {
	Plan(ctx context.Context, a Actor, tool string, input json.RawMessage) (Change, error)
	Apply(ctx context.Context, a Actor, c Change, rows []string) (Applied, error)
	Undo(ctx context.Context, a Actor, c Change, ap Applied) error
}

// PlanSource gives the billing plan and seat count of an org. Nil means
// plan "" (limits.default).
type PlanSource interface {
	AssistantPlan(ctx context.Context, orgID uuid.UUID) (plan string, seats int, err error)
}

// Settings gives the org settings that the assistant needs.
type Settings interface {
	AssistantSettings(ctx context.Context, orgID uuid.UUID) (off bool, aiChanges store.AIChanges, err error)
}

// Actor is the user who asks. Token has the same type as in authctx.
type Actor struct {
	OrgID, UserID uuid.UUID
	Role          store.Role
	Token         store.Token
}

// Service runs questions and proposals.
type Service struct {
	cfg      config.AssistantConfig
	model    Model
	tools    Tools
	writer   Writer
	store    Store
	plans    PlanSource
	settings Settings
	log      *slog.Logger
	now      func() time.Time
	// applyMu keeps two applies of one proposal apart.
	applyMu sync.Mutex
}

// NewService makes the service. A nil Writer gives no write tools. A nil
// PlanSource uses limits.default. A nil Settings uses the defaults.
func NewService(cfg config.AssistantConfig, m Model, t Tools, w Writer, st Store,
	plans PlanSource, set Settings, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{cfg: cfg, model: m, tools: t, writer: w, store: st, plans: plans, settings: set, log: log, now: time.Now}
}

// Chats gives the store that the chat routes use.
func (s *Service) Chats() ChatStore { return s.store }

// orgSettings reads the org settings.
func (s *Service) orgSettings(ctx context.Context, orgID uuid.UUID) (bool, store.AIChanges, error) {
	if s.settings == nil {
		return false, store.DefaultMCPSettings().AIChanges, nil
	}
	return s.settings.AssistantSettings(ctx, orgID)
}

// ctx gives a ctx that the tools read the token and the role from.
func (a Actor) ctx(ctx context.Context) context.Context {
	return authctxWith(ctx, a)
}

// Question is one user question.
type Question struct {
	Text     string      `json:"text"`
	Context  PageContext `json:"context"`
	Mentions []string    `json:"mentions"` // host IDs
}

// PageContext tells what the user looks at.
type PageContext struct {
	Route   string    `json:"route"`
	Kind    string    `json:"kind,omitempty"` // "why" | "fewer" | "explain_change"
	HostID  string    `json:"hostId,omitempty"`
	AlertID string    `json:"alertId,omitempty"`
	RuleID  string    `json:"ruleId,omitempty"`
	Metric  string    `json:"metric,omitempty"`
	Process string    `json:"process,omitempty"`
	Mount   string    `json:"mount,omitempty"`
	From    time.Time `json:"from,omitzero"`
	To      time.Time `json:"to,omitzero"`
}
