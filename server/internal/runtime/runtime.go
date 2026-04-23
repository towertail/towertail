package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// Component is a lazily-started server subsystem. Runtime.Start calls
// Start in registration order; Stop runs in reverse.
type Component interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Ready() bool
}

// Runtime coordinates component lifecycle. Each Require(name) call
// triggers a first-time start; Ready(name) reports the component's
// post-start readiness so HTTP handlers can 503 callers until the
// backing resource has finished dialing.
type Runtime struct {
	log        *slog.Logger
	mu         sync.Mutex
	order      []string
	components map[string]Component
	started    map[string]bool
	startErrs  map[string]error
}

func New(log *slog.Logger) *Runtime {
	if log == nil {
		log = slog.Default()
	}
	return &Runtime{
		log:        log,
		components: map[string]Component{},
		started:    map[string]bool{},
		startErrs:  map[string]error{},
	}
}

// Register records a component without starting it. Safe to call from
// main() before the server begins serving.
func (r *Runtime) Register(c Component) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := c.Name()
	if _, ok := r.components[name]; ok {
		panic(fmt.Sprintf("runtime: component %q already registered", name))
	}
	r.components[name] = c
	r.order = append(r.order, name)
}

// Require lazily starts the named component if it hasn't been started
// yet. Safe for concurrent callers; only one start attempt per
// component. Returns the startup error, if any, on every subsequent
// call (so handlers keep surfacing the same 503).
func (r *Runtime) Require(ctx context.Context, name string) error {
	r.mu.Lock()
	c, ok := r.components[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("runtime: unknown component %q", name)
	}
	if r.started[name] {
		err := r.startErrs[name]
		r.mu.Unlock()
		return err
	}
	r.started[name] = true
	r.mu.Unlock()

	r.log.Info("runtime: starting component", "name", name)
	err := c.Start(ctx)
	r.mu.Lock()
	r.startErrs[name] = err
	r.mu.Unlock()
	if err != nil {
		r.log.Error("runtime: component start failed", "name", name, "err", err)
	}
	return err
}

// Ready returns true when the named component has started and reports
// itself ready.
func (r *Runtime) Ready(name string) bool {
	r.mu.Lock()
	c, ok := r.components[name]
	started := r.started[name]
	errored := r.startErrs[name] != nil
	r.mu.Unlock()
	if !ok || !started || errored {
		return false
	}
	return c.Ready()
}

// StartAll starts every registered component eagerly. Used by boot
// paths that don't want lazy semantics (e.g. CLI admin commands).
func (r *Runtime) StartAll(ctx context.Context) error {
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	r.mu.Unlock()
	for _, name := range names {
		if err := r.Require(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// StopAll stops every started component in reverse registration order.
// Errors are joined and returned once every component has been
// attempted.
func (r *Runtime) StopAll(ctx context.Context) error {
	r.mu.Lock()
	names := append([]string(nil), r.order...)
	started := map[string]bool{}
	for k, v := range r.started {
		started[k] = v
	}
	r.mu.Unlock()

	var errs []error
	for i := len(names) - 1; i >= 0; i-- {
		name := names[i]
		if !started[name] {
			continue
		}
		r.log.Info("runtime: stopping component", "name", name)
		if err := r.components[name].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
