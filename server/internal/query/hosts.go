package query

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

const (
	// topHosts is the length of each top list of the fleet summary.
	topHosts = 5
	// factsAge is how far back the host facts look. Raw samples keep 7 days.
	factsAge = 7 * 24 * time.Hour
)

// HostDetail is one host with its latest values, facts, open alerts and
// effective rules. Latest and Facts are nil when the server has no
// ClickHouse, or the host has no sample.
type HostDetail struct {
	Node       store.Node
	Latest     *wire.FleetHost
	Facts      *clickhouse.HostFacts
	OpenAlerts []wire.Alert
	Effective  store.EffectiveRules
}

// HostDetail gives the detail of one host. It returns store.ErrNotFound
// for a node that the org does not have.
func (s *Service) HostDetail(ctx context.Context, orgID, nodeID uuid.UUID) (HostDetail, error) {
	node, err := s.node(ctx, orgID, nodeID)
	if err != nil {
		return HostDetail{}, err
	}
	settings, err := s.settings(ctx, orgID)
	if err != nil {
		return HostDetail{}, err
	}
	out := HostDetail{Node: node, OpenAlerts: []wire.Alert{}, Effective: settings.Rules().For(node.Rules())}
	if s.ch == nil {
		return out, nil
	}
	hosts, err := s.Hosts(ctx, orgID, SortCPU, 0)
	if err != nil {
		return HostDetail{}, err
	}
	if i := slices.IndexFunc(hosts.Hosts, func(h wire.FleetHost) bool { return h.NodeID == nodeID }); i >= 0 {
		out.Latest = &hosts.Hosts[i]
	}
	now := s.now().UTC()
	since, _ := s.Clamp(ctx, orgID, now.Add(-factsAge), now)
	facts, ok, err := s.ch.HostFacts(ctx, orgID, nodeID, since)
	if err != nil {
		return HostDetail{}, err
	}
	if ok {
		out.Facts = &facts
	}
	open, err := s.Alerts(ctx, AlertsRequest{OrgID: orgID, Open: true, NodeID: nodeID})
	if err != nil {
		return HostDetail{}, err
	}
	out.OpenAlerts = open.Alerts
	return out, nil
}

// HostOver is a host with a latest value at or over a limit of its
// effective rules. Value is in the unit of the tool: a percent for cpu,
// a ratio for mem and disk.
type HostOver struct {
	NodeID uuid.UUID
	Name   string
	Metric string
	Value  float64
	Tint   wire.Tint
}

// FleetSummary is the state of the fleet at a glance.
type FleetSummary struct {
	Hosts, Online, Offline int
	OpenAlerts             map[wire.Tint]int
	Over                   []HostOver
	TopCPU, TopMem         []wire.FleetHost
	TopDisk                []wire.FleetHost
	Alerts24h              int
	Notifications24h       int
	Tags                   map[string]int
}

// FleetSummary counts the hosts, the open alerts and the notifications of
// the last 24 hours. Over compares the latest cpu, mem and disk of each
// online host with its effective pair. The plan clamps the 24 hours.
func (s *Service) FleetSummary(ctx context.Context, orgID uuid.UUID) (FleetSummary, error) {
	fleet, err := s.Hosts(ctx, orgID, SortCPU, 0)
	if err != nil {
		return FleetSummary{}, err
	}
	nodes, err := s.nodes.ListNodes(ctx, orgID)
	if err != nil {
		return FleetSummary{}, err
	}
	settings, err := s.settings(ctx, orgID)
	if err != nil {
		return FleetSummary{}, err
	}
	out := FleetSummary{
		Hosts: len(fleet.Hosts), OpenAlerts: map[wire.Tint]int{}, Over: []HostOver{}, Tags: map[string]int{},
		TopCPU: SortCPU.top(fleet.Hosts, topHosts), TopMem: SortMem.top(fleet.Hosts, topHosts), TopDisk: SortDisk.top(fleet.Hosts, topHosts),
	}
	byID := make(map[uuid.UUID]store.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
		for _, tag := range n.Tags {
			out.Tags[tag]++
		}
	}
	org := settings.Rules()
	for _, h := range fleet.Hosts {
		if !h.Online {
			out.Offline++
			continue
		}
		out.Online++
		out.Over = append(out.Over, overOf(h, org.For(byID[h.NodeID].Rules()))...)
	}
	slices.SortFunc(out.Over, func(a, b HostOver) int {
		if c := cmp.Compare(rank(a.Tint), rank(b.Tint)); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Metric, b.Metric)
	})
	open, err := s.Alerts(ctx, AlertsRequest{OrgID: orgID, Open: true})
	if err != nil {
		return FleetSummary{}, err
	}
	for _, a := range open.Alerts {
		out.OpenAlerts[a.Tint]++
	}
	now := s.now().UTC()
	from, _ := s.Clamp(ctx, orgID, now.Add(-24*time.Hour), now)
	page, err := s.ListAlerts(ctx, AlertQuery{OrgID: orgID, State: "all", From: from, To: now, StartedFrom: &from, Limit: 1, Now: now})
	if err != nil {
		return FleetSummary{}, err
	}
	out.Alerts24h = page.Total
	sent, err := s.ch.NotifiedCounts(ctx, clickhouse.NotifiedQuery{OrgID: orgID, From: from, To: now})
	if err != nil {
		return FleetSummary{}, err
	}
	for _, r := range sent {
		out.Notifications24h += r.Sends
	}
	return out, nil
}

