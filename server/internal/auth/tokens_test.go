package auth

import (
	"context"
	"strings"
	"sync/atomic"
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
	raw, tok, err := issuer.Issue(context.Background(), store.ZeroOrg, store.TokenKindUser, "dev", nil, nil)
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

func TestIssueWithUser(t *testing.T) {
	s, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	user := uuid.New()
	raw, tok, err := NewIssuer(s).Issue(ctx, store.ZeroOrg, store.TokenKindAPI, "ci", nil, &user)
	if err != nil {
		t.Fatal(err)
	}
	if tok.UserID == nil || *tok.UserID != user || tok.Kind != store.TokenKindAPI {
		t.Fatalf("issued = %+v", tok)
	}
	got, err := s.GetTokenByHash(ctx, store.HashToken(raw))
	if err != nil || got.UserID == nil || *got.UserID != user {
		t.Fatalf("stored = %+v, %v", got, err)
	}
}

// touchStore counts the TouchToken calls.
type touchStore struct {
	*store.BoltStore
	touches atomic.Int32
}

func (s *touchStore) TouchToken(ctx context.Context, hash []byte, at time.Time) error {
	s.touches.Add(1)
	return s.BoltStore.TouchToken(ctx, hash, at)
}

func TestVerifyTouch(t *testing.T) {
	bolt, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bolt.Close()
	s := &touchStore{BoltStore: bolt}
	ctx := context.Background()
	issuer := NewIssuer(s)
	now := time.Now().UTC().Truncate(time.Millisecond)
	issuer.now = func() time.Time { return now }
	raw, tok, err := issuer.Issue(ctx, store.ZeroOrg, store.TokenKindAPI, "k", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// settle waits for the background touch and checks the count.
	settle := func(want int32) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for s.touches.Load() < want && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond)
		if got := s.touches.Load(); got != want {
			t.Fatalf("touches = %d, want %d", got, want)
		}
	}
	verify := func() {
		t.Helper()
		if _, err := issuer.Verify(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}

	verify()
	settle(1)
	got, _ := bolt.GetTokenByHash(ctx, tok.Hash)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, now)
	}
	now = now.Add(time.Minute)
	verify()
	settle(1)
	now = now.Add(5 * time.Minute)
	verify()
	settle(2)
}

func TestVerifyTouchSkipsFresh(t *testing.T) {
	bolt, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bolt.Close()
	s := &touchStore{BoltStore: bolt}
	ctx := context.Background()
	issuer := NewIssuer(s)
	raw, tok, err := issuer.Issue(ctx, store.ZeroOrg, store.TokenKindAPI, "k", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-time.Minute)
	tok.LastUsedAt = &recent
	if err := bolt.PutToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Verify(ctx, raw); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := s.touches.Load(); got != 0 {
		t.Fatalf("touches = %d, want 0 for a token used 1 minute ago", got)
	}
}
