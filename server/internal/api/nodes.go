package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

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
	if err := s.checkNodeLimit(c.Request().Context(), t.OrgID, n.ID); err != nil {
		return err
	}
	if err := s.store.PutNode(c.Request().Context(), t.OrgID, n); err != nil {
		return err
	}
	s.ranks.drop(t.OrgID)
	s.audit(c, "create", "node", n.ID, "name", n.DisplayName, "kind", n.Kind)
	s.broadcastNode(t.OrgID, n)
	return c.JSON(http.StatusCreated, n)
}

// checkNodeLimit replies 402 when a new node goes above the plan limit.
// An update of a node that exists always passes.
func (s *Server) checkNodeLimit(ctx context.Context, orgID, id uuid.UUID) error {
	if s.limits == nil {
		return nil
	}
	max := s.limits.MaxNodes(ctx, orgID)
	if max < 0 {
		return nil
	}
	if _, err := s.store.GetNode(ctx, orgID, id); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	nodes, err := s.store.ListNodes(ctx, orgID)
	if err != nil {
		return err
	}
	if len(nodes) >= max {
		return echo.NewHTTPError(http.StatusPaymentRequired, fmt.Sprintf("the plan allows %d hosts", max))
	}
	return nil
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
	// A client that does not know allowDiskScan, hideProcFolders,
	// ignoredProcs, machineId or mutedSignals keeps the stored values. Such a client also does not know
	// customLongProc.
	var opt struct {
		AllowDiskScan   *bool           `json:"allowDiskScan"`
		HideProcFolders *bool           `json:"hideProcFolders"`
		IgnoredProcs    json.RawMessage `json:"ignoredProcs"`
		MachineID       *string         `json:"machineId"`
		MutedSignals    json.RawMessage `json:"mutedSignals"`
	}
	_ = json.Unmarshal(body, &opt)
	if opt.AllowDiskScan == nil || opt.HideProcFolders == nil || opt.IgnoredProcs == nil || opt.MachineID == nil || opt.MutedSignals == nil {
		if old, err := s.store.GetNode(c.Request().Context(), t.OrgID, id); err == nil {
			if opt.AllowDiskScan == nil {
				n.AllowDiskScan = old.AllowDiskScan
			}
			if opt.HideProcFolders == nil {
				n.HideProcFolders = old.HideProcFolders
			}
			if opt.IgnoredProcs == nil {
				n.IgnoredProcs = old.IgnoredProcs
				n.CustomLongProc = old.CustomLongProc
			}
			if opt.MachineID == nil {
				n.MachineID = old.MachineID
			}
			if opt.MutedSignals == nil {
				n.MutedSignals = old.MutedSignals
			}
		}
	}
	n.ID = id
	if err := s.checkNodeLimit(c.Request().Context(), t.OrgID, id); err != nil {
		return err
	}
	if err := s.store.PutNode(c.Request().Context(), t.OrgID, n); err != nil {
		return err
	}
	s.ranks.drop(t.OrgID)
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
	s.ranks.drop(t.OrgID)
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

func (s *Server) handleSnoozeNode(c echo.Context) error {
	until, err := parseFuture(c.QueryParam("until"), time.Now())
	if err != nil {
		return err
	}
	return s.snoozeNode(c, &until)
}

func (s *Server) handleUnsnoozeNode(c echo.Context) error { return s.snoozeNode(c, nil) }

// snoozeNode sets or clears the snooze of the host in the path.
func (s *Server) snoozeNode(c echo.Context, until *time.Time) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	n, err := s.store.EditNode(c.Request().Context(), t.OrgID, id, func(n *store.Node) error {
		n.SnoozedUntil = until
		return nil
	})
	if err != nil {
		return err
	}
	s.audit(c, "snooze", "node", id, "until", until)
	s.broadcastNode(t.OrgID, n)
	return c.JSON(http.StatusOK, n)
}
