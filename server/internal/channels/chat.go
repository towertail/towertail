package channels

import (
	"context"
	"fmt"
	"strings"

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
type slack struct {
	p     *poster
	links notify.Links
}

func (s slack) send(ctx context.Context, ch store.Channel, a notify.Alert) error {
	return s.p.post(ctx, ch.Target, map[string]any{
		"text": a.Title(),
		"attachments": []map[string]any{{
			"color":  hexColor(tintColors[a.To]),
			"text":   joinLines(a.Text(), s.footer(a)),
			"footer": "Towertail",
			"ts":     a.At.Unix(),
		}},
	}, nil)
}

// discord sends to a Discord webhook.
type discord struct {
	p     *poster
	links notify.Links
}

func (d discord) send(ctx context.Context, ch store.Channel, a notify.Alert) error {
	return d.p.post(ctx, ch.Target, map[string]any{
		"username": "Towertail",
		"embeds": []map[string]any{{
			"title":       a.Title(),
			"description": joinLines(a.Text(), d.footer(a)),
			"color":       tintColors[a.To],
			"timestamp":   a.At.UTC().Format("2006-01-02T15:04:05Z"),
		}},
		"allowed_mentions": map[string]any{"parse": []string{}},
	}, nil)
}

// footer is the mrkdwn line with the links and the count of emails.
func (s slack) footer(a notify.Alert) string {
	var parts []string
	for _, l := range alertLinks(s.links, a) {
		parts = append(parts, "<"+slackEscape.Replace(l.url)+"|"+l.label+">")
	}
	return withCount(parts, a)
}

// footer is the Markdown line with the links and the count of emails.
func (d discord) footer(a notify.Alert) string {
	var parts []string
	for _, l := range alertLinks(d.links, a) {
		parts = append(parts, "["+l.label+"]("+discordEscape.Replace(l.url)+")")
	}
	return withCount(parts, a)
}

type alertLink struct{ label, url string }

// alertLinks gives the links of the alert. A test alert has none.
func alertLinks(l notify.Links, a notify.Alert) []alertLink {
	if l == nil || a.Test {
		return nil
	}
	var out []alertLink
	if u := l.AskURL(a); u != "" {
		out = append(out, alertLink{"✦ Ask why", u})
	}
	if u := l.FewerURL(a); u != "" {
		out = append(out, alertLink{"Get fewer of these →", u})
	}
	return out
}

func withCount(parts []string, a notify.Alert) string {
	if a.RuleEmails > 0 && !a.Test {
		parts = append(parts, emailCount(a.RuleEmails)+" in 30 d")
	}
	return strings.Join(parts, " · ")
}

func joinLines(text, footer string) string {
	if footer == "" {
		return text
	}
	return text + "\n" + footer
}

// emailCount is "1 email" or "N emails".
func emailCount(n int) string {
	if n == 1 {
		return "1 email"
	}
	return fmt.Sprintf("%d emails", n)
}

var (
	slackEscape   = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "%7C")
	discordEscape = strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29")
)

func hexColor(c int) string { return fmt.Sprintf("#%06X", c) }
