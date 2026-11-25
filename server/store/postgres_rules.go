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
	hosts := make([]uuid.UUID, 0, len(ch.Hosts))
	for id := range ch.Hosts {
		hosts = append(hosts, id)
	}
	var out RulesBackup
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		settings, nodes, err := loadRuleTargets(ctx, tx, orgID, ch.Org != nil, hosts)
		if err != nil {
			return err
		}
		out = ch.apply(settings, nodes)
		return saveRuleTargets(ctx, tx, orgID, settings, nodes, out)
	})
	return out, err
}

func (s *PostgresStore) EditRules(ctx context.Context, orgID uuid.UUID, ed RulesEdit) (RulesBackup, error) {
	var out RulesBackup
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		settings, nodes, err := loadRuleTargets(ctx, tx, orgID, ed.Org, ed.Hosts)
		if err != nil {
			return err
		}
		if out, err = ed.run(settings, nodes); err != nil {
			return err
		}
		return saveRuleTargets(ctx, tx, orgID, settings, nodes, out)
	})
	if err != nil {
		return RulesBackup{}, err
	}
	return out, nil
}

// loadRuleTargets locks and reads the settings (when org is set) and the
// nodes. A missing node gives ErrNotFound.
func loadRuleTargets(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, org bool, hosts []uuid.UUID) (*ServerSettings, map[uuid.UUID]*Node, error) {
	var settings *ServerSettings
	if org {
		def, err := json.Marshal(DefaultServerSettings())
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO settings (org_id, data) VALUES ($1, $2) ON CONFLICT (org_id) DO NOTHING`, orgID, def); err != nil {
			return nil, nil, err
		}
		var data []byte
		if err := tx.QueryRow(ctx, `SELECT data FROM settings WHERE org_id = $1 FOR UPDATE`, orgID).Scan(&data); err != nil {
			return nil, nil, err
		}
		settings = &ServerSettings{}
		if err := json.Unmarshal(data, settings); err != nil {
			return nil, nil, err
		}
	}
	// A fixed lock order avoids a deadlock between two edits.
	ids := slices.Clone(hosts)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	ids = slices.Compact(ids)
	nodes := make(map[uuid.UUID]*Node, len(ids))
	for _, id := range ids {
		n, err := scanNode(tx.QueryRow(ctx,
			`SELECT data, created_at FROM nodes WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
		if err != nil {
			return nil, nil, notFound(err)
		}
		nodes[id] = &n
	}
	return settings, nodes, nil
}

// saveRuleTargets writes the targets. A backup with an id is written too,
// and the backups older than the TTL are deleted.
func saveRuleTargets(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, settings *ServerSettings, nodes map[uuid.UUID]*Node, out RulesBackup) error {
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
	if out.ID == uuid.Nil {
		return nil
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
