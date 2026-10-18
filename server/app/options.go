package app

import (
	"log/slog"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/runtime"
	"github.com/towertail/towertail/server/store"
)

// Router gives route groups that require a bearer token of one of the
// kinds.
type Router interface {
	Group(prefix string, kinds ...store.TokenKind) *echo.Group
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
}

// WithLogger replaces the logger from the log config.
func WithLogger(l *slog.Logger) Option { return func(b *builder) { b.log = l } }

// WithStore replaces the Bolt store. The caller closes the store.
func WithStore(s store.ConfigStore) Option { return func(b *builder) { b.store = s } }

// WithNotifier adds a channel for alerts. More than one is allowed.
func WithNotifier(n notify.Notifier) Option {
	return func(b *builder) { b.notifiers = append(b.notifiers, n) }
}

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
