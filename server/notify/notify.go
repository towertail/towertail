// Package notify holds the contract between the alerter and the alert
// channels. TOWERTAIL-28 adds the open-source channels.
package notify

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// Alert is one change of tint for one metric of one node.
type Alert struct {
	OrgID    uuid.UUID
	NodeID   uuid.UUID
	NodeName string
	Metric   string // cpu, mem, disk, reachability
	From, To wire.Tint
	Value    float64
	At       time.Time
}

// Notifier sends an Alert to one channel.
type Notifier interface {
	Name() string
	Notify(ctx context.Context, a Alert) error
}
