package mcp

import (
	"context"
	"math"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
)

// Limits of suggest_thresholds.
const (
	defaultSuggestRange = "7d"
	maxCandidates       = 10
)

// LimitPairIn is a pair of limits to test, as ratios from 0 to 1.
type LimitPairIn struct {
	Warn     float64 `json:"warn" jsonschema:"warn limit, a ratio above 0 and up to 1. 0.75 is 75 percent."`
	Critical float64 `json:"critical" jsonschema:"critical limit, a ratio from warn up to 1"`
}

// SuggestThresholdsIn is the input of suggest_thresholds.
type SuggestThresholdsIn struct {
	Metric         string        `json:"metric" jsonschema:"cpu, mem or disk"`
	Host           string        `json:"host,omitempty" jsonschema:"host ID or name. Empty means the hosts that use the limits of the organization (at most 50)."`
	Range          string        `json:"range,omitempty" jsonschema:"span up to now to replay, 15m to 30d, for example 7d. Default 7d."`
	Candidates     []LimitPairIn `json:"candidates,omitempty" jsonschema:"extra pairs of limits to test (at most 10). The current limits and pairs from the percentiles of the data are always tested."`
	SustainSeconds *int          `json:"sustain_seconds,omitempty" jsonschema:"sustain time to test, 0 to 3600. Default is the sustain time of the rule."`
}

// CandidateOut is the replay of one pair of limits. The counts add up all
// hosts. Warn and critical are ratios. WarnSeconds and CriticalSeconds are
// the time that the hosts spent at each level.
type CandidateOut struct {
	Warn            float64 `json:"warn"`
	Critical        float64 `json:"critical"`
	SustainSeconds  int     `json:"sustain_seconds"`
	Current         bool    `json:"current,omitempty"`
	Alerts          int     `json:"alerts"`
	CriticalAlerts  int     `json:"critical_alerts"`
	Notifications   int     `json:"notifications"`
	HostsAlerting   int     `json:"hosts_alerting"`
	WarnSeconds     float64 `json:"warn_seconds"`
	CriticalSeconds float64 `json:"critical_seconds"`
}

// SuggestThresholdsOut is the output of suggest_thresholds. The data is the
// 1 minute rollup, so the counts are approximate (approximate is true).
// actual_alerts and actual_sends are what happened with the limits
// of that time. The percentiles are ratios of the values of all hosts.
type SuggestThresholdsOut struct {
	From         string             `json:"from"`
	To           string             `json:"to"`
	Resolution   string             `json:"resolution"`
	Clamped      bool               `json:"clamped,omitempty"`
	Truncated    bool               `json:"truncated,omitempty"`
	Approximate  bool               `json:"approximate"`
	Metric       string             `json:"metric"`
	Unit         string             `json:"unit"`
	Hosts        int                `json:"hosts"`
	ActualAlerts int                `json:"actual_alerts"`
	ActualSends  int                `json:"actual_sends"`
	Percentiles  map[string]float64 `json:"percentiles"`
	Candidates   []CandidateOut     `json:"candidates"`
}

func (s *Server) suggestThresholds(ctx context.Context, _ *sdk.CallToolRequest, in SuggestThresholdsIn) (*sdk.CallToolResult, SuggestThresholdsOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, SuggestThresholdsOut{}, err
	}
	orgID := c.Token.OrgID
	m := store.ThresholdMetric(in.Metric)
	if !slices.Contains(store.UsageMetrics, m) {
		return nil, SuggestThresholdsOut{}, badf("metric must be one of cpu, mem, disk")
	}
	if len(in.Candidates) > maxCandidates {
		return nil, SuggestThresholdsOut{}, badf("candidates must have at most %d pairs", maxCandidates)
	}
	req := query.SuggestRequest{OrgID: orgID, Metric: m, SustainSeconds: in.SustainSeconds}
	for _, p := range in.Candidates {
		pair := store.ThresholdPair{Warn: p.Warn, Critical: p.Critical}
		if err := pair.Validate(true); err != nil || math.IsNaN(p.Warn) || math.IsNaN(p.Critical) {
			return nil, SuggestThresholdsOut{}, badf("each candidate needs 0 < warn <= critical <= 1 (ratios)")
		}
		req.Candidates = append(req.Candidates, pair)
	}
	if v := in.SustainSeconds; v != nil && (*v < 0 || *v > store.MaxSustainSeconds) {
		return nil, SuggestThresholdsOut{}, badf("sustain_seconds must be 0 to %d", store.MaxSustainSeconds)
	}
	rangeText := in.Range
	if rangeText == "" {
		rangeText = defaultSuggestRange
	}
	if req.From, req.To, err = parseRange(rangeText, "", "", s.now().UTC()); err != nil {
		return nil, SuggestThresholdsOut{}, err
	}
	if req.To.Sub(req.From) > query.MaxSuggestRange {
		return nil, SuggestThresholdsOut{}, badf("range must be 15m to 30d")
	}
	if req.NodeID, err = s.hostID(ctx, orgID, in.Host); err != nil {
		return nil, SuggestThresholdsOut{}, s.toolError("suggest_thresholds", err)
	}
	sug, err := s.q.SuggestThresholds(ctx, req)
	if err != nil {
		return nil, SuggestThresholdsOut{}, s.toolError("suggest_thresholds", err)
	}
	out := SuggestThresholdsOut{
		From: ts(sug.From), To: ts(sug.To), Resolution: resolutionText(sug.Resolution), Clamped: sug.Clamped, Truncated: sug.Truncated,
		Approximate: sug.Approximate, Metric: string(sug.Metric), Unit: thresholdUnit(sug.Metric), Hosts: sug.Hosts,
		ActualAlerts: sug.ActualAlerts, ActualSends: sug.ActualSends, Percentiles: sug.Percentiles,
		Candidates: make([]CandidateOut, 0, len(sug.Candidates)),
	}
	if out.Percentiles == nil {
		out.Percentiles = map[string]float64{}
	}
	for _, cd := range sug.Candidates {
		out.Candidates = append(out.Candidates, CandidateOut{
			Warn: cd.Pair.Warn, Critical: cd.Pair.Critical, SustainSeconds: cd.SustainSeconds, Current: cd.Current,
			Alerts: cd.Count.Alerts, CriticalAlerts: cd.Count.Critical, Notifications: cd.Count.Notifications, HostsAlerting: cd.HostsAlerting,
			WarnSeconds: cd.Count.WarnTime.Seconds(), CriticalSeconds: cd.Count.CriticalTime.Seconds(),
		})
	}
	return nil, out, nil
}
