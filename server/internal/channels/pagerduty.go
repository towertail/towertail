package channels

import (
	"context"

	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

const pagerDutyURL = "https://events.pagerduty.com/v2/enqueue"

// pagerDuty sends to the PagerDuty Events API v2. The target is the
// routing key. A change to ok resolves the incident of the metric.
type pagerDuty struct {
	p   *poster
	url string
}

func (pd pagerDuty) send(ctx context.Context, ch store.Channel, a notify.Alert) error {
	body := map[string]any{
		"routing_key":  ch.Target,
		"event_action": "trigger",
		"dedup_key":    a.DedupKey(),
	}
	if a.Test {
		// A test makes no incident that someone must close.
		body["dedup_key"] = "towertail-test-" + a.EventID.String()
	}
	if a.To == wire.TintOK {
		body["event_action"] = "resolve"
	} else {
		severity := "warning"
		if a.To == wire.TintCritical {
			severity = "critical"
		}
		body["payload"] = map[string]any{
			"summary":   a.Title() + " - " + a.Text(),
			"source":    a.NodeName,
			"severity":  severity,
			"timestamp": a.At.UTC().Format("2006-01-02T15:04:05Z"),
			"component": a.Metric,
			"custom_details": map[string]any{
				"node_id": a.NodeID, "metric": a.Metric, "value": a.Value, "from": a.From, "tags": a.Tags,
			},
		}
	}
	return pd.p.post(ctx, pd.url, body, nil)
}
