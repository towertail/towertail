package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Channels checks a channel and sends a test alert to it.
type Channels interface {
	Validate(ch store.Channel) error
	Test(ctx context.Context, ch store.Channel) error
}

// ChannelLimits tells if the plan of an org permits a channel kind.
type ChannelLimits interface {
	ChannelAllowed(ctx context.Context, orgID uuid.UUID, kind store.ChannelKind) bool
}

// channelTestTimeout is the time limit of a test send.
const channelTestTimeout = 15 * time.Second

// channelAllowed reports whether the plan of the org permits the kind.
func (s *Server) channelAllowed(ctx context.Context, orgID uuid.UUID, kind store.ChannelKind) bool {
	return s.channelLimits == nil || s.channelLimits.ChannelAllowed(ctx, orgID, kind)
}

// requireChannelKind answers 402 when the plan does not permit the kind.
func (s *Server) requireChannelKind(ctx context.Context, orgID uuid.UUID, kind store.ChannelKind) error {
	if s.channelAllowed(ctx, orgID, kind) {
		return nil
	}
	return echo.NewHTTPError(http.StatusPaymentRequired, fmt.Sprintf("channel kind %s needs the Team plan", kind))
}

func (s *Server) toWireChannel(ctx context.Context, ch store.Channel) wire.Channel {
	return wire.Channel{
		Blocked: !s.channelAllowed(ctx, ch.OrgID, ch.Kind),
		ID:      ch.ID, Kind: string(ch.Kind), Name: ch.Name, Enabled: ch.Enabled, Target: ch.Target,
		HasSecret: ch.Secret != "",
		Route:     wire.ChannelRoute{MinTint: ch.Route.MinTint, Metrics: ch.Route.Metrics, Tags: ch.Route.Tags},
		CreatedAt: ch.CreatedAt,
	}
}

// applyChannelRequest sets the fields of the request on ch. A nil pointer keeps the
// value of ch.
func applyChannelRequest(ch *store.Channel, req wire.ChannelRequest) {
	ch.Kind = store.ChannelKind(req.Kind)
	ch.Name = req.Name
	ch.Route = store.ChannelRoute{MinTint: req.Route.MinTint, Metrics: req.Route.Metrics, Tags: req.Route.Tags}
	if req.Enabled != nil {
		ch.Enabled = *req.Enabled
	}
	if req.Target != nil {
		ch.Target = *req.Target
	}
	if req.Secret != nil {
		ch.Secret = *req.Secret
	}
}

func (s *Server) channelID(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	return id, nil
}

func (s *Server) handleListChannels(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	list, err := s.store.ListChannels(c.Request().Context(), t.OrgID)
	if err != nil {
		return err
	}
	out := make([]wire.Channel, 0, len(list))
	for _, ch := range list {
		out = append(out, s.toWireChannel(c.Request().Context(), ch))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) handleGetChannel(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := s.channelID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	ch, err := s.store.GetChannel(ctx, t.OrgID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s.toWireChannel(ctx, ch))
}

func (s *Server) handleCreateChannel(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	var req wire.ChannelRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	ch := store.Channel{ID: uuid.New(), OrgID: t.OrgID, Enabled: true, CreatedAt: time.Now().UTC()}
	applyChannelRequest(&ch, req)
	ctx := c.Request().Context()
	if err := s.requireChannelKind(ctx, t.OrgID, ch.Kind); err != nil {
		return err
	}
	if err := s.channels.Validate(ch); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := s.store.PutChannel(ctx, ch); err != nil {
		return err
	}
	s.audit(c, "create", "channel", ch.ID, "kind", ch.Kind, "name", ch.Name)
	return c.JSON(http.StatusCreated, s.toWireChannel(ctx, ch))
}

func (s *Server) handleUpdateChannel(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := s.channelID(c)
	if err != nil {
		return err
	}
	var req wire.ChannelRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	ctx := c.Request().Context()
	ch, err := s.store.GetChannel(ctx, t.OrgID, id)
	if err != nil {
		return err
	}
	kind := ch.Kind
	applyChannelRequest(&ch, req)
	if ch.Kind != kind {
		if err := s.requireChannelKind(ctx, t.OrgID, ch.Kind); err != nil {
			return err
		}
	}
	if err := s.channels.Validate(ch); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := s.store.PutChannel(ctx, ch); err != nil {
		return err
	}
	s.audit(c, "update", "channel", ch.ID, "kind", ch.Kind, "name", ch.Name)
	return c.JSON(http.StatusOK, s.toWireChannel(ctx, ch))
}

func (s *Server) handleDeleteChannel(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := s.channelID(c)
	if err != nil {
		return err
	}
	if err := s.store.DeleteChannel(c.Request().Context(), t.OrgID, id); err != nil {
		return err
	}
	s.audit(c, "delete", "channel", id)
	return c.NoContent(http.StatusNoContent)
}

// handleTestChannel sends a test alert. A failed send gives 200 with
// ok=false, so the client can show the error of the channel.
func (s *Server) handleTestChannel(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := s.channelID(c)
	if err != nil {
		return err
	}
	ch, err := s.store.GetChannel(c.Request().Context(), t.OrgID, id)
	if err != nil {
		return err
	}
	if err := s.requireChannelKind(c.Request().Context(), t.OrgID, ch.Kind); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), channelTestTimeout)
	defer cancel()
	if err := s.channels.Test(ctx, ch); err != nil {
		return c.JSON(http.StatusOK, wire.ChannelTestResponse{Error: err.Error()})
	}
	return c.JSON(http.StatusOK, wire.ChannelTestResponse{OK: true})
}
