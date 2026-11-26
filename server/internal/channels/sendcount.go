package channels

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SendCounter counts the email sends of one rule.
type SendCounter interface {
	RuleSends(ctx context.Context, q RuleSendsQuery) (int, error)
}

// RuleSendsQuery selects the sends of one rule on one host. Metric is the
// base metric. Channels are the names of the email channels of the org.
type RuleSendsQuery struct {
	OrgID, NodeID uuid.UUID
	Metric        string
	Channels      []string
	Since         time.Time
}

type sendKey struct {
	org, node uuid.UUID
	metric    string
}

type sendEntry struct {
	n       int
	expires time.Time
}

// sendCache keeps the counts for a short time, so an alert storm does not
// run one query for each email.
type sendCache struct {
	mu      sync.Mutex
	entries map[sendKey]sendEntry
	ttl     time.Duration
	max     int
	now     func() time.Time
}

func newSendCache(ttl time.Duration, max int, now func() time.Time) *sendCache {
	return &sendCache{entries: map[sendKey]sendEntry{}, ttl: ttl, max: max, now: now}
}

func (c *sendCache) get(k sendKey) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || !c.now().Before(e.expires) {
		return 0, false
	}
	return e.n, true
}

func (c *sendCache) put(k sendKey, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[k]; !ok && len(c.entries) >= c.max {
		clear(c.entries)
	}
	c.entries[k] = sendEntry{n: n, expires: c.now().Add(c.ttl)}
}

// add adds n to an entry that exists. The expiry does not change.
func (c *sendCache) add(k sendKey, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[k]; ok {
		e.n += n
		c.entries[k] = e
	}
}
