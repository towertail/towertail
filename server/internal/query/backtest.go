package query

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/alerter"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

const (
	// MaxSuggestRange is the longest range of SuggestThresholds.
	MaxSuggestRange = 30 * 24 * time.Hour
	// MaxSuggestHosts is the most hosts that one backtest replays.
	MaxSuggestHosts = 50
	// minSuggestLimit is the lowest limit of a generated candidate.
	minSuggestLimit = 0.01
)

// SuggestRequest asks what other limits of cpu, mem or disk would have
// done. A zero NodeID means the hosts that use the org limits. A zero To
// means now. Candidates are extra pairs to test, in the units of the
// rules (ratios). A nil SustainSeconds keeps the sustain time of the rule.
type SuggestRequest struct {
	OrgID, NodeID  uuid.UUID
	Metric         store.ThresholdMetric
	From, To       time.Time
	Candidates     []store.ThresholdPair
	SustainSeconds *int
}

// Candidate is a pair of limits and the replay of it. Count adds up the
// hosts. Current marks the limits that apply now.
type Candidate struct {
	Pair           store.ThresholdPair
	SustainSeconds int
	Count          alerter.ReplayCount
	HostsAlerting  int
	Current        bool
}

// Suggestion is the answer of SuggestThresholds. The data is the 1 minute
// rollup, so the counts are approximate: a short peak inside a minute does
// not show. ActualAlerts and ActualSends are what really happened with the
// limits of that time. Percentiles are of the values of all hosts, as
// ratios.
type Suggestion struct {
	From, To                  time.Time
	Clamped, Truncated        bool
	Approximate               bool
	Resolution                time.Duration
	Metric                    store.ThresholdMetric
	Hosts                     int
	ActualAlerts, ActualSends int
	Percentiles               map[string]float64
	Candidates                []Candidate
}

type suggestHost struct {
	id  uuid.UUID
	eff store.EffectiveRules
}

// SuggestThresholds replays the stored values of a metric under the
// current limits, the requested pairs and pairs from the percentiles of
// the data. It uses the engine rules (sustain, tolerance, debounce,
// cooldown and notify level), but not the notify flags. The plan clamps
// From.
func (s *Service) SuggestThresholds(ctx context.Context, r SuggestRequest) (Suggestion, error) {
	m := r.Metric
	if !slices.Contains(store.UsageMetrics, m) {
		return Suggestion{}, ErrBadMetric
	}
	now := s.now().UTC()
	to := r.To
	if to.IsZero() {
		to = now
	}
	if !r.From.Before(to) || to.Sub(r.From) > MaxSuggestRange {
		return Suggestion{}, ErrBadRange
	}
	if s.ch == nil {
		return Suggestion{}, ErrNoHistory
	}
	settings, err := s.settings(ctx, r.OrgID)
	if err != nil {
		return Suggestion{}, err
	}
	org := settings.Rules()
	hosts, truncated, err := s.suggestHosts(ctx, r, org)
	if err != nil {
		return Suggestion{}, err
	}
	from, to, clamped := s.clampRange(ctx, r.OrgID, r.From, to, now)
	out := Suggestion{
		From: from, To: to, Clamped: clamped, Truncated: truncated, Approximate: true, Resolution: time.Minute,
		Metric: m, Hosts: len(hosts), Candidates: []Candidate{},
	}
	if len(hosts) == 0 || !from.Before(to) {
		return out, nil
	}
	ids := make([]uuid.UUID, len(hosts))
	for i, h := range hosts {
		ids[i] = h.id
	}
	series, err := s.ch.MinuteHistory(ctx, clickhouse.HistoryMultiQuery{OrgID: r.OrgID, NodeIDs: ids, Metrics: []string{string(m)}, From: from, To: to})
	if err != nil {
		return Suggestion{}, err
	}
	points, all := replayPoints(m, ids, series)
	out.Percentiles = percentiles(all)

	debounce := time.Duration(settings.NotifyDebounceSeconds) * time.Second
	cooldown := time.Duration(settings.NotifyCooldownSeconds) * time.Second
	sustainOf := func(eff store.EffectiveRules) int {
		if r.SustainSeconds != nil {
			return min(max(*r.SustainSeconds, 0), store.MaxSustainSeconds)
		}
		rule, _ := eff.Alerts.For(string(m))
		return rule.SustainSeconds
	}
	current := org.Thresholds.Pair(m)
	reportSustain := sustainOf(org.For(store.HostRules{}))
	if r.NodeID != uuid.Nil {
		current, reportSustain = hosts[0].eff.Thresholds.Pair(m), sustainOf(hosts[0].eff)
	}
	for i, pair := range candidatePairs(current, r.Candidates, out.Percentiles) {
		c := Candidate{Pair: pair, SustainSeconds: reportSustain, Current: i == 0}
		for _, h := range hosts {
			rule, _ := h.eff.Alerts.For(string(m))
			rule.SustainSeconds = sustainOf(h.eff)
			n := alerter.NewReplay(rule, h.eff.Alerts.Tolerance, debounce, cooldown).Run(points[h.id], pair)
			c.Count.Alerts += n.Alerts
			c.Count.Critical += n.Critical
			c.Count.Notifications += n.Notifications
			c.Count.WarnTime += n.WarnTime
			c.Count.CriticalTime += n.CriticalTime
			if n.Alerts > 0 {
				c.HostsAlerting++
			}
		}
		out.Candidates = append(out.Candidates, c)
	}
	stats, err := s.AlertStats(ctx, AlertStatsRequest{OrgID: r.OrgID, From: from, To: to, Hosts: ids, Kinds: []string{string(m)}})
	if err != nil {
		return Suggestion{}, err
	}
	out.ActualAlerts, out.ActualSends = stats.Alerts, stats.Sends
	return out, nil
}

