// Package channels sends alerts to the channels of each org: Slack,
// Discord, PagerDuty, generic webhooks and email (SMTP).
package channels

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
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

type sender interface {
	send(ctx context.Context, ch store.Channel, a notify.Alert) error
}

// Router is the notifier of the org channels. For each alert, it sends
// to each enabled channel whose route matches, at the same time, with
// retries.
type Router struct {
	store   Store
	log     *slog.Logger
	senders map[store.ChannelKind]sender
	// delays are the waits before the second and the third attempt.
	delays []time.Duration
}

// NewRouter makes the router. The email channel uses m when it is not
// nil, else SMTP. It is off when there is no m and no SMTP host.
func NewRouter(st Store, cfg config.NotifyConfig, m notify.Mailer, log *slog.Logger) *Router {
	p := newPoster(cfg.BlockPrivateTargets)
	r := &Router{
		store: st,
		log:   log,
		senders: map[store.ChannelKind]sender{
			store.ChannelSlack:     slack{p: p},
			store.ChannelDiscord:   discord{p: p},
			store.ChannelPagerDuty: pagerDuty{p: p, url: pagerDutyURL},
			store.ChannelWebhook:   webhook{p: p},
		},
		delays: []time.Duration{time.Second, 3 * time.Second},
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
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, ch := range list {
		if !ch.Enabled || !ch.Route.Match(a) {
			continue
		}
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
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
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
