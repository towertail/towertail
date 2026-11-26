package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/wire"
)

func TestCodedError(t *testing.T) {
	reset := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	e := echo.New()
	e.HTTPErrorHandler = httpErrorHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.GET("/daily", func(echo.Context) error {
		return &codedError{Status: http.StatusTooManyRequests, Code: "daily_limit", Message: "slow down", ResetAt: &reset}
	})
	e.GET("/plan", func(echo.Context) error {
		return &codedError{Status: http.StatusPaymentRequired, Code: "plan_limit", Message: "upgrade", ResetAt: &reset}
	})
	e.GET("/missing", func(echo.Context) error { return echo.NewHTTPError(http.StatusNotFound, "gone") })
	e.GET("/boom", func(echo.Context) error { return errors.New("boom") })

	get := func(path string) (*httptest.ResponseRecorder, wire.APIError) {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var env wire.APIErrorEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return rec, env.Error
	}

	rec, body := get("/daily")
	if rec.Code != 429 || body.Code != "daily_limit" || body.ResetAt == nil || !body.ResetAt.Equal(reset) {
		t.Fatalf("daily: %d %+v", rec.Code, body)
	}
	if n, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || n < 5300 || n > 5400 {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	rec, body = get("/plan")
	if rec.Code != 402 || body.Code != "plan_limit" || body.ResetAt == nil || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("plan: %d %+v retry-after %q", rec.Code, body, rec.Header().Get("Retry-After"))
	}
	if rec, body = get("/missing"); rec.Code != 404 || body.Code != "not_found" || body.ResetAt != nil {
		t.Fatalf("missing: %d %+v", rec.Code, body)
	}
	if rec, body = get("/boom"); rec.Code != 500 || body.Code != "internal" {
		t.Fatalf("boom: %d %+v", rec.Code, body)
	}
}
