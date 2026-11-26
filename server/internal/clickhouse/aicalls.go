package clickhouse

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrNoAICall is returned when the org has no AI call with the ID.
var ErrNoAICall = errors.New("clickhouse: AI call not found")

const (
	maxAICallLimit  = 200
	defAICallLimit  = 50
	aiCallListInput = 200 // characters of input in a list row
	aiCallNames     = 100 // most distinct clients or tools in the stats
	maxAICallText   = 1024
	maxAICallTool   = 100
)

// AICall is one AI tool call. It has no result values. Change is the diff
// of a write tool, as JSON.
type AICall struct {
	TS                       time.Time
	CallID, OrgID, TokenID   uuid.UUID
	UserID, GrantID          *uuid.UUID
	Via, Client, Tool, Input string
	Status, Error, Change    string
	Clamped                  bool
	DurationMs, Rows, Bytes  uint32
}

// Clip cuts the tool, the input and the error text to their limits. A cut
// keeps whole characters.
func (c *AICall) Clip() {
	c.Tool = clip(c.Tool, maxAICallTool)
	c.Input = clip(c.Input, maxAICallText)
	c.Error = clip(c.Error, maxAICallText)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// AICallFilter selects calls by outcome.
type AICallFilter string

const (
	AICallAll     AICallFilter = ""
	AICallErrors  AICallFilter = "errors" // status is not ok
	AICallClamped AICallFilter = "clamped"
)

// AICallQuery selects the calls of one org in the range [From, To). A nil
// UserID covers all users. BeforeTS and BeforeID are the keyset of the
// last row of the page before. Limit is at most 200.
type AICallQuery struct {
	OrgID    uuid.UUID
	UserID   *uuid.UUID
	From, To time.Time
	Client   string
	Tool     string
	Filter   AICallFilter
	BeforeTS time.Time
	BeforeID uuid.UUID
	Limit    int
}

// AICallStats are the totals of a query. Clients and Tools are the
// distinct names of the range, for the filters.
type AICallStats struct {
	Calls, People, Errors, Clamped int
	P95Ms                          int
	Clients, Tools                 []string
}

// AICallUsage is the use of one grant, or of one token with no grant.
// The other ID is the zero UUID.
type AICallUsage struct {
	GrantID, TokenID uuid.UUID
	Calls            int
	LastAt           time.Time
}

// WriteAICalls inserts the rows in one batch.
func (c *Client) WriteAICalls(ctx context.Context, rows []AICall) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.table("ai_calls"))
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := batch.Append(r.TS, r.CallID, r.OrgID, r.UserID, r.TokenID, r.GrantID, r.Via, r.Client, r.Tool,
			r.Input, r.Status, r.Clamped, r.Error, r.DurationMs, r.Rows, r.Bytes, r.Change); err != nil {
			return err
		}
	}
	return batch.Send()
}

// where gives the conditions of the query, with the args. The org, the
// range and the user always apply. withFilters adds the client, tool and outcome.
func (q AICallQuery) where(withFilters bool) (string, []any) {
	cond := []string{"org_id = ?"}
	args := []any{q.OrgID}
	if !q.From.IsZero() {
		cond = append(cond, "ts >= ?")
		args = append(args, q.From)
	}
	if !q.To.IsZero() {
		cond = append(cond, "ts < ?")
		args = append(args, q.To)
	}
	if q.UserID != nil {
		cond = append(cond, "user_id = ?")
		args = append(args, *q.UserID)
	}
	if withFilters {
		if q.Client != "" {
			cond = append(cond, "client = ?")
			args = append(args, q.Client)
		}
		if q.Tool != "" {
			cond = append(cond, "tool = ?")
			args = append(args, q.Tool)
		}
		switch q.Filter {
		case AICallErrors:
			cond = append(cond, "status != 'ok'")
		case AICallClamped:
			cond = append(cond, "clamped")
		}
	}
	return strings.Join(cond, " AND "), args
}

const aiCallColumns = "ts, call_id, org_id, user_id, token_id, grant_id, toString(via), client, tool, "

func scanAICall(rows interface{ Scan(...any) error }, full bool) (AICall, error) {
	var r AICall
	dest := []any{&r.TS, &r.CallID, &r.OrgID, &r.UserID, &r.TokenID, &r.GrantID, &r.Via, &r.Client, &r.Tool,
		&r.Input, &r.Status, &r.Clamped, &r.Error, &r.DurationMs, &r.Rows, &r.Bytes}
	if full {
		dest = append(dest, &r.Change)
	}
	err := rows.Scan(dest...)
	return r, err
}

