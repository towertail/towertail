package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTestStore(t *testing.T) *BoltStore {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenBolt(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestBolt_NodeRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	org := ZeroOrg
	n := Node{ID: uuid.New(), DisplayName: "web-1", Kind: "ssh", Enabled: true}
	if err := s.PutNode(ctx, org, n); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.GetNode(ctx, org, n.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "web-1" {
		t.Errorf("got %s", got.DisplayName)
	}
	list, err := s.ListNodes(ctx, org)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 node, got %d", len(list))
	}
	if err := s.DeleteNode(ctx, org, n.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = s.GetNode(ctx, org, n.ID)
	if err == nil {
		t.Error("expected not found after delete")
	}
}

func TestBolt_Settings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	org := ZeroOrg
	cfg, err := s.GetSettings(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Thresholds.CPUWarn == 0 {
		t.Error("expected default thresholds on first read")
	}
	cfg.Thresholds.CPUWarn = 0.5
	if err := s.PutSettings(ctx, org, cfg); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSettings(ctx, org)
	if got.Thresholds.CPUWarn != 0.5 {
		t.Errorf("got %v", got.Thresholds.CPUWarn)
	}
}

func TestBolt_TokenRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	hash := []byte("fake-hash-fake-hash-fake-hash-12") // 32 bytes
	tok := Token{
		ID:        uuid.New(),
		OrgID:     ZeroOrg,
		Kind:      TokenKindSampler,
		Hash:      hash,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.PutToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTokenByHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tok.ID {
		t.Errorf("mismatch: %v", got)
	}
	if err := s.DeleteToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetTokenByHash(ctx, hash)
	if err == nil {
		t.Error("expected not found after delete")
	}
}

func TestBolt_ControlQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	nodeID := uuid.New()
	payload := []byte(`{"pid":123}`)

	if err := s.EnqueueControl(ctx, ControlMessage{
		NodeID:  nodeID,
		Kind:    "kill_process",
		Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	msg, err := s.ClaimControl(ctx, nodeID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if msg.Kind != "kill_process" {
		t.Errorf("kind: %s", msg.Kind)
	}
	// Second claim should find nothing.
	_, err = s.ClaimControl(ctx, nodeID)
	if err == nil {
		t.Error("expected empty queue")
	}
}
