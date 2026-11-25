package mcp

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

// Limits of the tool replies.
const (
	defaultHosts = 50
	maxHosts     = 200
	maxPoints    = 300
	maxCompare   = 50
)

// metricNames are the metrics that host_metrics and compare accept.
var metricNames = []string{"cpu", "mem", "net_rx", "net_tx", "disk_read", "disk_write"}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return ts(*t)
}

// hostID gives the node ID of a host reference. An empty reference means
// the whole fleet and gives uuid.Nil.
func (s *Server) hostID(ctx context.Context, orgID uuid.UUID, ref string) (uuid.UUID, error) {
	if strings.TrimSpace(ref) == "" {
		return uuid.Nil, nil
	}
	n, err := s.q.ResolveHost(ctx, orgID, ref)
	return n.ID, err
}

func checkMetric(m string) error {
	if !slices.Contains(metricNames, m) {
		return badf("metric must be one of %s", strings.Join(metricNames, ", "))
	}
	return nil
}

// HostMetricsIn is the input of host_metrics.
type HostMetricsIn struct {
	Host       string   `json:"host" jsonschema:"host ID or name"`
	Metrics    []string `json:"metrics,omitempty" jsonschema:"metrics to read, at most 6: cpu (percent), mem (ratio 0 to 1), net_rx and net_tx (bytes each second), disk_read and disk_write (bytes each second). Default cpu."`
	Range      string   `json:"range,omitempty" jsonschema:"span up to now, 15m to 365d, for example 1h, 24h or 7d. Default 1h. Do not use with from."`
	From       string   `json:"from,omitempty" jsonschema:"start of the range in RFC 3339. Use it with to, instead of range."`
	To         string   `json:"to,omitempty" jsonschema:"end of the range in RFC 3339. Default now."`
	Resolution string   `json:"resolution,omitempty" jsonschema:"auto, raw, 1m, 5m or 1h. Default auto. The server raises the step to keep at most 300 points."`
}

// Point is one bucket of a series.
type Point struct {
	TS    string  `json:"ts"`
	Value float64 `json:"value"`
	Max   float64 `json:"max,omitempty"`
}

// Series is one metric of a host over a range. Count is the number of
// points before the cap. A Count of 0 means no data.
type Series struct {
	Metric     string  `json:"metric"`
	Resolution string  `json:"resolution"`
	Count      int     `json:"count"`
	Min        float64 `json:"min"`
	Avg        float64 `json:"avg"`
	Max        float64 `json:"max"`
	Last       float64 `json:"last"`
	Points     []Point `json:"points"`
}

