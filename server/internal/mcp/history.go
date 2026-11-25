package mcp

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
)

// Limits of host_metrics.
const (
	maxSeriesPoints = 300
	minSeriesPoints = 20
	// pointBudget is the most points of all series in one call.
	pointBudget    = 6000
	maxMetricHosts = 50
	maxOneHostMets = 16
	maxManyHostMet = 8
	maxManyRange   = 30 * 24 * time.Hour
)

// HostMetricsIn is the input of host_metrics.
type HostMetricsIn struct {
	Host       string   `json:"host,omitempty" jsonschema:"one host, by ID or name. Give one of host, hosts or tag."`
	Hosts      []string `json:"hosts,omitempty" jsonschema:"at most 50 hosts, by ID or name"`
	Tag        string   `json:"tag,omitempty" jsonschema:"all hosts with this tag, at most 50"`
	Metrics    []string `json:"metrics,omitempty" jsonschema:"history metrics to read: cpu, mem, net_rx, net_tx, disk_read, disk_write, load_1, load (the same as load_1), procs, disk, inodes, zombies, pids, files, psi_cpu, psi_mem, psi_mem_full, psi_io, psi_io_full and mem_pressure. At most 16 for one host and 8 for many hosts. Default cpu. Call reference with topic metrics for the units."`
	Range      string   `json:"range,omitempty" jsonschema:"span up to now, 15m to 365d (30d for many hosts), for example 1h, 24h or 7d. Default 1h. Do not use with from."`
	From       string   `json:"from,omitempty" jsonschema:"start of the range in RFC 3339. Use it with to, instead of range."`
	To         string   `json:"to,omitempty" jsonschema:"end of the range in RFC 3339. Default now."`
	Resolution string   `json:"resolution,omitempty" jsonschema:"auto, raw, 1m, 5m or 1h. Default auto. The server raises the step to keep at most 300 points."`
	Compare    bool     `json:"compare,omitempty" jsonschema:"also read the window of the same length before the range. Each series then has previous and change_avg, and the biggest change comes first."`
}

// Point is one bucket of a series.
type Point struct {
	TS    string  `json:"ts"`
	Value float64 `json:"value"`
	Max   float64 `json:"max,omitempty"`
}

// PrevStats are the stats of a series in the window before the range.
type PrevStats struct {
	Avg   float64 `json:"avg"`
	Max   float64 `json:"max"`
	Min   float64 `json:"min"`
	Count int     `json:"count"`
}

// Series is one metric of a host over a range. Count is the number of
// points before the cap. A Count of 0 means no data. ChangeAvg is the
// average now minus the average before. It is missing when one of the
// windows has no data.
type Series struct {
	Metric     string     `json:"metric"`
	Unit       string     `json:"unit"`
	Resolution string     `json:"resolution"`
	Count      int        `json:"count"`
	Min        float64    `json:"min"`
	Avg        float64    `json:"avg"`
	Max        float64    `json:"max"`
	Last       float64    `json:"last"`
	Points     []Point    `json:"points"`
	Previous   *PrevStats `json:"previous,omitempty"`
	ChangeAvg  *float64   `json:"change_avg,omitempty"`
}

// HostSeries are the series of one host.
type HostSeries struct {
	Host    string   `json:"host"`
	HostID  string   `json:"host_id"`
	Metrics []Series `json:"metrics"`
}

// TimeWindow is a time range.
type TimeWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// HostMetricsOut is the output of host_metrics. Hosts come in name order.
// With compare, the host and the series with the biggest change come first.
type HostMetricsOut struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Resolution string `json:"resolution"`
	// Clamped is true when the plan history limit moved the start of a window later.
	Clamped  bool         `json:"clamped,omitempty"`
	Previous *TimeWindow  `json:"previous,omitempty"`
	Hosts    []HostSeries `json:"hosts"`
}

// parseResolution reads the resolution input. 0 means the tier.
func parseResolution(v string) (time.Duration, error) {
	switch v {
	case "", "auto", "raw":
		return 0, nil
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	}
	return 0, badf("resolution must be auto, raw, 1m, 5m or 1h")
}

