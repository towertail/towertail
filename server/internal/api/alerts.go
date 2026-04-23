package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Phase H scaffolds alert endpoints. Full implementation reads/writes
// the ClickHouse events table; for Phase A we return empty lists so
// clients wired up first don't 404.

type alertListResponse struct {
	Alerts []any `json:"alerts"`
}

func (s *Server) handleListAlerts(c echo.Context) error {
	return c.JSON(http.StatusOK, alertListResponse{Alerts: []any{}})
}

func (s *Server) handleAckAlert(c echo.Context) error {
	_, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	// Placeholder: recording an ack requires an event lookup in CH and
	// will be implemented in Phase H alongside the events table writer.
	return c.JSON(http.StatusOK, map[string]any{
		"id":       c.Param("id"),
		"acked_at": time.Now().UTC(),
	})
}

func (s *Server) handleSnoozeAlert(c echo.Context) error {
	_, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	return c.JSON(http.StatusOK, map[string]any{
		"id":             c.Param("id"),
		"snoozed_until":  c.QueryParam("until"),
	})
}
