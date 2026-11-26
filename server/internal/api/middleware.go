package api

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// codedError gives an HTTP error with its own code and an optional reset
// time.
type codedError struct {
	Status  int
	Code    string
	Message string
	ResetAt *time.Time
}

func (e *codedError) Error() string { return e.Message }

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

		var resetAt *time.Time
		var ce *codedError
		var he *echo.HTTPError
		if errors.As(err, &ce) {
			status, code, msg, resetAt = ce.Status, ce.Code, ce.Message, ce.ResetAt
			if resetAt != nil && status == http.StatusTooManyRequests {
				wait := max(int(math.Ceil(time.Until(*resetAt).Seconds())), 1)
				c.Response().Header().Set(echo.HeaderRetryAfter, strconv.Itoa(wait))
			}
		} else if errors.As(err, &he) {
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
			case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
				code = "bad_request"
			case http.StatusConflict:
				code = "conflict"
			case http.StatusPaymentRequired:
				code = "plan_limit"
			case http.StatusTooManyRequests:
				code = "rate_limited"
			case http.StatusServiceUnavailable:
				code = "unavailable"
			}
		} else if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			code = "not_found"
		} else if errors.Is(err, store.ErrInvalidRules) {
			status = http.StatusBadRequest
			code = "bad_request"
		} else if errors.Is(err, store.ErrNotImplemented) {
			status = http.StatusNotImplemented
			code = "not_implemented"
		}

		reqID := c.Response().Header().Get(echo.HeaderXRequestID)
		if status >= http.StatusInternalServerError {
			log.Error("api: error", "status", status, "err", err, "request_id", reqID)
		}
		_ = c.JSON(status, wire.APIErrorEnvelope{
			Error: wire.APIError{Code: code, Message: msg, RequestID: reqID, ResetAt: resetAt},
		})
	}
}

// actionError maps the errors of the actions service to HTTP errors. Other
// errors pass through, and httpErrorHandler maps them.
func actionError(err error) error {
	status := 0
	var conflict *actions.ConflictError
	switch {
	case errors.As(err, &conflict):
		status = http.StatusConflict
	case errors.Is(err, actions.ErrNotFound), errors.Is(err, actions.ErrNotOpen):
		status = http.StatusNotFound
	case errors.Is(err, actions.ErrNotReady):
		status = http.StatusServiceUnavailable
	case errors.Is(err, actions.ErrBadInput), errors.Is(err, actions.ErrBadUntil),
		errors.Is(err, actions.ErrNoProcess), errors.Is(err, actions.ErrBadMetric):
		status = http.StatusBadRequest
	default:
		return err
	}
	return echo.NewHTTPError(status, err.Error())
}

// audit emits a structured log line for every state-changing action.
// Read-only endpoints are already covered by the request-log middleware;
// audit focuses on *who did what to which resource* and is the thing
// we'd keep when routing to an immutable log (Loki, ClickHouse `events`)
// in a follow-up.
func (s *Server) audit(c echo.Context, action, resource string, id uuid.UUID, extra ...any) {
	t, _ := authctx.TokenFrom(c)
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
