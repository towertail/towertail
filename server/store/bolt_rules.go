package store

import (
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
	var out RulesBackup
	err := s.db.Update(func(tx *bolt.Tx) error {
		var settings *ServerSettings
		if ch.Org != nil {
			settings = &ServerSettings{}
			if v := tx.Bucket(bucketSettings).Get(orgID[:]); v != nil {
				if err := json.Unmarshal(v, settings); err != nil {
					return err
				}
			} else {
				*settings = DefaultServerSettings()
			}
		}
		nodeBucket := tx.Bucket(bucketNodes)
		nodes := make(map[uuid.UUID]*Node, len(ch.Hosts))
		for id := range ch.Hosts {
			v := nodeBucket.Get(nodeKey(orgID, id))
			if v == nil {
				return ErrNotFound
			}
			var n Node
			if err := json.Unmarshal(v, &n); err != nil {
				return err
			}
			nodes[id] = &n
		}
		out = ch.apply(settings, nodes)
		if settings != nil {
			data, err := json.Marshal(settings)
			if err != nil {
				return err
			}
			if err := tx.Bucket(bucketSettings).Put(orgID[:], data); err != nil {
				return err
			}
		}
		for id, n := range nodes {
			data, err := json.Marshal(n)
			if err != nil {
				return err
			}
			if err := nodeBucket.Put(nodeKey(orgID, id), data); err != nil {
				return err
			}
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
	})
	return out, err
}

func (s *BoltStore) eachRulesBackup(b *bolt.Bucket, orgID uuid.UUID, f func(key []byte, rb RulesBackup)) error {
	prefix := orgPrefix(orgID)
	c := b.Cursor()
	for k, v := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, v = c.Next() {
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
