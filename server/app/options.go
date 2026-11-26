package app

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/internal/api"
	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/runtime"
	"github.com/towertail/towertail/server/store"
)

// Limits gives the plan limits of an org.
type Limits = api.Limits

// OrgFacts are the facts of an org for the MCP server. MaxHosts is 0 when
// the plan has no host limit.
type OrgFacts struct {
	Name, Plan  string
	HistoryDays int
	MaxHosts    int
}

// OrgInfo gives the facts of an org. The cloud sets it.
type OrgInfo interface {
	OrgFacts(ctx context.Context, orgID uuid.UUID) (OrgFacts, error)
}

// orgInfoAdapter gives an OrgInfo as an mcp.OrgInfo.
type orgInfoAdapter struct{ info OrgInfo }

func (a orgInfoAdapter) OrgFacts(ctx context.Context, orgID uuid.UUID) (mcp.OrgFacts, error) {
	f, err := a.info.OrgFacts(ctx, orgID)
	return mcp.OrgFacts(f), err
}

// ChannelLimits tells if the plan of an org permits a channel kind. When
// the Limits value of WithLimits also implements it, the server uses it.
// Else all kinds are permitted.
type ChannelLimits = api.ChannelLimits

// AssistantPlans gives the assistant plan of an org. When the Limits value
// of WithLimits also implements it, the server uses it.
type AssistantPlans = api.AssistantPlans

// Roles gives the role of a user in an org. Role returns
// store.ErrNoMembership when the user has no role in the org.
type Roles = api.Roles

// Router gives route groups for extra routes.
type Router interface {
	// Group requires a bearer token of one of the kinds.
	Group(prefix string, kinds ...store.TokenKind) *echo.Group
	// Public requires no token, for example for a login.
	Public(prefix string) *echo.Group
}

// Option changes how New builds the App.
type Option func(*builder)

type builder struct {
	log        *slog.Logger
	store      store.ConfigStore
	notifiers  []notify.Notifier
	routes     []func(Router)
	middleware []echo.MiddlewareFunc
	components []runtime.Component
	limits     Limits
	orgInfo    OrgInfo
	mailer     notify.Mailer
	roles      Roles
	alertLinks notify.Links
}

// WithLogger replaces the logger from the log config.
func WithLogger(l *slog.Logger) Option { return func(b *builder) { b.log = l } }

// WithStore replaces the Bolt store. The caller closes the store.
func WithStore(s store.ConfigStore) Option { return func(b *builder) { b.store = s } }

// WithNotifier adds a channel for alerts. More than one is allowed.
func WithNotifier(n notify.Notifier) Option {
	return func(b *builder) { b.notifiers = append(b.notifiers, n) }
}

// WithAlertLinks adds "Ask why" and "Get fewer of these" links to alert
// messages. Without it, alerts have no links.
func WithAlertLinks(l notify.Links) Option { return func(b *builder) { b.alertLinks = l } }

// WithMailer sends the mail of the email alert channel through m, in
// place of SMTP.
func WithMailer(m notify.Mailer) Option { return func(b *builder) { b.mailer = m } }

// WithRoutes adds routes after the core routes.
func WithRoutes(f func(r Router)) Option {
	return func(b *builder) { b.routes = append(b.routes, f) }
}

// WithMiddleware adds middleware after the core middleware.
func WithMiddleware(m echo.MiddlewareFunc) Option {
	return func(b *builder) { b.middleware = append(b.middleware, m) }
}

// WithComponent adds a component. Run starts it and stops it at the end.
func WithComponent(c runtime.Component) Option {
	return func(b *builder) { b.components = append(b.components, c) }
}

// WithLimits sets the plan limits. The node create endpoints reply 402
// above MaxNodes. The history endpoints clamp "from" to
// the history days of the org. A value that implements ChannelLimits also
// gates the alert channel kinds.
func WithLimits(l Limits) Option { return func(b *builder) { b.limits = l } }

// WithOrgInfo sets the source of the org name and the plan for the MCP
// tool whoami. With no source, the server gives the self-hosted facts.
func WithOrgInfo(o OrgInfo) Option { return func(b *builder) { b.orgInfo = o } }

// WithRoles sets the source of the roles. With no source, each user token
// has the owner role.
func WithRoles(r Roles) Option { return func(b *builder) { b.roles = r } }

// RequireRole rejects a request when the role on the context is below
// need, or when there is no role. Use it on the routes of an extension.
func RequireRole(need store.Role) echo.MiddlewareFunc { return api.RequireRole(need) }
