package assistant

import (
	"context"
	"testing"

	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/store"
)

func TestActorCtxHasVia(t *testing.T) {
	ctx := Actor{Role: store.RoleMember}.ctx(context.Background())
	if via, ok := mcp.ViaFrom(ctx); !ok || via != "assistant" {
		t.Fatalf("via = %q, %v", via, ok)
	}
}
