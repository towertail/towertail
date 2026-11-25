package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
)

type fakeOrgInfo struct {
	facts OrgFacts
	got   uuid.UUID
}

func (f *fakeOrgInfo) OrgFacts(_ context.Context, org uuid.UUID) (OrgFacts, error) {
	f.got = org
	return f.facts, nil
}

func TestWhoami(t *testing.T) {
	f := newFake()
	f.days = 14
	cs := connect(t, f, tokenA())
	out := decode[WhoamiOut](t, call(t, cs, "whoami", nil))
	if out.Role != "viewer" || out.TokenKind != "api" || out.Plan != "self-hosted" || out.HistoryDays != 14 || out.OrgID != orgA.String() {
		t.Fatalf("out = %+v", out)
	}
	if out.ServerVersion != "test" || out.OrgName != "" || out.MaxHosts != 0 || out.UserID != "" {
		t.Fatalf("out = %+v, want no org name, no max hosts and no user", out)
	}
}

func TestWhoamiOrgInfo(t *testing.T) {
	user := uuid.New()
	info := &fakeOrgInfo{facts: OrgFacts{Name: "Acme", Plan: "team", HistoryDays: 30, MaxHosts: 25}}
	tok := &store.Token{ID: uuid.New(), OrgID: orgA, Kind: store.TokenKindOAuth, UserID: &user}
	cs := connectOpts(t, newFake(), tok, func(o *Options) { o.OrgInfo = info })
	out := decode[WhoamiOut](t, call(t, cs, "whoami", nil))
	if out.OrgName != "Acme" || out.Plan != "team" || out.HistoryDays != 30 || out.MaxHosts != 25 || out.TokenKind != "oauth" || out.UserID != user.String() {
		t.Fatalf("out = %+v", out)
	}
	if info.got != orgA {
		t.Fatalf("OrgFacts org = %s, want %s", info.got, orgA)
	}
}

func TestCallerRole(t *testing.T) {
	ctx := context.Background()
	if _, err := callerOf(ctx); err == nil {
		t.Fatal("callerOf with no token gives no error")
	}
	tok := store.Token{ID: uuid.New(), OrgID: orgA}
	c, err := callerOf(authctx.WithToken(ctx, tok))
	if err != nil || c.Role != store.RoleViewer || c.Token.OrgID != orgA {
		t.Fatalf("no role: caller = %+v, err = %v; want viewer", c, err)
	}
	c, err = callerOf(authctx.WithRole(authctx.WithToken(ctx, tok), store.RoleAdmin))
	if err != nil || c.Role != store.RoleAdmin {
		t.Fatalf("admin: caller = %+v, err = %v", c, err)
	}
}
