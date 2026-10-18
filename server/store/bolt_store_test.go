package store

import (
	"testing"

	"github.com/google/uuid"
)

func TestBoltContract(t *testing.T) {
	s, err := OpenBolt(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	testContract(t, storeHarness{s: s, newOrg: func(*testing.T) uuid.UUID { return uuid.New() }})
}
