package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/towertail/towertail/server/store"
)

// The errors of a refused write. F1 marks a call as denied when the error of
// the result matches one of them.
var (
	ErrNoScope      = errors.New("mcp: no write scope")
	ErrRole         = errors.New("mcp: role too low")
	ErrAIChangesOff = errors.New("mcp: changes by AI are off")
)

// rightError is a refused write. Error gives the text that the model sees.
// Unwrap gives one of the three errors above.
type rightError struct {
	err error
	msg string
}

func (e *rightError) Error() string { return e.msg }
func (e *rightError) Unwrap() error { return e.err }

// need is the right that a write needs.
type need struct {
	scope store.Scope
	role  store.Role
}

var (
	needAlerts = need{store.ScopeAlertsWrite, store.RoleMember}
	needRules  = need{store.ScopeRulesWrite, store.RoleAdmin}
)

// grant is what a caller may do: the token, the role of the user and the
// MCP rules of the org.
type grant struct {
	token store.Token
	role  store.Role
	mcp   store.MCPSettings
}

// grantOf reads the MCP rules of the org of the caller. With no settings
// reader, the rules are the defaults.
func (s *Server) grantOf(ctx context.Context, c caller) (grant, error) {
	g := grant{token: c.Token, role: c.Role, mcp: store.DefaultMCPSettings()}
	if s.settings == nil {
		return g, nil
	}
	cfg, err := s.settings.GetSettings(ctx, c.Token.OrgID)
	if err != nil {
		return grant{}, err
	}
	g.mcp = cfg.MCP
	return g, nil
}

// allow gives the first reason why the grant does not meet n, or nil. The
// kind check comes first, because HasScope gives user and admin tokens all
// scopes.
func (g grant) allow(n need) error {
	switch {
	case g.token.Kind == store.TokenKindAPI:
		return &rightError{ErrNoScope, "This connection uses an API key. API keys can only read."}
	case g.token.Kind != store.TokenKindOAuth:
		return &rightError{ErrNoScope, "This connection has no write scopes."}
	case !g.token.HasScope(n.scope):
		return &rightError{ErrNoScope, fmt.Sprintf("This connection does not have the scope %s.", n.scope)}
	case !g.role.Allows(n.role):
		return &rightError{ErrRole, fmt.Sprintf("Your role is %s. This change needs the role %s.", g.role, n.role)}
	case g.mcp.AIChanges == store.AIChangesOff:
		return &rightError{ErrAIChangesOff, "The org setting Changes by AI is off."}
	case !g.mcp.Allows(n.scope):
		return &rightError{ErrAIChangesOff, "The org setting Changes by AI allows alert actions only."}
	}
	return nil
}

// Can tells which kinds of change the grant allows.
type Can struct {
	Alerts bool `json:"alerts"`
	Rules  bool `json:"rules"`
}

func (g grant) can() Can {
	return Can{Alerts: g.allow(needAlerts) == nil, Rules: g.allow(needRules) == nil}
}
