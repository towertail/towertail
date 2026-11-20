package api

import (
	"cmp"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// maxAPIKeys is the most API keys of one user.
const maxAPIKeys = 20

// sameUser is true when both IDs are set and equal, or both are unset.
func sameUser(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// seesAllKeys is true for a role that manages the keys of the org.
func seesAllKeys(c echo.Context) bool {
	r, ok := authctx.RoleFrom(c)
	return ok && r.Allows(store.RoleAdmin)
}

func apiKeyOf(t store.Token) wire.APIKey {
	return wire.APIKey{ID: t.ID, Label: t.Label, UserID: t.UserID, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt}
}

// handleCreateAPIKey issues a read-only key for the caller. The answer
// has the secret one time.
func (s *Server) handleCreateAPIKey(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	var req wire.APIKeyRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	req.Label = strings.TrimSpace(req.Label)
	if err := c.Validate(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad label")
	}
	ctx := c.Request().Context()
	all, err := s.store.ListTokens(ctx, t.OrgID)
	if err != nil {
		return err
	}
	own := 0
	for _, tk := range all {
		if tk.Kind == store.TokenKindAPI && tk.RevokedAt == nil && sameUser(tk.UserID, t.UserID) {
			own++
		}
	}
	if own >= maxAPIKeys {
		return echo.NewHTTPError(http.StatusConflict, "a user can have 20 API keys")
	}
	raw, key, err := s.issuer.Issue(ctx, t.OrgID, store.TokenKindAPI, req.Label, nil, t.UserID)
	if err != nil {
		return err
	}
	s.audit(c, "create", "api_key", key.ID)
	return c.JSON(http.StatusCreated, wire.APIKeyCreated{APIKey: apiKeyOf(key), Token: raw})
}

// handleListAPIKeys lists the keys of the org that the caller manages.
// An admin or owner sees all keys. Other roles see their own.
func (s *Server) handleListAPIKeys(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	all, err := s.store.ListTokens(c.Request().Context(), t.OrgID)
	if err != nil {
		return err
	}
	allKeys := seesAllKeys(c)
	keys := []store.Token{}
	for _, tk := range all {
		if tk.Kind != store.TokenKindAPI || tk.RevokedAt != nil {
			continue
		}
		if allKeys || (t.UserID != nil && sameUser(tk.UserID, t.UserID)) {
			keys = append(keys, tk)
		}
	}
	slices.SortFunc(keys, func(a, b store.Token) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID.String(), b.ID.String())
	})
	out := make([]wire.APIKey, 0, len(keys))
	for _, tk := range keys {
		out = append(out, apiKeyOf(tk))
	}
	return c.JSON(http.StatusOK, out)
}

// handleRevokeAPIKey deletes a key. A non-admin can revoke only own keys.
func (s *Server) handleRevokeAPIKey(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	ctx := c.Request().Context()
	all, err := s.store.ListTokens(ctx, t.OrgID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(all, func(tk store.Token) bool { return tk.ID == id && tk.Kind == store.TokenKindAPI })
	if i < 0 {
		return echo.NewHTTPError(http.StatusNotFound, "key not found")
	}
	if !seesAllKeys(c) && !(t.UserID != nil && sameUser(all[i].UserID, t.UserID)) {
		return echo.NewHTTPError(http.StatusForbidden, "not your key")
	}
	if err := s.store.DeleteToken(ctx, id); err != nil {
		return err
	}
	s.audit(c, "revoke", "api_key", id)
	return c.NoContent(http.StatusNoContent)
}
