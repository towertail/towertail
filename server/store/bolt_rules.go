package store

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func (s *BoltStore) ReplaceRules(_ context.Context, orgID uuid.UUID, ch RulesChange) (RulesBackup, error) {
	if err := ch.Validate(); err != nil {
		return RulesBackup{}, err
	}
	hosts := make([]uuid.UUID, 0, len(ch.Hosts))
	for id := range ch.Hosts {
		hosts = append(hosts, id)
	}
	var out RulesBackup
	err := s.db.Update(func(tx *bolt.Tx) error {
		settings, nodes, err := s.loadRuleTargets(tx, orgID, ch.Org != nil, hosts)
		if err != nil {
			return err
		}
		out = ch.apply(settings, nodes)
		return s.saveRuleTargets(tx, orgID, settings, nodes, out)
	})
	return out, err
}

func (s *BoltStore) EditRules(_ context.Context, orgID uuid.UUID, ed RulesEdit) (RulesBackup, error) {
	var out RulesBackup
	err := s.db.Update(func(tx *bolt.Tx) error {
		settings, nodes, err := s.loadRuleTargets(tx, orgID, ed.Org, ed.Hosts)
		if err != nil {
			return err
		}
		if out, err = ed.run(settings, nodes); err != nil {
			return err
		}
		return s.saveRuleTargets(tx, orgID, settings, nodes, out)
	})
	if err != nil {
		return RulesBackup{}, err
	}
	return out, nil
}

// loadRuleTargets reads the settings (when org is set) and the nodes. A
// missing node gives ErrNotFound.
func (s *BoltStore) loadRuleTargets(tx *bolt.Tx, orgID uuid.UUID, org bool, hosts []uuid.UUID) (*ServerSettings, map[uuid.UUID]*Node, error) {
	var settings *ServerSettings
	if org {
		settings = &ServerSettings{}
		if v := tx.Bucket(bucketSettings).Get(orgID[:]); v != nil {
			if err := json.Unmarshal(v, settings); err != nil {
				return nil, nil, err
			}
		} else {
			*settings = DefaultServerSettings()
		}
	}
	nodeBucket := tx.Bucket(bucketNodes)
	nodes := make(map[uuid.UUID]*Node, len(hosts))
	for _, id := range hosts {
		v := nodeBucket.Get(nodeKey(orgID, id))
		if v == nil {
			return nil, nil, ErrNotFound
		}
		var n Node
		if err := json.Unmarshal(v, &n); err != nil {
			return nil, nil, err
		}
		nodes[id] = &n
	}
	return settings, nodes, nil
}

// saveRuleTargets writes the targets. A backup with an id is written too,
// and the backups older than the TTL are deleted.
func (s *BoltStore) saveRuleTargets(tx *bolt.Tx, orgID uuid.UUID, settings *ServerSettings, nodes map[uuid.UUID]*Node, out RulesBackup) error {
	if settings != nil {
		data, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bucketSettings).Put(orgID[:], data); err != nil {
			return err
		}
	}
	nodeBucket := tx.Bucket(bucketNodes)
	for id, n := range nodes {
		data, err := json.Marshal(n)
		if err != nil {
			return err
		}
		if err := nodeBucket.Put(nodeKey(orgID, id), data); err != nil {
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
	b := tx.Bucket(bucketRulesBackups)
	if err := b.Put(nodeKey(orgID, out.ID), data); err != nil {
		return err
	}
	cutoff := out.At.Add(-RulesBackupTTL)
	var old [][]byte
	err = s.eachRulesBackup(b, orgID, func(k []byte, rb RulesBackup) {
		if rb.At.Before(cutoff) {
			old = append(old, slices.Clone(k))
		}
	})
	if err != nil {
		return err
	}
	for _, k := range old {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func (s *BoltStore) eachRulesBackup(b *bolt.Bucket, orgID uuid.UUID, f func(key []byte, rb RulesBackup)) error {
	prefix := orgPrefix(orgID)
	c := b.Cursor()
	for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
		var rb RulesBackup
		if err := json.Unmarshal(v, &rb); err != nil {
			return err
		}
		f(k, rb)
	}
	return nil
}

func (s *BoltStore) ListRulesBackups(_ context.Context, orgID uuid.UUID) ([]RulesBackup, error) {
	var out []RulesBackup
	cutoff := time.Now().Add(-RulesBackupTTL)
	err := s.db.View(func(tx *bolt.Tx) error {
		return s.eachRulesBackup(tx.Bucket(bucketRulesBackups), orgID, func(_ []byte, rb RulesBackup) {
			if rb.At.After(cutoff) {
				out = append(out, rb)
			}
		})
	})
	slices.SortFunc(out, func(a, b RulesBackup) int { return b.At.Compare(a.At) })
	return out, err
}

func (s *BoltStore) GetRulesBackup(_ context.Context, orgID, id uuid.UUID) (RulesBackup, error) {
	var rb RulesBackup
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketRulesBackups).Get(nodeKey(orgID, id))
		if v == nil {
			return ErrNotFound
		}
		return json.Unmarshal(v, &rb)
	})
	if err == nil && !rb.At.After(time.Now().Add(-RulesBackupTTL)) {
		return RulesBackup{}, ErrNotFound
	}
	return rb, err
}
