package api

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// handleSamplerEnroll binds (or creates) a Node and issues a sampler
// token in one round-trip. Called by `sampler service install`.
func (s *Server) handleSamplerEnroll(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	if t.Kind != store.TokenKindUser && t.Kind != store.TokenKindAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "user or admin token required")
	}
	var req wire.EnrollRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := c.Validate(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	nodeID := req.NodeID
	if nodeID == uuid.Nil {
		nodeID = uuid.New()
		if err := s.checkNodeLimit(c.Request().Context(), t.OrgID, nodeID); err != nil {
			return err
		}
		node := store.Node{
			ID:               nodeID,
			DisplayName:      req.DisplayName,
			Kind:             "ssh",
			Enabled:          true,
			IconOnWarn:       true,
			IconOnCritical:   true,
			NotifyOnWarn:     true,
			NotifyOnCritical: true,
		}
		if err := s.store.PutNode(c.Request().Context(), t.OrgID, node); err != nil {
			return err
		}
		s.broadcastNode(t.OrgID, node)
	}

	raw, _, err := s.issuer.Issue(c.Request().Context(), t.OrgID, store.TokenKindSampler, req.DisplayName, &nodeID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, wire.EnrollResponse{
		NodeID:       nodeID,
		SamplerToken: raw,
		Endpoint:     "", // filled by caller-side config
	})
}

// handleListSamplerTokens lists the sampler tokens of the org that are
// not revoked.
func (s *Server) handleListSamplerTokens(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	all, err := s.store.ListTokens(c.Request().Context(), t.OrgID)
	if err != nil {
		return err
	}
	out := []wire.SamplerToken{}
	for _, tk := range all {
		if tk.Kind != store.TokenKindSampler || tk.RevokedAt != nil {
			continue
		}
		out = append(out, wire.SamplerToken{ID: tk.ID, Label: tk.Label, NodeID: tk.NodeID, CreatedAt: tk.CreatedAt, LastUsedAt: tk.LastUsedAt})
	}
	return c.JSON(http.StatusOK, out)
}

// handleRevokeSamplerToken deletes a sampler token of the org. The node
// stays.
func (s *Server) handleRevokeSamplerToken(c echo.Context) error {
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
	i := slices.IndexFunc(all, func(tk store.Token) bool { return tk.ID == id && tk.Kind == store.TokenKindSampler })
	if i < 0 {
		return echo.NewHTTPError(http.StatusNotFound, "token not found")
	}
	if err := s.store.DeleteToken(ctx, id); err != nil {
		return err
	}
	s.audit(c, "revoke", "sampler_token", id)
	return c.NoContent(http.StatusNoContent)
}

// handleSamplerVersions returns the expected sampler SHA per triple.
// Stub until the bundled manifest is hooked in (out of scope for the
// self-hosted path v1 but kept on the surface for RemoteBackend).
func (s *Server) handleSamplerVersions(c echo.Context) error {
	empty := map[string]string{}
	out, _ := json.Marshal(empty)
	return c.JSONBlob(http.StatusOK, out)
}
