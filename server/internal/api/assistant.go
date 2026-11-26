package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/assistant"
	"github.com/towertail/towertail/server/store"
)

const askTimeout = 5 * time.Minute

// AssistantPlans gives the plan and seats of an org.
type AssistantPlans interface {
	AssistantPlan(ctx context.Context, orgID uuid.UUID) (plan string, seats int, err error)
}

// settingsSource gives the assistant the org settings from the store.
type settingsSource struct{ st store.ConfigStore }

func (s settingsSource) AssistantSettings(ctx context.Context, orgID uuid.UUID) (bool, store.AIChanges, error) {
	cfg, err := s.st.GetSettings(ctx, orgID)
	if err != nil {
		return false, "", err
	}
	ai := cfg.MCP.AIChanges
	if ai == "" {
		ai = store.DefaultMCPSettings().AIChanges
	}
	return cfg.AssistantOff, ai, nil
}

// actorFrom gives the user of the request. A token with no user acts as
// itself.
func actorFrom(c echo.Context) (assistant.Actor, error) {
	t, ok := authctx.TokenFrom(c)
	role, hasRole := authctx.RoleFrom(c)
	if !ok || !hasRole {
		return assistant.Actor{}, echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
	}
	user := t.ID
	if t.UserID != nil {
		user = *t.UserID
	}
	return assistant.Actor{OrgID: t.OrgID, UserID: user, Role: role, Token: t}, nil
}

// needAssistant gives 503 when the server has no assistant.
func (s *Server) needAssistant(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if s.assistant == nil {
			return &codedError{Status: http.StatusServiceUnavailable, Code: "assistant_unavailable", Message: "The assistant is not available on this server."}
		}
		return next(c)
	}
}

// assistantError maps the errors of the assistant to HTTP errors. Other
// errors pass through.
func assistantError(err error) error {
	var ae *assistant.Error
	switch {
	case errors.As(err, &ae):
		ce := &codedError{Status: http.StatusInternalServerError, Code: ae.Code, Message: ae.Message}
		switch ae.Code {
		case assistant.CodeAssistantOff:
			ce.Status = http.StatusForbidden
		case assistant.CodeDailyLimit:
			ce.Status = http.StatusTooManyRequests
		case assistant.CodePlanLimit:
			ce.Status = http.StatusPaymentRequired
		case assistant.CodeProviderError:
			ce.Status = http.StatusBadGateway
		}
		if !ae.ResetAt.IsZero() {
			ce.ResetAt = &ae.ResetAt
		}
		return ce
	case errors.Is(err, assistant.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	case errors.Is(err, assistant.ErrEmptyQuestion), errors.Is(err, assistant.ErrQuestionTooLong), errors.Is(err, assistant.ErrBadRows):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, assistant.ErrNotPending):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, assistant.ErrExpired):
		return &codedError{Status: http.StatusGone, Code: "expired", Message: "The proposal expired."}
	case errors.Is(err, assistant.ErrRole):
		return echo.NewHTTPError(http.StatusForbidden, "Your role does not allow this change.")
	case errors.Is(err, assistant.ErrAIChanges):
		return &codedError{Status: http.StatusForbidden, Code: "ai_changes_off", Message: "The setting Changes by AI of this organization does not allow this change."}
	}
	return err
}

// checkOn gives 403 assistant_off when an admin turned the assistant off.
func (s *Server) checkOn(ctx context.Context, a assistant.Actor) error {
	off, _, err := settingsSource{s.store}.AssistantSettings(ctx, a.OrgID)
	if err != nil {
		return err
	}
	if off {
		return &codedError{Status: http.StatusForbidden, Code: assistant.CodeAssistantOff, Message: "An admin turned the assistant off for this organization."}
	}
	return nil
}

func (s *Server) handleCreateChat(c echo.Context) error {
	a, err := actorFrom(c)
	if err != nil {
		return err
	}
	var body struct {
		Title string `json:"title"`
	}
	if c.Request().ContentLength != 0 {
		if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "bad body")
		}
	}
	now := time.Now().UTC()
	chat := assistant.Chat{ID: uuid.New(), OrgID: a.OrgID, UserID: a.UserID, Title: body.Title, CreatedAt: now, UpdatedAt: now}
	if err := s.assistant.Chats().CreateChat(c.Request().Context(), chat); err != nil {
		return assistantError(err)
	}
	return c.JSON(http.StatusCreated, chat)
}

