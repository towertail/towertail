package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/wire"
)

// handleSamplerEnroll binds (or creates) a Node and issues a sampler
// token in one round-trip. Called by `sampler service install`.
func (s *Server) handleSamplerEnroll(c echo.Context) error {
	t, _ := tokenFromCtx(c)
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

// handleSamplerVersions returns the expected sampler SHA per triple.
// Stub until the bundled manifest is hooked in (out of scope for the
// self-hosted path v1 but kept on the surface for RemoteBackend).
func (s *Server) handleSamplerVersions(c echo.Context) error {
	empty := map[string]string{}
	out, _ := json.Marshal(empty)
	return c.JSONBlob(http.StatusOK, out)
}
