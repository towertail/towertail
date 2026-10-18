// Package notify holds the contract between the alerter and the alert
// channels.
package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// Alert is one change of tint for one metric of one node.
type Alert struct {
	EventID  uuid.UUID
	OrgID    uuid.UUID
	NodeID   uuid.UUID
	NodeName string
	// Tags are the tags of the node, for routing.
	Tags     []string
	Metric   string // cpu, mem, disk, reachability
	From, To wire.Tint
	// Value is a ratio (0..1) for cpu, mem and disk. For reachability it
	// is the seconds since the last sample.
	Value float64
	At    time.Time
	// Test is true for a test send from the API.
	Test bool
}

// Mailer sends the mail of the email alert channel. The server uses SMTP
// when no Mailer is given (app.WithMailer).
type Mailer interface {
	SendAlert(ctx context.Context, to []string, a Alert) error
}

// ErrPermanent in the error chain of a Mailer means: do not try again.
var ErrPermanent = errors.New("notify: permanent error")

// Notifier sends an Alert to one channel.
type Notifier interface {
	Name() string
	Notify(ctx context.Context, a Alert) error
}

var metricLabels = map[string]string{
	"cpu":          "CPU",
	"mem":          "Memory",
	"disk":         "Disk",
	"reachability": "Reachability",
}

// Title is one short line, for example "web-1: CPU critical".
func (a Alert) Title() string {
	name := a.NodeName
	if name == "" {
		name = a.NodeID.String()
	}
	var what string
	switch {
	case a.Metric == "reachability" && a.To == wire.TintOK:
		what = "reachable again"
	case a.Metric == "reachability":
		what = "unreachable"
	case a.To == wire.TintOK:
		what = a.MetricLabel() + " back to ok"
	default:
		what = a.MetricLabel() + " " + string(a.To)
	}
	if a.Test {
		return "[test] " + name + ": " + what
	}
	return name + ": " + what
}

// Text gives the value and the previous tint, for example
// "CPU 93% (was warn)".
func (a Alert) Text() string {
	if a.Metric == "reachability" {
		if a.To == wire.TintOK {
			return "Samples come in again."
		}
		return fmt.Sprintf("No sample for %s.", (time.Duration(a.Value) * time.Second).Round(time.Second))
	}
	return fmt.Sprintf("%s %.0f%% (was %s)", a.MetricLabel(), a.Value*100, a.From)
}

// MetricLabel is the name of the metric for people.
func (a Alert) MetricLabel() string {
	if l, ok := metricLabels[a.Metric]; ok {
		return l
	}
	return a.Metric
}

// DedupKey is the same for all alerts of one metric of one node.
func (a Alert) DedupKey() string {
	return a.OrgID.String() + ":" + a.NodeID.String() + ":" + a.Metric
}
