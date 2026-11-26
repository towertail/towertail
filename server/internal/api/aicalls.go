package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/assistant"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/wire"
)

const (
	defAICallsLimit = 50
	maxAICallsLimit = 100
)

// aiCallReader is the part of the ClickHouse client that the AI activity
// routes read.
type aiCallReader interface {
	AICalls(ctx context.Context, q clickhouse.AICallQuery) ([]clickhouse.AICall, error)
	AICall(ctx context.Context, orgID, callID uuid.UUID) (clickhouse.AICall, error)
	AICallStats(ctx context.Context, q clickhouse.AICallQuery) (clickhouse.AICallStats, error)
	AICallUsage(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, since time.Time) ([]clickhouse.AICallUsage, error)
}

// needAICalls gives 503 when the server has no call log.
func (s *Server) needAICalls(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if s.aiCalls == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse disabled — no AI activity available")
		}
		if s.ch != nil && !s.ch.Ready() {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
		}
		return next(c)
	}
}

// aiScope is the org and the user that a request may read. An admin or
// owner reads the whole org. Other roles read their own calls.
func aiScope(c echo.Context) (clickhouse.AICallQuery, bool) {
	t, _ := authctx.TokenFrom(c)
	q := clickhouse.AICallQuery{OrgID: t.OrgID}
	if seesAllKeys(c) {
		return q, true
	}
	own := uuid.Nil // matches no row
	if t.UserID != nil {
		own = *t.UserID
	}
	q.UserID = &own
	return q, false
}

var aiRanges = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

func badParam(name string) error {
	return echo.NewHTTPError(http.StatusBadRequest, "bad "+name)
}

// aiQuery reads the range and the filters of a request.
func aiQuery(c echo.Context) (clickhouse.AICallQuery, error) {
	q, _ := aiScope(c)
	span := c.QueryParam("range")
	if span == "" {
		span = "7d"
	}
	d, ok := aiRanges[span]
	if !ok {
		return q, badParam("range")
	}
	q.To = time.Now().UTC()
	q.From = q.To.Add(-d)
	q.Client, q.Tool = c.QueryParam("client"), c.QueryParam("tool")
	switch c.QueryParam("status") {
	case "", "all":
	case "errors":
		q.Filter = clickhouse.AICallErrors
	case "clamped":
		q.Filter = clickhouse.AICallClamped
	default:
		return q, badParam("status")
	}
	return q, nil
}

func aiCallOf(r clickhouse.AICall) wire.AICall {
	return wire.AICall{
		ID: r.CallID, TS: r.TS, UserID: r.UserID, Via: r.Via, Client: r.Client, GrantID: r.GrantID, TokenID: r.TokenID,
		Tool: r.Tool, Input: r.Input, Status: r.Status, Clamped: r.Clamped, Error: r.Error,
		DurationMs: int(r.DurationMs), Rows: int(r.Rows), Bytes: int(r.Bytes),
	}
}

// handleListAICalls lists the AI calls, newest first.
func (s *Server) handleListAICalls(c echo.Context) error {
	q, err := aiQuery(c)
	if err != nil {
		return err
	}
	limit := defAICallsLimit
	if v := c.QueryParam("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxAICallsLimit {
			return badParam("limit")
		}
	}
	if v := c.QueryParam("cursor"); v != "" {
		if q.BeforeTS, q.BeforeID, err = decodeCursor(v); err != nil {
			return badParam("cursor")
		}
	}
	// One more row tells if a next page exists.
	q.Limit = limit + 1
	rows, err := s.aiCalls.AICalls(c.Request().Context(), q)
	if err != nil {
		return err
	}
	page := wire.AICallPage{Calls: make([]wire.AICall, 0, len(rows))}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.Next = encodeCursor(last.TS, last.CallID)
	}
	for _, r := range rows {
		page.Calls = append(page.Calls, aiCallOf(r))
	}
	return c.JSON(http.StatusOK, page)
}

// handleAICallStats gives the totals of the calls with the same filters.
func (s *Server) handleAICallStats(c echo.Context) error {
	q, err := aiQuery(c)
	if err != nil {
		return err
	}
	st, err := s.aiCalls.AICallStats(c.Request().Context(), q)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, wire.AICallStats{
		Calls: st.Calls, People: st.People, Errors: st.Errors, Clamped: st.Clamped, P95Ms: st.P95Ms,
		Clients: append([]string{}, st.Clients...), Tools: append([]string{}, st.Tools...),
	})
}

