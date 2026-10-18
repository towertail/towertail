package api

import (
	"context"
	"encoding/json"
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
	"github.com/towertail/towertail/server/wire"
)

func TestSamplerTokens(t *testing.T) {
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	issuer := auth.NewIssuer(st)
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
		Issuer:     issuer,
		Middleware: []echo.MiddlewareFunc{user},
	})
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	list := func() []wire.SamplerToken {
		rec := do(http.MethodGet, "/v1/sampler/tokens", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		var out []wire.SamplerToken
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("empty list: %v", got)
	}
	rec := do(http.MethodPost, "/v1/sampler/enroll", `{"display_name":"web-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", rec.Code, rec.Body)
	}
	var enr wire.EnrollResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &enr)
	// A user token and a token of another org do not show.
	ctx := context.Background()
	if _, _, err := issuer.Issue(ctx, store.ZeroOrg, store.TokenKindUser, "me", nil); err != nil {
		t.Fatal(err)
	}
	_, other, err := issuer.Issue(ctx, uuid.New(), store.TokenKindSampler, "other", nil)
	if err != nil {
		t.Fatal(err)
	}

	got := list()
	if len(got) != 1 || got[0].Label != "web-1" || got[0].NodeID == nil || *got[0].NodeID != enr.NodeID {
		t.Fatalf("list: %+v", got)
	}
	if strings.Contains(do(http.MethodGet, "/v1/sampler/tokens", "").Body.String(), "hash") {
		t.Fatal("the list shows the hash")
	}
	if rec := do(http.MethodDelete, "/v1/sampler/tokens/"+other.ID.String(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete other org: %d", rec.Code)
	}
	if rec := do(http.MethodDelete, "/v1/sampler/tokens/"+got[0].ID.String(), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := issuer.Verify(ctx, enr.SamplerToken); err == nil {
		t.Fatal("the revoked token still works")
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("list after delete: %v", got)
	}
	if _, err := st.GetNode(ctx, store.ZeroOrg, enr.NodeID); err != nil {
		t.Fatalf("the node must stay: %v", err)
	}
}
