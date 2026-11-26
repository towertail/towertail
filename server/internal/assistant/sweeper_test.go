package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
)

func TestSweeperRetention(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	cfg := config.AssistantConfig{Limits: config.AssistantLimits{
		Default: config.AssistantPlanLimits{RetentionDays: 0},
		Plans: map[string]config.AssistantPlanLimits{
			"pro":  {RetentionDays: 30},
			"free": {RetentionDays: 0},
		},
	}}
	st := NewMemoryStore()
	pro, free, def := uuid.New(), uuid.New(), uuid.New()
	plans := fakePlanByOrg{pro: "pro", free: "free"}
	chats := map[string][]Chat{}
	for name, org := range map[string]uuid.UUID{"pro": pro, "free": free, "def": def} {
		for _, age := range []int{40, 10} {
			at := now.AddDate(0, 0, -age)
			chats[name] = append(chats[name], newChat(t, st, org, uuid.New(), name, at))
		}
	}
	w := NewSweeper(st, plans, cfg, nil)
	w.now = func() time.Time { return now }
	w.sweep(ctx)

	count := func(org uuid.UUID) int {
		n := 0
		for _, c := range st.chats {
			if c.OrgID == org {
				n++
			}
		}
		return n
	}
	if got := count(pro); got != 1 {
		t.Errorf("pro chats %d, want 1", got)
	}
	if _, ok := st.chats[chats["pro"][0].ID]; ok {
		t.Error("the old chat of the pro org stays")
	}
	if count(free) != 2 || count(def) != 2 {
		t.Errorf("retention 0 deleted chats: free %d, default %d", count(free), count(def))
	}
}

type fakePlanByOrg map[uuid.UUID]string

func (f fakePlanByOrg) AssistantPlan(_ context.Context, org uuid.UUID) (string, int, error) {
	return f[org], 1, nil
}

func TestSweeperStop(t *testing.T) {
	st := NewMemoryStore()
	w := NewSweeper(st, nil, config.AssistantConfig{}, nil)
	w.every = time.Millisecond
	if w.Name() != "assistant-sweeper" || !w.Ready() {
		t.Fatal("name or ready")
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatalf("stop before start: %v", err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.done:
	default:
		t.Fatal("the loop did not end")
	}
}