func resolutionText(d time.Duration) string {
	if d == 0 {
		return "raw"
	}
	return d.String()
}

// metricHosts resolves the host, hosts or tag input to the hosts to read.
func (s *Server) metricHosts(ctx context.Context, orgID uuid.UUID, in HostMetricsIn) ([]store.Node, error) {
	var given int
	for _, ok := range []bool{strings.TrimSpace(in.Host) != "", len(in.Hosts) > 0, strings.TrimSpace(in.Tag) != ""} {
		if ok {
			given++
		}
	}
	switch {
	case given == 0:
		return nil, badf("host, hosts or tag is required")
	case given > 1:
		return nil, badf("use one of host, hosts or tag")
	case len(in.Hosts) > maxMetricHosts:
		return nil, badf("hosts has too many entries: at most %d", maxMetricHosts)
	}
	var nodes []store.Node
	switch {
	case in.Tag != "":
		var err error
		if nodes, err = s.q.HostsByTag(ctx, orgID, in.Tag); err != nil {
			return nil, s.toolError("host_metrics", err)
		}
		if len(nodes) == 0 {
			return nil, badf("no host has the tag %q", in.Tag)
		}
		if len(nodes) > maxMetricHosts {
			return nil, badf("the tag %q has %d hosts: at most %d", in.Tag, len(nodes), maxMetricHosts)
		}
	default:
		refs := in.Hosts
		if in.Host != "" {
			refs = []string{in.Host}
		}
		for _, ref := range refs {
			n, err := s.q.ResolveHost(ctx, orgID, ref)
			if err != nil {
				return nil, s.toolError("host_metrics", err)
			}
			if !slices.ContainsFunc(nodes, func(o store.Node) bool { return o.ID == n.ID }) {
				nodes = append(nodes, n)
			}
		}
	}
	slices.SortStableFunc(nodes, func(a, b store.Node) int {
		return cmp.Or(strings.Compare(a.DisplayName, b.DisplayName), strings.Compare(a.ID.String(), b.ID.String()))
	})
	return nodes, nil
}

// metricList checks the metrics and gives them in name order. The empty
// input gives cpu.
func metricList(in []string, hosts int) ([]string, error) {
	if len(in) == 0 {
		return []string{"cpu"}, nil
	}
	names := clickhouse.MetricNames()
	for _, m := range in {
		if !slices.Contains(names, m) {
			return nil, badf("metric must be one of %s", strings.Join(names, ", "))
		}
	}
	metrics := slices.Compact(slices.Sorted(slices.Values(in)))
	limit := maxManyHostMet
	if hosts == 1 {
		limit = maxOneHostMets
	}
	if len(metrics) > limit {
		return nil, badf("metrics has too many names: at most %d for %d host(s)", limit, hosts)
	}
	return metrics, nil
}

func (s *Server) hostMetrics(ctx context.Context, _ *sdk.CallToolRequest, in HostMetricsIn) (*sdk.CallToolResult, HostMetricsOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, HostMetricsOut{}, err
	}
	orgID := c.Token.OrgID
	nodes, err := s.metricHosts(ctx, orgID, in)
	if err != nil {
		return nil, HostMetricsOut{}, err
	}
	metrics, err := metricList(in.Metrics, len(nodes))
	if err != nil {
		return nil, HostMetricsOut{}, err
	}
	res, err := parseResolution(in.Resolution)
	if err != nil {
		return nil, HostMetricsOut{}, err
	}
	from, to, err := parseRange(in.Range, in.From, in.To, s.now().UTC())
	if err != nil {
		return nil, HostMetricsOut{}, err
	}
	if len(nodes) > 1 && to.Sub(from) > maxManyRange {
		return nil, HostMetricsOut{}, badf("range must be at most 30d for many hosts")
	}
	ids := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	// The step keeps the points of a series under the cap.
	minStep := ((to.Sub(from)/maxSeriesPoints + time.Second - 1) / time.Second) * time.Second
	h, err := s.q.HistoryMulti(ctx, query.HistoryMultiRequest{
		OrgID: orgID, NodeIDs: ids, Metrics: metrics, From: from, To: to, Step: max(res, minStep), Previous: in.Compare,
	})
	if err != nil {
		return nil, HostMetricsOut{}, s.toolError("host_metrics", err)
	}
	out := HostMetricsOut{
		From: ts(h.From), To: ts(h.To), Resolution: resolutionText(h.Bucket), Clamped: h.Clamped, Hosts: make([]HostSeries, 0, len(nodes)),
	}
	if p := h.Previous; p != nil && p.From.Before(p.To) {
		out.Previous = &TimeWindow{From: ts(p.From), To: ts(p.To)}
	}
	perSeries := min(max(pointBudget/(len(nodes)*len(metrics)), minSeriesPoints), maxSeriesPoints)
	for _, n := range nodes {
		hs := HostSeries{Host: n.DisplayName, HostID: n.ID.String(), Metrics: make([]Series, 0, len(metrics))}
		for _, m := range metrics {
			sr := newSeries(m, h.Bucket, h.Series[n.ID][m], perSeries)
			if h.Previous != nil {
				sr.addPrevious(h.Previous.Series[n.ID][m])
			}
			hs.Metrics = append(hs.Metrics, sr)
		}
		out.Hosts = append(out.Hosts, hs)
	}
	if in.Compare {
		sortByChange(out.Hosts)
	}
	return nil, out, nil
}

