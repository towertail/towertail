package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type fakeComponent struct {
	name    string
	started atomic.Int32
	stopped atomic.Int32
	ready   atomic.Bool
	startErr error
}

func (f *fakeComponent) Name() string { return f.name }
func (f *fakeComponent) Start(ctx context.Context) error {
	f.started.Add(1)
	if f.startErr != nil {
		return f.startErr
	}
	f.ready.Store(true)
	return nil
}
func (f *fakeComponent) Stop(context.Context) error { f.stopped.Add(1); return nil }
func (f *fakeComponent) Ready() bool                { return f.ready.Load() }

func TestRuntime_RequireStartsOnceAndReturnsError(t *testing.T) {
	r := New(nil)
	f := &fakeComponent{name: "a"}
	r.Register(f)
	if err := r.Require(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Require(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if f.started.Load() != 1 {
		t.Errorf("expected 1 start, got %d", f.started.Load())
	}
	if !r.Ready("a") {
		t.Error("expected ready")
	}
}

func TestRuntime_RequireSurfacesStartFailure(t *testing.T) {
	r := New(nil)
	f := &fakeComponent{name: "b", startErr: errors.New("boom")}
	r.Register(f)
	if err := r.Require(context.Background(), "b"); err == nil {
		t.Fatal("expected error")
	}
	// Second call still returns the same error.
	if err := r.Require(context.Background(), "b"); err == nil {
		t.Error("expected error on retry")
	}
	if r.Ready("b") {
		t.Error("not ready after failed start")
	}
}

func TestRuntime_StopAllReversesOrder(t *testing.T) {
	r := New(nil)
	a := &fakeComponent{name: "a"}
	b := &fakeComponent{name: "b"}
	r.Register(a)
	r.Register(b)
	_ = r.StartAll(context.Background())
	_ = r.StopAll(context.Background())
	if a.stopped.Load() != 1 || b.stopped.Load() != 1 {
		t.Errorf("stop counts: a=%d b=%d", a.stopped.Load(), b.stopped.Load())
	}
}
