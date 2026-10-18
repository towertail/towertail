package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

func (s *Server) handleListNodes(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	nodes, err := s.store.ListNodes(c.Request().Context(), t.OrgID)
	if err != nil {
		return err
	}
	if nodes == nil {
		nodes = []store.Node{}
	}
	return c.JSON(http.StatusOK, nodes)
}

func (s *Server) handleCreateNode(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	var n store.Node
	if err := c.Bind(&n); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if n.ID == uuid.Nil {
		n.ID = uuid.New()
	}
	if err := s.store.PutNode(c.Request().Context(), t.OrgID, n); err != nil {
		return err
	}
	s.audit(c, "create", "node", n.ID, "name", n.DisplayName, "kind", n.Kind)
	s.broadcastNode(t.OrgID, n)
	return c.JSON(http.StatusCreated, n)
}

func (s *Server) handleGetNode(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	n, err := s.store.GetNode(c.Request().Context(), t.OrgID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, n)
}

func (s *Server) handleUpdateNode(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	var n store.Node
	if err := json.Unmarshal(body, &n); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	// A client that does not know allowDiskScan keeps the stored value.
	var opt struct {
		AllowDiskScan *bool `json:"allowDiskScan"`
	}
	_ = json.Unmarshal(body, &opt)
	if opt.AllowDiskScan == nil {
		if old, err := s.store.GetNode(c.Request().Context(), t.OrgID, id); err == nil {
			n.AllowDiskScan = old.AllowDiskScan
		}
	}
	n.ID = id
	if err := s.store.PutNode(c.Request().Context(), t.OrgID, n); err != nil {
		return err
	}
	s.audit(c, "update", "node", n.ID, "name", n.DisplayName)
	s.broadcastNode(t.OrgID, n)
	return c.JSON(http.StatusOK, n)
}

func (s *Server) handleDeleteNode(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	if err := s.store.DeleteNode(c.Request().Context(), t.OrgID, id); err != nil {
		return err
	}
	s.audit(c, "delete", "node", id)
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) broadcastNode(orgID uuid.UUID, n store.Node) {
	if s.hub == nil {
		return
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return
	}
	s.hub.BroadcastOrg(orgID, wire.WSMessage{Type: "node_updated", Node: raw})
}
