package aicalls

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/towertail/towertail/server/internal/clickhouse"
)

type fakeWriter struct {
	mu      sync.Mutex
	batches [][]clickhouse.AICall
	err     error
	wrote   chan int
}

func newFakeWriter() *fakeWriter { return &fakeWriter{wrote: make(chan int, 100)} }

func (f *fakeWriter) WriteAICalls(_ context.Context, rows []clickhouse.AICall) error {
	f.mu.Lock()
	f.batches = append(f.batches, append([]clickhouse.AICall(nil), rows...))
	f.mu.Unlock()
	f.wrote <- len(rows)
	return f.err
}

// droppedCount reads the metric from the default registry.
func droppedCount(t *testing.T) float64 {
	t.Helper()
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() == "towertail_ai_calls_dropped_total" {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatal("metric not found")
	return 0
}

func newTestLog(w Writer) *Log {
	l := NewLog(w, slog.New(slog.NewTextHandler(io.Discard, nil)))
	l.every = time.Hour
	return l
}

func record(l *Log, n int) {
	for range n {
		l.Record(clickhouse.AICall{CallID: uuid.New(), Tool: "whoami"})
	}
}

func waitWrite(t *testing.T, f *fakeWriter) int {
	t.Helper()
	select {
	case n := <-f.wrote:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no write")
		return 0
	}
}

func TestLogBatchesAndDrops(t *testing.T) {
	// 500 records cause one write.
	f := newFakeWriter()
	l := newTestLog(f)
	record(l, 500)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	if n := waitWrite(t, f); n != 500 {
		t.Fatalf("first write = %d rows, want 500", n)
	}
	// Run writes the rest at stop.
	record(l, 7)
	cancel()
	<-done
	if n := waitWrite(t, f); n != 7 {
		t.Fatalf("last write = %d rows, want 7", n)
	}
	if len(f.batches) != 2 {
		t.Fatalf("writes = %d, want 2", len(f.batches))
	}

	// A full buffer drops rows and counts them.
	before := droppedCount(t)
	l = newTestLog(newFakeWriter())
	record(l, bufferSize+10)
	if got := droppedCount(t) - before; got != 10 {
		t.Fatalf("dropped = %v, want 10", got)
	}
}

func TestLogWriteErrorDropsBatch(t *testing.T) {
	f := newFakeWriter()
	f.err = errors.New("down")
	l := newTestLog(f)
	record(l, 3)
	before := droppedCount(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	cancel()
	<-done
	if n := waitWrite(t, f); n != 3 {
		t.Fatalf("write = %d rows, want 3", n)
	}
	if got := droppedCount(t) - before; got != 3 {
		t.Fatalf("dropped = %v, want 3", got)
	}
	if len(f.batches) != 1 {
		t.Fatalf("writes = %d, want 1: the failed batch must not return", len(f.batches))
	}
}

func TestLogTickWrites(t *testing.T) {
	f := newFakeWriter()
	l := newTestLog(f)
	l.every = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	record(l, 2)
	if n := waitWrite(t, f); n != 2 {
		t.Fatalf("write = %d rows, want 2", n)
	}
}

func TestNilLog(t *testing.T) {
	var l *Log
	l.Record(clickhouse.AICall{})
}
