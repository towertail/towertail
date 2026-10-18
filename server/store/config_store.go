package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("store: not found")

// ErrNotImplemented is returned by the Postgres stub for operations
// that the self-hosted path does not exercise.
var ErrNotImplemented = errors.New("store: not implemented")

// ConfigStore is the persistence seam that BoltStore implements for
// self-hosted deployments and PostgresStore stubs for future
// cloud-managed mode. Methods take a context so long-running network
// calls can be cancelled.
type ConfigStore interface {
	// Lifecycle
	Close() error

	// Nodes
	ListNodes(ctx context.Context, orgID uuid.UUID) ([]Node, error)
	GetNode(ctx context.Context, orgID, id uuid.UUID) (Node, error)
	PutNode(ctx context.Context, orgID uuid.UUID, node Node) error
	DeleteNode(ctx context.Context, orgID, id uuid.UUID) error

	// Settings
	GetSettings(ctx context.Context, orgID uuid.UUID) (ServerSettings, error)
	PutSettings(ctx context.Context, orgID uuid.UUID, settings ServerSettings) error

	// Tokens
	PutToken(ctx context.Context, t Token) error
	GetTokenByHash(ctx context.Context, hash []byte) (Token, error)
	DeleteToken(ctx context.Context, id uuid.UUID) error
	ListTokens(ctx context.Context, orgID uuid.UUID) ([]Token, error)

	// Users (stub surface; self-hosted keeps a single synthetic user)
	PutUser(ctx context.Context, u User) error
	GetUserByEmail(ctx context.Context, email string) (User, error)

	// Control queue
	EnqueueControl(ctx context.Context, msg ControlMessage) error
	ClaimControl(ctx context.Context, nodeID uuid.UUID) (ControlMessage, error)
}
