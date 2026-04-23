package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/wire"
)

func (s *Server) handleListNodes(c echo.Context) error {
	t, _ := tokenFromCtx(c)
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
	t, _ := tokenFromCtx(c)
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
	t, _ := tokenFromCtx(c)
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
	t, _ := tokenFromCtx(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	var n store.Node
	if err := c.Bind(&n); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
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
	t, _ := tokenFromCtx(c)
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
