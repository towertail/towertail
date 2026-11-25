package mcp

import (
	"cmp"
	"context"
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
)

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
