package clickhouse

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
)

// fakeInserter fails the first fails calls, then keeps the bundles.
type fakeInserter struct {
	mu    sync.Mutex
	fails int
	calls int
	got   []IngestBundle
}

func (f *fakeInserter) insert(_ context.Context, b []IngestBundle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fails > 0 {
		f.fails--
		return errors.New("down")
	}
	f.got = append(f.got, b...)
	return nil
}

func (f *fakeInserter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func testBatcher(t *testing.T, ins inserter) (*batcher, *spill) {
	t.Helper()
	sp, err := newSpill(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	b := newBatcher(config.ClickHouseConfig{BatchFlushPeriod: 10 * time.Millisecond}, ins, sp, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.delays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	return b, sp
}

func bundle() IngestBundle {
	return IngestBundle{
		Sample: SampleRow{TS: time.Now().UTC().Truncate(time.Millisecond), OrgID: uuid.New(), NodeID: uuid.New(), SchemaV: 1, HostName: "h", Errors: []string{}},
		Disks:  []DiskRow{{Mount: "/", Used: 1, Total: 2}},
	}
}

func TestEnqueueAllOrNothing(t *testing.T) {
	b, _ := testBatcher(t, &fakeInserter{})
	// No run loop, so the queue only fills.
	for range queueSize - 1 {
		if err := b.Enqueue(bundle()); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Enqueue(bundle(), bundle()); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if n := len(b.in); n != queueSize-1 {
		t.Fatalf("queue = %d, want %d", n, queueSize-1)
	}
	if err := b.Enqueue(bundle()); err != nil {
		t.Fatalf("last slot: %v", err)
	}
}

func TestFlushSpillsThenReplays(t *testing.T) {
	ins := &fakeInserter{fails: 4}
	b, sp := testBatcher(t, ins)

	first := bundle()
	b.flushPending("test", []IngestBundle{first})
	if ins.count() != 0 || ins.calls != 4 {
		t.Fatalf("calls = %d, rows = %d, want 4 calls and no rows", ins.calls, ins.count())
	}
	path, ok := sp.oldest()
	if !ok {
		t.Fatal("no spill file")
	}

	b.flushPending("test", []IngestBundle{bundle()})
	if ins.count() != 2 {
		t.Fatalf("rows = %d, want 2", ins.count())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("spill file not removed: %v", err)
	}
	if got := ins.got[1]; got.Sample.NodeID != first.Sample.NodeID || !got.Sample.TS.Equal(first.Sample.TS) || len(got.Disks) != 1 {
		t.Fatalf("replayed bundle = %+v, want %+v", got, first)
	}
}

func TestSpillRejectsBadFile(t *testing.T) {
	ins := &fakeInserter{}
	b, sp := testBatcher(t, ins)
	bad := filepath.Join(sp.dir, "00000000000000000001"+spillExt)
	if err := os.WriteFile(bad, []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.flushPending("test", []IngestBundle{bundle()})
	if _, err := os.Stat(bad + ".bad"); err != nil {
		t.Fatalf("bad file not moved: %v", err)
	}
	if _, ok := sp.oldest(); ok {
		t.Fatal("bad file still listed")
	}
}

func TestSpillMax(t *testing.T) {
	sp, err := newSpill(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.write([]IngestBundle{bundle()}); err == nil {
		t.Fatal("write over max: want error")
	}
	if _, ok := sp.oldest(); ok {
		t.Fatal("file kept over max")
	}
}

func TestStopSpillsWithoutDelay(t *testing.T) {
	ins := &fakeInserter{fails: 100}
	b, sp := testBatcher(t, ins)
	b.delays = []time.Duration{time.Hour}
	b.cfg.BatchFlushPeriod = time.Hour
	b.start()
	if err := b.Enqueue(bundle()); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { b.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop waits for the retry delay")
	}
	if _, ok := sp.oldest(); !ok {
		t.Fatal("no spill file after stop")
	}
}
