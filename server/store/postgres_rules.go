package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) ReplaceRules(ctx context.Context, orgID uuid.UUID, ch RulesChange) (RulesBackup, error) {
	if err := ch.Validate(); err != nil {
		return RulesBackup{}, err
	}
	var out RulesBackup
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var settings *ServerSettings
		if ch.Org != nil {
			def, err := json.Marshal(DefaultServerSettings())
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO settings (org_id, data) VALUES ($1, $2) ON CONFLICT (org_id) DO NOTHING`, orgID, def); err != nil {
				return err
			}
			var data []byte
			if err := tx.QueryRow(ctx, `SELECT data FROM settings WHERE org_id = $1 FOR UPDATE`, orgID).Scan(&data); err != nil {
				return err
			}
			settings = &ServerSettings{}
			if err := json.Unmarshal(data, settings); err != nil {
				return err
			}
		}
		// A fixed lock order avoids a deadlock between two replaces.
		ids := make([]uuid.UUID, 0, len(ch.Hosts))
		for id := range ch.Hosts {
			ids = append(ids, id)
		}
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
		nodes := make(map[uuid.UUID]*Node, len(ids))
		for _, id := range ids {
			n, err := scanNode(tx.QueryRow(ctx,
				`SELECT data, created_at FROM nodes WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
			if err != nil {
				return notFound(err)
			}
			nodes[id] = &n
		}
		out = ch.apply(settings, nodes)
		if settings != nil {
			data, err := json.Marshal(settings)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE settings SET data = $2, updated_at = now() WHERE org_id = $1`, orgID, data); err != nil {
				return err
			}
		}
		for id, n := range nodes {
			data, err := json.Marshal(n)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE nodes SET data = $3, updated_at = now() WHERE org_id = $1 AND id = $2`, orgID, id, data); err != nil {
				return err
			}
		}
		data, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO rules_backups (id, org_id, data, created_at) VALUES ($1, $2, $3, $4)`,
			out.ID, orgID, data, out.At); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM rules_backups WHERE org_id = $1 AND created_at < $2`, orgID, out.At.Add(-RulesBackupTTL))
		return err
	})
	return out, err
}

func (s *PostgresStore) ListRulesBackups(ctx context.Context, orgID uuid.UUID) ([]RulesBackup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT data FROM rules_backups WHERE org_id = $1 AND created_at > $2
		ORDER BY created_at DESC, id`, orgID, time.Now().Add(-RulesBackupTTL))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RulesBackup
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var b RulesBackup
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, fmt.Errorf("store: rules backup: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetRulesBackup(ctx context.Context, orgID, id uuid.UUID) (RulesBackup, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `
		SELECT data FROM rules_backups WHERE org_id = $1 AND id = $2 AND created_at > $3`,
		orgID, id, time.Now().Add(-RulesBackupTTL)).Scan(&data)
	if err != nil {
		return RulesBackup{}, notFound(err)
	}
	var b RulesBackup
	err = json.Unmarshal(data, &b)
	return b, err
}
