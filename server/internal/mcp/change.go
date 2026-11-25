package mcp

import (
	"context"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
)

// ExplainChangeIn is the input of explain_change.
type ExplainChangeIn struct {
	Host   string `json:"host" jsonschema:"host ID or name"`
	Metric string `json:"metric" jsonschema:"history metric to explain, for example cpu or mem. Call reference with topic metrics for the names and units."`
	Range  string `json:"range,omitempty" jsonschema:"span up to now, 15m to 365d, for example 6h or 24h. Default 24h. Do not use with from."`
	From   string `json:"from,omitempty" jsonschema:"start of the range in RFC 3339. Use it with to, instead of range."`
	To     string `json:"to,omitempty" jsonschema:"end of the range in RFC 3339. Default now."`
}

// ChangePointOut is a shift of the level of the metric. TS is the start of
// the new level. Before and After are the average levels on each side.
type ChangePointOut struct {
	TS     string  `json:"ts"`
	Before float64 `json:"before"`
	After  float64 `json:"after"`
	Delta  float64 `json:"delta"`
}

// ProcDeltaOut is the change of one process across the largest change
// point, in process_unit. New means the process was not in the window
// before, and Gone means it is not in the window after.
type ProcDeltaOut struct {
	Key     string  `json:"key"`
	Name    string  `json:"name"`
	Command string  `json:"command"`
	Before  float64 `json:"before"`
	After   float64 `json:"after"`
	Delta   float64 `json:"delta"`
	New     bool    `json:"new,omitempty"`
	Gone    bool    `json:"gone,omitempty"`
}

// ExplainChangeOut is the output of explain_change. Points come with the
// largest shift first. The windows, the processes and the alerts belong to
// the largest shift. Processes come with the biggest change in its
// direction first (at most 10). Alerts are the ones of the host that
// started in the two windows. All three are empty when the series has no
// change point.
type ExplainChangeOut struct {
	Host        string           `json:"host"`
	HostID      string           `json:"host_id"`
	From        string           `json:"from"`
	To          string           `json:"to"`
	Resolution  string           `json:"resolution"`
	Clamped     bool             `json:"clamped,omitempty"`
	Metric      string           `json:"metric"`
	Unit        string           `json:"unit"`
	Points      []ChangePointOut `json:"points"`
	Before      *TimeWindow      `json:"before,omitempty"`
	After       *TimeWindow      `json:"after,omitempty"`
	ProcessesBy string           `json:"processes_by"`
	ProcessUnit string           `json:"process_unit"`
	Processes   []ProcDeltaOut   `json:"processes"`
	Alerts      []AlertItem      `json:"alerts"`
}

func processUnit(by clickhouse.ProcOrder) string {
	switch by {
	case clickhouse.ProcByMem:
		return "bytes"
	case clickhouse.ProcByIO:
		return "bytes each second"
	}
	return "percent of one core"
}

func (s *Server) explainChange(ctx context.Context, _ *sdk.CallToolRequest, in ExplainChangeIn) (*sdk.CallToolResult, ExplainChangeOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, ExplainChangeOut{}, err
	}
	orgID := c.Token.OrgID
	if strings.TrimSpace(in.Host) == "" {
		return nil, ExplainChangeOut{}, badf("host is required")
	}
	if names := clickhouse.MetricNames(); !slices.Contains(names, in.Metric) {
		return nil, ExplainChangeOut{}, badf("metric must be one of %s", strings.Join(names, ", "))
	}
	rangeText := in.Range
	if rangeText == "" && in.From == "" {
		rangeText = "24h"
	}
	from, to, err := parseRange(rangeText, in.From, in.To, s.now().UTC())
	if err != nil {
		return nil, ExplainChangeOut{}, err
	}
	node, err := s.q.ResolveHost(ctx, orgID, in.Host)
	if err != nil {
		return nil, ExplainChangeOut{}, s.toolError("explain_change", err)
	}
	rep, err := s.q.ExplainChange(ctx, query.ChangeRequest{OrgID: orgID, NodeID: node.ID, Metric: in.Metric, From: from, To: to})
	if err != nil {
		return nil, ExplainChangeOut{}, s.toolError("explain_change", err)
	}
	out := ExplainChangeOut{
		Host: node.DisplayName, HostID: node.ID.String(), From: ts(rep.From), To: ts(rep.To), Resolution: resolutionText(rep.Bucket),
		Clamped: rep.Clamped, Metric: rep.Metric, Unit: metricFacts[rep.Metric].unit,
		Points: make([]ChangePointOut, 0, len(rep.Points)), ProcessesBy: string(rep.By), ProcessUnit: processUnit(rep.By),
		Processes: make([]ProcDeltaOut, 0, len(rep.Processes)), Alerts: make([]AlertItem, 0, len(rep.Alerts)),
	}
	for _, p := range rep.Points {
		out.Points = append(out.Points, ChangePointOut{TS: ts(p.TS), Before: p.Before, After: p.After, Delta: p.Delta})
	}
	if len(rep.Points) > 0 {
		out.Before = &TimeWindow{From: ts(rep.Before.From), To: ts(rep.Before.To)}
		out.After = &TimeWindow{From: ts(rep.After.From), To: ts(rep.After.To)}
	}
	for _, p := range rep.Processes {
		out.Processes = append(out.Processes, ProcDeltaOut{
			Key: p.Key.String(), Name: p.Name, Command: p.Cmd, Before: p.Before, After: p.After, Delta: p.Delta, New: p.New, Gone: p.Gone,
		})
	}
	for _, a := range rep.Alerts {
		out.Alerts = append(out.Alerts, newAlertItem(a, node.DisplayName, s.now()))
	}
	return nil, out, nil
}