// HostMetricsOut is the output of host_metrics.
type HostMetricsOut struct {
	Host   string `json:"host"`
	HostID string `json:"host_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	// Clamped is true when the plan history limit moved the start later.
	Clamped bool     `json:"clamped,omitempty"`
	Metrics []Series `json:"metrics"`
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

func (s *Server) hostMetrics(ctx context.Context, _ *sdk.CallToolRequest, in HostMetricsIn) (*sdk.CallToolResult, HostMetricsOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, HostMetricsOut{}, err
	}
	orgID := c.Token.OrgID
	metrics := in.Metrics
	if len(metrics) == 0 {
		metrics = []string{"cpu"}
	}
	metrics = slices.Compact(slices.Sorted(slices.Values(metrics)))
	if len(metrics) > len(metricNames) {
		return nil, HostMetricsOut{}, badf("metrics has too many names")
	}
	for _, m := range metrics {
		if err := checkMetric(m); err != nil {
			return nil, HostMetricsOut{}, err
		}
	}
	res, err := parseResolution(in.Resolution)
	if err != nil {
		return nil, HostMetricsOut{}, err
	}
	from, to, err := parseRange(in.Range, in.From, in.To, s.now().UTC())
	if err != nil {
		return nil, HostMetricsOut{}, err
	}
	if strings.TrimSpace(in.Host) == "" {
		return nil, HostMetricsOut{}, badf("host is required")
	}
	node, err := s.q.ResolveHost(ctx, orgID, in.Host)
	if err != nil {
		return nil, HostMetricsOut{}, s.toolError("host_metrics", err)
	}
	// The step keeps the points of a metric under the cap.
	minStep := ((to.Sub(from)/maxPoints + time.Second - 1) / time.Second) * time.Second
	step := max(res, minStep)
	out := HostMetricsOut{Host: node.DisplayName, HostID: node.ID.String(), From: ts(from), To: ts(to), Metrics: []Series{}}
	for _, m := range metrics {
		pts, bucket, clamped, err := s.q.History(ctx, query.HistoryRequest{
			OrgID: orgID, NodeID: node.ID, Metric: m, From: from, To: to, Step: step,
		})
		if err != nil {
			return nil, HostMetricsOut{}, s.toolError("host_metrics", err)
		}
		out.Clamped = out.Clamped || clamped
		out.Metrics = append(out.Metrics, newSeries(m, bucket, pts))
	}
	return nil, out, nil
}

// newSeries gives the stats of all points, and at most maxPoints points.
func newSeries(metric string, bucket time.Duration, pts []clickhouse.HistoryPoint) Series {
	sr := Series{Metric: metric, Resolution: resolutionText(bucket), Count: len(pts), Points: []Point{}}
	if len(pts) == 0 {
		return sr
	}
	sr.Min, sr.Max, sr.Last = pts[0].Value, pts[0].Value, pts[len(pts)-1].Value
	var sum float64
	for _, p := range pts {
		sum += p.Value
		sr.Min = min(sr.Min, p.Value)
		sr.Max = max(sr.Max, p.Value, p.Max)
	}
	sr.Avg = sum / float64(len(pts))
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

// TopProcessesIn is the input of top_processes.
type TopProcessesIn struct {
	Host  string `json:"host,omitempty" jsonschema:"host ID or name. Empty means the whole fleet."`
	By    string `json:"by,omitempty" jsonschema:"order: cpu (average CPU) or mem (peak memory). Default cpu."`
	Range string `json:"range,omitempty" jsonschema:"span up to now, 15m to 365d, for example 1h, 24h or 7d. Default 1h."`
	Limit int    `json:"limit,omitempty" jsonschema:"most processes to return, 1 to 50. Default 20."`
}

// Process is one process of a host over a range.
type Process struct {
	Host        string  `json:"host"`
	HostID      string  `json:"host_id"`
	Name        string  `json:"name"`
	Command     string  `json:"command"`
	Repo        string  `json:"repo,omitempty"`
	CPUPctAvg   float64 `json:"cpu_pct_avg"`
	CPUPctMax   float64 `json:"cpu_pct_max"`
	RSSMaxBytes uint64  `json:"rss_max_bytes"`
}

// TopProcessesOut is the output of top_processes.
type TopProcessesOut struct {
	From    string `json:"from"`
	To      string `json:"to"`
	By      string `json:"by"`
	Clamped bool   `json:"clamped,omitempty"`
	// Processes come in the order of By.
	Processes []Process `json:"processes"`
}

func (s *Server) topProcesses(ctx context.Context, _ *sdk.CallToolRequest, in TopProcessesIn) (*sdk.CallToolResult, TopProcessesOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, TopProcessesOut{}, err
	}
	orgID := c.Token.OrgID
	by, err := query.ParseProcOrder(in.By)
	if err != nil {
		return nil, TopProcessesOut{}, s.toolError("top_processes", err)
	}
	from, to, err := parseRange(in.Range, "", "", s.now().UTC())
	if err != nil {
		return nil, TopProcessesOut{}, err
	}
	node, err := s.hostID(ctx, orgID, in.Host)
	if err != nil {
		return nil, TopProcessesOut{}, s.toolError("top_processes", err)
	}
	res, err := s.q.TopProcesses(ctx, query.ProcQuery{OrgID: orgID, NodeID: node, From: from, To: to, By: by, Limit: in.Limit})
	if err != nil {
		return nil, TopProcessesOut{}, s.toolError("top_processes", err)
	}
	out := TopProcessesOut{From: ts(res.From), To: ts(res.To), By: res.By, Clamped: res.Clamped, Processes: make([]Process, 0, len(res.Processes))}
	for _, p := range res.Processes {
		out.Processes = append(out.Processes, Process{
			Host: p.NodeName, HostID: p.NodeID.String(), Name: p.Name, Command: p.Cmd, Repo: p.Repo,
			CPUPctAvg: p.CPUPctAvg, CPUPctMax: p.CPUPctMax, RSSMaxBytes: p.RSSMax,
		})
	}
	return nil, out, nil
}

// AlertsIn is the input of alerts.
type AlertsIn struct {
	State string `json:"state,omitempty" jsonschema:"open (default) or recent. Recent alerts also include the changes back to ok."`
	Days  int    `json:"days,omitempty" jsonschema:"for recent alerts: how many days back, 1 to 90. Default 7."`
	Host  string `json:"host,omitempty" jsonschema:"host ID or name. Empty means the whole fleet."`
}

// AlertItem is one alert event.
type AlertItem struct {
	ID     string `json:"id"`
	Host   string `json:"host"`
	HostID string `json:"host_id"`
	Kind   string `json:"kind"`
	Metric string `json:"metric"`
	// Tint is ok, warn or crit.
	Tint         string  `json:"tint"`
	Value        float64 `json:"value"`
	Start        string  `json:"start"`
	AckedBy      string  `json:"acked_by,omitempty"`
	AckedAt      string  `json:"acked_at,omitempty"`
	SnoozedUntil string  `json:"snoozed_until,omitempty"`
}

// newAlertItem converts an alert of the query service.
func newAlertItem(a wire.Alert, host string) AlertItem {
	item := AlertItem{
		ID: a.ID.String(), Host: host, HostID: a.NodeID.String(), Kind: a.Kind, Metric: a.Metric,
		Tint: string(a.Tint), Value: a.Value, Start: ts(a.TS), AckedAt: tsPtr(a.AckedAt), SnoozedUntil: tsPtr(a.SnoozedUntil),
	}
	if a.AckedBy != nil {
		item.AckedBy = a.AckedBy.String()
	}
	return item
}

// AlertsOut is the output of alerts.
type AlertsOut struct {
	// Clamped is true when the plan history limit moved the start later.
	Clamped bool        `json:"clamped,omitempty"`
	Alerts  []AlertItem `json:"alerts"`
}

func (s *Server) alerts(ctx context.Context, _ *sdk.CallToolRequest, in AlertsIn) (*sdk.CallToolResult, AlertsOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, AlertsOut{}, err
	}
	orgID := c.Token.OrgID
	var open bool
	switch in.State {
	case "", "open":
		open = true
	case "recent":
	default:
		return nil, AlertsOut{}, badf("state must be open or recent")
	}
	node, err := s.hostID(ctx, orgID, in.Host)
	if err != nil {
		return nil, AlertsOut{}, s.toolError("alerts", err)
	}
	res, err := s.q.Alerts(ctx, query.AlertsRequest{OrgID: orgID, Open: open, Days: in.Days, NodeID: node})
	if err != nil {
		return nil, AlertsOut{}, s.toolError("alerts", err)
	}
	names, err := s.q.HostNames(ctx, orgID)
	if err != nil {
		return nil, AlertsOut{}, s.toolError("alerts", err)
	}
	out := AlertsOut{Clamped: res.Clamped, Alerts: make([]AlertItem, 0, len(res.Alerts))}
	for _, a := range res.Alerts {
		out.Alerts = append(out.Alerts, newAlertItem(a, names[a.NodeID]))
	}
	return nil, out, nil
}

// DisksIn is the input of disks.
type DisksIn struct {
	Host  string `json:"host" jsonschema:"host ID or name"`
	Range string `json:"range,omitempty" jsonschema:"span up to now, 15m to 365d, for example 24h or 7d. Default 24h."`
}

// Mount is the use of one mount.
type Mount struct {
	Mount      string  `json:"mount"`
	FS         string  `json:"fs"`
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Ratio      float64 `json:"ratio"`
	// ChangeBytes is the used bytes now minus the used bytes at the start of the range.
	ChangeBytes int64 `json:"change_bytes"`
}

// DisksOut is the output of disks.
type DisksOut struct {
	Host    string  `json:"host"`
	HostID  string  `json:"host_id"`
	From    string  `json:"from"`
	To      string  `json:"to"`
	Clamped bool    `json:"clamped,omitempty"`
	Mounts  []Mount `json:"mounts"`
}

func (s *Server) disks(ctx context.Context, _ *sdk.CallToolRequest, in DisksIn) (*sdk.CallToolResult, DisksOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, DisksOut{}, err
	}
	orgID := c.Token.OrgID
	from, to, err := parseRange(cmp.Or(in.Range, "24h"), "", "", s.now().UTC())
	if err != nil {
		return nil, DisksOut{}, err
	}
	if strings.TrimSpace(in.Host) == "" {
		return nil, DisksOut{}, badf("host is required")
	}
	node, err := s.q.ResolveHost(ctx, orgID, in.Host)
	if err != nil {
		return nil, DisksOut{}, s.toolError("disks", err)
	}
	res, err := s.q.Disks(ctx, orgID, node.ID, from, to)
	if err != nil {
		return nil, DisksOut{}, s.toolError("disks", err)
	}
	out := DisksOut{Host: node.DisplayName, HostID: node.ID.String(), From: ts(res.From), To: ts(res.To), Clamped: res.Clamped, Mounts: []Mount{}}
	// The points come in mount order, the oldest first.
	var first clickhouse.DiskPoint
	for i, p := range res.Points {
		if i == 0 || p.Mount != res.Points[i-1].Mount {
			first = p
			out.Mounts = append(out.Mounts, Mount{Mount: p.Mount})
		}
		m := &out.Mounts[len(out.Mounts)-1]
		m.FS, m.UsedBytes, m.TotalBytes = p.FS, p.Used, p.Total
		m.ChangeBytes = int64(p.Used) - int64(first.Used)
		m.Ratio = 0
		if p.Total > 0 {
			m.Ratio = float64(p.Used) / float64(p.Total)
		}
	}
	return nil, out, nil
}

// CompareIn is the input of compare.
type CompareIn struct {
	Metric string `json:"metric,omitempty" jsonschema:"metric to compare: cpu (percent), mem (ratio 0 to 1), net_rx, net_tx, disk_read or disk_write (bytes each second). Default cpu."`
	Window string `json:"window,omitempty" jsonschema:"length of each window, 15m to 30d, for example 1h, 1d or 7d. The last window ends now. Default 1h."`
	Host   string `json:"host,omitempty" jsonschema:"host ID or name. Empty means all hosts."`
}

// StatOut is the average and the highest value of a metric in a window.
type StatOut struct {
	Avg    float64 `json:"avg"`
	Max    float64 `json:"max"`
	Points int     `json:"points"`
}

// WindowOut is a time range.
type WindowOut struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// CompareHost is the change of one host between two windows.
type CompareHost struct {
	Host     string   `json:"host"`
	HostID   string   `json:"host_id"`
	Current  StatOut  `json:"current"`
	Previous *StatOut `json:"previous,omitempty"`
	// ChangeAvg is the average now minus the average before. It is
	// missing when the host has no data in one of the windows.
	ChangeAvg *float64 `json:"change_avg,omitempty"`
}

// CompareOut is the output of compare. The hosts with the biggest
// change come first, and the list ends at 50 hosts.
type CompareOut struct {
	Metric     string        `json:"metric"`
	Current    WindowOut     `json:"current"`
	Previous   WindowOut     `json:"previous"`
	Clamped    bool          `json:"clamped,omitempty"`
	TotalHosts int           `json:"total_hosts"`
	Hosts      []CompareHost `json:"hosts"`
}

func (s *Server) compare(ctx context.Context, _ *sdk.CallToolRequest, in CompareIn) (*sdk.CallToolResult, CompareOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, CompareOut{}, err
	}
	orgID := c.Token.OrgID
	metric := cmp.Or(in.Metric, "cpu")
	if err := checkMetric(metric); err != nil {
		return nil, CompareOut{}, err
	}
	window, err := parseSpan(cmp.Or(in.Window, "1h"))
	if err != nil {
		return nil, CompareOut{}, err
	}
	node, err := s.hostID(ctx, orgID, in.Host)
	if err != nil {
		return nil, CompareOut{}, s.toolError("compare", err)
	}
	res, err := s.q.Compare(ctx, query.CompareQuery{OrgID: orgID, NodeID: node, Metric: metric, Window: window})
	if err != nil {
		return nil, CompareOut{}, s.toolError("compare", err)
	}
	out := CompareOut{
		Metric:     res.Metric,
		Current:    WindowOut{From: ts(res.Current.From), To: ts(res.Current.To)},
		Previous:   WindowOut{From: ts(res.Previous.From), To: ts(res.Previous.To)},
		Clamped:    res.Clamped,
		TotalHosts: len(res.Hosts),
		Hosts:      []CompareHost{},
	}
	for _, h := range res.Hosts[:min(len(res.Hosts), maxCompare)] {
		item := CompareHost{Host: h.Name, HostID: h.NodeID.String(), Current: StatOut(h.Current), ChangeAvg: h.DeltaAvg}
		if h.Previous != nil {
			item.Previous = &StatOut{Avg: h.Previous.Avg, Max: h.Previous.Max, Points: h.Previous.Points}
		}
		out.Hosts = append(out.Hosts, item)
	}
	return nil, out, nil
}
