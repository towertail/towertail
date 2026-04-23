package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the cloud-managed ConfigStore. Scaffolded behind the
// ConfigStore interface so the same handlers work in either mode; every
// method returns ErrNotImplemented until Phase K fills them in.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// OpenPostgres dials the given DSN. The pool is lazy, so callers can
// inject it into a runtime.Component that only starts under
// cloud_managed=true.
func OpenPostgres(ctx context.Context, dsn string, maxConns int) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Close() error {
	if s.pool != nil {
		s.pool.Close()
	}
	return nil
}

func (s *PostgresStore) ListNodes(context.Context, uuid.UUID) ([]Node, error) {
	return nil, ErrNotImplemented
}
func (s *PostgresStore) GetNode(context.Context, uuid.UUID, uuid.UUID) (Node, error) {
	return Node{}, ErrNotImplemented
}
func (s *PostgresStore) PutNode(context.Context, uuid.UUID, Node) error { return ErrNotImplemented }
func (s *PostgresStore) DeleteNode(context.Context, uuid.UUID, uuid.UUID) error {
	return ErrNotImplemented
}
func (s *PostgresStore) GetSettings(context.Context, uuid.UUID) (ServerSettings, error) {
	return ServerSettings{}, ErrNotImplemented
}
func (s *PostgresStore) PutSettings(context.Context, uuid.UUID, ServerSettings) error {
	return ErrNotImplemented
}
func (s *PostgresStore) PutToken(context.Context, Token) error { return ErrNotImplemented }
func (s *PostgresStore) GetTokenByHash(context.Context, []byte) (Token, error) {
	return Token{}, ErrNotImplemented
}
func (s *PostgresStore) DeleteToken(context.Context, uuid.UUID) error       { return ErrNotImplemented }
func (s *PostgresStore) ListTokens(context.Context, uuid.UUID) ([]Token, error) {
	return nil, ErrNotImplemented
}
func (s *PostgresStore) PutUser(context.Context, User) error { return ErrNotImplemented }
func (s *PostgresStore) GetUserByEmail(context.Context, string) (User, error) {
	return User{}, ErrNotImplemented
}
func (s *PostgresStore) EnqueueControl(context.Context, ControlMessage) error {
	return ErrNotImplemented
}
func (s *PostgresStore) ClaimControl(context.Context, uuid.UUID) (ControlMessage, error) {
	return ControlMessage{}, ErrNotImplemented
}
