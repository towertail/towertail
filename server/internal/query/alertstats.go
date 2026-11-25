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
	// DefaultStatsTop and MaxStatsTop are the limits of the top lists.
	DefaultStatsTop = 10
	MaxStatsTop     = 20
	// flapMinAlerts and flapMaxMedian tell when a metric flaps.
	flapMinAlerts = 3
	flapMaxMedian = 15 * time.Minute
	// statsBins is the number of bins of the start strip.
	statsBins = 24
)

// AlertStatsRequest is a request for the statistics of the alerts that
// overlap the range. Hosts and Kinds are empty for all. Top is 0 for the
// default.
type AlertStatsRequest struct {
	OrgID    uuid.UUID
	From, To time.Time
	Hosts    []uuid.UUID
	Kinds    []string
	Top      int
}

// CountRow counts the alerts and the sends of one host or one kind.
type CountRow struct {
	Key, Name                     string
	Alerts, Critical, Warn, Sends int
}

// RuleRow counts the alerts and the sends of one rule. A rule of the org
// has no node. Warn and Critical are zero when the rule has no limits.
type RuleRow struct {
	Kind           string
	Source         store.RuleSource
	NodeID         *uuid.UUID
	Host           string
	Warn, Critical float64
	SustainSeconds int
	Notify         store.AlertNotify
	Alerts, Sends  int
}

// FlapRow is a metric of a host that opens and closes alerts again and
// again.
type FlapRow struct {
	NodeID         uuid.UUID
	Host, Metric   string
	Alerts         int
	MedianDuration time.Duration
}

// ChannelRow counts the sends to one channel name. Kind is empty when the
// name matches no channel or more than one kind.
type ChannelRow struct {
	Name          string
	Kind          store.ChannelKind
	Sends, Failed int
}

// AlertStats are the statistics of the alerts of one range.
type AlertStats struct {
	From, To                  time.Time
	Clamped, Truncated        bool
	Alerts, Sends, EmailSends int
	ByHost, ByKind            []CountRow
	ByRule                    []RuleRow
	Flapping                  []FlapRow
	ByChannel                 []ChannelRow
	Bins                      []wire.AlertBin
}

// AlertStats counts the alerts that overlap the range, and the sends of
// these alerts, by host, kind, rule and channel. It reads at most
// MaxIncidents alerts. It returns ErrNoHistory with no ClickHouse.
func (s *Service) AlertStats(ctx context.Context, r AlertStatsRequest) (AlertStats, error) {
	top := cmp.Or(r.Top, DefaultStatsTop)
	if top < 1 || top > MaxStatsTop {
		return AlertStats{}, badAlertQuery("top")
	}
	now := s.now().UTC()
	q := AlertQuery{
		OrgID: r.OrgID, State: "all", Kinds: r.Kinds, Hosts: r.Hosts,
		From: r.From, To: r.To, Bins: statsBins, Limit: 1, Now: now,
	}
	clamped, err := q.Validate()
	if err != nil {
		return AlertStats{}, err
	}
	var plan bool
	q.From, plan = s.Clamp(ctx, q.OrgID, q.From, now)
	out := AlertStats{From: q.From, To: q.To, Clamped: clamped || plan}
	if s.ch == nil {
		return AlertStats{}, ErrNoHistory
	}
	if !q.From.Before(q.To) {
		return out, nil
	}
	set, truncated, err := s.loadAlerts(ctx, q)
	if err != nil {
		return AlertStats{}, err
	}
	set = set.filter(q, "")
	out.Truncated = truncated
	out.Alerts = len(set)
	out.Bins = set.bins(q)

	sent, err := s.sendsOf(ctx, q, set)
	if err != nil {
		return AlertStats{}, err
	}
	hosts, kinds := map[uuid.UUID]*CountRow{}, map[string]*CountRow{}
	for _, a := range set {
		n := sent[a.ID].Sends
		out.Sends += n
		h := rowOf(hosts, a.NodeID, func() CountRow { return CountRow{Key: a.NodeID.String(), Name: a.NodeName} })
		k := rowOf(kinds, kindOf(a), func() CountRow { return CountRow{Key: kindOf(a), Name: kindOf(a)} })
		for _, c := range []*CountRow{h, k} {
			c.Alerts++
			c.Sends += n
			if a.Severity == wire.TintCritical {
				c.Critical++
			} else {
				c.Warn++
			}
		}
	}
	out.ByHost = sortedRows(hosts)
	out.ByHost = out.ByHost[:min(top, len(out.ByHost))]
	out.ByKind = sortedRows(kinds)
	if out.ByRule, err = s.ruleRows(ctx, q.OrgID, set, sent); err != nil {
		return AlertStats{}, err
	}
	out.ByRule = out.ByRule[:min(top, len(out.ByRule))]
	out.Flapping = set.flapping(now)
	out.Flapping = out.Flapping[:min(top, len(out.Flapping))]
	if out.ByChannel, out.EmailSends, err = s.channelRows(ctx, q.OrgID, sent); err != nil {
		return AlertStats{}, err
	}
	return out, nil
}

