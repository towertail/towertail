package assistant

import (
	"context"
	"log/slog"
	"time"

	"github.com/towertail/towertail/server/config"
)

// Sweeper deletes the chats that are older than the retention of the org
// plan. It runs each hour. It implements runtime.Component.
type Sweeper struct {
	store  RetentionStore
	plans  PlanSource
	cfg    config.AssistantConfig
	log    *slog.Logger
	now    func() time.Time
	every  time.Duration
	cancel context.CancelFunc
	done   chan struct{}
}

// NewSweeper makes a sweeper. A nil PlanSource uses limits.default.
func NewSweeper(st RetentionStore, plans PlanSource, cfg config.AssistantConfig, log *slog.Logger) *Sweeper {
	if log == nil {
		log = slog.Default()
	}
	return &Sweeper{store: st, plans: plans, cfg: cfg, log: log, now: time.Now, every: time.Hour}
}

func (w *Sweeper) Name() string { return "assistant-sweeper" }

func (w *Sweeper) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel, w.done = cancel, make(chan struct{})
	go func() {
		defer close(w.done)
		t := time.NewTicker(w.every)
		defer t.Stop()
		for {
			w.sweep(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return nil
}

func (w *Sweeper) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
	case <-ctx.Done():
	}
	return nil
}

func (w *Sweeper) Ready() bool { return true }

// sweep runs one pass. A retention of 0 days keeps the chats of the org.
func (w *Sweeper) sweep(ctx context.Context) {
	orgs, err := w.store.OrgsWithChats(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Warn("assistant: sweep", "err", err)
		}
		return
	}
	for _, org := range orgs {
		var plan string
		if w.plans != nil {
			if plan, _, err = w.plans.AssistantPlan(ctx, org); err != nil {
				w.log.Warn("assistant: sweep plan", "org", org, "err", err)
				continue
			}
		}
		days := w.cfg.PlanLimits(plan).RetentionDays
		if days <= 0 {
			continue
		}
		before := w.now().AddDate(0, 0, -days)
		if n, err := w.store.DeleteChatsBefore(ctx, org, before); err != nil {
			w.log.Warn("assistant: sweep delete", "org", org, "err", err)
		} else if n > 0 {
			w.log.Info("assistant: deleted old chats", "org", org, "chats", n)
		}
	}
}
