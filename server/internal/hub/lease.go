package hub

import (
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

const (
	defaultTTL   = 60 * time.Second
	minTTL       = 10 * time.Second
	maxTTL       = 120 * time.Second
	renewEvery   = 30 * time.Second
	maxViewHosts = 500
)

// lease is the view that a lease client has asked for.
type lease struct {
	hosts   map[uuid.UUID]struct{}
	detail  *wire.ViewDetail
	expires time.Time
	renewed time.Time
}

func (l *lease) active(now time.Time) bool {
	return l != nil && now.Before(l.expires) && (len(l.hosts) > 0 || l.detail != nil)
}

func (l *lease) covers(node uuid.UUID, now time.Time) bool {
	if !l.active(now) {
		return false
	}
	if _, ok := l.hosts[node]; ok {
		return true
	}
	return l.detail != nil && l.detail.Host == node
}

func (l *lease) wantsDetail(node uuid.UUID, now time.Time) (wire.DetailTab, bool) {
	if !l.active(now) || l.detail == nil || l.detail.Host != node {
		return "", false
	}
	return l.detail.Tab, true
}

// clampTTL turns the ttl of a view (seconds) into a duration.
func clampTTL(ttl int) time.Duration {
	if ttl == 0 {
		return defaultTTL
	}
	return min(max(time.Duration(ttl)*time.Second, minTTL), maxTTL)
}

// newLease builds the lease of a view frame. It returns nil for an empty
// view.
func newLease(m wire.ClientMessage, now time.Time) *lease {
	l := &lease{hosts: map[uuid.UUID]struct{}{}, renewed: now, expires: now.Add(clampTTL(m.TTL))}
	for _, id := range m.Hosts[:min(len(m.Hosts), maxViewHosts)] {
		l.hosts[id] = struct{}{}
	}
	if d := m.Detail; d != nil && d.Tab.Valid() && d.Host != uuid.Nil {
		l.detail = &wire.ViewDetail{Host: d.Host, Tab: d.Tab}
	}
	if len(l.hosts) == 0 && l.detail == nil {
		return nil
	}
	return l
}
