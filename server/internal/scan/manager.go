package scan

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/towertail/sampler/pkg/schema"

	"github.com/towertail/towertail/server/internal/control"
	"github.com/towertail/towertail/server/wire"
)

// Limits and timeouts of the jobs.
const (
	// MaxResultBytes is the limit of the gzip result body.
	MaxResultBytes = 16 << 20
	// MaxResultJSONBytes is the limit of the result after gunzip.
	MaxResultJSONBytes = 256 << 20
	// MaxRootLen is the limit of the root path in a request.
	MaxRootLen = 4096

	QueuedTimeout   = 10 * time.Minute
	ProgressTimeout = 2 * time.Minute
	// NotifyInterval is the minimum time between two progress updates
	// of one job to the app.
	NotifyInterval = 2 * time.Second
)

var (
	// ErrFinal is the error for a change to a job in a final state.
	ErrFinal = errors.New("scan: job is in a final state")
	// ErrBadInput is the error for a request, line or result that the
	// server cannot use.
	ErrBadInput = errors.New("scan: bad input")
	// ErrTooLarge is the error for a result over the limit.
	ErrTooLarge = errors.New("scan: result too large")
)

// Enqueuer puts a control message on the queue of a node.
type Enqueuer interface {
	Enqueue(ctx context.Context, nodeID uuid.UUID, kind string, payload []byte) error
}

// Notifier tells the apps about a changed job.
type Notifier interface {
	PublishDiskScan(orgID uuid.UUID, job wire.DiskScanJob)
}

// Manager runs the life of the jobs: request, progress, result, cancel
// and expiry.
type Manager struct {
	store  *Store
	queue  Enqueuer
	notify Notifier
	log    *slog.Logger
	now    func() time.Time

	mu       sync.Mutex
	notified map[uuid.UUID]time.Time
}

// NewManager makes a Manager. notify can be nil. now can be nil, then
// the Manager uses time.Now.
func NewManager(st *Store, q Enqueuer, notify Notifier, log *slog.Logger, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:    st,
		queue:    q,
		notify:   notify,
		log:      log,
		now:      func() time.Time { return now().UTC() },
		notified: map[uuid.UUID]time.Time{},
	}
}

// Get gives the job, or store.ErrNotFound.
func (m *Manager) Get(ctx context.Context, id uuid.UUID) (Job, error) {
	return m.store.Get(ctx, id)
}

// List gives the jobs of the node, the newest first.
func (m *Manager) List(ctx context.Context, orgID, nodeID uuid.UUID) ([]Job, error) {
	return m.store.ListForNode(ctx, orgID, nodeID)
}

// Result gives the gzip result of the job, or store.ErrNotFound.
func (m *Manager) Result(ctx context.Context, id uuid.UUID) ([]byte, error) {
	return m.store.Result(ctx, id)
}

// Request makes a queued job and puts a disk_scan message on the
// control queue of the node. If the node has an active job, Request
// gives that job and ErrActive.
func (m *Manager) Request(ctx context.Context, orgID, nodeID uuid.UUID, req wire.DiskScanRequest) (Job, error) {
	if req.MaxDepth < 0 || req.MinBytes < 0 || len(req.Root) > MaxRootLen || len(req.RefreshOf) > MaxRootLen {
		return Job{}, fmt.Errorf("%w: max_depth and min_bytes must not be negative, root and refresh_of must be %d bytes at most", ErrBadInput, MaxRootLen)
	}
	if f := req.MinFrac; f != nil && (*f < 0 || *f >= 1) {
		return Job{}, fmt.Errorf("%w: min_frac must be in [0, 1)", ErrBadInput)
	}
	if req.RefreshOf != "" && !refreshes(req.RefreshOf, req.Root) {
		return Job{}, fmt.Errorf("%w: refresh_of must be ~ or an absolute path, and root must be below it", ErrBadInput)
	}
	j := Job{
		ID:        uuid.New(),
		OrgID:     orgID,
		NodeID:    nodeID,
		State:     StateQueued,
		Root:      req.Root,
		MaxDepth:  req.MaxDepth,
		MinBytes:  req.MinBytes,
		MinFrac:   req.MinFrac,
		RefreshOf: req.RefreshOf,
		CreatedTS: m.now(),
	}
	j, err := m.store.Create(ctx, j)
	if err != nil {
		return j, err
	}
	payload, err := json.Marshal(wire.DiskScanPayload{JobID: j.ID, Root: j.Root, MaxDepth: j.MaxDepth, MinBytes: j.MinBytes, MinFrac: j.MinFrac})
	if err == nil {
		err = m.queue.Enqueue(ctx, nodeID, control.KindDiskScan, payload)
	}
	if err != nil {
		now := m.now()
		_, _ = m.store.Update(ctx, j.ID, func(j *Job) error {
			j.Error = "the server could not queue the scan"
			return j.finish(StateFailed, now)
		})
		return Job{}, err
	}
	m.publish(j, true)
	return j, nil
}

