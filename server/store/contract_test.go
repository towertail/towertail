package store

import (
	"context"
	"errors"
	"reflect"
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
	t.Run("EditNode", func(t *testing.T) { contractEditNode(t, h) })
	t.Run("EditSettings", func(t *testing.T) { contractEditSettings(t, h) })
	t.Run("Rules", func(t *testing.T) { contractRules(t, h) })
	t.Run("Tokens", func(t *testing.T) { contractTokens(t, h) })
	t.Run("OAuthTokens", func(t *testing.T) { contractOAuthTokens(t, h) })
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
		ThresholdOverrides: &ThresholdOverrides{CPU: &ThresholdPair{Warn: 0.5, Critical: 0.6}},
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
		got.ThresholdOverrides.CPU.Warn != 0.5 || !got.SnoozedUntil.Equal(until) {
		t.Errorf("get: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at is not set")
	}
	n.DisplayName = "web-2"
	time.Sleep(10 * time.Millisecond)
	if err := h.s.PutNode(ctx, org, n); err != nil {
		t.Fatalf("update: %v", err)
	}
	if again, _ := h.s.GetNode(ctx, org, n.ID); !again.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("created_at changed: %v, want %v", again.CreatedAt, got.CreatedAt)
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
	m := Node{ID: uuid.New(), DisplayName: "mach", MachineID: "machine-1"}
	if err := h.s.PutNode(ctx, org, m); err != nil {
		t.Fatal(err)
	}
	found, err := h.s.FindNodeByMachineID(ctx, org, "machine-1")
	if err != nil || found.ID != m.ID || found.MachineID != "machine-1" {
		t.Errorf("find: %+v, %v", found, err)
	}
	if _, err := h.s.FindNodeByMachineID(ctx, org, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("find unknown: %v, want ErrNotFound", err)
	}
	if _, err := h.s.FindNodeByMachineID(ctx, other, "machine-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("find in other org: %v, want ErrNotFound", err)
	}
	if _, err := h.s.FindNodeByMachineID(ctx, org, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("find empty: %v, want ErrNotFound", err)
	}
	if err := h.s.DeleteNode(ctx, org, m.ID); err != nil {
		t.Fatal(err)
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
	if !reflect.DeepEqual(got, DefaultServerSettings()) {
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
	if again, _ := h.s.GetSettings(ctx, org); !reflect.DeepEqual(again, got) {
		t.Errorf("read: %+v, want %+v", again, got)
	}
	if o, _ := h.s.GetSettings(ctx, other); !reflect.DeepEqual(o, DefaultServerSettings()) {
		t.Errorf("other org: %+v", o)
	}
}

// Two parallel edits keep both changes.
func contractEditNode(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	n := Node{ID: uuid.New(), DisplayName: "web-1", Kind: "ssh", MachineID: "m-edit"}
	if err := h.s.PutNode(ctx, org, n); err != nil {
		t.Fatal(err)
	}
	before, _ := h.s.GetNode(ctx, org, n.ID)
	var wg sync.WaitGroup
	for _, name := range []string{"node", "vitest"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.s.EditNode(ctx, org, n.ID, func(n *Node) error {
				n.IgnoredProcs = append(n.IgnoredProcs, name)
				return nil
			})
			if err != nil {
				t.Errorf("edit %s: %v", name, err)
			}
		}()
	}
	wg.Wait()
	got, _ := h.s.GetNode(ctx, org, n.ID)
	if len(got.IgnoredProcs) != 2 || got.DisplayName != "web-1" || got.MachineID != "m-edit" || !got.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("after edits: %+v", got)
	}
	boom := errors.New("boom")
	if _, err := h.s.EditNode(ctx, org, n.ID, func(n *Node) error { n.DisplayName = "x"; return boom }); !errors.Is(err, boom) {
		t.Errorf("edit with error: %v", err)
	}
	if again, _ := h.s.GetNode(ctx, org, n.ID); again.DisplayName != "web-1" {
		t.Errorf("a failed edit wrote: %+v", again)
	}
	if _, err := h.s.EditNode(ctx, other, n.ID, func(*Node) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("edit in other org: %v, want ErrNotFound", err)
	}
}

