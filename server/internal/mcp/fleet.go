package mcp

import (
	"context"
	"maps"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// ListHostsIn is the input of list_hosts.
type ListHostsIn struct {
	Sort  string `json:"sort,omitempty" jsonschema:"order of the hosts, highest first: cpu, mem, disk or net. Default cpu."`
	Limit int    `json:"limit,omitempty" jsonschema:"most hosts to return, 1 to 200. Default 50."`
}

// Host is one host with its latest values.
type Host struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Tags     []string `json:"tags,omitempty"`
	Online   bool     `json:"online"`
	LastSeen string   `json:"last_seen,omitempty"`
	CPUPct   *float64 `json:"cpu_pct,omitempty"`
	MemRatio *float64 `json:"mem_ratio,omitempty"`
	// DiskRatio is the use of the fullest mount.
	DiskRatio *float64 `json:"disk_ratio,omitempty"`
	Load1     *float64 `json:"load1,omitempty"`
	NetRxBps  *uint64  `json:"net_rx_bps,omitempty"`
	NetTxBps  *uint64  `json:"net_tx_bps,omitempty"`
}

// ListHostsOut is the output of list_hosts.
type ListHostsOut struct {
	Hosts []Host `json:"hosts"`
}

func (s *Server) listHosts(ctx context.Context, _ *sdk.CallToolRequest, in ListHostsIn) (*sdk.CallToolResult, ListHostsOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, ListHostsOut{}, err
	}
	orgID := c.Token.OrgID
	sort, err := query.ParseHostSort(in.Sort)
	if err != nil {
		return nil, ListHostsOut{}, s.toolError("list_hosts", err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultHosts
	}
	res, err := s.q.Hosts(ctx, orgID, sort, min(limit, maxHosts))
	if err != nil {
		return nil, ListHostsOut{}, s.toolError("list_hosts", err)
	}
	out := ListHostsOut{Hosts: make([]Host, 0, len(res.Hosts))}
	for _, h := range res.Hosts {
		out.Hosts = append(out.Hosts, newHost(h))
	}
	return nil, out, nil
}

// newHost converts a host of the query service.
func newHost(h wire.FleetHost) Host {
	return Host{
		ID: h.NodeID.String(), Name: h.Name, Tags: h.Tags, Online: h.Online, LastSeen: tsPtr(h.LastSeen),
		CPUPct: h.CPU, MemRatio: h.Mem, DiskRatio: h.Disk, Load1: h.Load1, NetRxBps: h.NetRx, NetTxBps: h.NetTx,
	}
}

// FleetSummaryIn is the input of fleet_summary. It has no fields.
type FleetSummaryIn struct{}

// OpenAlertCounts counts the open alerts by tint.
type OpenAlertCounts struct {
	Critical int `json:"critical"`
	Warn     int `json:"warn"`
}

// HostOver is a host at or over a limit. Value is a percent for cpu and a
// ratio for mem and disk.
type HostOver struct {
	Host   string  `json:"host"`
	HostID string  `json:"host_id"`
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Tint   string  `json:"tint"`
}

// FleetSummaryOut is the output of fleet_summary.
type FleetSummaryOut struct {
	Hosts            int             `json:"hosts"`
	Online           int             `json:"online"`
	Offline          int             `json:"offline"`
	OpenAlerts       OpenAlertCounts `json:"open_alerts"`
	Over             []HostOver      `json:"over"`
	TopCPU           []Host          `json:"top_cpu"`
	TopMem           []Host          `json:"top_mem"`
	TopDisk          []Host          `json:"top_disk"`
	Alerts24h        int             `json:"alerts_24h"`
	Notifications24h int             `json:"notifications_24h"`
	Tags             map[string]int  `json:"tags"`
}

