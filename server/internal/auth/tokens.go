package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

// TokenIssuer issues opaque bearer tokens and persists a SHA-256 of
// each. The raw token is only ever returned to the caller at issue time.
type TokenIssuer struct {
	store store.ConfigStore
	now   func() time.Time
	log   *slog.Logger
	// touched maps a token ID to the time of its last touch.
	touched sync.Map
}

// Touch limits for LastUsedAt.
const (
	touchEvery   = 5 * time.Minute
	touchTimeout = 5 * time.Second
)

func NewIssuer(s store.ConfigStore) *TokenIssuer {
	return &TokenIssuer{store: s, now: time.Now, log: slog.Default()}
}

// Issue creates a new token. The returned string is the raw secret the
// caller hands to the client; the store retains only the hash.
func (i *TokenIssuer) Issue(ctx context.Context, orgID uuid.UUID, kind store.TokenKind, label string, nodeID, userID *uuid.UUID) (string, store.Token, error) {
	raw, t, err := store.NewToken(orgID, kind, label)
	if err != nil {
		return "", store.Token{}, err
	}
	t.NodeID = nodeID
	t.UserID = userID
	if err := i.store.PutToken(ctx, t); err != nil {
		return "", store.Token{}, err
	}
	return raw, t, nil
}

// IssueJoin creates a one-use join token that expires after ttl.
func (i *TokenIssuer) IssueJoin(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, label string, ttl time.Duration) (string, store.Token, error) {
	raw, t, err := store.NewToken(orgID, store.TokenKindJoin, label)
	if err != nil {
		return "", store.Token{}, err
	}
	exp := time.Now().UTC().Add(ttl)
	t.UserID = userID
	t.ExpiresAt = &exp
	if err := i.store.PutToken(ctx, t); err != nil {
		return "", store.Token{}, err
	}
	return raw, t, nil
}

// Verify looks up a token by the hash of the supplied secret. A match
// requires the token to be unrevoked and not expired.
func (i *TokenIssuer) Verify(ctx context.Context, raw string) (store.Token, error) {
	if raw == "" {
		return store.Token{}, errors.New("auth: empty token")
	}
	expected := store.HashToken(raw)
	t, err := i.store.GetTokenByHash(ctx, expected)
	if err != nil {
		return store.Token{}, err
	}
	if t.RevokedAt != nil {
		return store.Token{}, errors.New("auth: revoked")
	}
	if t.ExpiresAt != nil && !time.Now().Before(*t.ExpiresAt) {
		return store.Token{}, errors.New("auth: expired")
	}
	// Constant-time compare is already implicit in the bucket lookup
	// (hash is the key), but keep a subtle.ConstantTimeCompare here
	// so a future index change can't accidentally leak a timing
	// channel on the comparison step.
	if subtle.ConstantTimeCompare(t.Hash, expected) != 1 {
		return store.Token{}, errors.New("auth: hash mismatch")
	}
	i.touch(t)
	return t, nil
}

// touch sets LastUsedAt in the background, at most one time in each
// touchEvery for each token.
func (i *TokenIssuer) touch(t store.Token) {
	now := i.now()
	last := time.Time{}
	if t.LastUsedAt != nil {
		last = *t.LastUsedAt
	}
	if v, ok := i.touched.Load(t.ID); ok {
		last = v.(time.Time)
	}
	if now.Sub(last) < touchEvery {
		return
	}
	i.touched.Store(t.ID, now)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), touchTimeout)
		defer cancel()
		if err := i.store.TouchToken(ctx, t.Hash, now); err != nil {
			i.log.Warn("auth: touch token", "id", t.ID, "err", err)
		}
	}()
}

// Revoke deletes the token by id.
func (i *TokenIssuer) Revoke(ctx context.Context, id uuid.UUID) error {
	return i.store.DeleteToken(ctx, id)
}
