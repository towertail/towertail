package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/wire"
)

// handleControlNext is a short long-poll: samplers call this every
// heartbeat. We don't block here — we return 204 immediately if nothing
// is waiting. A blocking implementation can be added once we measure
// how noisy the poll is.
func (s *Server) handleControlNext(c echo.Context) error {
	t, _ := tokenFromCtx(c)
	if t.Kind != store.TokenKindSampler || t.NodeID == nil {
		return echo.NewHTTPError(http.StatusForbidden, "sampler token required")
	}
	msg, err := s.control.Claim(c.Request().Context(), *t.NodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.NoContent(http.StatusNoContent)
		}
		return err
	}
	return c.JSON(http.StatusOK, wire.ControlNextResponse{
		ID:      msg.ID,
		Kind:    msg.Kind,
		Payload: msg.Payload,
	})
}

// handleKillProcess enqueues a kill_process control message.
func (s *Server) handleKillProcess(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	pidStr := c.QueryParam("pid")
	pid64, err := strconv.ParseInt(pidStr, 10, 32)
	if err != nil || pid64 == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "pid required")
	}
	payload, _ := json.Marshal(wire.KillProcessPayload{PID: int32(pid64)})
	if err := s.control.Enqueue(c.Request().Context(), id, "kill_process", payload); err != nil {
		return err
	}
	s.audit(c, "kill_process", "node", id, "pid", pid64)
	return c.NoContent(http.StatusAccepted)
}
