package mcp

import (
	"context"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

// AlertStatsIn is the input of alert_stats.
type AlertStatsIn struct {
	Range string   `json:"range,omitempty" jsonschema:"span up to now, for example 24h or 7d. Alerts that overlap the range count. At most 90d. Default 7d. Do not use with from."`
	From  string   `json:"from,omitempty" jsonschema:"start of the range in RFC 3339. Use it with to, instead of range."`
	To    string   `json:"to,omitempty" jsonschema:"end of the range in RFC 3339. Default now."`
	Host  string   `json:"host,omitempty" jsonschema:"host ID or name. Empty means the whole fleet."`
	Kind  []string `json:"kind,omitempty" jsonschema:"keep these kinds: cpu, mem, disk, health, long (long-running process) or nodata"`
	Top   int      `json:"top,omitempty" jsonschema:"length of the top lists (hosts, rules, flapping), 1 to 20. Default 10."`
}

// CountOut counts the alerts and the notifications of one host or kind.
type CountOut struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	Alerts        int    `json:"alerts"`
	Critical      int    `json:"critical"`
	Warn          int    `json:"warn"`
	Notifications int    `json:"notifications"`
}

// RuleStatOut counts the alerts of one rule. Source is org or host. A host
// rule has host and host_id. Warn and critical are the limits of cpu, mem
// and disk. CPUPct is the limit of a long-process rule.
type RuleStatOut struct {
	Kind           string   `json:"kind"`
	Source         string   `json:"source"`
	Host           string   `json:"host,omitempty"`
	HostID         string   `json:"host_id,omitempty"`
	Warn           *float64 `json:"warn,omitempty"`
	Critical       *float64 `json:"critical,omitempty"`
	CPUPct         *float64 `json:"cpu_pct,omitempty"`
	Unit           string   `json:"unit,omitempty"`
	SustainSeconds int      `json:"sustain_seconds"`
	Notify         string   `json:"notify,omitempty"`
	Alerts         int      `json:"alerts"`
	Notifications  int      `json:"notifications"`
}

// FlapOut is a metric that opens and closes alerts again and again.
type FlapOut struct {
	Host                 string  `json:"host"`
	HostID               string  `json:"host_id"`
	Metric               string  `json:"metric"`
	Alerts               int     `json:"alerts"`
	MedianDurationSecond float64 `json:"median_duration_seconds"`
}

// ChannelStatOut counts the notifications that went to one channel. Kind
// is empty when the name matches no channel.
type ChannelStatOut struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`
	Sends  int    `json:"sends"`
	Failed int    `json:"failed"`
}

// AlertBinOut counts the alerts that started in one part of the range.
type AlertBinOut struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Critical int    `json:"critical"`
	Warn     int    `json:"warn"`
}

// AlertStatsOut is the output of alert_stats. Notifications counts each
// channel of each notification of these alerts, and EmailSends the ones to
// email channels. Truncated is true when the server read only the newest
// alerts of a big range.
type AlertStatsOut struct {
	From          string           `json:"from"`
	To            string           `json:"to"`
	Clamped       bool             `json:"clamped,omitempty"`
	Truncated     bool             `json:"truncated,omitempty"`
	Alerts        int              `json:"alerts"`
	Notifications int              `json:"notifications"`
	EmailSends    int              `json:"email_sends"`
	ByHost        []CountOut       `json:"by_host"`
	ByKind        []CountOut       `json:"by_kind"`
	ByRule        []RuleStatOut    `json:"by_rule"`
	Flapping      []FlapOut        `json:"flapping"`
	ByChannel     []ChannelStatOut `json:"by_channel"`
	Bins          []AlertBinOut    `json:"bins"`
}

func (s *Server) alertStats(ctx context.Context, _ *sdk.CallToolRequest, in AlertStatsIn) (*sdk.CallToolResult, AlertStatsOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, AlertStatsOut{}, err
	}
	orgID := c.Token.OrgID
	if err := oneOf("kind", in.Kind, query.AlertKinds); err != nil {
		return nil, AlertStatsOut{}, err
	}
	if in.Top < 0 || in.Top > query.MaxStatsTop {
		return nil, AlertStatsOut{}, badf("top must be 1 to %d", query.MaxStatsTop)
	}
	rangeText := in.Range
	if rangeText == "" && in.From == "" {
		rangeText = defaultAlertsRange
	}
	from, to, err := parseRange(rangeText, in.From, in.To, s.now().UTC())
	if err != nil {
		return nil, AlertStatsOut{}, err
	}
	var hosts []uuid.UUID
	if id, err := s.hostID(ctx, orgID, in.Host); err != nil {
		return nil, AlertStatsOut{}, s.toolError("alert_stats", err)
	} else if id != uuid.Nil {
		hosts = []uuid.UUID{id}
	}
	st, err := s.q.AlertStats(ctx, query.AlertStatsRequest{OrgID: orgID, From: from, To: to, Hosts: hosts, Kinds: in.Kind, Top: in.Top})
	if err != nil {
		return nil, AlertStatsOut{}, s.toolError("alert_stats", err)
	}
	return nil, newAlertStatsOut(st), nil
}

func newAlertStatsOut(st query.AlertStats) AlertStatsOut {
	out := AlertStatsOut{
		From: ts(st.From), To: ts(st.To), Clamped: st.Clamped, Truncated: st.Truncated,
		Alerts: st.Alerts, Notifications: st.Sends, EmailSends: st.EmailSends,
		ByHost: countsOut(st.ByHost), ByKind: countsOut(st.ByKind),
		ByRule: make([]RuleStatOut, 0, len(st.ByRule)), Flapping: make([]FlapOut, 0, len(st.Flapping)),
		ByChannel: make([]ChannelStatOut, 0, len(st.ByChannel)), Bins: binsOut(st.Bins),
	}
	for _, r := range st.ByRule {
		row := RuleStatOut{
			Kind: r.Kind, Source: string(r.Source), Host: r.Host, SustainSeconds: r.SustainSeconds,
			Notify: string(r.Notify), Alerts: r.Alerts, Notifications: r.Sends,
		}
		if r.NodeID != nil {
			row.HostID = r.NodeID.String()
		}
		switch r.Kind {
		case "cpu", "mem", "disk":
			row.Warn, row.Critical, row.Unit = &r.Warn, &r.Critical, pairUnit(r.Kind)
		case "long":
			row.CPUPct = &r.Warn
		}
		out.ByRule = append(out.ByRule, row)
	}
	for _, f := range st.Flapping {
		out.Flapping = append(out.Flapping, FlapOut{
			Host: f.Host, HostID: f.NodeID.String(), Metric: f.Metric, Alerts: f.Alerts,
			MedianDurationSecond: f.MedianDuration.Round(time.Second).Seconds(),
		})
	}
	for _, c := range st.ByChannel {
		out.ByChannel = append(out.ByChannel, ChannelStatOut{Name: c.Name, Kind: string(c.Kind), Sends: c.Sends, Failed: c.Failed})
	}
	return out
}

func countsOut(rows []query.CountRow) []CountOut {
	out := make([]CountOut, 0, len(rows))
	for _, r := range rows {
		out = append(out, CountOut{Key: r.Key, Name: r.Name, Alerts: r.Alerts, Critical: r.Critical, Warn: r.Warn, Notifications: r.Sends})
	}
	return out
}

func binsOut(bins []wire.AlertBin) []AlertBinOut {
	out := make([]AlertBinOut, 0, len(bins))
	for _, b := range bins {
		out = append(out, AlertBinOut{Start: ts(b.Start), End: ts(b.End), Critical: b.Critical, Warn: b.Warn})
	}
	return out
}
