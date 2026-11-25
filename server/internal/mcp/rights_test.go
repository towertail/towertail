package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

type fakeSettings struct {
	cfg store.ServerSettings
	err error
}

func (f *fakeSettings) GetSettings(context.Context, uuid.UUID) (store.ServerSettings, error) {
	return f.cfg, f.err
}

func TestRights(t *testing.T) {
	oauth := func(scopes ...store.Scope) store.Token {
		return store.Token{Kind: store.TokenKindOAuth, Scopes: scopes}
	}
	both := []store.Scope{store.ScopeFleetRead, store.ScopeAlertsWrite, store.ScopeRulesWrite}
	for _, tc := range []struct {
		name string
		g    grant
		n    need
		want error
		text string
	}{
		{"api key, admin", grant{store.Token{Kind: store.TokenKindAPI}, store.RoleAdmin, store.MCPSettings{AIChanges: store.AIChangesAlertsAndRules}}, needAlerts, ErrNoScope, "This connection uses an API key. API keys can only read."},
		{"user token, owner", grant{store.Token{Kind: store.TokenKindUser}, store.RoleOwner, store.MCPSettings{AIChanges: store.AIChangesAlertsAndRules}}, needRules, ErrNoScope, "This connection has no write scopes."},
		{"admin token", grant{store.Token{Kind: store.TokenKindAdmin}, store.RoleOwner, store.MCPSettings{AIChanges: store.AIChangesAlertsAndRules}}, needAlerts, ErrNoScope, "This connection has no write scopes."},
		{"read scope only", grant{oauth(store.ScopeFleetRead), store.RoleAdmin, store.MCPSettings{AIChanges: store.AIChangesAlertsAndRules}}, needAlerts, ErrNoScope, "This connection does not have the scope alerts:write."},
		{"alerts scope for rules", grant{oauth(store.ScopeFleetRead, store.ScopeAlertsWrite), store.RoleAdmin, store.MCPSettings{AIChanges: store.AIChangesAlertsAndRules}}, needRules, ErrNoScope, "This connection does not have the scope rules:write."},
		{"viewer", grant{oauth(both...), store.RoleViewer, store.MCPSettings{AIChanges: store.AIChangesAlerts}}, needAlerts, ErrRole, "Your role is viewer. This change needs the role member."},
		{"member for rules", grant{oauth(both...), store.RoleMember, store.MCPSettings{AIChanges: store.AIChangesAlertsAndRules}}, needRules, ErrRole, "Your role is member. This change needs the role admin."},
		{"no role", grant{oauth(both...), "", store.MCPSettings{AIChanges: store.AIChangesAlerts}}, needAlerts, ErrRole, "Your role is . This change needs the role member."},
		{"changes off", grant{oauth(both...), store.RoleAdmin, store.MCPSettings{AIChanges: store.AIChangesOff}}, needAlerts, ErrAIChangesOff, "The org setting Changes by AI is off."},
		{"alerts only for rules", grant{oauth(both...), store.RoleAdmin, store.MCPSettings{AIChanges: store.AIChangesAlerts}}, needRules, ErrAIChangesOff, "The org setting Changes by AI allows alert actions only."},
		{"scope before role", grant{oauth(store.ScopeFleetRead), store.RoleViewer, store.MCPSettings{AIChanges: store.AIChangesOff}}, needRules, ErrNoScope, "This connection does not have the scope rules:write."},
		{"role before setting", grant{oauth(both...), store.RoleViewer, store.MCPSettings{AIChanges: store.AIChangesOff}}, needAlerts, ErrRole, "Your role is viewer. This change needs the role member."},
		{"member alerts", grant{oauth(both...), store.RoleMember, store.MCPSettings{AIChanges: store.AIChangesAlerts}}, needAlerts, nil, ""},
		{"owner rules", grant{oauth(both...), store.RoleOwner, store.MCPSettings{AIChanges: store.AIChangesAlertsAndRules}}, needRules, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.g.allow(tc.n)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v, want none", err)
				}
				return
			}
			if !errors.Is(err, tc.want) || err.Error() != tc.text {
				t.Fatalf("err = %v, want %v with the text %q", err, tc.want, tc.text)
			}
			for _, other := range []error{ErrNoScope, ErrRole, ErrAIChangesOff} {
				if other != tc.want && errors.Is(err, other) {
					t.Errorf("err also matches %v", other)
				}
			}
		})
	}
}

func TestGrantCan(t *testing.T) {
	tok := store.Token{Kind: store.TokenKindOAuth, Scopes: []store.Scope{store.ScopeFleetRead, store.ScopeAlertsWrite}}
	g := grant{tok, store.RoleAdmin, store.MCPSettings{AIChanges: store.AIChangesAlertsAndRules}}
	if got := g.can(); got != (Can{Alerts: true}) {
		t.Fatalf("can = %+v, want alerts only", got)
	}
}

func TestGrantOf(t *testing.T) {
	ctx := context.Background()
	c := caller{Token: store.Token{OrgID: orgA, Kind: store.TokenKindOAuth}, Role: store.RoleAdmin}

	s := newServer(newFake())
	g, err := s.grantOf(ctx, c)
	if err != nil || g.mcp != store.DefaultMCPSettings() || g.role != store.RoleAdmin {
		t.Fatalf("no settings: grant = %+v, err = %v; want the defaults", g, err)
	}

	cfg := store.DefaultServerSettings()
	cfg.MCP.AIChanges = store.AIChangesOff
	s.settings = &fakeSettings{cfg: cfg}
	if g, err = s.grantOf(ctx, c); err != nil || g.mcp.AIChanges != store.AIChangesOff {
		t.Fatalf("grant = %+v, err = %v; want changes off", g, err)
	}

	boom := errors.New("boom")
	s.settings = &fakeSettings{err: boom}
	if _, err = s.grantOf(ctx, c); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store error", err)
	}
}

func TestToolErrorKeepsRightError(t *testing.T) {
	err := &rightError{ErrRole, "text"}
	got := newServer(newFake()).toolError("ack_alert", err)
	if got != error(err) || !errors.Is(got, ErrRole) {
		t.Fatalf("toolError = %v, want the right error", got)
	}
}
