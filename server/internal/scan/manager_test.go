package scan

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/store"
	"github.com/towertail/towertail/server/internal/wire"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

type fakeQueue struct {
	mu   sync.Mutex
	msgs []string
}

func (q *fakeQueue) Enqueue(_ context.Context, _ uuid.UUID, kind string, _ []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgs = append(q.msgs, kind)
	return nil
}

type fakeNotifier struct {
	jobs []wire.DiskScanJob
}

func (n *fakeNotifier) PublishDiskScan(_ uuid.UUID, j wire.DiskScanJob) { n.jobs = append(n.jobs, j) }

type fixture struct {
	clock  *fakeClock
	store  *Store
	queue  *fakeQueue
	notify *fakeNotifier
	m      *Manager
	org    uuid.UUID
	node   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	bs, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bs.Close() })
	st, err := NewStore(bs.DB())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		clock:  &fakeClock{t: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)},
		store:  st,
		queue:  &fakeQueue{},
		notify: &fakeNotifier{},
		org:    uuid.New(),
		node:   uuid.New(),
	}
	f.m = NewManager(st, f.queue, f.notify, nil, f.clock.Now)
	return f
}

func (f *fixture) request(t *testing.T) Job {
	t.Helper()
	j, err := f.m.Request(context.Background(), f.org, f.node, wire.DiskScanRequest{Root: "/"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return j
}

func (f *fixture) get(t *testing.T, id uuid.UUID) Job {
	t.Helper()
	j, err := f.store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return j
}

func resultGzip(t *testing.T) []byte {
	t.Helper()
	// The last line of testdata/scan-linux.ndjson of the sampler.
	data, err := os.ReadFile("testdata/scan-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

const progressLine = `{"t":"progress","v":1,"files":10,"dirs":2,"bytes":100,"no_access_dirs":0,"current":"/var","elapsed_ms":2000,"used_bytes":0,"top":[]}`

func TestManagerRequestConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.request(t)
	if first.State != StateQueued || len(f.queue.msgs) != 1 || f.queue.msgs[0] != "disk_scan" {
		t.Fatalf("job %+v, queue %v", first, f.queue.msgs)
	}
	active, err := f.m.Request(ctx, f.org, f.node, wire.DiskScanRequest{})
	if !errors.Is(err, ErrActive) || active.ID != first.ID {
		t.Fatalf("want ErrActive with job %s, got %v %s", first.ID, err, active.ID)
	}
	// Another node is free.
	if _, err := f.m.Request(ctx, f.org, uuid.New(), wire.DiskScanRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Request(ctx, f.org, f.node, wire.DiskScanRequest{MaxDepth: -1}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("want ErrBadInput, got %v", err)
	}
}

func TestManagerProgressFlow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	j := f.request(t)

	f.clock.Advance(time.Second)
	cancel, err := f.m.Progress(ctx, j.ID, []byte(progressLine))
	if err != nil || cancel {
		t.Fatalf("progress: %v %v", cancel, err)
	}
	j = f.get(t, j.ID)
	if j.State != StateRunning || j.StartedTS == nil || j.Progress == nil || j.Progress.Files != 10 {
		t.Fatalf("job after progress: %+v", j)
	}

	if _, err := f.m.Progress(ctx, j.ID, []byte(`{"t":"result","v":1}`)); !errors.Is(err, ErrBadInput) {
		t.Fatalf("result line on progress: %v", err)
	}
	if _, err := f.m.Progress(ctx, j.ID, []byte(`{"t":"progress","v":9}`)); !errors.Is(err, ErrBadInput) {
		t.Fatalf("bad version: %v", err)
	}

	if _, err := f.m.SaveResult(ctx, j.ID, []byte("not gzip")); !errors.Is(err, ErrBadInput) {
		t.Fatalf("bad gzip: %v", err)
	}
	gz := resultGzip(t)
	j, err = f.m.SaveResult(ctx, j.ID, gz)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != StateDone || j.ResultBytes != int64(len(gz)) || j.FinishedTS == nil {
		t.Fatalf("job after result: %+v", j)
	}
	got, err := f.m.Result(ctx, j.ID)
	if err != nil || !bytes.Equal(got, gz) {
		t.Fatalf("result: %v", err)
	}
	// A final job tells the sampler to stop, and does not change.
	cancel, err = f.m.Progress(ctx, j.ID, []byte(progressLine))
	if err != nil || !cancel {
		t.Fatalf("progress on done job: %v %v", cancel, err)
	}
	if _, err := f.m.SaveResult(ctx, j.ID, gz); !errors.Is(err, ErrFinal) {
		t.Fatalf("second result: %v", err)
	}
}

func TestManagerErrorLine(t *testing.T) {
	f := newFixture(t)
	j := f.request(t)
	_, err := f.m.Progress(context.Background(), j.ID, []byte(`{"t":"error","v":1,"message":"disk scans are off on this host"}`))
	if err != nil {
		t.Fatal(err)
	}
	j = f.get(t, j.ID)
	if j.State != StateFailed || j.Error != "disk scans are off on this host" {
		t.Fatalf("job: %+v", j)
	}
}

func TestManagerCancel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	queued := f.request(t)
	j, err := f.m.Cancel(ctx, queued.ID)
	if err != nil || j.State != StateCancelled {
		t.Fatalf("cancel queued: %+v %v", j, err)
	}
	// Cancel on a final job changes nothing.
	j, err = f.m.Cancel(ctx, queued.ID)
	if err != nil || j.State != StateCancelled {
		t.Fatalf("cancel final: %+v %v", j, err)
	}

	running := f.request(t)
	if _, err := f.m.Progress(ctx, running.ID, []byte(progressLine)); err != nil {
		t.Fatal(err)
	}
	j, err = f.m.Cancel(ctx, running.ID)
	if err != nil || j.State != StateRunning || !j.CancelRequested {
		t.Fatalf("cancel running: %+v %v", j, err)
	}
	cancel, err := f.m.Progress(ctx, running.ID, []byte(progressLine))
	if err != nil || !cancel {
		t.Fatalf("progress after cancel: %v %v", cancel, err)
	}
	if _, err := f.m.Progress(ctx, running.ID, []byte(`{"t":"cancelled","v":1}`)); err != nil {
		t.Fatal(err)
	}
	if j = f.get(t, running.ID); j.State != StateCancelled {
		t.Fatalf("state: %s", j.State)
	}
}

func TestManagerSweepExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	queued := f.request(t)
	f.clock.Advance(QueuedTimeout - time.Second)
	if err := f.m.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if j := f.get(t, queued.ID); j.State != StateQueued {
		t.Fatalf("expired too early: %s", j.State)
	}
	f.clock.Advance(2 * time.Second)
	if err := f.m.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if j := f.get(t, queued.ID); j.State != StateExpired || j.Error == "" || j.FinishedTS == nil {
		t.Fatalf("not expired: %+v", j)
	}

	running := f.request(t)
	if _, err := f.m.Progress(ctx, running.ID, []byte(progressLine)); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(ProgressTimeout - time.Second)
	if _, err := f.m.Progress(ctx, running.ID, []byte(progressLine)); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(ProgressTimeout - time.Second)
	if err := f.m.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if j := f.get(t, running.ID); j.State != StateRunning {
		t.Fatalf("failed with recent progress: %s", j.State)
	}
	f.clock.Advance(2 * time.Second)
	if err := f.m.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if j := f.get(t, running.ID); j.State != StateFailed || j.Error == "" {
		t.Fatalf("not failed: %+v", j)
	}
}

func TestManagerNotifyThrottle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	j := f.request(t) // queued: 1
	for i := 0; i < 5; i++ {
		// The first post changes the state: 2. The others are in 2 s: none.
		if _, err := f.m.Progress(ctx, j.ID, []byte(progressLine)); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(300 * time.Millisecond)
	}
	if n := len(f.notify.jobs); n != 2 {
		t.Fatalf("want 2 updates, got %d", n)
	}
	f.clock.Advance(NotifyInterval)
	if _, err := f.m.Progress(ctx, j.ID, []byte(progressLine)); err != nil {
		t.Fatal(err)
	}
	// A state change always goes out, also in the interval.
	if _, err := f.m.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(f.notify.jobs); n != 4 {
		t.Fatalf("want 4 updates, got %d", n)
	}
	last := f.notify.jobs[3]
	if !last.CancelRequested || last.NodeID != f.node {
		t.Fatalf("last update: %+v", last)
	}
}

