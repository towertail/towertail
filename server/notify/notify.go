// Package notify holds the contract between the alerter and the alert
// channels.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// Reasons of a reachability alert.
const (
	ReasonStoppedReporting = "stopped_reporting"
	ReasonNeverReported    = "never_reported"
)

// Alert is one change of tint for one metric of one node.
type Alert struct {
	EventID uuid.UUID
	// AlertID is the id of the incident: the same for each change from the
	// change out of ok to the change back to ok.
	AlertID  uuid.UUID
	OrgID    uuid.UUID
	NodeID   uuid.UUID
	NodeName string
	// Tags are the tags of the node, for routing.
	Tags   []string
	Metric string // cpu, mem, disk, reachability, long_proc
	// Instance tells apart the alerts of one metric, for example one
	// process of long_proc. It is empty for the other metrics.
	Instance string
	From, To wire.Tint
	// Value is a ratio (0..1) for cpu, mem and disk. For reachability it
	// is the seconds of the outage: the time without a sample, or the
	// down time when the host is back. For long_proc it is the CPU
	// percent of the process (top convention).
	Value float64
	// Reason tells apart the reachability alerts: stopped_reporting or
	// never_reported. It is empty for the other metrics.
	Reason string
	// Since is the start of the outage: the last sample, or the
	// registration of a host that never reported.
	Since time.Time
	// Host is user@host of the node. It can be empty.
	Host string
	// Detail replaces the default Text when it is set.
	Detail string
	At     time.Time
	// Test is true for a test send from the API.
	Test bool
	// Repeat is true for the one reminder after a snooze ends.
	Repeat bool
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
	"health":       "Health",
	"reachability": "Reachability",
	"long_proc":    "Long-running process",
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
		what = "reports again"
	case a.Metric == "reachability" && a.Reason == ReasonNeverReported:
		what = "never reported"
	case a.Metric == "reachability":
		what = "stopped reporting"
	case a.Metric == "long_proc" && a.To == wire.TintOK:
		what = "long-running process ended"
	case a.Metric == "long_proc":
		what = "long-running process"
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
	switch {
	case a.Repeat && a.Detail == "" && a.Metric != "reachability":
		return fmt.Sprintf("%s still %.0f%%", a.MetricLabel(), a.Value*100)
	case a.Repeat:
		return "Still open. " + a.baseText()
	}
	return a.baseText()
}

func (a Alert) baseText() string {
	if a.Detail != "" {
		return a.Detail
	}
	if a.Metric == "reachability" {
		return a.reachabilityText()
	}
	return fmt.Sprintf("%s %.0f%% (was %s)", a.MetricLabel(), a.Value*100, a.From)
}

func (a Alert) reachabilityText() string {
	d := shortDuration(a.Value)
	switch {
	case a.To == wire.TintOK:
		if a.Since.IsZero() {
			return fmt.Sprintf("Down for %s.", d)
		}
		return fmt.Sprintf("Down for %s, from %s to %s UTC.", d, a.Since.UTC().Format("15:04"), a.At.UTC().Format("15:04"))
	case a.Reason == ReasonNeverReported:
		return fmt.Sprintf("Registered %s ago. No sample came.", d)
	case a.Since.IsZero():
		return fmt.Sprintf("No sample for %s.", d)
	}
	return fmt.Sprintf("No sample for %s. The last sample came at %s UTC.", d, a.Since.UTC().Format("15:04:05"))
}

// shortDuration writes seconds as "12m40s", and "10m" for whole minutes.
func shortDuration(seconds float64) string {
	s := (time.Duration(seconds) * time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// MetricLabel is the name of the metric for people.
func (a Alert) MetricLabel() string {
	if l, ok := metricLabels[a.Metric]; ok {
		return l
	}
	return a.Metric
}

// DedupKey is the same for all alerts of one metric (and instance) of
// one node.
func (a Alert) DedupKey() string {
	k := a.OrgID.String() + ":" + a.NodeID.String() + ":" + a.Metric
	if a.Instance != "" {
		k += ":" + a.Instance
	}
	return k
}

// Ack is one ack or snooze of an open alert.
type Ack struct {
	OrgID   uuid.UUID
	EventID uuid.UUID
	// AlertID is the id of the incident that the ack or snooze applies to.
	AlertID uuid.UUID
	NodeID  uuid.UUID
	// Metric is the stored metric of the alert, with the instance after a
	// "/" (the metric of wire.Alert).
	Metric string
	// By is the user ID when the token has a user, else the token ID.
	By     uuid.UUID
	At     time.Time
	Snooze bool
	// Until is the end of the snooze. It is nil for an ack.
	Until *time.Time
	// Undo takes an ack or a snooze back.
	Undo bool
}

// DedupKey gives the same key as Alert.DedupKey for the same alert.
func (a Ack) DedupKey() string {
	metric, instance, _ := strings.Cut(a.Metric, "/")
	return Alert{OrgID: a.OrgID, NodeID: a.NodeID, Metric: metric, Instance: instance}.DedupKey()
}

// AckObserver is a notifier that hears each ack and snooze.
type AckObserver interface {
	AlertAcked(ctx context.Context, a Ack) error
}

// LineKind is the kind of a line of the alert log.
type LineKind string

const (
	LineNotified  LineKind = "notified"
	LineEscalated LineKind = "escalated"
	LineAcked     LineKind = "acked"
	LineUnacked   LineKind = "unacked"
	LineSnoozed   LineKind = "snoozed"
	LineUnsnoozed LineKind = "unsnoozed"
	LineClosed    LineKind = "closed"
)

// Line is one entry of the alert log: what happened to an alert.
type Line struct {
	OrgID, NodeID, AlertID uuid.UUID
	At                     time.Time
	Kind                   LineKind
	User                   *uuid.UUID
	Until                  *time.Time
	Text                   string
}

// Journal stores the lines of the alert log.
type Journal interface {
	Write(ctx context.Context, lines ...Line) error
}

// JournalUser is a notifier that writes lines. app.New gives it the
// journal.
type JournalUser interface {
	UseJournal(j Journal)
}