// Cancel cancels a queued job, and asks the sampler to stop a running
// job. A job in a final state does not change.
func (m *Manager) Cancel(ctx context.Context, id uuid.UUID) (Job, error) {
	now := m.now()
	changed := false
	j, err := m.store.Update(ctx, id, func(j *Job) error {
		switch j.State {
		case StateQueued:
			changed = true
			return j.finish(StateCancelled, now)
		case StateRunning:
			changed = !j.CancelRequested
			j.CancelRequested = true
			return nil
		}
		return ErrFinal
	})
	if errors.Is(err, ErrFinal) {
		return m.store.Get(ctx, id)
	}
	if err != nil {
		return j, err
	}
	if changed {
		m.publish(j, true)
	}
	return j, nil
}

// Progress applies one scan line from the sampler: progress, error or
// cancelled. cancel tells the sampler to stop.
func (m *Manager) Progress(ctx context.Context, id uuid.UUID, line []byte) (cancel bool, err error) {
	var head schema.ScanLine
	if err := json.Unmarshal(line, &head); err != nil {
		return false, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	if head.V != schema.ScanVersion {
		return false, fmt.Errorf("%w: unsupported scan line v=%d", ErrBadInput, head.V)
	}
	now := m.now()
	var apply func(*Job) error
	switch head.T {
	case schema.ScanLineProgress:
		var p schema.ScanProgress
		if err := json.Unmarshal(line, &p); err != nil {
			return false, fmt.Errorf("%w: %v", ErrBadInput, err)
		}
		apply = func(j *Job) error {
			if j.State == StateQueued {
				if err := j.Transition(StateRunning); err != nil {
					return err
				}
				j.StartedTS = &now
			}
			j.Progress = &p
			j.ProgressTS = &now
			return nil
		}
	case schema.ScanLineError:
		var e schema.ScanError
		if err := json.Unmarshal(line, &e); err != nil {
			return false, fmt.Errorf("%w: %v", ErrBadInput, err)
		}
		apply = func(j *Job) error {
			j.Error = e.Message
			return j.finish(StateFailed, now)
		}
	case schema.ScanLineCancelled:
		apply = func(j *Job) error { return j.finish(StateCancelled, now) }
	default:
		return false, fmt.Errorf("%w: unexpected scan line t=%q", ErrBadInput, head.T)
	}

	var before State
	j, err := m.store.Update(ctx, id, func(j *Job) error {
		before = j.State
		if j.State.Final() {
			return ErrFinal
		}
		return apply(j)
	})
	if errors.Is(err, ErrFinal) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	m.publish(j, j.State != before)
	return j.CancelRequested, nil
}

// SaveResult checks and keeps the gzip result line of the job and moves
// the job to done.
func (m *Manager) SaveResult(ctx context.Context, id uuid.UUID, gz []byte) (Job, error) {
	if len(gz) > MaxResultBytes {
		return Job{}, ErrTooLarge
	}
	if err := checkResult(gz); err != nil {
		return Job{}, err
	}
	now := m.now()
	j, err := m.store.SaveResult(ctx, id, gz, func(j *Job) error {
		if j.State.Final() {
			return ErrFinal
		}
		if err := j.finish(StateDone, now); err != nil {
			return err
		}
		if j.StartedTS == nil {
			j.StartedTS = &now
		}
		j.ResultBytes = int64(len(gz))
		return nil
	})
	if err != nil {
		return j, err
	}
	m.publish(j, true)
	if _, err := m.store.Prune(ctx, now); err != nil {
		m.log.Warn("scan: prune", "err", err)
	}
	return j, nil
}

// checkResult decodes the gzip body as a scan result line.
func checkResult(gz []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return fmt.Errorf("%w: body is not gzip", ErrBadInput)
	}
	defer zr.Close()
	lr := &io.LimitedReader{R: zr, N: MaxResultJSONBytes + 1}
	var r schema.ScanResult
	if err := json.NewDecoder(lr).Decode(&r); err != nil {
		if lr.N <= 0 {
			return ErrTooLarge
		}
		return fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	if lr.N <= 0 {
		return ErrTooLarge
	}
	if r.T != schema.ScanLineResult || r.V != schema.ScanVersion {
		return fmt.Errorf("%w: not a v%d result line", ErrBadInput, schema.ScanVersion)
	}
	return nil
}

// Sweep expires the queued jobs that no sampler took, fails the running
// jobs with no progress, and prunes old results.
func (m *Manager) Sweep(ctx context.Context) error {
	jobs, err := m.store.ListActive(ctx)
	if err != nil {
		return err
	}
	now := m.now()
	for _, j := range jobs {
		var to State
		var reason string
		switch {
		case j.State == StateQueued && now.Sub(j.CreatedTS) > QueuedTimeout:
			to, reason = StateExpired, "no sampler took the scan in 10 min"
		case j.State == StateRunning && now.Sub(j.lastProgress()) > ProgressTimeout:
			to, reason = StateFailed, "no progress from the sampler for 2 min"
		default:
			continue
		}
		state := j.State
		j, err := m.store.Update(ctx, j.ID, func(j *Job) error {
			if j.State != state {
				return ErrFinal
			}
			j.Error = reason
			return j.finish(to, now)
		})
		if errors.Is(err, ErrFinal) {
			continue
		}
		if err != nil {
			return err
		}
		m.publish(j, true)
	}
	_, err = m.store.Prune(ctx, now)
	return err
}

// Run calls Sweep at each tick until ctx is done.
func (m *Manager) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.Sweep(ctx); err != nil {
				m.log.Warn("scan: sweep", "err", err)
			}
		}
	}
}