// rank orders critical before warn.
func rank(t wire.Tint) int {
	if t == wire.TintCritical {
		return 0
	}
	return 1
}

// overOf lists the metrics of the host that are at or over a limit. A
// muted metric is left out.
func overOf(h wire.FleetHost, eff store.EffectiveRules) []HostOver {
	var out []HostOver
	for _, m := range []struct {
		metric store.ThresholdMetric
		value  *float64
		scale  float64
	}{{store.MetricCPU, h.CPU, 100}, {store.MetricMem, h.Mem, 1}, {store.MetricDisk, h.Disk, 1}} {
		if m.value == nil || eff.Muted.Has(m.metric) {
			continue
		}
		pair, ratio := eff.Thresholds.Pair(m.metric), *m.value/m.scale
		switch {
		case ratio >= pair.Critical:
			out = append(out, HostOver{h.NodeID, h.Name, string(m.metric), *m.value, wire.TintCritical})
		case ratio >= pair.Warn:
			out = append(out, HostOver{h.NodeID, h.Name, string(m.metric), *m.value, wire.TintWarn})
		}
	}
	return out
}

// top gives the n online hosts with the highest value of the sort.
func (h HostSort) top(hosts []wire.FleetHost, n int) []wire.FleetHost {
	online := slices.DeleteFunc(slices.Clone(hosts), func(x wire.FleetHost) bool { return !x.Online })
	slices.SortStableFunc(online, func(a, b wire.FleetHost) int { return cmp.Compare(h.key(b), h.key(a)) })
	return online[:min(n, len(online))]
}

// node gives one node of the org.
func (s *Service) node(ctx context.Context, orgID, nodeID uuid.UUID) (store.Node, error) {
	if s.config != nil {
		return s.config.GetNode(ctx, orgID, nodeID)
	}
	nodes, err := s.nodes.ListNodes(ctx, orgID)
	if err != nil {
		return store.Node{}, err
	}
	if i := slices.IndexFunc(nodes, func(n store.Node) bool { return n.ID == nodeID }); i >= 0 {
		return nodes[i], nil
	}
	return store.Node{}, store.ErrNotFound
}

// settings gives the settings of the org, or the defaults with no Config.
func (s *Service) settings(ctx context.Context, orgID uuid.UUID) (store.ServerSettings, error) {
	if s.config == nil {
		return store.DefaultServerSettings(), nil
	}
	return s.config.GetSettings(ctx, orgID)
}

// defaultCooldown is the cooldown of the alerter when the settings have none.
const defaultCooldown = 10 * time.Minute

// NotifyFlags are the switches that a notification passes. The Host fields
// are nil for the org rules.
type NotifyFlags struct {
	Enabled, Warn, Critical bool
	HostWarn, HostCritical  *bool
	HostSnoozedUntil        *time.Time
}

// RulesResult holds the rules of the org and, for a host, the rules of the
// host and the rules that apply to it. Host and Effective are nil when
// NodeID is uuid.Nil.
type RulesResult struct {
	Org                store.OrgRules
	NodeID             uuid.UUID
	Host               *store.HostRules
	Effective          *store.EffectiveRules
	Cooldown, Debounce time.Duration
	Notify             NotifyFlags
}

// Rules gives the rules of the org. With a node, it also gives the rules of
// the node and the effective rules. It returns store.ErrNotFound for a node
// that the org does not have.
func (s *Service) Rules(ctx context.Context, orgID, nodeID uuid.UUID) (RulesResult, error) {
	settings, err := s.settings(ctx, orgID)
	if err != nil {
		return RulesResult{}, err
	}
	out := RulesResult{
		Org:      settings.Rules(),
		NodeID:   nodeID,
		Cooldown: time.Duration(settings.NotifyCooldownSeconds) * time.Second,
		Debounce: time.Duration(settings.NotifyDebounceSeconds) * time.Second,
		Notify:   NotifyFlags{Enabled: settings.NotificationsEnabled, Warn: settings.NotifyWarn, Critical: settings.NotifyCritical},
	}
	if out.Cooldown <= 0 {
		out.Cooldown = defaultCooldown
	}
	if nodeID == uuid.Nil {
		return out, nil
	}
	node, err := s.node(ctx, orgID, nodeID)
	if err != nil {
		return RulesResult{}, err
	}
	host := node.Rules()
	eff := out.Org.For(host)
	out.Host, out.Effective = &host, &eff
	out.Notify.HostWarn, out.Notify.HostCritical, out.Notify.HostSnoozedUntil = &node.NotifyOnWarn, &node.NotifyOnCritical, node.SnoozedUntil
	return out, nil
}
