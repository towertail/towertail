package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a lookup misses.
var ErrNotFound = errors.New("store: not found")

// ErrNoMembership is returned when a user has no role in an org.
var ErrNoMembership = errors.New("store: no membership")

// ErrNotImplemented is returned by a store for an operation that it does
// not support. The API maps it to 501.
var ErrNotImplemented = errors.New("store: not implemented")

// ConfigStore is the persistence seam. BoltStore is the self-hosted
// store, and PostgresStore is the cloud-managed store. Methods take a
// context so long-running network calls can be cancelled.
type ConfigStore interface {
	// Lifecycle
	Close() error

	// Nodes
	ListNodes(ctx context.Context, orgID uuid.UUID) ([]Node, error)
	GetNode(ctx context.Context, orgID, id uuid.UUID) (Node, error)
	PutNode(ctx context.Context, orgID uuid.UUID, node Node) error
	DeleteNode(ctx context.Context, orgID, id uuid.UUID) error
	// EditNode reads, changes and writes one node in one transaction, so
	// two edits do not lose each other's change. It returns ErrNotFound
	// for a missing node, and the error of f without a write.
	EditNode(ctx context.Context, orgID, id uuid.UUID, f func(*Node) error) (Node, error)
	// FindNodeByMachineID returns ErrNotFound when no node of the org has
	// the machine id.
	FindNodeByMachineID(ctx context.Context, orgID uuid.UUID, machineID string) (Node, error)

	// Settings
	GetSettings(ctx context.Context, orgID uuid.UUID) (ServerSettings, error)
	PutSettings(ctx context.Context, orgID uuid.UUID, settings ServerSettings) error
	// EditSettings is EditNode for the settings of the org. An org with no
	// stored settings starts from the defaults.
	EditSettings(ctx context.Context, orgID uuid.UUID, f func(*ServerSettings) error) (ServerSettings, error)

	// ReplaceRules replaces the rules of the org (when ch.Org is set) and of
	// the named hosts in one transaction. It keeps the old values in a
	// backup and deletes the backups older than RulesBackupTTL. It returns
	// ErrNotFound for a host that is not in the org (nothing is written),
	// and ErrInvalidRules for a bad rule.
	ReplaceRules(ctx context.Context, orgID uuid.UUID, ch RulesChange) (RulesBackup, error)
	// EditRules runs ed.Apply on the org settings (when ed.Org is set) and on
	// the named hosts in one transaction. It validates the new rules, sets
	// ed.Origin on each target whose rules changed, and writes the targets.
	// It keeps the old rules of the changed targets in one backup and deletes
	// the backups older than RulesBackupTTL. When no rule changed, it writes
	// the other fields and gives a zero backup (ID is uuid.Nil). It returns
	// ErrNotFound for a host that is not in the org, ErrInvalidRules for a bad
	// rule, and the error of Apply. These errors write nothing.
	EditRules(ctx context.Context, orgID uuid.UUID, ed RulesEdit) (RulesBackup, error)
	// ListRulesBackups gives the backups younger than the TTL, newest first.
	ListRulesBackups(ctx context.Context, orgID uuid.UUID) ([]RulesBackup, error)
	// GetRulesBackup returns ErrNotFound for an unknown or old backup.
	GetRulesBackup(ctx context.Context, orgID, id uuid.UUID) (RulesBackup, error)

	// Tokens
	PutToken(ctx context.Context, t Token) error
	GetTokenByHash(ctx context.Context, hash []byte) (Token, error)
	DeleteToken(ctx context.Context, id uuid.UUID) error
	ListTokens(ctx context.Context, orgID uuid.UUID) ([]Token, error)
	// TouchToken sets LastUsedAt of the token with the hash. An unknown
	// hash is not an error.
	TouchToken(ctx context.Context, hash []byte, at time.Time) error

	// Users. PutUser inserts or updates by email.
	PutUser(ctx context.Context, u User) error
	GetUserByEmail(ctx context.Context, email string) (User, error)

	// Alert channels. DeleteChannel returns ErrNotFound for a missing
	// channel.
	ListChannels(ctx context.Context, orgID uuid.UUID) ([]Channel, error)
	GetChannel(ctx context.Context, orgID, id uuid.UUID) (Channel, error)
	PutChannel(ctx context.Context, ch Channel) error
	DeleteChannel(ctx context.Context, orgID, id uuid.UUID) error

	// Control queue
	EnqueueControl(ctx context.Context, msg ControlMessage) error
	ClaimControl(ctx context.Context, nodeID uuid.UUID) (ControlMessage, error)
}