func (j Job) lastProgress() time.Time {
	if j.ProgressTS != nil {
		return *j.ProgressTS
	}
	if j.StartedTS != nil {
		return *j.StartedTS
	}
	return j.CreatedTS
}

// publish sends the job to the apps. A state change always goes out.
// Progress goes out at most once each NotifyInterval for each job.
func (m *Manager) publish(j Job, stateChanged bool) {
	if m.notify == nil {
		return
	}
	now := m.now()
	m.mu.Lock()
	last, seen := m.notified[j.ID]
	send := stateChanged || !seen || now.Sub(last) >= NotifyInterval
	if send {
		m.notified[j.ID] = now
	}
	if j.State.Final() {
		delete(m.notified, j.ID)
	}
	m.mu.Unlock()
	if send {
		m.notify.PublishDiskScan(j.OrgID, j.Wire())
	}
}

// refreshes is true when scope is ~ or an absolute path and root is a
// folder below it. With scope ~, the server does not know the home
// folder, so root must only be absolute or start with ~.
func refreshes(scope, root string) bool {
	abs := func(p string) bool {
		return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) ||
			len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
	}
	if scope == "~" {
		return abs(root) || strings.HasPrefix(root, "~/") || strings.HasPrefix(root, `~\`)
	}
	if !abs(scope) || !abs(root) {
		return false
	}
	base := strings.TrimRight(scope, `/\`)
	if len(root) <= len(base)+1 || !strings.EqualFold(root[:len(base)], base) {
		return false
	}
	return root[len(base)] == '/' || root[len(base)] == '\\'
}
