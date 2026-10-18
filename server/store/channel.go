package store

import (
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/wire"
)

// ChannelKind is the type of an alert channel.
type ChannelKind string

const (
	ChannelSlack     ChannelKind = "slack"
	ChannelDiscord   ChannelKind = "discord"
	ChannelPagerDuty ChannelKind = "pagerduty"
	ChannelWebhook   ChannelKind = "webhook"
	ChannelEmail     ChannelKind = "email"
)

// Channel is one alert destination of an org.
type Channel struct {
	ID      uuid.UUID   `json:"id"`
	OrgID   uuid.UUID   `json:"orgId"`
	Kind    ChannelKind `json:"kind"`
	Name    string      `json:"name"`
	Enabled bool        `json:"enabled"`
	// Target is the webhook URL, the PagerDuty routing key, or the email
	// addresses (comma-separated).
	Target string `json:"target"`
	// Secret signs the body of a generic webhook. The API never returns it.
	Secret    string       `json:"secret,omitempty"`
	Route     ChannelRoute `json:"route"`
	CreatedAt time.Time    `json:"createdAt"`
}

// ChannelRoute selects the alerts that a channel gets.
type ChannelRoute struct {
	// MinTint is warn (the default) or critical. A change back to ok
	// matches when the tint that it ends matches.
	MinTint wire.Tint `json:"minTint,omitempty"`
	// Metrics is empty for all metrics.
	Metrics []string `json:"metrics,omitempty"`
	// Tags is empty for all nodes. Else the node must have one of them.
	Tags []string `json:"tags,omitempty"`
}

// Match tells if the alert goes to the channel.
func (r ChannelRoute) Match(a notify.Alert) bool {
	tint := a.To
	if tint == wire.TintOK {
		tint = a.From
	}
	if r.MinTint == wire.TintCritical && tint != wire.TintCritical {
		return false
	}
	if len(r.Metrics) > 0 && !slices.Contains(r.Metrics, a.Metric) {
		return false
	}
	if len(r.Tags) > 0 && !slices.ContainsFunc(r.Tags, func(t string) bool { return slices.Contains(a.Tags, t) }) {
		return false
	}
	return true
}
