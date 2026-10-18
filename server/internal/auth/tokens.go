package auth

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
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
	raw, t, err := store.NewToken(orgID, kind, label)
	if err != nil {
		return "", store.Token{}, err
	}
	t.NodeID = nodeID
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
	expected := store.HashToken(raw)
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