// pointStats gives the min, avg, max and last value of the points.
func pointStats(pts []clickhouse.HistoryPoint) (lo, avg, hi, last float64) {
	if len(pts) == 0 {
		return
	}
	lo, hi, last = pts[0].Value, pts[0].Value, pts[len(pts)-1].Value
	var sum float64
	for _, p := range pts {
		sum += p.Value
		lo = min(lo, p.Value)
		hi = max(hi, p.Value, p.Max)
	}
	return lo, sum / float64(len(pts)), hi, last
}

// newSeries gives the stats of all points, and at most maxPoints points.
func newSeries(metric string, bucket time.Duration, pts []clickhouse.HistoryPoint, maxPoints int) Series {
	sr := Series{Metric: metric, Unit: metricFacts[metric].unit, Resolution: resolutionText(bucket), Count: len(pts), Points: []Point{}}
	if len(pts) == 0 {
		return sr
	}
	sr.Min, sr.Avg, sr.Max, sr.Last = pointStats(pts)
	size := (len(pts) + maxPoints - 1) / maxPoints
	for i := 0; i < len(pts); i += size {
		group := pts[i:min(i+size, len(pts))]
		p := Point{TS: ts(group[0].TS)}
		for _, g := range group {
			p.Value += g.Value / float64(len(group))
			p.Max = max(p.Max, g.Max)
		}
		sr.Points = append(sr.Points, p)
	}
	return sr
}

// addPrevious sets the stats of the window before. It sets the change only
// when both windows have data.
func (sr *Series) addPrevious(pts []clickhouse.HistoryPoint) {
	if len(pts) == 0 {
		return
	}
	lo, avg, hi, _ := pointStats(pts)
	sr.Previous = &PrevStats{Avg: avg, Max: hi, Min: lo, Count: len(pts)}
	if sr.Count > 0 {
		change := sr.Avg - avg
		sr.ChangeAvg = &change
	}
}

// absChange is the size of the change. A series with no change is last.
func (sr Series) absChange() float64 {
	if sr.ChangeAvg == nil {
		return -1
	}
	return math.Abs(*sr.ChangeAvg)
}

// sortByChange puts the series with the biggest change first in each
// host, and the host with the biggest change first. The sort is stable, so
// equal changes keep the name order.
func sortByChange(hosts []HostSeries) {
	biggest := func(h HostSeries) float64 {
		v := -1.0
		for _, sr := range h.Metrics {
			v = max(v, sr.absChange())
		}
		return v
	}
	for _, h := range hosts {
		slices.SortStableFunc(h.Metrics, func(a, b Series) int { return cmp.Compare(b.absChange(), a.absChange()) })
	}
	slices.SortStableFunc(hosts, func(a, b HostSeries) int { return cmp.Compare(biggest(b), biggest(a)) })
}
