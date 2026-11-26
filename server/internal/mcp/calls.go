package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

// CallLog takes the record of each tool call. A nil CallLog records
// nothing.
type CallLog interface {
	Record(c clickhouse.AICall)
}

type viaKey struct{}

// WithVia sets the way of the calls on ctx: api_key, oauth, user or
// assistant. Without it, the token kind decides.
func WithVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, viaKey{}, via)
}

// ViaFrom gives the value that WithVia set.
func ViaFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(viaKey{}).(string)
	return v, ok
}

// toolGroups maps each tool to its group on the AI assistants page.
var toolGroups = map[string]string{
	"fleet_summary": "fleet", "list_hosts": "fleet", "get_host": "fleet",
	"host_metrics":  "history",
	"top_processes": "processes", "process_history": "processes",
	"alerts": "alerts", "get_alert": "alerts", "host_rules": "alerts",
	"disks": "disks", "disk_scans": "disks",
	"alert_stats": "analysis", "explain_change": "analysis", "suggest_thresholds": "analysis",
	"reference": "reference", "whoami": "reference",
	"search": "search", "fetch": "search",
	"ack_alert": "changes", "snooze_alert": "changes", "snooze_host": "changes", "mute_metric": "changes",
	"set_thresholds": "changes", "ignore_process": "changes", "undo_rules_change": "changes",
}

// Group gives the group of a tool. An unknown tool gives an empty text.
func Group(tool string) string { return toolGroups[tool] }

// recordCalls is the receiving middleware that records each tools/call.
func (s *Server) recordCalls(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		if method != "tools/call" || s.calls == nil {
			return next(ctx, method, req)
		}
		start := s.now()
		res, err := next(ctx, method, req)
		cr, _ := req.(*sdk.CallToolRequest)
		if cr == nil || cr.Params == nil {
			return res, err
		}
		var out json.RawMessage
		callErr := err
		if r, ok := res.(*sdk.CallToolResult); ok && r != nil {
			if callErr == nil {
				callErr = r.GetError()
				if callErr == nil && r.IsError {
					callErr = errors.New("tool error")
				}
			}
			out = structured(r.StructuredContent)
		}
		s.record(ctx, cr.Params.Name, cr.Params.Arguments, start, out, callErr)
		return res, err
	}
}

func structured(v any) json.RawMessage {
	switch v := v.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return v
	}
	b, _ := json.Marshal(v)
	return b
}

// record writes one row for a finished call. out is the structured output.
// The row keeps no value of the output, only its size, the clamped flag and
// the change of a write tool.
func (s *Server) record(ctx context.Context, tool string, args json.RawMessage, start time.Time, out json.RawMessage, err error) {
	if s.calls == nil {
		return
	}
	tok, ok := authctx.FromContext(ctx)
	if !ok {
		return
	}
	via, ok := ViaFrom(ctx)
	if !ok {
		via = viaOf(tok.Kind)
	}
	client := tok.Label
	if via == "assistant" {
		client = "assistant"
	}
	end := s.now()
	row := clickhouse.AICall{
		TS: start.UTC(), CallID: uuid.New(), OrgID: tok.OrgID, UserID: tok.UserID, TokenID: tok.ID, GrantID: tok.GrantID,
		Via: via, Client: client, Tool: tool, Input: string(args),
		Status: "ok", DurationMs: uint32(max(0, end.Sub(start).Milliseconds())), Bytes: uint32(len(out)),
	}
	switch {
	case err == nil:
	case errors.Is(err, ErrNoScope), errors.Is(err, ErrRole), errors.Is(err, ErrAIChangesOff):
		row.Status, row.Error = "denied", err.Error()
	default:
		row.Status, row.Error = "error", err.Error()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(out, &fields) == nil {
		row.Clamped = string(fields["clamped"]) == "true"
		for k, v := range fields {
			switch {
			case k == "change" && string(v) != "null":
				row.Change = string(v)
			case len(v) > 0 && v[0] == '[':
				var items []json.RawMessage
				if json.Unmarshal(v, &items) == nil {
					row.Rows += uint32(len(items))
				}
			}
		}
	}
	row.Clip()
	s.calls.Record(row)
}

func viaOf(k store.TokenKind) string {
	switch k {
	case store.TokenKindOAuth:
		return "oauth"
	case store.TokenKindAPI:
		return "api_key"
	}
	return "user"
}
