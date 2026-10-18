package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
)

// Sampler is a node with its sampler token.
type Sampler struct {
	NodeID uuid.UUID
	Token  string
}

// IssueSamplers makes n nodes in the org, each with one sampler token.
// The load generator and the cloud test org use it. The node names are
// prefix-1 to prefix-n.
func IssueSamplers(ctx context.Context, st store.ConfigStore, orgID uuid.UUID, n int, prefix string) ([]Sampler, error) {
	issuer := auth.NewIssuer(st)
	out := make([]Sampler, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("%s-%d", prefix, i)
		node := store.Node{
			ID:               uuid.New(),
			DisplayName:      name,
			Kind:             "ssh",
			Enabled:          true,
			IconOnWarn:       true,
			IconOnCritical:   true,
			NotifyOnWarn:     true,
			NotifyOnCritical: true,
		}
		if err := st.PutNode(ctx, orgID, node); err != nil {
			return out, fmt.Errorf("node %s: %w", name, err)
		}
		raw, _, err := issuer.Issue(ctx, orgID, store.TokenKindSampler, name, &node.ID)
		if err != nil {
			return out, fmt.Errorf("token %s: %w", name, err)
		}
		out = append(out, Sampler{NodeID: node.ID, Token: raw})
	}
	return out, nil
}