func (s *Server) handleListChats(c echo.Context) error {
	a, err := actorFrom(c)
	if err != nil {
		return err
	}
	limit := 50
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, "limit must be a positive number")
		}
		limit = min(n, 200)
	}
	chats, err := s.assistant.Chats().Chats(c.Request().Context(), a.OrgID, a.UserID, limit)
	if err != nil {
		return assistantError(err)
	}
	if chats == nil {
		chats = []assistant.Chat{}
	}
	return c.JSON(http.StatusOK, map[string]any{"chats": chats})
}

func (s *Server) handleGetChat(c echo.Context) error {
	a, err := actorFrom(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	chat, msgs, err := s.assistant.Chats().Chat(c.Request().Context(), a.OrgID, a.UserID, id)
	if err != nil {
		return assistantError(err)
	}
	if msgs == nil {
		msgs = []assistant.StoredMessage{}
	}
	return c.JSON(http.StatusOK, map[string]any{"chat": chat, "messages": msgs})
}

func (s *Server) handleDeleteChat(c echo.Context) error {
	a, err := actorFrom(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err := s.assistant.Chats().DeleteChat(c.Request().Context(), a.OrgID, a.UserID, id); err != nil {
		return assistantError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// handleAsk streams the answer as server-sent events. An error before the
// first event is a JSON error. After it, the error is an error event.
func (s *Server) handleAsk(c echo.Context) error {
	a, err := actorFrom(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	var q assistant.Question
	if err := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20)).Decode(&q); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad body")
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), askTimeout)
	defer cancel()

	sse := newSSEWriter(c)
	ping := s.ssePing
	if ping <= 0 {
		ping = sseKeepAlive
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(ping)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if sse.Comment("ping") != nil {
					return
				}
			}
		}
	}()
	err = s.assistant.Ask(ctx, a, id, q, func(ev assistant.Event) error { return sse.Send(ev.Name, ev.Data) })
	close(stop)
	<-stopped

	switch {
	case err == nil:
		return nil
	case !sse.Started():
		return assistantError(err)
	case ctx.Err() != nil:
		return nil
	}
	ev := assistant.ErrorEvent{Code: assistant.CodeInternal, Message: "The answer failed."}
	var ae *assistant.Error
	if errors.As(err, &ae) {
		ev = assistant.ErrorEvent{Code: ae.Code, Message: ae.Message, ResetAt: ae.ResetAt}
	} else {
		s.log.Error("assistant: ask failed", "err", err)
	}
	_ = sse.Send(assistant.EventError, ev)
	return nil
}

func (s *Server) handleApplyProposal(c echo.Context) error {
	a, err := actorFrom(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	var body struct {
		Rows []string `json:"rows"`
	}
	if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad body")
	}
	ctx := c.Request().Context()
	if err := s.checkOn(ctx, a); err != nil {
		return err
	}
	p, err := s.assistant.ApplyProposal(ctx, a, id, body.Rows)
	if err != nil {
		return assistantError(err)
	}
	s.auditProposal(c, "assistant.apply", p)
	return c.JSON(http.StatusOK, map[string]any{"proposal": p})
}

func (s *Server) handleUndoProposal(c echo.Context) error {
	a, err := actorFrom(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	ctx := c.Request().Context()
	if err := s.checkOn(ctx, a); err != nil {
		return err
	}
	p, err := s.assistant.UndoProposal(ctx, a, id)
	if err != nil {
		return assistantError(err)
	}
	s.auditProposal(c, "assistant.undo", p)
	return c.JSON(http.StatusOK, map[string]any{"proposal": p})
}

func (s *Server) auditProposal(c echo.Context, action string, p assistant.Proposal) {
	var backup uuid.UUID
	if p.Applied != nil {
		backup = p.Applied.BackupID
	}
	s.audit(c, action, "proposal", p.ID, "tool", p.Tool, "backup_id", backup)
}

func (s *Server) handleAssistantUsage(c echo.Context) error {
	if s.assistant == nil {
		return c.JSON(http.StatusOK, map[string]any{"enabled": false})
	}
	a, err := actorFrom(c)
	if err != nil {
		return err
	}
	v, err := s.assistant.Usage(c.Request().Context(), a)
	if err != nil {
		return assistantError(err)
	}
	return c.JSON(http.StatusOK, v)
}
