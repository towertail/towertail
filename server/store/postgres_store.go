package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/postgres"
)

// PostgresStore is the ConfigStore of the cloud-managed mode. It holds
// many orgs. A token, a user, a node or the settings can only belong to
// an org that is in the orgs table.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// OpenPostgres opens the pool and runs the core migrations. Close closes
// the pool.
func OpenPostgres(ctx context.Context, cfg config.PostgresConfig) (*PostgresStore, error) {
	pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}

// Pool gives the pool to stores that keep their own tables in the same
// database.
func (s *PostgresStore) Pool() *pgxpool.Pool { return s.pool }

func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

func (s *PostgresStore) ListNodes(ctx context.Context, orgID uuid.UUID) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM nodes WHERE org_id = $1 ORDER BY id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var n Node
		if err := json.Unmarshal(data, &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetNode(ctx context.Context, orgID, id uuid.UUID) (Node, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM nodes WHERE org_id = $1 AND id = $2`, orgID, id).Scan(&data)
	if err != nil {
		return Node{}, notFound(err)
	}
	var n Node
	err = json.Unmarshal(data, &n)
	return n, err
}

func (s *PostgresStore) PutNode(ctx context.Context, orgID uuid.UUID, node Node) error {
	if node.ID == uuid.Nil {
		return errors.New("store: node id required")
	}
	data, err := json.Marshal(node)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO nodes (org_id, id, data) VALUES ($1, $2, $3)
		ON CONFLICT (org_id, id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		orgID, node.ID, data)
	return err
}

func (s *PostgresStore) DeleteNode(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM nodes WHERE org_id = $1 AND id = $2`, orgID, id)
	return err
}

func (s *PostgresStore) GetSettings(ctx context.Context, orgID uuid.UUID) (ServerSettings, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM settings WHERE org_id = $1`, orgID).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultServerSettings(), nil
	}
	if err != nil {
		return ServerSettings{}, err
	}
	var out ServerSettings
	err = json.Unmarshal(data, &out)
	return out, err
}

func (s *PostgresStore) PutSettings(ctx context.Context, orgID uuid.UUID, settings ServerSettings) error {
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO settings (org_id, data) VALUES ($1, $2)
		ON CONFLICT (org_id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		orgID, data)
	return err
}

const tokenCols = `id, org_id, kind, label, token_hash, node_id, created_at, last_used_at, revoked_at`

func (s *PostgresStore) PutToken(ctx context.Context, t Token) error {
	if len(t.Hash) == 0 {
		return errors.New("store: token hash required")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO api_tokens (`+tokenCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			label = EXCLUDED.label, last_used_at = EXCLUDED.last_used_at, revoked_at = EXCLUDED.revoked_at`,
		t.ID, t.OrgID, string(t.Kind), t.Label, t.Hash, t.NodeID, t.CreatedAt, t.LastUsedAt, t.RevokedAt)
	return err
}

func (s *PostgresStore) GetTokenByHash(ctx context.Context, hash []byte) (Token, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE token_hash = $1`, hash)
	t, err := scanToken(row)
	return t, notFound(err)
}

func (s *PostgresStore) DeleteToken(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListTokens(ctx context.Context, orgID uuid.UUID) ([]Token, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE org_id = $1 ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanToken(row pgx.Row) (Token, error) {
	var (
		t     Token
		kind  string
		label *string
	)
	err := row.Scan(&t.ID, &t.OrgID, &kind, &label, &t.Hash, &t.NodeID, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt)
	t.Kind = TokenKind(kind)
	if label != nil {
		t.Label = *label
	}
	return t, err
}

// PutUser inserts the user, or updates the user with the same email.
func (s *PostgresStore) PutUser(ctx context.Context, u User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	var hash *string
	if u.PasswordHash != "" {
		hash = &u.PasswordHash
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (id, org_id, email, password_hash, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (email) DO UPDATE SET org_id = EXCLUDED.org_id, password_hash = EXCLUDED.password_hash`,
		u.ID, u.OrgID, u.Email, hash, u.CreatedAt)
	return err
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	var (
		u    User
		hash *string
	)
	err := s.pool.QueryRow(ctx, `SELECT id, org_id, email, password_hash, created_at FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.OrgID, &u.Email, &hash, &u.CreatedAt)
	if hash != nil {
		u.PasswordHash = *hash
	}
	return u, notFound(err)
}

func (s *PostgresStore) ListChannels(ctx context.Context, orgID uuid.UUID) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM channels WHERE org_id = $1 ORDER BY id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var ch Channel
		if err := json.Unmarshal(data, &ch); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetChannel(ctx context.Context, orgID, id uuid.UUID) (Channel, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM channels WHERE org_id = $1 AND id = $2`, orgID, id).Scan(&data)
	if err != nil {
		return Channel{}, notFound(err)
	}
	var ch Channel
	err = json.Unmarshal(data, &ch)
	return ch, err
}

func (s *PostgresStore) PutChannel(ctx context.Context, ch Channel) error {
	if ch.ID == uuid.Nil {
		return errors.New("store: channel id required")
	}
	data, err := json.Marshal(ch)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO channels (org_id, id, data) VALUES ($1, $2, $3)
		ON CONFLICT (org_id, id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		ch.OrgID, ch.ID, data)
	return err
}

func (s *PostgresStore) DeleteChannel(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM channels WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) EnqueueControl(ctx context.Context, msg ControlMessage) error {
	if msg.ID == uuid.Nil {
		msg.ID = uuid.New()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO control_messages (id, node_id, kind, payload, created_at) VALUES ($1, $2, $3, $4, $5)`,
		msg.ID, msg.NodeID, msg.Kind, msg.Payload, msg.CreatedAt)
	return err
}

// ClaimControl removes the oldest message of the node and returns it.
// Two callers never get the same message.
func (s *PostgresStore) ClaimControl(ctx context.Context, nodeID uuid.UUID) (ControlMessage, error) {
	var m ControlMessage
	err := s.pool.QueryRow(ctx, `
		DELETE FROM control_messages WHERE id = (
			SELECT id FROM control_messages WHERE node_id = $1
			ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED
		) RETURNING id, node_id, kind, payload, created_at`, nodeID).
		Scan(&m.ID, &m.NodeID, &m.Kind, &m.Payload, &m.CreatedAt)
	return m, notFound(err)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

var _ ConfigStore = (*PostgresStore)(nil)
