package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
)

type maxNodes int

func (maxNodes) HistoryDays(context.Context, uuid.UUID) int { return 0 }
func (m maxNodes) MaxNodes(context.Context, uuid.UUID) int  { return int(m) }

func TestNodeLimit(t *testing.T) {
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authctx.Set(c, store.Token{OrgID: store.ZeroOrg, Kind: store.TokenKindUser})
			return next(c)
		}
	}
	srv := New(Deps{
		Config:     &config.Config{},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:      st,
		Issuer:     auth.NewIssuer(st),
		Middleware: []echo.MiddlewareFunc{user},
		Limits:     maxNodes(2),
	})
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	a, b := uuid.NewString(), uuid.NewString()
	for _, c := range []struct {
		method, target, body string
		want                 int
	}{
		{http.MethodPost, "/v1/nodes", `{"id":"` + a + `","displayName":"a"}`, http.StatusCreated},
		{http.MethodPut, "/v1/nodes/" + b, `{"displayName":"b"}`, http.StatusOK},
		{http.MethodPost, "/v1/nodes", `{"displayName":"c"}`, http.StatusPaymentRequired},
		{http.MethodPut, "/v1/nodes/" + uuid.NewString(), `{"displayName":"d"}`, http.StatusPaymentRequired},
		{http.MethodPost, "/v1/sampler/enroll", `{"display_name":"e"}`, http.StatusPaymentRequired},
		// An update of a node that exists passes.
		{http.MethodPut, "/v1/nodes/" + a, `{"displayName":"a2"}`, http.StatusOK},
		{http.MethodPost, "/v1/nodes", `{"id":"` + b + `","displayName":"b2"}`, http.StatusCreated},
	} {
		rec := do(c.method, c.target, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s: %d, want %d: %s", c.method, c.target, rec.Code, c.want, rec.Body.String())
		}
		if c.want == http.StatusPaymentRequired && !strings.Contains(rec.Body.String(), `"plan_limit"`) {
			t.Errorf("402 body: %s", rec.Body.String())
		}
	}
}
