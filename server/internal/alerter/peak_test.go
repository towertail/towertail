package alerter

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
)

// eventStore keeps the written events.
type eventStore struct {
	nopStore
	mu     sync.Mutex
	events []clickhouse.EventRow
}

func (s *eventStore) WriteEvent(_ context.Context, e clickhouse.EventRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func (s *eventStore) payload(t *testing.T, i int) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal([]byte(s.events[i].Payload), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAlerter_PeakTracked(t *testing.T) {
	clock := &testClock{t: time.Unix(1700000000, 0).UTC()}
	cfg := instantSettings()
	cfg.NotifyDebounceSeconds = 0
	cfg.Thresholds.CPUWarn, cfg.Thresholds.CPUCritical = 0.85, 0.95
	st := &eventStore{}
	e := New(nil, clock, nil, st, newFakeConfig(cfg))
	ctx := context.Background()
	org, node := uuid.New(), uuid.New()

	sample := func(cpu float64) {
		clock.advance(10 * time.Second)
		e.Evaluate(ctx, org, node, nil, Reading{CPU: cpu, Mem: 0.1, Disk: 0.1})
	}
	sample(0.10)
	sample(0.90)
	sample(0.97)
	sample(0.93)

	st.mu.Lock()
	id := st.events[0].AlertID
	st.mu.Unlock()
	if got := e.Peaks(org)[id]; got != 0.97 {
		t.Fatalf("peak = %v, want 0.97", got)
	}
	if got := e.Peaks(uuid.New()); len(got) != 0 {
		t.Fatalf("peaks of another org = %v", got)
	}
	if p := st.payload(t, 0); p["warn"] != 0.85 || p["critical"] != 0.95 {
		t.Fatalf("first payload = %v, want the limits", p)
	}

	sample(0.10)
	last := len(st.events) - 1
	if st.events[last].Tint != "ok" {
		t.Fatalf("last event = %+v, want ok", st.events[last])
	}
	if p := st.payload(t, last); p["peak"] != 0.97 {
		t.Fatalf("ok payload = %v, want peak 0.97", p)
	}
	if got := e.Peaks(org); len(got) != 0 {
		t.Fatalf("peaks after the alert closed = %v", got)
	}
}
