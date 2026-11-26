// Package channels sends alerts to the channels of each org: Slack,
// Discord, PagerDuty, generic webhooks and email (SMTP).
package channels

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Store gives the channels of an org.
type Store interface {
	ListChannels(ctx context.Context, orgID uuid.UUID) ([]store.Channel, error)
}

// Gate tells if the plan of an org permits a channel kind.
type Gate interface {
	ChannelAllowed(ctx context.Context, orgID uuid.UUID, kind store.ChannelKind) bool
}

type sender interface {
	send(ctx context.Context, ch store.Channel, a notify.Alert) error
}

// Router is the notifier of the org channels. For each alert, it sends
// to each enabled channel whose route matches, at the same time, with
// retries.
type Router struct {
	store   Store
	gate    Gate
	log     *slog.Logger
	senders map[store.ChannelKind]sender
	// delays are the waits before the second and the third attempt.
	delays  []time.Duration
	journal notify.Journal
	links   notify.Links
	counter SendCounter
	sends   *sendCache
}

// The count of email sends is for the last 30 days. The cache keeps it for 5 min.
const (
	sendWindow   = 30 * 24 * time.Hour
	sendTTL      = 5 * time.Minute
	sendMaxKeys  = 10000
	sendCountMax = 500 * time.Millisecond
)

// RouterOption changes how NewRouter builds the Router.
type RouterOption func(*Router)

// WithLinks adds "Ask why" and "Get fewer of these" links to the Slack and
// Discord messages.
func WithLinks(l notify.Links) RouterOption { return func(r *Router) { r.links = l } }

// WithSendCounter sets the counter of the email sends of a rule.
func WithSendCounter(c SendCounter) RouterOption { return func(r *Router) { r.counter = c } }

// UseJournal sets the journal that gets one "notified" line for each
// alert.
func (r *Router) UseJournal(j notify.Journal) { r.journal = j }

// NewRouter makes the router. The email channel uses m when it is not
// nil, else SMTP. It is off when there is no m and no SMTP host. A nil
// gate permits all kinds.
func NewRouter(st Store, cfg config.NotifyConfig, m notify.Mailer, gate Gate, log *slog.Logger, opts ...RouterOption) *Router {
	p := newPoster(cfg.BlockPrivateTargets)
	r := &Router{
		store:  st,
		gate:   gate,
		log:    log,
		delays: []time.Duration{time.Second, 3 * time.Second},
		sends:  newSendCache(sendTTL, sendMaxKeys, time.Now),
	}
	for _, o := range opts {
		o(r)
	}
	r.senders = map[store.ChannelKind]sender{
		store.ChannelSlack:     slack{p: p, links: r.links},
		store.ChannelDiscord:   discord{p: p, links: r.links},
		store.ChannelPagerDuty: pagerDuty{p: p, url: pagerDutyURL},
		store.ChannelWebhook:   webhook{p: p},
	}
	switch {
	case m != nil:
		r.senders[store.ChannelEmail] = mailer{m: m}
	case cfg.SMTP.Host != "":
		r.senders[store.ChannelEmail] = email{cfg: cfg.SMTP}
	}
	return r
}

func (r *Router) Name() string { return "channels" }

// Notify sends the alert to the channels of its org.
func (r *Router) Notify(ctx context.Context, a notify.Alert) error {
	list, err := r.store.ListChannels(ctx, a.OrgID)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}
	var matched []store.Channel
	for _, ch := range list {
		if !ch.Enabled || !ch.Route.Match(a) {
			continue
		}
		if r.gate != nil && !r.gate.ChannelAllowed(ctx, a.OrgID, ch.Kind) {
			r.log.Debug("channels: kind blocked by plan", "channel_id", ch.ID, "kind", ch.Kind)
			continue
		}
		matched = append(matched, ch)
	}
	key, counted := r.countEmails(ctx, &a, list, matched)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		sent = make([]string, 0, len(matched))
		// failed[i] is set by the send of sent[i].
		failed = make([]bool, len(matched))
	)
	for _, ch := range matched {
		i := len(sent)
		sent = append(sent, ch.Name)
		wg.Go(func() {
			err := r.sendRetry(ctx, ch, a)
			result := "ok"
			if err != nil {
				result = "error"
			}
			sendsTotal.WithLabelValues(string(ch.Kind), result).Inc()
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("channel %s (%s): %w", ch.Name, ch.Kind, err))
				mu.Unlock()
				failed[i] = true
			}
		})
	}
	wg.Wait()
	emails := 0
	for i := range sent {
		if failed[i] {
			sent[i] += " (failed)"
		} else if matched[i].Kind == store.ChannelEmail {
			emails++
		}
	}
	if counted && emails > 0 {
		r.sends.add(key, emails)
	}
	r.writeNotified(ctx, a, sent)
	return errors.Join(errs...)
}

