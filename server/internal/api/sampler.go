package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

const joinTokenTTL = time.Hour

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
	ctx := c.Request().Context()
	node, isNew, err := s.resolveEnrollNode(ctx, t.OrgID, req)
	if err != nil {
		return err
	}
	return s.finishEnroll(c, t.OrgID, node, isNew)
}

// handleCreateJoinToken makes a one-use join token for a new host.
func (s *Server) handleCreateJoinToken(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	if t.Kind != store.TokenKindUser && t.Kind != store.TokenKindAdmin {
		return echo.NewHTTPError(http.StatusForbidden, "user or admin token required")
	}
	var req wire.JoinTokenRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	ctx := c.Request().Context()
	if err := s.checkNodeLimit(ctx, t.OrgID, uuid.New()); err != nil {
		return err
	}
	label := req.DisplayName
	if label == "" {
		label = "join"
	}
	raw, tok, err := s.issuer.IssueJoin(ctx, t.OrgID, t.UserID, label, joinTokenTTL)
	if err != nil {
		return err
	}
	s.audit(c, "create", "join_token", tok.ID)
	return c.JSON(http.StatusCreated, wire.JoinTokenResponse{Token: raw, ExpiresAt: *tok.ExpiresAt})
}

// handleJoin trades a join token for a node and a sampler token. The join
// token works one time.
func (s *Server) handleJoin(c echo.Context) error {
	var req wire.JoinRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := c.Validate(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	ctx := c.Request().Context()
	jt, err := s.issuer.Verify(ctx, req.JoinToken)
	if err != nil || jt.Kind != store.TokenKindJoin {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid join token")
	}
	node, isNew, err := s.resolveEnrollNode(ctx, jt.OrgID, wire.EnrollRequest{DisplayName: req.DisplayName, MachineID: req.MachineID})
	if err != nil {
		return err
	}
	// The delete claims the token. A second call with it finds nothing.
	if err := s.store.DeleteToken(ctx, jt.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid join token")
		}
		return err
	}
	return s.finishEnroll(c, jt.OrgID, node, isNew)
}

// resolveEnrollNode gives the node that an enroll call binds to. A node
// that is new is not saved yet. It passes the plan limit.
func (s *Server) resolveEnrollNode(ctx context.Context, orgID uuid.UUID, req wire.EnrollRequest) (node store.Node, isNew bool, err error) {
	switch {
	case req.NodeID != uuid.Nil:
		node, err = s.store.GetNode(ctx, orgID, req.NodeID)
		if err == nil {
			return node, false, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return node, false, err
		}
	case req.MachineID != "":
		node, err = s.store.FindNodeByMachineID(ctx, orgID, req.MachineID)
		if err == nil {
			return node, false, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return node, false, err
		}
	}
	id := req.NodeID
	if id == uuid.Nil {
		id = uuid.New()
	}
	if err := s.checkNodeLimit(ctx, orgID, id); err != nil {
		return store.Node{}, false, err
	}
	return store.Node{
		ID:               id,
		DisplayName:      req.DisplayName,
		Kind:             "ssh",
		Enabled:          true,
		IconOnWarn:       true,
		IconOnCritical:   true,
		NotifyOnWarn:     true,
		NotifyOnCritical: true,
		MachineID:        req.MachineID,
	}, true, nil
}

// finishEnroll saves a new node, issues the sampler token and replies.
func (s *Server) finishEnroll(c echo.Context, orgID uuid.UUID, node store.Node, isNew bool) error {
	ctx := c.Request().Context()
	if isNew {
		if err := s.store.PutNode(ctx, orgID, node); err != nil {
			return err
		}
		s.ranks.drop(orgID)
		s.broadcastNode(orgID, node)
		if s.alerter != nil {
			s.alerter.Expect(ctx, orgID, node.ID, time.Now().UTC())
		}
	}
	raw, _, err := s.issuer.Issue(ctx, orgID, store.TokenKindSampler, node.DisplayName, &node.ID, nil)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, wire.EnrollResponse{
		NodeID:       node.ID.String(),
		SamplerToken: raw,
		Endpoint:     s.publicURL(c),
	})
}

// publicURL gives the address that samplers use: http.public_url, or the
// scheme and host of the request.
func (s *Server) publicURL(c echo.Context) string {
	if u := strings.TrimRight(s.cfg.HTTP.PublicURL, "/"); u != "" {
		return u
	}
	return c.Scheme() + "://" + c.Request().Host
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
