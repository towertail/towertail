package scan

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"

	"github.com/towertail/towertail/server/store"
)

// ErrActive is the error for a second job on a node that has a queued
// or running job.
var ErrActive = errors.New("scan: node has an active job")

// Retention of the results.
const (
	KeepResults = 3
	ResultTTL   = 7 * 24 * time.Hour
)

var (
	bucketJobs    = []byte("scan_jobs")
	bucketResults = []byte("scan_results")
)

// Store keeps the jobs and the gzip results in the Bolt file of the
// config store. Key = job id. A Postgres store comes with TOWERTAIL-29.
type Store struct {
	db *bolt.DB
}

// NewStore makes the buckets when they are not there.
func NewStore(db *bolt.DB) (*Store, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketJobs, bucketResults} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Create puts a new job. If the node has an active job, Create gives
// that job and ErrActive.
func (s *Store) Create(_ context.Context, j Job) (Job, error) {
	var active Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		err := b.ForEach(func(_, v []byte) error {
			var o Job
			if err := json.Unmarshal(v, &o); err != nil {
				return err
			}
			if o.OrgID == j.OrgID && o.NodeID == j.NodeID && !o.State.Final() {
				active = o
				return ErrActive
			}
			return nil
		})
		if err != nil {
			return err
		}
		return putJob(b, j)
	})
	if errors.Is(err, ErrActive) {
		return active, ErrActive
	}
	return j, err
}

// Get gives the job, or store.ErrNotFound.
func (s *Store) Get(_ context.Context, id uuid.UUID) (Job, error) {
	var j Job
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		j, err = getJob(tx.Bucket(bucketJobs), id)
		return err
	})
	return j, err
}

// ListForNode gives the jobs of the node, the newest first.
func (s *Store) ListForNode(_ context.Context, orgID, nodeID uuid.UUID) ([]Job, error) {
	return s.list(func(j Job) bool { return j.OrgID == orgID && j.NodeID == nodeID })
}

// ListActive gives the queued and running jobs of all nodes.
func (s *Store) ListActive(_ context.Context) ([]Job, error) {
	return s.list(func(j Job) bool { return !j.State.Final() })
}

func (s *Store) list(keep func(Job) bool) ([]Job, error) {
	out := []Job{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobs).ForEach(func(_, v []byte) error {
			var j Job
			if err := json.Unmarshal(v, &j); err != nil {
				return err
			}
			if keep(j) {
				out = append(out, j)
			}
			return nil
		})
	})
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedTS.After(out[b].CreatedTS) })
	return out, err
}

// Update runs fn on the job in one transaction and keeps the result.
// If fn gives an error, the job does not change.
func (s *Store) Update(_ context.Context, id uuid.UUID, fn func(*Job) error) (Job, error) {
	var j Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		j, err = updateJob(tx.Bucket(bucketJobs), id, fn)
		return err
	})
	return j, err
}

// SaveResult runs fn on the job and keeps the job and the gzip result
// in one transaction.
func (s *Store) SaveResult(_ context.Context, id uuid.UUID, gz []byte, fn func(*Job) error) (Job, error) {
	var j Job
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		j, err = updateJob(tx.Bucket(bucketJobs), id, fn)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketResults).Put(id[:], gz)
	})
	return j, err
}

// Result gives the gzip result of the job, or store.ErrNotFound.
func (s *Store) Result(_ context.Context, id uuid.UUID) ([]byte, error) {
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketResults).Get(id[:])
		if v == nil {
			return store.ErrNotFound
		}
		out = append([]byte(nil), v...)
		return nil
	})
	return out, err
}

// Prune keeps the last KeepResults jobs with a result for each node. It
// deletes the jobs in a final state that finished more than ResultTTL
// before now, with their results. It gives the number of deleted jobs.
func (s *Store) Prune(_ context.Context, now time.Time) (int, error) {
	deleted := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		jobs := tx.Bucket(bucketJobs)
		var all []Job
		err := jobs.ForEach(func(_, v []byte) error {
			var j Job
			if err := json.Unmarshal(v, &j); err != nil {
				return err
			}
			all = append(all, j)
			return nil
		})
		if err != nil {
			return err
		}
		sort.Slice(all, func(a, b int) bool { return all[a].CreatedTS.After(all[b].CreatedTS) })

		type nodeKey struct{ org, node uuid.UUID }
		withResult := map[nodeKey]int{}
		for _, j := range all {
			if !j.State.Final() {
				continue
			}
			old := now.Sub(j.finishedOrCreated()) > ResultTTL
			drop := old
			if j.ResultBytes > 0 {
				k := nodeKey{j.OrgID, j.NodeID}
				withResult[k]++
				drop = drop || withResult[k] > KeepResults
			}
			if !drop {
				continue
			}
			if err := jobs.Delete(j.ID[:]); err != nil {
				return err
			}
			if err := tx.Bucket(bucketResults).Delete(j.ID[:]); err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	return deleted, err
}

func (j Job) finishedOrCreated() time.Time {
	if j.FinishedTS != nil {
		return *j.FinishedTS
	}
	return j.CreatedTS
}

func getJob(b *bolt.Bucket, id uuid.UUID) (Job, error) {
	var j Job
	v := b.Get(id[:])
	if v == nil {
		return j, store.ErrNotFound
	}
	err := json.Unmarshal(v, &j)
	return j, err
}

func putJob(b *bolt.Bucket, j Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return b.Put(j.ID[:], data)
}

func updateJob(b *bolt.Bucket, id uuid.UUID, fn func(*Job) error) (Job, error) {
	j, err := getJob(b, id)
	if err != nil {
		return j, err
	}
	if err := fn(&j); err != nil {
		return j, err
	}
	return j, putJob(b, j)
}