// countEmails sets a.RuleEmails for the rule of the alert. It reports the
// cache key when the count is on. A failed query gives 0.
func (r *Router) countEmails(ctx context.Context, a *notify.Alert, all, matched []store.Channel) (sendKey, bool) {
	if a.Test || r.counter == nil {
		return sendKey{}, false
	}
	show := slices.ContainsFunc(matched, func(ch store.Channel) bool {
		return ch.Kind == store.ChannelEmail || ch.Kind == store.ChannelSlack || ch.Kind == store.ChannelDiscord
	})
	if !show {
		return sendKey{}, false
	}
	var names []string
	for _, ch := range all {
		if ch.Kind == store.ChannelEmail {
			names = append(names, ch.Name)
		}
	}
	if len(names) == 0 {
		return sendKey{}, false
	}
	key := sendKey{org: a.OrgID, node: a.NodeID, metric: a.Metric}
	n, ok := r.sends.get(key)
	if !ok {
		qctx, cancel := context.WithTimeout(ctx, sendCountMax)
		defer cancel()
		var err error
		n, err = r.counter.RuleSends(qctx, RuleSendsQuery{
			OrgID: a.OrgID, NodeID: a.NodeID, Metric: a.Metric, Channels: names,
			Since: time.Now().Add(-sendWindow),
		})
		if err != nil {
			r.log.Debug("channels: count sends", "err", err)
			n = 0
		}
		r.sends.put(key, n)
	}
	a.RuleEmails = n
	return key, true
}

// writeNotified writes the line of the channels that got the alert.
func (r *Router) writeNotified(ctx context.Context, a notify.Alert, sent []string) {
	if r.journal == nil || a.Test || a.AlertID == uuid.Nil || len(sent) == 0 {
		return
	}
	err := r.journal.Write(ctx, notify.Line{
		OrgID: a.OrgID, NodeID: a.NodeID, AlertID: a.AlertID,
		At: time.Now().UTC(), Kind: notify.LineNotified, Text: strings.Join(sent, ", "),
	})
	if err != nil {
		r.log.Warn("channels: alert log", "err", err)
	}
}

// Test sends one test alert to the channel, with no retry.
func (r *Router) Test(ctx context.Context, ch store.Channel) error {
	s, ok := r.senders[ch.Kind]
	if !ok {
		return fmt.Errorf("channel kind %q is not available", ch.Kind)
	}
	return s.send(ctx, ch, notify.Alert{
		EventID: uuid.New(), OrgID: ch.OrgID, NodeID: uuid.Nil, NodeName: "test-node",
		Metric: "cpu", From: wire.TintOK, To: wire.TintWarn, Value: 0.8, At: time.Now().UTC(), Test: true,
	})
}

func (r *Router) sendRetry(ctx context.Context, ch store.Channel, a notify.Alert) error {
	s, ok := r.senders[ch.Kind]
	if !ok {
		return fmt.Errorf("channel kind %q is not available", ch.Kind)
	}
	for attempt := 0; ; attempt++ {
		err := s.send(ctx, ch, a)
		if err == nil || !retryable(err) || attempt >= len(r.delays) {
			return err
		}
		r.log.Debug("channels: retry", "channel_id", ch.ID, "attempt", attempt+1, "err", err)
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(r.delays[attempt]):
		}
	}
}

// Validate checks a channel before the store keeps it.
func (r *Router) Validate(ch store.Channel) error {
	if strings.TrimSpace(ch.Name) == "" {
		return errors.New("name is required")
	}
	if _, ok := r.senders[ch.Kind]; !ok {
		if ch.Kind == store.ChannelEmail {
			return errors.New("the email channel needs notify.smtp.host in the server config")
		}
		return fmt.Errorf("unknown kind %q", ch.Kind)
	}
	switch ch.Route.MinTint {
	case "", wire.TintWarn, wire.TintCritical:
	default:
		return fmt.Errorf("route.minTint must be warn or critical, not %q", ch.Route.MinTint)
	}
	switch ch.Kind {
	case store.ChannelEmail:
		_, err := recipients(ch.Target)
		return err
	case store.ChannelPagerDuty:
		if len(ch.Target) != 32 {
			return errors.New("target must be the 32-character routing key of a PagerDuty Events API v2 integration")
		}
		return nil
	default:
		u, err := url.Parse(ch.Target)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("target must be an http or https URL")
		}
		return nil
	}
}