// rowOf gives the row of the key, and adds it with the make function when
// it is new.
func rowOf[K comparable](m map[K]*CountRow, key K, mk func() CountRow) *CountRow {
	if m[key] == nil {
		r := mk()
		m[key] = &r
	}
	return m[key]
}

// sortedRows lists the rows, the most alerts first, then by name.
func sortedRows[K comparable](m map[K]*CountRow) []CountRow {
	out := make([]CountRow, 0, len(m))
	for _, r := range m {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b CountRow) int {
		return cmp.Or(cmp.Compare(b.Alerts, a.Alerts), strings.Compare(a.Name, b.Name), strings.Compare(a.Key, b.Key))
	})
	return out
}

// sendsOf gives the sends of the alerts of the set, by alert id.
func (s *Service) sendsOf(ctx context.Context, q AlertQuery, set alertSet) (map[uuid.UUID]clickhouse.NotifiedRow, error) {
	rows, err := s.ch.NotifiedCounts(ctx, clickhouse.NotifiedQuery{OrgID: q.OrgID, From: q.From, To: q.To, NodeIDs: q.Hosts})
	if err != nil {
		return nil, err
	}
	in := make(map[uuid.UUID]bool, len(set))
	for _, a := range set {
		in[a.ID] = true
	}
	out := make(map[uuid.UUID]clickhouse.NotifiedRow, len(rows))
	for _, r := range rows {
		if in[r.AlertID] {
			out[r.AlertID] = r
		}
	}
	return out, nil
}

// ruleRows groups the alerts by the rule of their kind: one row for the
// org rule of a kind, and one row for each host that has its own rule.
func (s *Service) ruleRows(ctx context.Context, orgID uuid.UUID, set alertSet, sent map[uuid.UUID]clickhouse.NotifiedRow) ([]RuleRow, error) {
	settings, err := s.settings(ctx, orgID)
	if err != nil {
		return nil, err
	}
	org := settings.Rules()
	hostRules := map[uuid.UUID]store.HostRules{}
	if s.nodes != nil {
		nodes, err := s.nodes.ListNodes(ctx, orgID)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			hostRules[n.ID] = n.Rules()
		}
	}
	type key struct {
		kind   string
		source store.RuleSource
		node   uuid.UUID
	}
	rows := map[key]*RuleRow{}
	for _, a := range set {
		kind := kindOf(a)
		row := ruleOf(kind, org.For(hostRules[a.NodeID]))
		k := key{kind: kind, source: row.Source}
		if row.Source == store.SourceHost {
			k.node = a.NodeID
		}
		if rows[k] == nil {
			if row.Source == store.SourceHost {
				id := a.NodeID
				row.NodeID, row.Host = &id, a.NodeName
			}
			rows[k] = &row
		}
		rows[k].Alerts++
		rows[k].Sends += sent[a.ID].Sends
	}
	out := make([]RuleRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b RuleRow) int {
		return cmp.Or(cmp.Compare(b.Alerts, a.Alerts), strings.Compare(a.Kind, b.Kind), strings.Compare(a.Host, b.Host))
	})
	return out, nil
}

