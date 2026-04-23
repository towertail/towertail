package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// BoltStore is the self-hosted ConfigStore. State lives in a single
// BoltDB file so operators don't have to stand up a separate database
// for the v1 deployment.
type BoltStore struct {
	db *bolt.DB
}

var (
	bucketNodes    = []byte("nodes")
	bucketSettings = []byte("settings")
	bucketTokens   = []byte("tokens")
	bucketUsers    = []byte("users")
	bucketControl  = []byte("control")
)

// OpenBolt initialises the ConfigStore backed by a BoltDB file at the
// given directory. The file is named config.db. Creates the directory
// when absent.
func OpenBolt(dir string) (*BoltStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "config.db")
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := &BoltStore{db: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *BoltStore) init() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketNodes, bucketSettings, bucketTokens, bucketUsers, bucketControl} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *BoltStore) Close() error { return s.db.Close() }

// nodes bucket layout: key = orgID-bytes | nodeID-bytes, value = JSON.
func nodeKey(orgID, nodeID uuid.UUID) []byte {
	k := make([]byte, 32)
	copy(k, orgID[:])
	copy(k[16:], nodeID[:])
	return k
}

func orgPrefix(orgID uuid.UUID) []byte {
	p := make([]byte, 16)
	copy(p, orgID[:])
	return p
}

func (s *BoltStore) ListNodes(_ context.Context, orgID uuid.UUID) ([]Node, error) {
	var out []Node
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNodes)
		c := b.Cursor()
		prefix := orgPrefix(orgID)
		for k, v := c.Seek(prefix); k != nil && hasPrefix(k, prefix); k, v = c.Next() {
			var n Node
			if err := json.Unmarshal(v, &n); err != nil {
				return err
			}
			out = append(out, n)
		}
		return nil
	})
	return out, err
}

func (s *BoltStore) GetNode(_ context.Context, orgID, id uuid.UUID) (Node, error) {
	var n Node
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNodes)
		v := b.Get(nodeKey(orgID, id))
		if v == nil {
			return ErrNotFound
		}
		return json.Unmarshal(v, &n)
	})
	return n, err
}

func (s *BoltStore) PutNode(_ context.Context, orgID uuid.UUID, node Node) error {
	if node.ID == uuid.Nil {
		return errors.New("store: node id required")
	}
	data, err := json.Marshal(node)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketNodes).Put(nodeKey(orgID, node.ID), data)
	})
}

func (s *BoltStore) DeleteNode(_ context.Context, orgID, id uuid.UUID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketNodes).Delete(nodeKey(orgID, id))
	})
}

func (s *BoltStore) GetSettings(_ context.Context, orgID uuid.UUID) (ServerSettings, error) {
	var out ServerSettings
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketSettings).Get(orgID[:])
		if v == nil {
			out = DefaultServerSettings()
			return nil
		}
		return json.Unmarshal(v, &out)
	})
	return out, err
}

func (s *BoltStore) PutSettings(_ context.Context, orgID uuid.UUID, settings ServerSettings) error {
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSettings).Put(orgID[:], data)
	})
}

// token bucket layout:
//   hash-bytes → JSON token (primary index used by auth middleware)
// plus a parallel bucket "tokens_index" keyed by token UUID? Kept simple
// for v1 — ListTokens scans all entries.
func (s *BoltStore) PutToken(_ context.Context, t Token) error {
	if len(t.Hash) == 0 {
		return errors.New("store: token hash required")
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTokens).Put(t.Hash, data)
	})
}

func (s *BoltStore) GetTokenByHash(_ context.Context, hash []byte) (Token, error) {
	var t Token
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketTokens).Get(hash)
		if v == nil {
			return ErrNotFound
		}
		return json.Unmarshal(v, &t)
	})
	return t, err
}

func (s *BoltStore) DeleteToken(_ context.Context, id uuid.UUID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTokens)
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var t Token
			if err := json.Unmarshal(v, &t); err != nil {
				continue
			}
			if t.ID == id {
				return b.Delete(k)
			}
		}
		return ErrNotFound
	})
}

func (s *BoltStore) ListTokens(_ context.Context, orgID uuid.UUID) ([]Token, error) {
	var out []Token
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTokens)
		return b.ForEach(func(_, v []byte) error {
			var t Token
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			if t.OrgID == orgID {
				out = append(out, t)
			}
			return nil
		})
	})
	return out, err
}

func (s *BoltStore) PutUser(_ context.Context, u User) error {
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketUsers).Put([]byte(u.Email), data)
	})
}

func (s *BoltStore) GetUserByEmail(_ context.Context, email string) (User, error) {
	var u User
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketUsers).Get([]byte(email))
		if v == nil {
			return ErrNotFound
		}
		return json.Unmarshal(v, &u)
	})
	return u, err
}

// control bucket layout: key = nodeID | createdAt-nanos | msgID, value = JSON.
func controlKey(msg ControlMessage) []byte {
	k := make([]byte, 16+8+16)
	copy(k, msg.NodeID[:])
	nanos := msg.CreatedAt.UnixNano()
	for i := 0; i < 8; i++ {
		k[16+i] = byte(nanos >> (56 - 8*i))
	}
	copy(k[24:], msg.ID[:])
	return k
}

func (s *BoltStore) EnqueueControl(_ context.Context, msg ControlMessage) error {
	if msg.ID == uuid.Nil {
		msg.ID = uuid.New()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketControl).Put(controlKey(msg), data)
	})
}

func (s *BoltStore) ClaimControl(_ context.Context, nodeID uuid.UUID) (ControlMessage, error) {
	var out ControlMessage
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketControl)
		c := b.Cursor()
		prefix := nodeID[:]
		k, v := c.Seek(prefix)
		if k == nil || !hasPrefix(k, prefix) {
			return ErrNotFound
		}
		if err := json.Unmarshal(v, &out); err != nil {
			return err
		}
		return b.Delete(k)
	})
	return out, err
}

func hasPrefix(k, prefix []byte) bool {
	if len(k) < len(prefix) {
		return false
	}
	for i := range prefix {
		if k[i] != prefix[i] {
			return false
		}
	}
	return true
}
