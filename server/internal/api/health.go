package api

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func (s *Server) handleHealthz(c echo.Context) error {
	return c.String(http.StatusOK, "ok")
}

func (s *Server) handleReadyz(c echo.Context) error {
	// When CH is disabled (dev mode), the server is ready the moment HTTP
	// is listening. In all other cases, we wait for CH migrations to
	// complete so ingest doesn't 503 the first sampler push.
	if s.ch == nil {
		return c.String(http.StatusOK, "ready")
	}
	if s.ch.Ready() {
		return c.String(http.StatusOK, "ready")
	}
	return c.String(http.StatusServiceUnavailable, "starting")
}
