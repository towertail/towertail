package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/wire"
)

// httpErrorHandler is installed as Echo's global error handler. It
// writes the uniform {"error": {...}} envelope.
func httpErrorHandler(log *slog.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		status := http.StatusInternalServerError
		code := "internal"
		msg := err.Error()

		var he *echo.HTTPError
		if errors.As(err, &he) {
			status = he.Code
			if s, ok := he.Message.(string); ok {
				msg = s
			}
			switch status {
			case http.StatusUnauthorized:
				code = "unauthorized"
			case http.StatusForbidden:
				code = "forbidden"
			case http.StatusNotFound:
				code = "not_found"
			case http.StatusBadRequest:
				code = "bad_request"
			case http.StatusConflict:
				code = "conflict"
			case http.StatusTooManyRequests:
				code = "rate_limited"
			case http.StatusServiceUnavailable:
				code = "unavailable"
			}
		} else if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			code = "not_found"
		} else if errors.Is(err, store.ErrNotImplemented) {
			status = http.StatusNotImplemented
			code = "not_implemented"
		}

		reqID := c.Response().Header().Get(echo.HeaderXRequestID)
		if status >= http.StatusInternalServerError {
			log.Error("api: error", "status", status, "err", err, "request_id", reqID)
		}
		_ = c.JSON(status, wire.APIErrorEnvelope{
			Error: wire.APIError{Code: code, Message: msg, RequestID: reqID},
		})
	}
}

// audit emits a structured log line for every state-changing action.
// Read-only endpoints are already covered by the request-log middleware;
// audit focuses on *who did what to which resource* and is the thing
// we'd keep when routing to an immutable log (Loki, ClickHouse `events`)
// in a follow-up.
func (s *Server) audit(c echo.Context, action, resource string, id uuid.UUID, extra ...any) {
	t, _ := tokenFromCtx(c)
	args := []any{
		"action", action,
		"resource", resource,
		"id", id,
		"org_id", t.OrgID,
		"actor_token", t.ID,
		"actor_kind", t.Kind,
		"request_id", c.Response().Header().Get(echo.HeaderXRequestID),
	}
	args = append(args, extra...)
	s.log.Info("audit", args...)
}
