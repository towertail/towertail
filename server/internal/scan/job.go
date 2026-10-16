// Package scan keeps the disk scan jobs of the nodes (docs/wire.md §3.1).
package scan

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/towertail/sampler/pkg/schema"

	"github.com/towertail/towertail/server/internal/wire"
)

// State is the state of a job.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateDone      State = "done"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StateExpired   State = "expired"
)

// ErrTransition is the error for a move that the state table refuses.
var ErrTransition = errors.New("scan: state change not allowed")

// transitions holds the allowed moves. The sampler can post an error or
// cancelled line, or PUT the result, before its first progress line, so
// queued can go to each final state.
var transitions = map[State][]State{
	StateQueued:  {StateRunning, StateDone, StateFailed, StateCancelled, StateExpired},
	StateRunning: {StateDone, StateFailed, StateCancelled},
}

// Final is true for a state that does not change.
func (s State) Final() bool {
	_, ok := transitions[s]
	return !ok
}

// Job is a disk scan of one node.
type Job struct {
	ID              uuid.UUID            `json:"id"`
	OrgID           uuid.UUID            `json:"org_id"`
	NodeID          uuid.UUID            `json:"node_id"`
	State           State                `json:"state"`
	Root            string               `json:"root"`
	MaxDepth        int                  `json:"max_depth"`
	MinBytes        int64                `json:"min_bytes"`
	MinFrac         *float64             `json:"min_frac,omitempty"`
	RefreshOf       string               `json:"refresh_of,omitempty"`
	Progress        *schema.ScanProgress `json:"progress"`
	Error           string               `json:"error"`
	CancelRequested bool                 `json:"cancel_requested"`
	CreatedTS       time.Time            `json:"created_ts"`
	StartedTS       *time.Time           `json:"started_ts"`
	FinishedTS      *time.Time           `json:"finished_ts"`
	// ProgressTS is the time of the last progress line.
	ProgressTS  *time.Time `json:"progress_ts"`
	ResultBytes int64      `json:"result_bytes"`
}

// Transition moves the job to the state to, if the state table allows it.
func (j *Job) Transition(to State) error {
	for _, s := range transitions[j.State] {
		if s == to {
			j.State = to
			return nil
		}
	}
	return fmt.Errorf("%w: %s to %s", ErrTransition, j.State, to)
}

// finish moves the job to a final state at time now.
func (j *Job) finish(to State, now time.Time) error {
	if err := j.Transition(to); err != nil {
		return err
	}
	j.FinishedTS = &now
	return nil
}

// Wire gives the job as the app sees it. Times have a precision of one second.
func (j Job) Wire() wire.DiskScanJob {
	return wire.DiskScanJob{
		ID:              j.ID,
		NodeID:          j.NodeID,
		State:           string(j.State),
		Root:            j.Root,
		MaxDepth:        j.MaxDepth,
		MinBytes:        j.MinBytes,
		MinFrac:         j.MinFrac,
		RefreshOf:       j.RefreshOf,
		Progress:        j.Progress,
		Error:           j.Error,
		CancelRequested: j.CancelRequested,
		CreatedTS:       j.CreatedTS.UTC().Truncate(time.Second),
		StartedTS:       seconds(j.StartedTS),
		FinishedTS:      seconds(j.FinishedTS),
		ResultBytes:     j.ResultBytes,
	}
}

func seconds(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC().Truncate(time.Second)
	return &v
}
