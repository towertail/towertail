package control

import (
	"context"

	"github.com/google/uuid"

	"github.com/towertail/server/internal/store"
)

// Queue wraps the ConfigStore's control helpers with a thin,
// typed surface so callers don't have to carry a generic ConfigStore
// reference just for enqueue/claim.
type Queue struct {
	store store.ConfigStore
}

func New(s store.ConfigStore) *Queue { return &Queue{store: s} }

func (q *Queue) Enqueue(ctx context.Context, nodeID uuid.UUID, kind string, payload []byte) error {
	msg := store.ControlMessage{
		NodeID:  nodeID,
		Kind:    kind,
		Payload: payload,
	}
	return q.store.EnqueueControl(ctx, msg)
}

func (q *Queue) Claim(ctx context.Context, nodeID uuid.UUID) (store.ControlMessage, error) {
	return q.store.ClaimControl(ctx, nodeID)
}
