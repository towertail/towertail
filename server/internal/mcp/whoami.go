package mcp

import (
	"context"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/store"
)

// WhoamiIn is the input of whoami. It has no fields.
type WhoamiIn struct{}

// WhoamiOut is the output of whoami. HistoryDays 0 means no limit.
type WhoamiOut struct {
	OrgID         string `json:"org_id"`
	OrgName       string `json:"org_name,omitempty"`
	Plan          string `json:"plan"`
	HistoryDays   int    `json:"history_days"`
	MaxHosts      int    `json:"max_hosts,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	TokenKind     string `json:"token_kind"`
	Role          string `json:"role"`
	ServerVersion string `json:"server_version"`
	// Scopes are the scopes of the token. ChangesByAI is the org setting
	// (off, alerts or alerts_and_rules). Can tells which changes this
	// connection can make now.
	Scopes      []string `json:"scopes"`
	ChangesByAI string   `json:"changes_by_ai"`
	Can         Can      `json:"can"`
}

func (s *Server) whoami(ctx context.Context, _ *sdk.CallToolRequest, _ WhoamiIn) (*sdk.CallToolResult, WhoamiOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, WhoamiOut{}, err
	}
	facts, err := s.orgFacts(ctx, c.Token.OrgID)
	if err != nil {
		return nil, WhoamiOut{}, s.toolError("whoami", err)
	}
	g, err := s.grantOf(ctx, c)
	if err != nil {
		return nil, WhoamiOut{}, s.toolError("whoami", err)
	}
	out := WhoamiOut{
		OrgID: c.Token.OrgID.String(), OrgName: facts.Name, Plan: facts.Plan, HistoryDays: facts.HistoryDays, MaxHosts: facts.MaxHosts,
		TokenKind: string(c.Token.Kind), Role: string(c.Role), ServerVersion: s.version,
		Scopes: []string{}, ChangesByAI: string(g.mcp.AIChanges), Can: g.can(),
	}
	for _, sc := range []store.Scope{store.ScopeFleetRead, store.ScopeAlertsWrite, store.ScopeRulesWrite} {
		if c.Token.HasScope(sc) {
			out.Scopes = append(out.Scopes, string(sc))
		}
	}
	if c.Token.UserID != nil {
		out.UserID = c.Token.UserID.String()
	}
	return nil, out, nil
}

// orgFacts gives the facts of the org. With no OrgInfo, the org is
// self-hosted and the history limit comes from the queries.
func (s *Server) orgFacts(ctx context.Context, orgID uuid.UUID) (OrgFacts, error) {
	if s.orgInfo == nil {
		return OrgFacts{Plan: "self-hosted", HistoryDays: s.q.HistoryDays(ctx, orgID)}, nil
	}
	return s.orgInfo.OrgFacts(ctx, orgID)
}
