package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"

	"github.com/towertail/server/internal/alerter"
	"github.com/towertail/server/internal/auth"
	"github.com/towertail/server/internal/clickhouse"
	"github.com/towertail/server/internal/config"
	"github.com/towertail/server/internal/control"
	"github.com/towertail/server/internal/hub"
	"github.com/towertail/server/internal/runtime"
	"github.com/towertail/server/internal/store"
)

// Server assembles the Echo instance and wires every dependency.
type Server struct {
	echo    *echo.Echo
	cfg     *config.Config
	log     *slog.Logger
	runtime *runtime.Runtime
	store   store.ConfigStore
	ch      *clickhouse.Client
	issuer  *auth.TokenIssuer
	hub     *hub.Hub
	alerter *alerter.Engine
	control *control.Queue
}

// Deps is the dependency bundle supplied by main.
type Deps struct {
	Config  *config.Config
	Log     *slog.Logger
	Runtime *runtime.Runtime
	Store   store.ConfigStore
	CH      *clickhouse.Client
	Issuer  *auth.TokenIssuer
	Hub     *hub.Hub
	Alerter *alerter.Engine
	Control *control.Queue
}

// New wires the handlers and middleware onto a fresh Echo instance.
func New(d Deps) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Validator = newValidator()
	e.HTTPErrorHandler = httpErrorHandler(d.Log)

	e.Use(middleware.RequestID())
	e.Use(middleware.Recover())
	e.Use(slogMiddleware(d.Log))
	// Skip gzip on the WebSocket upgrade path: the middleware wraps the
	// ResponseWriter with a gzip.Writer which writes headers after the
	// raw-TCP hijack completes, corrupting the client's view of the
	// stream. Mac clients don't send `Accept-Encoding: gzip` on /v1/stream,
	// but echo's middleware doesn't check that before wrapping.
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Skipper: func(c echo.Context) bool {
			return strings.HasPrefix(c.Request().URL.Path, "/v1/stream")
		},
	}))
	origins := d.Config.HTTP.CORSAllowOrigins
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: origins,
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{echo.HeaderAuthorization, echo.HeaderContentType, echo.HeaderContentEncoding},
	}))
	// Skip rate limiting on /v1/ingest here — samplers pushing metrics
	// need a much higher budget, so the ingest route applies its own
	// limiter inside registerRoutes.
	rps := d.Config.HTTP.RateLimitRPS
	if rps <= 0 {
		rps = 100
	}
	e.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Store: middleware.NewRateLimiterMemoryStore(rate.Limit(rps)),
		Skipper: func(c echo.Context) bool {
			return strings.HasPrefix(c.Request().URL.Path, "/v1/ingest")
		},
	}))

	s := &Server{
		echo:    e,
		cfg:     d.Config,
		log:     d.Log,
		runtime: d.Runtime,
		store:   d.Store,
		ch:      d.CH,
		issuer:  d.Issuer,
		hub:     d.Hub,
		alerter: d.Alerter,
		control: d.Control,
	}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler { return s.echo }

// Start runs the HTTP server until ctx is cancelled or ListenAndServe
// errors out.
func (s *Server) Start(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.cfg.HTTP.Addr,
		Handler:      s.echo,
		ReadTimeout:  s.cfg.HTTP.ReadTimeout,
		WriteTimeout: s.cfg.HTTP.WriteTimeout,
	}
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("api: listening", "addr", s.cfg.HTTP.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), s.cfg.HTTP.ShutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errCh:
		return err
	}
}

func (s *Server) registerRoutes() {
	e := s.echo
	e.GET("/healthz", s.handleHealthz)
	e.GET("/readyz", s.handleReadyz)
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()))

	v1 := e.Group("/v1")

	// Auth (public login).
	v1.POST("/auth/login", s.handleLogin)

	// User/admin-authenticated group.
	userGroup := v1.Group("", bearerMiddleware(s.issuer, store.TokenKindUser, store.TokenKindAdmin))
	userGroup.POST("/auth/logout", s.handleLogout)
	userGroup.GET("/nodes", s.handleListNodes)
	userGroup.POST("/nodes", s.handleCreateNode)
	userGroup.GET("/nodes/:id", s.handleGetNode)
	userGroup.PUT("/nodes/:id", s.handleUpdateNode)
	userGroup.DELETE("/nodes/:id", s.handleDeleteNode)
	userGroup.POST("/nodes/:id/kill-process", s.handleKillProcess)
	userGroup.GET("/nodes/:id/history", s.handleHistory)
	userGroup.GET("/settings", s.handleGetSettings)
	userGroup.PUT("/settings", s.handlePutSettings)
	userGroup.GET("/alerts", s.handleListAlerts)
	userGroup.POST("/alerts/:id/ack", s.handleAckAlert)
	userGroup.POST("/alerts/:id/snooze", s.handleSnoozeAlert)
	userGroup.POST("/sampler/enroll", s.handleSamplerEnroll)
	userGroup.GET("/sampler/versions", s.handleSamplerVersions)

	// Sampler-authenticated group. Ingest gets its own (higher) budget
	// since a fleet of samplers pushing 1/s can easily exceed the
	// generic per-IP limit.
	ingestRPS := s.cfg.HTTP.IngestRateLimitRPS
	if ingestRPS <= 0 {
		ingestRPS = 1000
	}
	samplerGroup := v1.Group("",
		bearerMiddleware(s.issuer, store.TokenKindSampler),
		middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
			Store: middleware.NewRateLimiterMemoryStore(rate.Limit(ingestRPS)),
		}),
	)
	samplerGroup.POST("/ingest/samples", s.handleIngest)
	samplerGroup.GET("/control/next", s.handleControlNext)

	// Stream group: user tokens only, attached directly (no nested group
	// so the WS upgrade can use the original ResponseWriter cleanly).
	v1.GET("/stream", s.handleStream, bearerMiddleware(s.issuer, store.TokenKindUser, store.TokenKindAdmin))
}

type echoValidator struct{ v *validator.Validate }

func newValidator() *echoValidator {
	return &echoValidator{v: validator.New()}
}

func (ev *echoValidator) Validate(i any) error { return ev.v.Struct(i) }

func slogMiddleware(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			dur := time.Since(start)
			req := c.Request()
			status := c.Response().Status
			if err != nil {
				var herr *echo.HTTPError
				if errors.As(err, &herr) {
					status = herr.Code
				} else {
					status = http.StatusInternalServerError
				}
			}
			log.Info("api: request",
				"method", req.Method,
				"path", req.URL.Path,
				"status", status,
				"dur_ms", dur.Milliseconds(),
				"request_id", c.Response().Header().Get(echo.HeaderXRequestID),
			)
			return err
		}
	}
}
