package assistant

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// storeHarness gives a store and an org that the store accepts.
type storeHarness struct {
	st     Store
	newOrg func(t *testing.T) uuid.UUID
}

// testStore runs the same tests on each Store.
func testStore(t *testing.T, h storeHarness) {
	t.Run("Chats", func(t *testing.T) { contractChats(t, h) })
	t.Run("Messages", func(t *testing.T) { contractMessages(t, h) })
	t.Run("Proposals", func(t *testing.T) { contractProposals(t, h) })
	t.Run("ProposalClaim", func(t *testing.T) { contractProposalClaim(t, h) })
	t.Run("Reserve", func(t *testing.T) { contractReserve(t, h) })
	t.Run("ReserveRace", func(t *testing.T) { contractReserveRace(t, h) })
	t.Run("Usage", func(t *testing.T) { contractUsage(t, h) })
	t.Run("Retention", func(t *testing.T) { contractRetention(t, h) })
}

func TestMemoryStoreContract(t *testing.T) {
	testStore(t, storeHarness{
		st:     NewMemoryStore(),
		newOrg: func(*testing.T) uuid.UUID { return uuid.New() },
	})
}

func newChat(t *testing.T, st Store, org, user uuid.UUID, title string, at time.Time) Chat {
	t.Helper()
	c := Chat{ID: uuid.New(), OrgID: org, UserID: user, Title: title, CreatedAt: at, UpdatedAt: at}
	if err := st.CreateChat(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func contractChats(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	user, user2 := uuid.New(), uuid.New()
	t0 := time.Now().UTC().Truncate(time.Second)
	old := newChat(t, h.st, org, user, "old", t0.Add(-time.Hour))
	cur := newChat(t, h.st, org, user, "new", t0)
	newChat(t, h.st, org, user2, "theirs", t0)

	list, err := h.st.Chats(ctx, org, user, 0)
	if err != nil || len(list) != 2 || list[0].ID != cur.ID || list[1].ID != old.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	if list, _ = h.st.Chats(ctx, org, user, 1); len(list) != 1 || list[0].ID != cur.ID {
		t.Fatalf("limit %+v", list)
	}
	if list, _ = h.st.Chats(ctx, other, user, 0); len(list) != 0 {
		t.Fatalf("other org %+v", list)
	}

	got, msgs, err := h.st.Chat(ctx, org, user, cur.ID)
	if err != nil || got.Title != "new" || got.OrgID != org || got.UserID != user || !got.CreatedAt.Equal(t0) || len(msgs) != 0 {
		t.Fatalf("get %+v %v %v", got, msgs, err)
	}
	for name, f := range map[string]func() error{
		"other user":        func() error { _, _, err := h.st.Chat(ctx, org, user2, cur.ID); return err },
		"other org":         func() error { _, _, err := h.st.Chat(ctx, other, user, cur.ID); return err },
		"unknown":           func() error { _, _, err := h.st.Chat(ctx, org, user, uuid.New()); return err },
		"delete other user": func() error { return h.st.DeleteChat(ctx, org, user2, cur.ID) },
		"delete other org":  func() error { return h.st.DeleteChat(ctx, other, user, cur.ID) },
		"delete unknown":    func() error { return h.st.DeleteChat(ctx, org, user, uuid.New()) },
	} {
		if err := f(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := h.st.DeleteChat(ctx, org, user, cur.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.Chat(ctx, org, user, cur.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func contractMessages(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, user := h.newOrg(t), uuid.New()
	t0 := time.Now().UTC().Truncate(time.Second)
	c := newChat(t, h.st, org, user, "", t0)

	if err := h.st.AppendMessage(ctx, StoredMessage{ID: uuid.New(), ChatID: uuid.New(), Role: "user"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("append to unknown chat: %v", err)
	}
	var ids []uuid.UUID
	for i, role := range []string{"user", "assistant", "user"} {
		m := StoredMessage{ID: uuid.New(), ChatID: c.ID, Role: role, Status: StatusDone, CreatedAt: t0.Add(time.Duration(i+1) * time.Minute)}
		if role == "assistant" {
			m.Native = []Part{{Kind: PartText, Text: "hi"}, {Kind: PartToolCall, Call: &ToolCall{ID: "c1", Name: "list_nodes"}}}
			m.Parts = []Event{newEvent(EventText, map[string]string{"text": "hi"})}
			m.Model = "claude-sonnet-5-5"
			m.Usage = &Usage{Input: 3, Output: 4}
		}
		if err := h.st.AppendMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	got, msgs, err := h.st.Chat(ctx, org, user, c.ID)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("get %v %v", msgs, err)
	}
	for i, m := range msgs {
		if m.Seq != i+1 || m.ID != ids[i] {
			t.Errorf("message %d: seq %d id %s", i, m.Seq, m.ID)
		}
	}
	a := msgs[1]
	if len(a.Native) != 2 || a.Native[1].Call.Name != "list_nodes" || len(a.Parts) != 1 || a.Parts[0].Name != EventText || !jsonEqual(a.Parts[0].Data, `{"text":"hi"}`) ||
		a.Model != "claude-sonnet-5-5" || a.Usage.Output != 4 || a.Role != "assistant" || a.Status != StatusDone {
		t.Errorf("assistant message %+v", a)
	}
	if msgs[0].Native != nil || msgs[0].Parts != nil || msgs[0].Usage != nil {
		t.Errorf("user message %+v", msgs[0])
	}
	if !got.UpdatedAt.Equal(t0.Add(3 * time.Minute)) {
		t.Errorf("updated_at %s", got.UpdatedAt)
	}

	up := a
	up.Parts = []Event{newEvent(EventText, map[string]string{"text": "hello"})}
	up.Status = StatusStopped
	up.Seq = 99
	if err := h.st.UpdateMessage(ctx, up); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpdateMessage(ctx, StoredMessage{ID: uuid.New(), ChatID: c.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}
	_, msgs, _ = h.st.Chat(ctx, org, user, c.ID)
	if !jsonEqual(msgs[1].Parts[0].Data, `{"text":"hello"}`) || msgs[1].Status != StatusStopped || msgs[1].Seq != 2 {
		t.Errorf("after update %+v", msgs[1])
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.st.AppendMessage(ctx, StoredMessage{ID: uuid.New(), ChatID: c.ID, Role: "user"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	_, msgs, _ = h.st.Chat(ctx, org, user, c.ID)
	if len(msgs) != 13 || msgs[12].Seq != 13 {
		t.Errorf("concurrent appends: %d messages", len(msgs))
	}
}

func newProposal(t *testing.T, st Store, org, chat uuid.UUID) Proposal {
	t.Helper()
	p := Proposal{
		ID: uuid.New(), OrgID: org, ChatID: chat, MessageID: uuid.New(), Status: ProposalPending,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Change: Change{Tool: "set_thresholds", Kind: "rules", MinRole: "admin", Backup: true,
			Rows:   []ChangeRow{{ID: "r1", Target: "web-1", Field: "cpuWarn", Old: "80", New: "97", Checked: true}},
			Impact: &Impact{Before: 9, After: 2, Window: "7d"}, Input: []byte(`{"a":1}`)},
	}
	if err := st.SaveProposal(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func contractProposals(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other, user := h.newOrg(t), h.newOrg(t), uuid.New()
	c := newChat(t, h.st, org, user, "", time.Now())
	p := newProposal(t, h.st, org, c.ID)

	got, err := h.st.Proposal(ctx, org, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tool != "set_thresholds" || got.Status != ProposalPending || got.ChatID != c.ID || got.MessageID != p.MessageID ||
		len(got.Rows) != 1 || got.Rows[0].New != "97" || got.Impact.Before != 9 || !jsonEqual(got.Input, `{"a":1}`) ||
		!got.CreatedAt.Equal(p.CreatedAt) || got.Applied != nil || !got.AppliedAt.IsZero() || got.AppliedBy != uuid.Nil {
		t.Fatalf("get %+v", got)
	}
	if _, err := h.st.Proposal(ctx, other, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org: %v", err)
	}
	if _, err := h.st.Proposal(ctx, org, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}

	user2 := uuid.New()
	at := time.Now().UTC().Truncate(time.Second)
	got.Status, got.AppliedBy, got.AppliedAt = ProposalApplied, user2, at
	got.Applied = &Applied{BackupID: uuid.New(), Rows: []string{"r1"}}
	got.Rows = slices.Clone(got.Rows)
	got.Rows[0].Checked = false
	if err := h.st.UpdateProposal(ctx, got, ProposalPending); err != nil {
		t.Fatal(err)
	}
	back, _ := h.st.Proposal(ctx, org, p.ID)
	if back.Status != ProposalApplied || back.AppliedBy != user2 || !back.AppliedAt.Equal(at) ||
		back.Applied.BackupID != got.Applied.BackupID || back.Rows[0].Checked {
		t.Fatalf("applied %+v", back)
	}
	back.Status, back.UndoneAt = ProposalUndone, at
	if err := h.st.UpdateProposal(ctx, back, ProposalApplied); err != nil {
		t.Fatal(err)
	}
	if last, _ := h.st.Proposal(ctx, org, p.ID); last.Status != ProposalUndone || !last.UndoneAt.Equal(at) {
		t.Fatalf("undone %+v", last)
	}

	if err := h.st.UpdateProposal(ctx, Proposal{ID: uuid.New(), OrgID: org}, ProposalPending); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unknown: %v", err)
	}
	back.OrgID = other
	if err := h.st.UpdateProposal(ctx, back, ProposalUndone); !errors.Is(err, ErrNotFound) {
		t.Errorf("update other org: %v", err)
	}
}

func contractProposalClaim(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, user := h.newOrg(t), uuid.New()
	c := newChat(t, h.st, org, user, "", time.Now())
	p := newProposal(t, h.st, org, c.ID)

	claim := p
	claim.Status = ProposalApplying
	if err := h.st.UpdateProposal(ctx, claim, ProposalApplied); !errors.Is(err, ErrNotPending) {
		t.Fatalf("wrong from: %v", err)
	}
	if got, _ := h.st.Proposal(ctx, org, p.ID); got.Status != ProposalPending {
		t.Fatalf("status %s after a lost claim", got.Status)
	}

	var (
		mu           sync.Mutex
		wg           sync.WaitGroup
		wins, losses int
	)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := h.st.UpdateProposal(ctx, claim, ProposalPending)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrNotPending):
				losses++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || losses != 9 {
		t.Fatalf("claims: %d won, %d lost", wins, losses)
	}
}

func contractReserve(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, user, user2 := h.newOrg(t), uuid.New(), uuid.New()
	at := time.Date(2026, 11, 25, 10, 30, 0, 0, time.UTC)
	dayEnd := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	r := Reservation{OrgID: org, UserID: user, At: at, PerDay: 2, PerMonth: 3}

	v, err := h.st.Reserve(ctx, r)
	if err != nil || v.Day.Used != 1 || v.Month.Used != 1 || v.Day.Limit != 2 || v.Month.Limit != 3 {
		t.Fatalf("first %+v %v", v, err)
	}
	if !v.Day.ResetAt.Equal(dayEnd) || !v.Month.ResetAt.Equal(monthEnd) {
		t.Errorf("reset %s %s", v.Day.ResetAt, v.Month.ResetAt)
	}
	if v, err = h.st.Reserve(ctx, r); err != nil || v.Day.Used != 2 || v.Month.Used != 2 {
		t.Fatalf("second %+v %v", v, err)
	}

	var le *LimitError
	_, err = h.st.Reserve(ctx, r)
	if !errors.Is(err, ErrLimit) || !errors.As(err, &le) || le.Scope != "day" || le.Used != 2 || le.Limit != 2 || !le.ResetAt.Equal(dayEnd) {
		t.Fatalf("day limit: %v %+v", err, le)
	}

	// Another user has a day of their own and shares the month.
	r2 := r
	r2.UserID = user2
	if _, err = h.st.Reserve(ctx, r2); err != nil {
		t.Fatal(err)
	}
	_, err = h.st.Reserve(ctx, r2)
	if !errors.As(err, &le) || le.Scope != "month" || le.Used != 3 || le.Limit != 3 || !le.ResetAt.Equal(monthEnd) {
		t.Fatalf("month limit: %v %+v", err, le)
	}
	// A refused question does not count.
	if u, _ := h.st.Usage(ctx, org, user2, at); u.Day.Used != 1 || u.Month.Used != 3 {
		t.Errorf("usage after refusal %+v", u)
	}

	// The next day has a new day count. The month stays full.
	r3 := r
	r3.At = at.Add(24 * time.Hour)
	if _, err = h.st.Reserve(ctx, r3); !errors.As(err, &le) || le.Scope != "month" {
		t.Fatalf("next day: %v", err)
	}
	// The next month starts at zero. A zero limit never refuses.
	r4 := Reservation{OrgID: org, UserID: user, At: monthEnd}
	for range 5 {
		if _, err = h.st.Reserve(ctx, r4); err != nil {
			t.Fatalf("no limit: %v", err)
		}
	}
}

func contractReserveRace(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, user := h.newOrg(t), uuid.New()
	r := Reservation{OrgID: org, UserID: user, At: time.Now().UTC(), PerDay: 5}
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		ok, limited int
	)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.st.Reserve(ctx, r)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrLimit):
				limited++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != 5 || limited != 15 {
		t.Fatalf("%d reserved, %d refused", ok, limited)
	}
	if u, _ := h.st.Usage(ctx, org, user, r.At); u.Day.Used != 5 || u.Month.Used != 5 {
		t.Errorf("usage %+v", u)
	}
}

func contractUsage(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, user := h.newOrg(t), uuid.New()
	at := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	if u, err := h.st.Usage(ctx, org, user, at); err != nil || u.Day.Used != 0 || u.Month.Used != 0 {
		t.Fatalf("empty %+v %v", u, err)
	}
	// Tokens and tool calls do not move the question count.
	for range 2 {
		if err := h.st.AddUsage(ctx, org, user, at, 100, 3); err != nil {
			t.Fatal(err)
		}
	}
	if u, _ := h.st.Usage(ctx, org, user, at); u.Day.Used != 0 || u.Month.Used != 0 {
		t.Fatalf("after add %+v", u)
	}
	if _, err := h.st.Reserve(ctx, Reservation{OrgID: org, UserID: user, At: at}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AddUsage(ctx, org, user, at, 1, 1); err != nil {
		t.Fatal(err)
	}
	u, err := h.st.Usage(ctx, org, user, at)
	if err != nil || u.Day.Used != 1 || u.Month.Used != 1 {
		t.Fatalf("usage %+v %v", u, err)
	}
}

func contractRetention(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other, user := h.newOrg(t), h.newOrg(t), uuid.New()
	t0 := time.Now().UTC().Truncate(time.Second)
	old := newChat(t, h.st, org, user, "old", t0.Add(-48*time.Hour))
	fresh := newChat(t, h.st, org, user, "fresh", t0)
	keep := newChat(t, h.st, other, user, "other org", t0.Add(-48*time.Hour))
	if err := h.st.AppendMessage(ctx, StoredMessage{ID: uuid.New(), ChatID: old.ID, Role: "user", CreatedAt: t0.Add(-47 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	p := newProposal(t, h.st, org, old.ID)
	kept := newProposal(t, h.st, org, fresh.ID)

	orgs, err := h.st.OrgsWithChats(ctx)
	if err != nil || !slices.Contains(orgs, org) || !slices.Contains(orgs, other) {
		t.Fatalf("orgs %v %v", orgs, err)
	}
	n, err := h.st.DeleteChatsBefore(ctx, org, t0.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("deleted %d %v", n, err)
	}
	if _, _, err := h.st.Chat(ctx, org, user, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old chat: %v", err)
	}
	if _, err := h.st.Proposal(ctx, org, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old proposal: %v", err)
	}
	if err := h.st.AppendMessage(ctx, StoredMessage{ID: uuid.New(), ChatID: old.ID, Role: "user"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("append to deleted chat: %v", err)
	}
	if _, _, err := h.st.Chat(ctx, org, user, fresh.ID); err != nil {
		t.Errorf("fresh chat: %v", err)
	}
	if _, err := h.st.Proposal(ctx, org, kept.ID); err != nil {
		t.Errorf("fresh proposal: %v", err)
	}
	if _, _, err := h.st.Chat(ctx, other, user, keep.ID); err != nil {
		t.Errorf("other org chat: %v", err)
	}
}
