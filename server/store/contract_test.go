package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// storeHarness is one store under test. newOrg gives an org that the
// store accepts, so each test has its own data.
type storeHarness struct {
	s      ConfigStore
	newOrg func(t *testing.T) uuid.UUID
}

// testContract runs the same tests on each ConfigStore.
func testContract(t *testing.T, h storeHarness) {
	t.Run("Nodes", func(t *testing.T) { contractNodes(t, h) })
	t.Run("Settings", func(t *testing.T) { contractSettings(t, h) })
	t.Run("Tokens", func(t *testing.T) { contractTokens(t, h) })
	t.Run("Users", func(t *testing.T) { contractUsers(t, h) })
	t.Run("Control", func(t *testing.T) { contractControl(t, h) })
	t.Run("Channels", func(t *testing.T) { contractChannels(t, h) })
}

func contractNodes(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	user, until := "root", time.Now().UTC().Truncate(time.Second)
	n := Node{
		ID: uuid.New(), DisplayName: "web-1", Kind: "ssh", SSHUser: &user, Tags: []string{"prod"},
		Enabled: true, NotifyOnCritical: true, SnoozedUntil: &until,
		CustomThresholds: &MetricThresholds{CPUWarn: 0.5},
	}
	if err := h.s.PutNode(ctx, org, Node{}); err == nil {
		t.Error("put without id: want error")
	}
	if err := h.s.PutNode(ctx, org, n); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := h.s.GetNode(ctx, org, n.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "web-1" || *got.SSHUser != "root" || got.Tags[0] != "prod" ||
		got.CustomThresholds.CPUWarn != 0.5 || !got.SnoozedUntil.Equal(until) {
		t.Errorf("get: %+v", got)
	}
	n.DisplayName = "web-2"
	if err := h.s.PutNode(ctx, org, n); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := h.s.PutNode(ctx, org, Node{ID: uuid.New(), DisplayName: "db-1"}); err != nil {
		t.Fatal(err)
	}
	list, err := h.s.ListNodes(ctx, org)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %d nodes, %v", len(list), err)
	}
	if _, err := h.s.GetNode(ctx, other, n.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get from other org: %v, want ErrNotFound", err)
	}
	if list, _ := h.s.ListNodes(ctx, other); len(list) != 0 {
		t.Errorf("other org: %d nodes", len(list))
	}
	if err := h.s.DeleteNode(ctx, org, n.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := h.s.GetNode(ctx, org, n.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := h.s.DeleteNode(ctx, org, n.ID); err != nil {
		t.Errorf("delete twice: %v", err)
	}
}

func contractSettings(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	got, err := h.s.GetSettings(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if got != DefaultServerSettings() {
		t.Errorf("first read: %+v, want defaults", got)
	}
	got.Thresholds.CPUWarn = 0.5
	got.NotifyCooldownSeconds = 30
	if err := h.s.PutSettings(ctx, org, got); err != nil {
		t.Fatal(err)
	}
	got.NotifyCooldownSeconds = 60
	if err := h.s.PutSettings(ctx, org, got); err != nil {
		t.Fatal(err)
	}
	if again, _ := h.s.GetSettings(ctx, org); again != got {
		t.Errorf("read: %+v, want %+v", again, got)
	}
	if o, _ := h.s.GetSettings(ctx, other); o != DefaultServerSettings() {
		t.Errorf("other org: %+v", o)
	}
}

func contractTokens(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	node := uuid.New()
	tok := Token{
		ID: uuid.New(), OrgID: org, Kind: TokenKindSampler, Label: "web-1",
		Hash: []byte(uuid.NewString()), NodeID: &node, CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := h.s.PutToken(ctx, Token{ID: uuid.New(), OrgID: org}); err == nil {
		t.Error("put without hash: want error")
	}
	if err := h.s.PutToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if err := h.s.PutToken(ctx, Token{ID: uuid.New(), OrgID: other, Kind: TokenKindUser, Hash: []byte(uuid.NewString())}); err != nil {
		t.Fatal(err)
	}
	got, err := h.s.GetTokenByHash(ctx, tok.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tok.ID || got.OrgID != org || got.Kind != TokenKindSampler || got.Label != "web-1" ||
		*got.NodeID != node || !got.CreatedAt.Equal(tok.CreatedAt) || got.RevokedAt != nil {
		t.Errorf("get: %+v", got)
	}

	revoked := time.Now().UTC().Truncate(time.Millisecond)
	got.RevokedAt = &revoked
	if err := h.s.PutToken(ctx, got); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got, _ := h.s.GetTokenByHash(ctx, tok.Hash); got.RevokedAt == nil || !got.RevokedAt.Equal(revoked) {
		t.Errorf("revoked_at: %v", got.RevokedAt)
	}

	list, err := h.s.ListTokens(ctx, org)
	if err != nil || len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if err := h.s.DeleteToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetTokenByHash(ctx, tok.Hash); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := h.s.DeleteToken(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v, want ErrNotFound", err)
	}
}

func contractUsers(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	email := uuid.NewString() + "@example.com"
	if _, err := h.s.GetUserByEmail(ctx, email); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user: %v", err)
	}
	if err := h.s.PutUser(ctx, User{OrgID: org, Email: email, PasswordHash: "h1"}); err != nil {
		t.Fatal(err)
	}
	u, err := h.s.GetUserByEmail(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == uuid.Nil || u.OrgID != org || u.PasswordHash != "h1" || u.CreatedAt.IsZero() {
		t.Errorf("get: %+v", u)
	}
	u.OrgID, u.PasswordHash = other, "h2"
	if err := h.s.PutUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.s.GetUserByEmail(ctx, email); got.ID != u.ID || got.OrgID != other || got.PasswordHash != "h2" {
		t.Errorf("update: %+v", got)
	}
}

func contractControl(t *testing.T, h storeHarness) {
	ctx := context.Background()
	node, other := uuid.New(), uuid.New()
	base := time.Now().UTC()
	for i, kind := range []string{"kill_process", "disk_scan"} {
		msg := ControlMessage{NodeID: node, Kind: kind, Payload: []byte(`{"i":1}`), CreatedAt: base.Add(time.Duration(i) * time.Second)}
		if err := h.s.EnqueueControl(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.s.ClaimControl(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Errorf("other node: %v", err)
	}
	for _, want := range []string{"kill_process", "disk_scan"} {
		m, err := h.s.ClaimControl(ctx, node)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if m.Kind != want || m.ID == uuid.Nil || string(m.Payload) != `{"i":1}` {
			t.Errorf("claim: %+v, want %s", m, want)
		}
	}
	if _, err := h.s.ClaimControl(ctx, node); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty queue: %v", err)
	}

	// Two callers at the same time never get the same message.
	const n = 20
	for range n {
		if err := h.s.EnqueueControl(ctx, ControlMessage{NodeID: node, Kind: "disk_scan"}); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu   sync.Mutex
		seen = map[uuid.UUID]bool{}
		wg   sync.WaitGroup
	)
	for range 4 {
		wg.Go(func() {
			for {
				m, err := h.s.ClaimControl(ctx, node)
				if err != nil {
					return
				}
				mu.Lock()
				if seen[m.ID] {
					t.Errorf("message %s claimed twice", m.ID)
				}
				seen[m.ID] = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("claimed %d of %d", len(seen), n)
	}
}

func contractChannels(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	ch := Channel{
		ID: uuid.New(), OrgID: org, Kind: ChannelWebhook, Name: "ops", Enabled: true,
		Target: "https://example.com/hook", Secret: "s3cret",
		Route:     ChannelRoute{MinTint: "critical", Metrics: []string{"cpu"}, Tags: []string{"prod"}},
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := h.s.PutChannel(ctx, Channel{OrgID: org}); err == nil {
		t.Error("put without id: want error")
	}
	if err := h.s.PutChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	got, err := h.s.GetChannel(ctx, org, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != "s3cret" || got.Route.Tags[0] != "prod" || got.Route.MinTint != "critical" || !got.CreatedAt.Equal(ch.CreatedAt) {
		t.Errorf("get: %+v", got)
	}
	ch.Name = "ops-2"
	if err := h.s.PutChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	list, err := h.s.ListChannels(ctx, org)
	if err != nil || len(list) != 1 || list[0].Name != "ops-2" {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if _, err := h.s.GetChannel(ctx, other, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other org: %v", err)
	}
	if err := h.s.DeleteChannel(ctx, other, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete from other org: %v", err)
	}
	if err := h.s.DeleteChannel(ctx, org, ch.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := h.s.ListChannels(ctx, org); len(list) != 0 {
		t.Errorf("after delete: %d", len(list))
	}
}
