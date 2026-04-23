package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func (s *Server) handleHealthz(c echo.Context) error {
	return c.String(http.StatusOK, "ok")
}

func (s *Server) handleReadyz(c echo.Context) error {
	if s.ch != nil && s.ch.Ready() {
		return c.String(http.StatusOK, "ready")
	}
	return c.String(http.StatusServiceUnavailable, "starting")
}