func TestStorePrune(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	gz := resultGzip(t)

	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		j := f.request(t)
		if _, err := f.store.SaveResult(ctx, j.ID, gz, func(j *Job) error {
			j.ResultBytes = int64(len(gz))
			now := f.clock.Now()
			return j.finish(StateDone, now)
		}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
		f.clock.Advance(time.Hour)
	}
	failed := f.request(t)
	if _, err := f.m.Progress(ctx, failed.ID, []byte(`{"t":"error","v":1,"message":"x"}`)); err != nil {
		t.Fatal(err)
	}
	active := f.request(t)

	n, err := f.store.Prune(ctx, f.clock.Now())
	if err != nil || n != 2 {
		t.Fatalf("prune: %d %v", n, err)
	}
	for i, id := range ids {
		_, err := f.store.Result(ctx, id)
		if keep := i >= 2; keep != (err == nil) {
			t.Errorf("job %d: keep=%v, result err=%v", i, keep, err)
		}
	}

	// After 7 days, all final jobs go. The active job stays.
	f.clock.Advance(ResultTTL + time.Hour)
	if _, err := f.store.Prune(ctx, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	jobs, err := f.store.ListForNode(ctx, f.org, f.node)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != active.ID {
		t.Fatalf("jobs after TTL: %+v", jobs)
	}
}

func TestStoreListNewestFirst(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.request(t)
	if _, err := f.m.Cancel(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Minute)
	b := f.request(t)
	jobs, err := f.store.ListForNode(ctx, f.org, f.node)
	if err != nil || len(jobs) != 2 || jobs[0].ID != b.ID || jobs[1].ID != a.ID {
		t.Fatalf("list: %+v %v", jobs, err)
	}
	if _, err := f.store.Get(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
}
