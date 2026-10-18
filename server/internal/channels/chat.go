package channels

import (
	"context"
	"fmt"

	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Colors of the tints, for Slack and Discord.
var tintColors = map[wire.Tint]int{
	wire.TintOK:       0x30A46C,
	wire.TintWarn:     0xF5A524,
	wire.TintCritical: 0xE5484D,
}

// slack sends to a Slack incoming webhook.
type slack struct{ p *poster }

func (s slack) send(ctx context.Context, ch store.Channel, a notify.Alert) error {
	return s.p.post(ctx, ch.Target, map[string]any{
		"text": a.Title(),
		"attachments": []map[string]any{{
			"color":  hexColor(tintColors[a.To]),
			"text":   a.Text(),
			"footer": "Towertail",
			"ts":     a.At.Unix(),
		}},
	}, nil)
}

// discord sends to a Discord webhook.
type discord struct{ p *poster }

func (d discord) send(ctx context.Context, ch store.Channel, a notify.Alert) error {
	return d.p.post(ctx, ch.Target, map[string]any{
		"username": "Towertail",
		"embeds": []map[string]any{{
			"title":       a.Title(),
			"description": a.Text(),
			"color":       tintColors[a.To],
			"timestamp":   a.At.UTC().Format("2006-01-02T15:04:05Z"),
		}},
		"allowed_mentions": map[string]any{"parse": []string{}},
	}, nil)
}

func hexColor(c int) string { return fmt.Sprintf("#%06X", c) }
