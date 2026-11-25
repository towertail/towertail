package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// Migrate runs the goose SQL files at the root of fsys, with table as the
// version table. The cloud uses it for its own tables.
func (s *PostgresStore) Migrate(ctx context.Context, fsys fs.FS, table string) error {
	return postgres.MigrateFS(ctx, s.pool, fsys, table)
}

func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

func (s *PostgresStore) ListNodes(ctx context.Context, orgID uuid.UUID) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT data, created_at FROM nodes WHERE org_id = $1 ORDER BY id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetNode(ctx context.Context, orgID, id uuid.UUID) (Node, error) {
	row := s.pool.QueryRow(ctx, `SELECT data, created_at FROM nodes WHERE org_id = $1 AND id = $2`, orgID, id)
	n, err := scanNode(row)
	return n, notFound(err)
}

// FindNodeByMachineID gives the oldest node of the org with the machine id.
func (s *PostgresStore) FindNodeByMachineID(ctx context.Context, orgID uuid.UUID, machineID string) (Node, error) {
	if machineID == "" {
		return Node{}, ErrNotFound
	}
	row := s.pool.QueryRow(ctx, `
		SELECT data, created_at FROM nodes WHERE org_id = $1 AND machine_id = $2
		ORDER BY created_at, id LIMIT 1`, orgID, machineID)
	n, err := scanNode(row)
	return n, notFound(err)
}

// scanNode reads a node. The column created_at wins over the copy in data.
func scanNode(row pgx.Row) (Node, error) {
	var (
		data []byte
		at   time.Time
		n    Node
	)
	if err := row.Scan(&data, &at); err != nil {
		return Node{}, err
	}
	if err := json.Unmarshal(data, &n); err != nil {
		return Node{}, err
	}
	n.CreatedAt = at.UTC()
	return n, nil
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
		INSERT INTO nodes (org_id, id, data, machine_id) VALUES ($1, $2, $3, NULLIF($4, ''))
		ON CONFLICT (org_id, id) DO UPDATE SET
			data = EXCLUDED.data, machine_id = EXCLUDED.machine_id, updated_at = now()`,
		orgID, node.ID, data, node.MachineID)
	return err
}

func (s *PostgresStore) EditNode(ctx context.Context, orgID, id uuid.UUID, f func(*Node) error) (Node, error) {
	var out Node
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := scanNode(tx.QueryRow(ctx,
			`SELECT data, created_at FROM nodes WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
		if err != nil {
			return notFound(err)
		}
		if err := f(&n); err != nil {
			return err
		}
		n.ID = id
		data, err := json.Marshal(n)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE nodes SET data = $3, machine_id = NULLIF($4, ''), updated_at = now()
			WHERE org_id = $1 AND id = $2`, orgID, id, data, n.MachineID)
		out = n
		return err
	})
	return out, err
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

func (s *PostgresStore) EditSettings(ctx context.Context, orgID uuid.UUID, f func(*ServerSettings) error) (ServerSettings, error) {
	var out ServerSettings
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		def, err := json.Marshal(DefaultServerSettings())
		if err != nil {
			return err
		}
		// A missing row is made first, so the next read can lock it.
		if _, err := tx.Exec(ctx, `INSERT INTO settings (org_id, data) VALUES ($1, $2) ON CONFLICT (org_id) DO NOTHING`, orgID, def); err != nil {
			return err
		}
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT data FROM settings WHERE org_id = $1 FOR UPDATE`, orgID).Scan(&data); err != nil {
			return err
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return err
		}
		if err := f(&out); err != nil {
			return err
		}
		data, err = json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE settings SET data = $2, updated_at = now() WHERE org_id = $1`, orgID, data)
		return err
	})
	return out, err
}

const tokenCols = `id, org_id, kind, label, token_hash, node_id, user_id, created_at, last_used_at, revoked_at, expires_at`

func (s *PostgresStore) PutToken(ctx context.Context, t Token) error {
	if len(t.Hash) == 0 {
		return errors.New("store: token hash required")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO api_tokens (`+tokenCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO UPDATE SET
			label = EXCLUDED.label, last_used_at = EXCLUDED.last_used_at, revoked_at = EXCLUDED.revoked_at`,
		t.ID, t.OrgID, string(t.Kind), t.Label, t.Hash, t.NodeID, t.UserID, t.CreatedAt, t.LastUsedAt, t.RevokedAt, t.ExpiresAt)
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

func (s *PostgresStore) TouchToken(ctx context.Context, hash []byte, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = $2 WHERE token_hash = $1`, hash, at)
	return err
}

func scanToken(row pgx.Row) (Token, error) {
	var (
		t     Token
		kind  string
		label *string
	)
	err := row.Scan(&t.ID, &t.OrgID, &kind, &label, &t.Hash, &t.NodeID, &t.UserID, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt, &t.ExpiresAt)
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
