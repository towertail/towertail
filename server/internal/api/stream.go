package api

import (
	"net/http"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
)

// handleStream accepts a websocket connection and hands it to the hub.
func (s *Server) handleStream(c echo.Context) error {
	t, _ := tokenFromCtx(c)
	conn, err := websocket.Accept(c.Response().Writer, c.Request(), &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	client := s.hub.Register(t.OrgID, conn)
	defer s.hub.Unregister(client)

	s.hub.ReplayFor(client)

	if err := client.Run(ctx); err != nil {
		// Normal close arrives here; we don't want 500s for it.
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
			websocket.CloseStatus(err) == websocket.StatusGoingAway {
			return nil
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	return nil
}

// --- compile-time assertion that websocket import is used when no WS
// configured in plan-mode dev builds ---
var _ = http.StatusOK