// suggestHosts gives the node, or the hosts that use the org limits of the
// metric and do not mute it. It gives at most MaxSuggestHosts hosts, by name.
func (s *Service) suggestHosts(ctx context.Context, r SuggestRequest, org store.OrgRules) ([]suggestHost, bool, error) {
	if r.NodeID != uuid.Nil {
		n, err := s.node(ctx, r.OrgID, r.NodeID)
		if err != nil {
			return nil, false, err
		}
		return []suggestHost{{n.ID, org.For(n.Rules())}}, false, nil
	}
	nodes, err := s.nodes.ListNodes(ctx, r.OrgID)
	if err != nil {
		return nil, false, err
	}
	slices.SortFunc(nodes, func(a, b store.Node) int {
		return cmp.Or(strings.Compare(a.DisplayName, b.DisplayName), strings.Compare(a.ID.String(), b.ID.String()))
	})
	var out []suggestHost
	for _, n := range nodes {
		eff := org.For(n.Rules())
		if eff.Sources.Thresholds[r.Metric] == store.SourceOrg && !eff.Muted.Has(r.Metric) {
			out = append(out, suggestHost{n.ID, eff})
		}
	}
	truncated := len(out) > MaxSuggestHosts
	return out[:min(len(out), MaxSuggestHosts)], truncated, nil
}

// replayPoints converts the series to the units of the rules. The history
// metric cpu is a percent and the rule is a ratio. It also gives all the
// values, sorted.
func replayPoints(m store.ThresholdMetric, ids []uuid.UUID, series map[uuid.UUID]map[string][]clickhouse.HistoryPoint) (map[uuid.UUID][]alerter.ReplayPoint, []float64) {
	scale := 1.0
	if m == store.MetricCPU {
		scale = 100
	}
	out := make(map[uuid.UUID][]alerter.ReplayPoint, len(ids))
	var all []float64
	for _, id := range ids {
		for _, p := range series[id][string(m)] {
			out[id] = append(out[id], alerter.ReplayPoint{At: p.TS, Value: p.Value / scale})
			all = append(all, p.Value/scale)
		}
	}
	slices.Sort(all)
	return out, all
}

// percentiles gives p50, p95, p99, p99.9 and the maximum of sorted values
// (nearest rank), or nil with no values.
func percentiles(sorted []float64) map[string]float64 {
	if len(sorted) == 0 {
		return nil
	}
	at := func(p float64) float64 {
		return sorted[max(int(math.Ceil(p*float64(len(sorted))))-1, 0)]
	}
	return map[string]float64{"p50": at(0.5), "p95": at(0.95), "p99": at(0.99), "p99.9": at(0.999), "max": sorted[len(sorted)-1]}
}

// candidatePairs lists the current pair, then the requested pairs, then
// the pairs from the percentiles (p95/p99 and p99/p99.9). Each pair is
// capped to 0.01 to 1 and a pair is listed once.
func candidatePairs(current store.ThresholdPair, asked []store.ThresholdPair, pct map[string]float64) []store.ThresholdPair {
	out := []store.ThresholdPair{current}
	add := func(p store.ThresholdPair) {
		p.Warn = min(max(math.Round(p.Warn*1000)/1000, minSuggestLimit), 1)
		p.Critical = min(max(math.Round(p.Critical*1000)/1000, p.Warn), 1)
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, p := range asked {
		add(p)
	}
	if pct != nil {
		add(store.ThresholdPair{Warn: pct["p95"], Critical: pct["p99"]})
		add(store.ThresholdPair{Warn: pct["p99"], Critical: pct["p99.9"]})
	}
	return out
}
