package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/server/internal/store"
)

// TokenIssuer issues opaque bearer tokens and persists a SHA-256 of
// each. The raw token is only ever returned to the caller at issue time.
type TokenIssuer struct {
	store store.ConfigStore
}

func NewIssuer(s store.ConfigStore) *TokenIssuer {
	return &TokenIssuer{store: s}
}

// Issue creates a new token. The returned string is the raw secret the
// caller hands to the client; the store retains only the hash.
func (i *TokenIssuer) Issue(ctx context.Context, orgID uuid.UUID, kind store.TokenKind, label string, nodeID *uuid.UUID) (string, store.Token, error) {
	raw, err := generateSecret(32)
	if err != nil {
		return "", store.Token{}, err
	}
	hash := hashToken(raw)
	t := store.Token{
		ID:        uuid.New(),
		OrgID:     orgID,
		Kind:      kind,
		Label:     label,
		Hash:      hash,
		NodeID:    nodeID,
		CreatedAt: time.Now().UTC(),
	}
	if err := i.store.PutToken(ctx, t); err != nil {
		return "", store.Token{}, err
	}
	return raw, t, nil
}

// Verify looks up a token by the hash of the supplied secret. A match
// requires the token to be unrevoked.
func (i *TokenIssuer) Verify(ctx context.Context, raw string) (store.Token, error) {
	if raw == "" {
		return store.Token{}, errors.New("auth: empty token")
	}
	expected := hashToken(raw)
	t, err := i.store.GetTokenByHash(ctx, expected)
	if err != nil {
		return store.Token{}, err
	}
	if t.RevokedAt != nil {
		return store.Token{}, errors.New("auth: revoked")
	}
	// Constant-time compare is already implicit in the bucket lookup
	// (hash is the key), but keep a subtle.ConstantTimeCompare here
	// so a future index change can't accidentally leak a timing
	// channel on the comparison step.
	if subtle.ConstantTimeCompare(t.Hash, expected) != 1 {
		return store.Token{}, errors.New("auth: hash mismatch")
	}
	return t, nil
}

// Revoke deletes the token by id.
func (i *TokenIssuer) Revoke(ctx context.Context, id uuid.UUID) error {
	return i.store.DeleteToken(ctx, id)
}

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// HashForSeed is exposed for the bootstrap-token install path where the
// caller supplies a known raw secret rather than generating one.
func HashForSeed(raw string) []byte {
	return hashToken(raw)
}

func generateSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tt_" + base64.RawURLEncoding.EncodeToString(b), nil
}
