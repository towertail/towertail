package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

func TestIssuerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenBolt(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	issuer := NewIssuer(s)
	raw, tok, err := issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindUser, "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || tok.ID == uuid.Nil {
		t.Fatal("expected non-empty raw + id")
	}
	got, err := issuer.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.ID != tok.ID {
		t.Errorf("mismatch %v vs %v", got.ID, tok.ID)
	}
	// Unknown token rejected.
	if _, err := issuer.Verify(context.Background(), "tt_not_a_real_token"); err == nil {
		t.Error("expected verify failure")
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("correct horse battery staple", h)
	if err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v", ok, err)
	}
	ok, _ = VerifyPassword("wrong", h)
	if ok {
		t.Error("expected false for wrong password")
	}
}

func TestVerify_Expired(t *testing.T) {
	s, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	issuer := NewIssuer(s)

	raw, _, err := issuer.IssueJoin(ctx, store.ZeroOrg, nil, "j", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Verify(ctx, raw); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
	raw, _, err = issuer.IssueJoin(ctx, store.ZeroOrg, nil, "j", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Verify(ctx, raw); err == nil {
		t.Fatal("expired token verifies")
	}
}

func TestNewToken_JoinPrefix(t *testing.T) {
	raw, tok, err := store.NewToken(store.ZeroOrg, store.TokenKindJoin, "j")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "tt_join_") || tok.Kind != store.TokenKindJoin {
		t.Errorf("raw %q, kind %q", raw, tok.Kind)
	}
	raw, _, _ = store.NewToken(store.ZeroOrg, store.TokenKindSampler, "s")
	if strings.HasPrefix(raw, "tt_join_") {
		t.Errorf("sampler token %q has the join prefix", raw)
	}
}