func contractEditSettings(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org := h.newOrg(t)
	var wg sync.WaitGroup
	for _, name := range []string{"node", "vitest"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.s.EditSettings(ctx, org, func(s *ServerSettings) error {
				s.LongProc.Ignore = append(s.LongProc.Ignore, name)
				return nil
			})
			if err != nil {
				t.Errorf("edit %s: %v", name, err)
			}
		}()
	}
	wg.Wait()
	got, _ := h.s.GetSettings(ctx, org)
	if len(got.LongProc.Ignore) != 2 || !got.NotifyWarn {
		t.Errorf("after edits: %+v", got)
	}
	boom := errors.New("boom")
	if _, err := h.s.EditSettings(ctx, org, func(s *ServerSettings) error { s.NotifyWarn = false; return boom }); !errors.Is(err, boom) {
		t.Errorf("edit with error: %v", err)
	}
	if again, _ := h.s.GetSettings(ctx, org); !again.NotifyWarn {
		t.Errorf("a failed edit wrote: %+v", again)
	}
}

func contractTokens(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	node := uuid.New()
	user := User{OrgID: org, Email: uuid.NewString() + "@example.com"}
	if err := h.s.PutUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	user, _ = h.s.GetUserByEmail(ctx, user.Email)
	tok := Token{
		ID: uuid.New(), OrgID: org, Kind: TokenKindSampler, Label: "web-1",
		Hash: []byte(uuid.NewString()), NodeID: &node, UserID: &user.ID, CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
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
		*got.NodeID != node || *got.UserID != user.ID || !got.CreatedAt.Equal(tok.CreatedAt) || got.RevokedAt != nil {
		t.Errorf("get: %+v", got)
	}

	touched := time.Now().UTC().Truncate(time.Millisecond).Add(time.Minute)
	if err := h.s.TouchToken(ctx, tok.Hash, touched); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if got, _ := h.s.GetTokenByHash(ctx, tok.Hash); got.LastUsedAt == nil || !got.LastUsedAt.Equal(touched) || got.Label != "web-1" {
		t.Errorf("after touch: %+v, want LastUsedAt %v", got, touched)
	}
	if err := h.s.TouchToken(ctx, []byte("unknown"), touched); err != nil {
		t.Errorf("touch of an unknown hash: %v", err)
	}

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	join := Token{ID: uuid.New(), OrgID: org, Kind: TokenKindJoin, Hash: []byte(uuid.NewString()), ExpiresAt: &exp}
	if err := h.s.PutToken(ctx, join); err != nil {
		t.Fatal(err)
	}
	if got, err := h.s.GetTokenByHash(ctx, join.Hash); err != nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.Kind != TokenKindJoin {
		t.Errorf("expires_at: %+v, %v", got, err)
	}
	if got, _ := h.s.GetTokenByHash(ctx, tok.Hash); got.ExpiresAt != nil {
		t.Errorf("token without expiry: %v", got.ExpiresAt)
	}
	if err := h.s.DeleteToken(ctx, join.ID); err != nil {
		t.Fatal(err)
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

func contractRules(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, other := h.newOrg(t), h.newOrg(t)
	a, b, foreign := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{a, b} {
		if err := h.s.PutNode(ctx, org, Node{ID: id, DisplayName: "n-" + id.String()[:4]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.PutNode(ctx, other, Node{ID: foreign}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	origin := RulesOrigin{Client: "mac", Device: "mbp", UserID: uuid.New(), At: at}

	orgRules := DefaultServerSettings().Rules()
	orgRules.Thresholds.CPUWarn = 0.6
	orgRules.Alerts.CPU = AlertRule{SustainSeconds: 60, Notify: NotifyAll}
	orgRules.Muted = MutedSignals{MetricZombies}
	hostA := HostRules{
		Thresholds: &ThresholdOverrides{Mem: &ThresholdPair{Warn: 0.85, Critical: 0.95}},
		Muted:      MutedSignals{MetricDisk},
	}
	bk, err := h.s.ReplaceRules(ctx, org, RulesChange{Org: &orgRules, Hosts: map[uuid.UUID]HostRules{a: hostA, b: {}}, Origin: origin})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	cfg, _ := h.s.GetSettings(ctx, org)
	if cfg.Thresholds.CPUWarn != 0.6 || cfg.Alerts.CPU.SustainSeconds != 60 || !cfg.Muted.Has(MetricZombies) ||
		cfg.RulesOrigin == nil || cfg.RulesOrigin.Client != "mac" || !cfg.RulesOrigin.At.Equal(at) {
		t.Errorf("settings: %+v", cfg)
	}
	na, _ := h.s.GetNode(ctx, org, a)
	if na.ThresholdOverrides == nil || na.ThresholdOverrides.Mem.Warn != 0.85 || !na.MutedSignals.Has(MetricDisk) ||
		na.RulesOrigin == nil || na.RulesOrigin.Device != "mbp" {
		t.Errorf("node a: %+v", na)
	}
	if nb, _ := h.s.GetNode(ctx, org, b); nb.RulesOrigin == nil || nb.ThresholdOverrides != nil {
		t.Errorf("node b: %+v", nb)
	}
	if bk.Org == nil || bk.Org.Thresholds.CPUWarn != 0.75 || bk.Hosts[a].Thresholds != nil {
		t.Errorf("backup has the new values: %+v", bk)
	}
	got, err := h.s.GetRulesBackup(ctx, org, bk.ID)
	if err != nil || got.Org == nil || got.Org.Thresholds.CPUWarn != 0.75 || got.Origin.Client != "mac" {
		t.Errorf("get backup: %+v, %v", got, err)
	}
	if _, err := h.s.GetRulesBackup(ctx, other, bk.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("backup of other org: %v, want ErrNotFound", err)
	}

	// A host of another org: nothing is written.
	bad := orgRules
	bad.Thresholds.CPUWarn = 0.3
	_, err = h.s.ReplaceRules(ctx, org, RulesChange{Org: &bad, Hosts: map[uuid.UUID]HostRules{foreign: {}}, Origin: origin})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign host: %v, want ErrNotFound", err)
	}
	if cfg, _ := h.s.GetSettings(ctx, org); cfg.Thresholds.CPUWarn != 0.6 {
		t.Errorf("a failed replace wrote: %+v", cfg.Thresholds)
	}
	bad.Thresholds.CPUWarn = 0.99
	if _, err := h.s.ReplaceRules(ctx, org, RulesChange{Org: &bad, Origin: origin}); !errors.Is(err, ErrInvalidRules) {
		t.Errorf("warn over critical: %v, want ErrInvalidRules", err)
	}

	// An org with no org rules in the change keeps its settings.
	if _, err := h.s.ReplaceRules(ctx, org, RulesChange{Hosts: map[uuid.UUID]HostRules{b: {Muted: MutedSignals{MetricCPU}}}, Origin: origin}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := h.s.GetSettings(ctx, org); cfg.Thresholds.CPUWarn != 0.6 {
		t.Errorf("settings changed: %+v", cfg.Thresholds)
	}

	// A backup older than the TTL is gone after the next replace.
	old := origin
	old.At = at.Add(-RulesBackupTTL - 24*time.Hour)
	oldBk, err := h.s.ReplaceRules(ctx, org, RulesChange{Hosts: map[uuid.UUID]HostRules{b: {}}, Origin: old})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.ReplaceRules(ctx, org, RulesChange{Hosts: map[uuid.UUID]HostRules{b: {}}, Origin: origin}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetRulesBackup(ctx, org, oldBk.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old backup: %v, want ErrNotFound", err)
	}
	list, err := h.s.ListRulesBackups(ctx, org)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %d backups, %v", len(list), err)
	}
	if list[0].At.Before(list[1].At) {
		t.Error("list is not newest first")
	}
}

func contractOAuthTokens(t *testing.T, h storeHarness) {
	ctx := context.Background()
	org, grant := h.newOrg(t), uuid.New()
	exp := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	tok := Token{
		ID: uuid.New(), OrgID: org, Kind: TokenKindOAuth, Label: "Claude",
		Hash: []byte(uuid.NewString()), ExpiresAt: &exp,
		Scopes: []Scope{ScopeAlertsWrite, ScopeFleetRead}, Audience: "https://api.example.com/mcp", GrantID: &grant,
	}
	plain := Token{ID: uuid.New(), OrgID: org, Kind: TokenKindAPI, Hash: []byte(uuid.NewString())}
	for _, tk := range []Token{tok, plain} {
		if err := h.s.PutToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.s.GetTokenByHash(ctx, tok.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Scopes, tok.Scopes) || got.Audience != tok.Audience || got.GrantID == nil || *got.GrantID != grant {
		t.Errorf("get: %+v", got)
	}
	list, err := h.s.ListTokens(ctx, org)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	for _, tk := range list {
		if tk.ID == tok.ID && (len(tk.Scopes) != 2 || tk.Audience == "" || tk.GrantID == nil) {
			t.Errorf("list: %+v", tk)
		}
	}
	got, err = h.s.GetTokenByHash(ctx, plain.Hash)
	if err != nil || len(got.Scopes) != 0 || got.Audience != "" || got.GrantID != nil {
		t.Errorf("plain token: %+v %v", got, err)
	}
}
