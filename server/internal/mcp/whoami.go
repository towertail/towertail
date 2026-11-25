package mcp

import (
	"context"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
	out := WhoamiOut{
		OrgID: c.Token.OrgID.String(), OrgName: facts.Name, Plan: facts.Plan, HistoryDays: facts.HistoryDays, MaxHosts: facts.MaxHosts,
		TokenKind: string(c.Token.Kind), Role: string(c.Role), ServerVersion: s.version,
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
