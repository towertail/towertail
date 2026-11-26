package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	sseWriteTimeout = 30 * time.Second
	sseKeepAlive    = 15 * time.Second
)

// sseWriter writes server-sent events. The first write starts the stream,
// so an error before it can still be a normal JSON error. A write sets its
// own deadline, so the stream outlives http.Server.WriteTimeout.
type sseWriter struct {
	c       echo.Context
	rc      *http.ResponseController
	mu      sync.Mutex
	started bool
}

func newSSEWriter(c echo.Context) *sseWriter {
	return &sseWriter{c: c, rc: http.NewResponseController(c.Response())}
}

// Started tells if the stream sent its first byte.
func (w *sseWriter) Started() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started
}

// Send writes one event. The data is one line of JSON.
func (w *sseWriter) Send(event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.write(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data))
}

// Comment writes a keep-alive line.
func (w *sseWriter) Comment(text string) error {
	return w.write(": " + strings.ReplaceAll(text, "\n", " ") + "\n\n")
}

func (w *sseWriter) write(s string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if !w.started {
		h := w.c.Response().Header()
		h.Set(echo.HeaderContentType, "text/event-stream")
		h.Set(echo.HeaderCacheControl, "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.c.Response().WriteHeader(http.StatusOK)
		w.started = true
	}
	if _, err := w.c.Response().Write([]byte(s)); err != nil {
		return err
	}
	if err := w.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}