// handleGetAICall gives one call with its full input and its change. A
// call of another user is 404 for a caller who does not see the whole org.
func (s *Server) handleGetAICall(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	scope, all := aiScope(c)
	r, err := s.aiCalls.AICall(c.Request().Context(), scope.OrgID, id)
	switch {
	case errors.Is(err, clickhouse.ErrNoAICall):
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	case err != nil:
		return err
	case !all && !sameUser(r.UserID, scope.UserID):
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	d := wire.AICallDetail{AICall: aiCallOf(r)}
	if json.Valid([]byte(r.Change)) {
		d.Change = json.RawMessage(r.Change)
	}
	return c.JSON(http.StatusOK, d)
}

// handleAIUsage counts the calls of each connected assistant.
func (s *Server) handleAIUsage(c echo.Context) error {
	days := 7
	if v := c.QueryParam("days"); v != "" {
		var err error
		if days, err = strconv.Atoi(v); err != nil || days < 1 || days > 30 {
			return badParam("days")
		}
	}
	scope, _ := aiScope(c)
	rows, err := s.aiCalls.AICallUsage(c.Request().Context(), scope.OrgID, scope.UserID, time.Now().UTC().AddDate(0, 0, -days))
	if err != nil {
		return err
	}
	out := make([]wire.AIUsage, 0, len(rows))
	for _, r := range rows {
		u := wire.AIUsage{Calls: r.Calls, LastAt: r.LastAt}
		if r.GrantID != uuid.Nil {
			u.GrantID = &r.GrantID
		}
		if r.TokenID != uuid.Nil {
			u.TokenID = &r.TokenID
		}
		out = append(out, u)
	}
	return c.JSON(http.StatusOK, out)
}

// handleAITools lists the tools of the MCP server with their groups.
func (s *Server) handleAITools(c echo.Context) error {
	if s.mcp == nil {
		return echo.NewHTTPError(http.StatusNotFound, "MCP is off")
	}
	tools := s.mcp.Tools(mcp.ToolSetLatest)
	out := make([]wire.AITool, 0, len(tools))
	for _, t := range tools {
		w := wire.AITool{Name: t.Name, Title: t.Title, Description: t.Description, Group: mcp.Group(t.Name)}
		if a := t.Annotations; a != nil {
			w.ReadOnly = a.ReadOnlyHint
			w.Destructive = a.DestructiveHint != nil && *a.DestructiveHint
		}
		out = append(out, w)
	}
	return c.JSON(http.StatusOK, out)
}

func encodeCursor(ts time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(ts.UnixMilli(), 10) + "." + id.String()))
}

func decodeCursor(s string) (time.Time, uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	ms, id, ok := strings.Cut(string(b), ".")
	if !ok {
		return time.Time{}, uuid.Nil, errors.New("bad cursor")
	}
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	return time.UnixMilli(n).UTC(), u, nil
}

// failedProposal loads the proposal of a request that failed, so the log
// can name its tool. The rows are the ones of the apply request, or nil. A
// proposal that does not load gives a zero value.
func (s *Server) failedProposal(ctx context.Context, a assistant.Actor, id uuid.UUID, rows []string) assistant.Proposal {
	p, err := s.assistant.Proposal(ctx, a, id)
	if err != nil {
		return assistant.Proposal{}
	}
	if rows != nil {
		p.Rows = slices.Clone(p.Rows)
		for i := range p.Rows {
			p.Rows[i].Checked = slices.Contains(rows, p.Rows[i].ID)
		}
	}
	return p
}

// recordProposal logs the apply or the undo of a proposal as one AI call,
// with the tool that the writer calls. A failed request is logged too.
func (s *Server) recordProposal(c echo.Context, p assistant.Proposal, undo bool, start time.Time, err error) {
	t, ok := authctx.TokenFrom(c)
	if s.calls == nil || !ok || p.Tool == "" {
		return
	}
	row := clickhouse.AICall{
		TS: start.UTC(), CallID: uuid.New(), OrgID: t.OrgID, UserID: t.UserID, TokenID: t.ID, Via: "assistant", Client: "assistant",
		Status: "ok", DurationMs: uint32(max(0, time.Since(start).Milliseconds())),
	}
	switch {
	case err == nil:
	case errors.Is(err, assistant.ErrRole), errors.Is(err, assistant.ErrAIChanges):
		row.Status, row.Error = "denied", err.Error()
	default:
		row.Status, row.Error = "error", err.Error()
	}
	row.Tool, row.Input, row.Change = proposalCall(p, undo)
	row.Clip()
	s.calls.Record(row)
}

// proposalCall gives the tool, the input and the change of the C call that
// an apply or an undo makes. The change has the shape of the change key of
// the C write tools.
func proposalCall(p assistant.Proposal, undo bool) (tool, input, change string) {
	rows := p.Rows
	var backup uuid.UUID
	if p.Applied != nil {
		backup = p.Applied.BackupID
	}
	var ids []string
	for _, r := range rows {
		if r.Checked {
			ids = append(ids, r.ID)
		}
	}
	in := map[string]json.RawMessage{}
	_ = json.Unmarshal(p.Input, &in)
	tool = p.Tool
	switch {
	case undo && p.Kind == "rules":
		tool = "undo_rules_change"
		in = map[string]json.RawMessage{"backup_id": mustJSON(backup.String())}
	case undo:
		in["undo"] = mustJSON(true)
	default:
		in["rows"] = mustJSON(ids)
	}
	ch := mcp.Change{Targets: []mcp.ChangeTarget{}, Fields: []mcp.ChangeField{}}
	if backup != uuid.Nil {
		ch.BackupID = backup.String()
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if !r.Checked {
			continue
		}
		if !seen[r.Target] {
			seen[r.Target] = true
			ch.Targets = append(ch.Targets, mcp.ChangeTarget{Kind: targetKind(r.Target), Name: r.Target})
		}
		before, after := any(r.Old), any(r.New)
		if undo {
			before, after = after, before
		}
		ch.Fields = append(ch.Fields, mcp.ChangeField{Target: r.Target, Path: r.Field, Before: before, After: after})
	}
	return tool, string(mustJSON(in)), string(mustJSON(ch))
}

func targetKind(target string) string {
	switch {
	case target == "org":
		return "org"
	case strings.HasPrefix(target, "alert "):
		return "alert"
	}
	return "host"
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("api: marshal %T: %v", v, err))
	}
	return b
}
