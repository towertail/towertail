package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/google/uuid"
)

// NewToken makes a token with a new secret. The caller keeps the token
// with PutToken and gives raw to the client once.
func NewToken(orgID uuid.UUID, kind TokenKind, label string) (raw string, t Token, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", Token{}, err
	}
	raw = "tt_" + base64.RawURLEncoding.EncodeToString(b)
	return raw, Token{
		ID:        uuid.New(),
		OrgID:     orgID,
		Kind:      kind,
		Label:     label,
		Hash:      HashToken(raw),
		CreatedAt: time.Now().UTC(),
	}, nil
}

// HashToken is the SHA-256 of a raw token. The store keeps only this.
func HashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