func (s *Server) fleetSummary(ctx context.Context, _ *sdk.CallToolRequest, _ FleetSummaryIn) (*sdk.CallToolResult, FleetSummaryOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, FleetSummaryOut{}, err
	}
	sum, err := s.q.FleetSummary(ctx, c.Token.OrgID)
	if err != nil {
		return nil, FleetSummaryOut{}, s.toolError("fleet_summary", err)
	}
	out := FleetSummaryOut{
		Hosts: sum.Hosts, Online: sum.Online, Offline: sum.Offline,
		OpenAlerts: OpenAlertCounts{Critical: sum.OpenAlerts[wire.TintCritical], Warn: sum.OpenAlerts[wire.TintWarn]},
		Over:       make([]HostOver, 0, len(sum.Over)),
		TopCPU:     newHosts(sum.TopCPU), TopMem: newHosts(sum.TopMem), TopDisk: newHosts(sum.TopDisk),
		Alerts24h: sum.Alerts24h, Notifications24h: sum.Notifications24h, Tags: map[string]int{},
	}
	maps.Copy(out.Tags, sum.Tags)
	for _, o := range sum.Over {
		out.Over = append(out.Over, HostOver{Host: o.Name, HostID: o.NodeID.String(), Metric: o.Metric, Value: o.Value, Tint: string(o.Tint)})
	}
	return nil, out, nil
}

func newHosts(hosts []wire.FleetHost) []Host {
	out := make([]Host, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, newHost(h))
	}
	return out
}

// GetHostIn is the input of get_host.
type GetHostIn struct {
	Host string `json:"host" jsonschema:"host ID or name"`
}

// HostFactsOut are the static facts of a host.
type HostFactsOut struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Kernel        string `json:"kernel"`
	Cores         int    `json:"cores"`
	MemTotalBytes uint64 `json:"mem_total_bytes"`
	UptimeS       uint64 `json:"uptime_s"`
	Sampler       string `json:"sampler"`
}

// Limit is the effective warn and critical level of a metric.
type Limit struct {
	Warn     float64 `json:"warn"`
	Critical float64 `json:"critical"`
	Unit     string  `json:"unit"`
	// Source is org or host.
	Source string `json:"source"`
}

// GetHostOut is the output of get_host. Online is false and the latest
// values are missing for an offline host.
type GetHostOut struct {
	Host
	Kind            string           `json:"kind"`
	Enabled         bool             `json:"enabled"`
	Facts           *HostFactsOut    `json:"facts,omitempty"`
	SnoozedUntil    string           `json:"snoozed_until,omitempty"`
	OpenAlerts      []AlertItem      `json:"open_alerts"`
	EffectiveLimits map[string]Limit `json:"effective_limits"`
}

func (s *Server) getHost(ctx context.Context, _ *sdk.CallToolRequest, in GetHostIn) (*sdk.CallToolResult, GetHostOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, GetHostOut{}, err
	}
	orgID := c.Token.OrgID
	if strings.TrimSpace(in.Host) == "" {
		return nil, GetHostOut{}, badf("host is required")
	}
	node, err := s.q.ResolveHost(ctx, orgID, in.Host)
	if err != nil {
		return nil, GetHostOut{}, s.toolError("get_host", err)
	}
	d, err := s.q.HostDetail(ctx, orgID, node.ID)
	if err != nil {
		return nil, GetHostOut{}, s.toolError("get_host", err)
	}
	out := GetHostOut{
		Host: Host{ID: d.Node.ID.String(), Name: d.Node.DisplayName, Tags: d.Node.Tags},
		Kind: d.Node.Kind, Enabled: d.Node.Enabled, SnoozedUntil: tsPtr(d.Node.SnoozedUntil),
		OpenAlerts: make([]AlertItem, 0, len(d.OpenAlerts)), EffectiveLimits: map[string]Limit{},
	}
	if d.Latest != nil {
		out.Host = newHost(*d.Latest)
	}
	if f := d.Facts; f != nil {
		out.Facts = &HostFactsOut{
			OS: f.OS, Arch: f.Arch, Kernel: f.Kernel, Cores: int(f.Cores), MemTotalBytes: f.MemTotal, UptimeS: f.UptimeS, Sampler: f.Sampler,
		}
	}
	for _, a := range d.OpenAlerts {
		out.OpenAlerts = append(out.OpenAlerts, newAlertItem(a, d.Node.DisplayName))
	}
	for _, m := range slices.Concat(store.UsageMetrics, store.HealthMetrics) {
		p := d.Effective.Thresholds.Pair(m)
		out.EffectiveLimits[string(m)] = Limit{
			Warn: p.Warn, Critical: p.Critical, Unit: thresholdUnit(m), Source: string(d.Effective.Sources.Thresholds[m]),
		}
	}
	return nil, out, nil
}