// AICalls gives the calls newest first. Input is cut to 200 characters
// and Change is empty. AICall gives the full row.
func (c *Client) AICalls(ctx context.Context, q AICallQuery) ([]AICall, error) {
	cond, args := q.where(true)
	if !q.BeforeTS.IsZero() {
		cond += " AND (toUnixTimestamp64Milli(ts) < ? OR (toUnixTimestamp64Milli(ts) = ? AND call_id < ?))"
		ms := q.BeforeTS.UnixMilli()
		args = append(args, ms, ms, q.BeforeID)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defAICallLimit
	}
	limit = min(limit, maxAICallLimit)
	rows, err := c.conn.Query(ctx,
		"SELECT "+aiCallColumns+"substringUTF8(input, 1, "+strconv.Itoa(aiCallListInput)+"), toString(status), clamped, error, duration_ms, rows, bytes FROM "+
			c.table("ai_calls")+" WHERE "+cond+" ORDER BY ts DESC, call_id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AICall
	for rows.Next() {
		r, err := scanAICall(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AICall gives one call with the full input and the change. It returns
// ErrNoAICall when the org has no such call.
func (c *Client) AICall(ctx context.Context, orgID, callID uuid.UUID) (AICall, error) {
	rows, err := c.conn.Query(ctx,
		"SELECT "+aiCallColumns+"input, toString(status), clamped, error, duration_ms, rows, bytes, change FROM "+
			c.table("ai_calls")+" WHERE org_id = ? AND call_id = ? LIMIT 1", orgID, callID)
	if err != nil {
		return AICall{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return AICall{}, err
		}
		return AICall{}, ErrNoAICall
	}
	return scanAICall(rows, true)
}

// AICallStats gives the totals of the query, and the distinct clients and
// tools of its range and user. The client, tool and outcome filters do not
// shrink the two lists.
func (c *Client) AICallStats(ctx context.Context, q AICallQuery) (AICallStats, error) {
	var (
		out                         AICallStats
		calls, people, errs, clamps uint64
		p95                         float64
	)
	cond, args := q.where(true)
	err := c.conn.QueryRow(ctx,
		"SELECT count(), uniq(user_id), countIf(status != 'ok'), countIf(clamped), quantile(0.95)(duration_ms) FROM "+
			c.table("ai_calls")+" WHERE "+cond, args...).Scan(&calls, &people, &errs, &clamps, &p95)
	if err != nil {
		return out, err
	}
	out.Calls, out.People, out.Errors, out.Clamped = int(calls), int(people), int(errs), int(clamps)
	if !math.IsNaN(p95) {
		out.P95Ms = int(math.Round(p95))
	}
	cond, args = q.where(false)
	for col, dst := range map[string]*[]string{"client": &out.Clients, "tool": &out.Tools} {
		rows, err := c.conn.Query(ctx,
			"SELECT DISTINCT "+col+" FROM "+c.table("ai_calls")+" WHERE "+cond+" ORDER BY "+col+" LIMIT ?",
			append(append([]any{}, args...), aiCallNames)...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				return out, err
			}
			*dst = append(*dst, name)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// AICallUsage counts the calls since the time for each grant, and for
// each token that has no grant.
func (c *Client) AICallUsage(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, since time.Time) ([]AICallUsage, error) {
	q := AICallQuery{OrgID: orgID, UserID: userID, From: since}
	cond, args := q.where(false)
	rows, err := c.conn.Query(ctx,
		"SELECT grant_id, if(isNull(grant_id), token_id, toUUID('00000000-0000-0000-0000-000000000000')) AS tok, count(), max(ts) FROM "+
			c.table("ai_calls")+" WHERE "+cond+" GROUP BY grant_id, tok ORDER BY max(ts) DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AICallUsage
	for rows.Next() {
		var (
			u     AICallUsage
			grant *uuid.UUID
			n     uint64
		)
		if err := rows.Scan(&grant, &u.TokenID, &n, &u.LastAt); err != nil {
			return nil, err
		}
		if grant != nil {
			u.GrantID = *grant
		}
		u.Calls = int(n)
		out = append(out, u)
	}
	return out, rows.Err()
}