// ruleOf gives the rule of the kind in the effective rules. A rule is from
// the host when its limits or its alert rule are.
func ruleOf(kind string, eff store.EffectiveRules) RuleRow {
	row := RuleRow{Kind: kind, Source: store.SourceOrg}
	switch kind {
	case "cpu", "mem", "disk":
		m := store.ThresholdMetric(kind)
		p, rule := eff.Thresholds.Pair(m), eff.Alerts
		alert, _ := rule.For(kind)
		row.Warn, row.Critical = p.Warn, p.Critical
		row.SustainSeconds, row.Notify = alert.SustainSeconds, alert.Notify
		if eff.Sources.Thresholds[m] == store.SourceHost || eff.Sources.Alerts[kind] == store.SourceHost {
			row.Source = store.SourceHost
		}
	case "health":
		alert, _ := eff.Alerts.For("health")
		row.SustainSeconds, row.Notify = alert.SustainSeconds, alert.Notify
		row.Source = eff.Sources.Alerts["health"]
	case "long":
		row.Warn = eff.LongProc.CPUPct
		row.SustainSeconds, row.Notify = eff.LongProc.SustainSeconds, store.AlertNotify(eff.LongProc.Notify)
		row.Source = eff.Sources.LongProc
	}
	return row
}

// flapping lists the metrics of a host with flapMinAlerts or more alerts
// and a median length under flapMaxMedian. The host with the most alerts
// comes first.
func (s alertSet) flapping(now time.Time) []FlapRow {
	type key struct {
		node   uuid.UUID
		metric string
	}
	type group struct {
		host string
		lens []time.Duration
	}
	groups := map[key]*group{}
	for _, a := range s {
		metric, _, _ := strings.Cut(a.Metric, "/")
		k := key{a.NodeID, metric}
		if groups[k] == nil {
			groups[k] = &group{host: a.NodeName}
		}
		end := now
		if a.End != nil {
			end = *a.End
		}
		groups[k].lens = append(groups[k].lens, end.Sub(*a.Start))
	}
	out := []FlapRow{}
	for k, g := range groups {
		if len(g.lens) < flapMinAlerts {
			continue
		}
		slices.Sort(g.lens)
		mid := len(g.lens) / 2
		median := g.lens[mid]
		if len(g.lens)%2 == 0 {
			median = (g.lens[mid-1] + g.lens[mid]) / 2
		}
		if median < flapMaxMedian {
			out = append(out, FlapRow{NodeID: k.node, Host: g.host, Metric: k.metric, Alerts: len(g.lens), MedianDuration: median})
		}
	}
	slices.SortFunc(out, func(a, b FlapRow) int {
		return cmp.Or(cmp.Compare(b.Alerts, a.Alerts), strings.Compare(a.Host, b.Host), strings.Compare(a.Metric, b.Metric))
	})
	return out
}

// channelRows counts the sends of each channel name, and the sends to
// channels of the kind email.
func (s *Service) channelRows(ctx context.Context, orgID uuid.UUID, sent map[uuid.UUID]clickhouse.NotifiedRow) ([]ChannelRow, int, error) {
	kinds := map[string]store.ChannelKind{}
	if s.channels != nil {
		list, err := s.channels.ListChannels(ctx, orgID)
		if err != nil {
			return nil, 0, err
		}
		dup := map[string]bool{}
		for _, c := range list {
			if k, ok := kinds[c.Name]; ok && k != c.Kind {
				dup[c.Name] = true
			}
			kinds[c.Name] = c.Kind
		}
		for n := range dup {
			delete(kinds, n)
		}
	}
	byName := map[string]*ChannelRow{}
	for _, r := range sent {
		for name, n := range r.Channels {
			if byName[name] == nil {
				byName[name] = &ChannelRow{Name: name, Kind: kinds[name]}
			}
			byName[name].Sends += n
			byName[name].Failed += r.FailedChannels[name]
		}
	}
	out, email := make([]ChannelRow, 0, len(byName)), 0
	for _, c := range byName {
		out = append(out, *c)
		if c.Kind == store.ChannelEmail {
			email += c.Sends
		}
	}
	slices.SortFunc(out, func(a, b ChannelRow) int {
		return cmp.Or(cmp.Compare(b.Sends, a.Sends), strings.Compare(a.Name, b.Name))
	})
	return out, email, nil
}
