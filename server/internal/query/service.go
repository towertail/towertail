// Package query holds the fleet reads and the history clamp. The REST
// handlers and the MCP tools use it.
package query

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

var (
	// ErrNoHistory is returned when the server has no ClickHouse.
	ErrNoHistory = errors.New("query: history is off")
	// ErrBadSort is returned for an unknown host sort.
	ErrBadSort = errors.New("query: bad sort")
	// ErrBadWindow is returned for a compare window out of range.
	ErrBadWindow = errors.New("query: window must be 15m to 30d")
	// ErrBadOrder is returned for an unknown process order.
	ErrBadOrder = errors.New("query: by must be cpu or mem")
	// ErrBadDays is returned for a days value out of range.
	ErrBadDays = errors.New("query: days must be 1 to 90")
	// ErrHostNotFound is returned when no host matches a reference.
	ErrHostNotFound = errors.New("query: host not found")
	// ErrHostAmbiguous is returned when a name matches more than one host.
	ErrHostAmbiguous = errors.New("query: host name is not unique")
)

// Limits of the fleet reads.
const (
	// HostFresh is the age of the newest sample of an online host.
	HostFresh   = 10 * time.Minute
	MinWindow   = 15 * time.Minute
	MaxWindow   = 30 * 24 * time.Hour
	MaxProcs    = 50
	defaultProc = 20
	// MaxAlertDays is the longest span of the recent alerts.
	MaxAlertDays = 90
	// RecentAlertsLimit is the most rows of the recent alerts.
	RecentAlertsLimit = 200
	defaultAlertDays  = 7
)

// compareMetrics are the metrics that Compare accepts.
var compareMetrics = map[string]bool{
	"cpu": true, "mem": true, "net_rx": true, "net_tx": true, "disk_read": true, "disk_write": true,
}

// Reader is the part of the ClickHouse client that the service reads.
type Reader interface {
	LatestByNode(ctx context.Context, orgID uuid.UUID, since time.Time) ([]clickhouse.NodeLatest, error)
	LatestDiskRatio(ctx context.Context, orgID uuid.UUID, since time.Time) (map[uuid.UUID]float64, error)
	TopProcesses(ctx context.Context, q clickhouse.RangeQuery, by clickhouse.ProcOrder, limit int) ([]clickhouse.TopProc, error)
	WindowStats(ctx context.Context, q clickhouse.StatsQuery) ([]clickhouse.NodeStat, error)
	QueryHistory(ctx context.Context, q clickhouse.HistoryQuery) ([]clickhouse.HistoryPoint, time.Duration, error)
	DiskHistory(ctx context.Context, q clickhouse.RangeQuery) ([]clickhouse.DiskPoint, time.Duration, error)
	OpenAlerts(ctx context.Context, orgID uuid.UUID) ([]clickhouse.AlertRow, error)
	RecentAlerts(ctx context.Context, orgID uuid.UUID, since time.Time, limit int) ([]clickhouse.AlertRow, error)
	AlertMarks(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertMark, error)
	Incidents(ctx context.Context, q clickhouse.IncidentQuery) ([]clickhouse.IncidentRow, bool, error)
	AlertsByID(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]clickhouse.AlertRow, error)
	AlertEvents(ctx context.Context, orgID, alertID uuid.UUID) ([]clickhouse.EventRow, error)
	AlertLines(ctx context.Context, orgID, alertID uuid.UUID) ([]clickhouse.AlertLine, error)
}

// Peaks gives the highest value of each open alert of an org, by alert id.
// The alerter keeps it in memory.
type Peaks interface {
	Peaks(orgID uuid.UUID) map[uuid.UUID]float64
}

// Nodes lists the nodes of an org.
type Nodes interface {
	ListNodes(ctx context.Context, orgID uuid.UUID) ([]store.Node, error)
}

// Limits gives the plan history of an org.
type Limits interface {
	// HistoryDays is the number of days of history that the org can
	// read. 0 means no limit.
	HistoryDays(ctx context.Context, orgID uuid.UUID) int
}

// Service reads the fleet of an org. A nil Reader gives ErrNoHistory on
// each read. A nil Limits gives no clamp. A nil Peaks gives no live peak.
type Service struct {
	ch     Reader
	nodes  Nodes
	limits Limits
	peaks  Peaks
	now    func() time.Time
}

func NewService(ch Reader, nodes Nodes, limits Limits, peaks Peaks) *Service {
	return &Service{ch: ch, nodes: nodes, limits: limits, peaks: peaks, now: time.Now}
}

// Clamp moves from to the oldest time that the plan of the org allows.
// It reports whether it moved from.
func (s *Service) Clamp(ctx context.Context, orgID uuid.UUID, from, now time.Time) (time.Time, bool) {
	if s.limits == nil {
		return from, false
	}
	days := s.limits.HistoryDays(ctx, orgID)
	if days <= 0 {
		return from, false
	}
	oldest := now.Add(-time.Duration(days) * 24 * time.Hour)
	if from.Before(oldest) {
		return oldest, true
	}
	return from, false
}

// HostSort is the order of the host list.
type HostSort string

const (
	SortCPU  HostSort = "cpu"
	SortMem  HostSort = "mem"
	SortDisk HostSort = "disk"
	SortNet  HostSort = "net"
)

// ParseHostSort reads a sort name. The empty name gives SortCPU.
func ParseHostSort(v string) (HostSort, error) {
	switch s := HostSort(v); s {
	case "":
		return SortCPU, nil
	case SortCPU, SortMem, SortDisk, SortNet:
		return s, nil
	}
	return "", ErrBadSort
}

// ParseProcOrder reads a process order. The empty name gives ProcByCPU.
func ParseProcOrder(v string) (clickhouse.ProcOrder, error) {
	switch by := clickhouse.ProcOrder(v); by {
	case "":
		return clickhouse.ProcByCPU, nil
	case clickhouse.ProcByCPU, clickhouse.ProcByMem, clickhouse.ProcByIO:
		return by, nil
	}
	return "", ErrBadOrder
}

func ptr[T any](v T) *T { return &v }

// key is the value of the host for the sort. An offline host has none.
func (h HostSort) key(host wire.FleetHost) float64 {
	if !host.Online {
		return math.Inf(-1)
	}
	switch h {
	case SortMem:
		return deref(host.Mem)
	case SortDisk:
		return deref(host.Disk)
	case SortNet:
		return float64(derefU(host.NetRx)) + float64(derefU(host.NetTx))
	}
	return deref(host.CPU)
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefU(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

// Hosts gives each node of the org with its latest values, the highest
// first. A node with no sample in the last 10 minutes is offline and has
// no values. A limit of 0 or less gives all hosts.
func (s *Service) Hosts(ctx context.Context, orgID uuid.UUID, sort HostSort, limit int) (wire.FleetHosts, error) {
	sort, err := ParseHostSort(string(sort))
	if err != nil {
		return wire.FleetHosts{}, err
	}
	if s.ch == nil {
		return wire.FleetHosts{}, ErrNoHistory
	}
	since := s.now().Add(-HostFresh)
	nodes, err := s.nodes.ListNodes(ctx, orgID)
	if err != nil {
		return wire.FleetHosts{}, err
	}
	latest, err := s.ch.LatestByNode(ctx, orgID, since)
	if err != nil {
		return wire.FleetHosts{}, err
	}
	disks, err := s.ch.LatestDiskRatio(ctx, orgID, since)
	if err != nil {
		return wire.FleetHosts{}, err
	}
	byNode := make(map[uuid.UUID]clickhouse.NodeLatest, len(latest))
	for _, l := range latest {
		byNode[l.NodeID] = l
	}
	out := wire.FleetHosts{Hosts: make([]wire.FleetHost, 0, len(nodes))}
	for _, n := range nodes {
		h := wire.FleetHost{NodeID: n.ID, Name: n.DisplayName, Tags: n.Tags}
		if l, ok := byNode[n.ID]; ok {
			h.Online = true
			h.LastSeen = ptr(l.TS)
			h.CPU, h.Mem, h.Load1 = ptr(l.CPUPct), ptr(l.MemRatio), ptr(l.Load1)
			h.NetRx, h.NetTx = ptr(l.NetRxBps), ptr(l.NetTxBps)
			if d, ok := disks[n.ID]; ok {
				h.Disk = ptr(d)
			}
		}
		out.Hosts = append(out.Hosts, h)
	}
	slices.SortStableFunc(out.Hosts, func(a, b wire.FleetHost) int {
		if c := cmp.Compare(sort.key(b), sort.key(a)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	if limit > 0 && len(out.Hosts) > limit {
		out.Hosts = out.Hosts[:limit]
	}
	return out, nil
}

// ProcQuery asks for the top processes of one node, or of all nodes when
// NodeID is uuid.Nil. A zero To means now.
type ProcQuery struct {
	OrgID    uuid.UUID
	NodeID   uuid.UUID
	From, To time.Time
	By       clickhouse.ProcOrder
	Limit    int
}

// TopProcesses gives the top processes of the fleet or of one node. The
// plan clamps From. A limit is 1 to 50, and 0 gives 20.
func (s *Service) TopProcesses(ctx context.Context, q ProcQuery) (wire.FleetProcesses, error) {
	by, err := ParseProcOrder(string(q.By))
	if err != nil {
		return wire.FleetProcesses{}, err
	}
	if s.ch == nil {
		return wire.FleetProcesses{}, ErrNoHistory
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultProc
	}
	limit = min(limit, MaxProcs)
	now := s.now().UTC()
	to := q.To
	if to.IsZero() {
		to = now
	}
	from, clamped := s.Clamp(ctx, q.OrgID, q.From, now)
	if to.Before(from) {
		to = from
	}
	names, err := s.HostNames(ctx, q.OrgID)
	if err != nil {
		return wire.FleetProcesses{}, err
	}
	procs, err := s.ch.TopProcesses(ctx, clickhouse.RangeQuery{OrgID: q.OrgID, NodeID: q.NodeID, From: from, To: to}, by, limit)
	if err != nil {
		return wire.FleetProcesses{}, err
	}
	out := wire.FleetProcesses{From: from, To: to, Clamped: clamped, By: string(by), Processes: make([]wire.FleetProcess, 0, len(procs))}
	for _, p := range procs {
		out.Processes = append(out.Processes, wire.FleetProcess{
			NodeID: p.NodeID, NodeName: names[p.NodeID], Key: wire.ProcessKey{Name: p.Name, CmdHash: p.CmdHash}, Name: p.Name, Cmd: p.Cmd,
			Repo: p.Repo, Cwd: p.Cwd, Parent: p.Parent,
			CPUPctAvg: p.CPUPctAvg, CPUPctMax: p.CPUPctMax, RSSMax: p.RSSMax,
		})
	}
	return out, nil
}

// CompareQuery asks for a metric in the last Window against the Window
// before it. A NodeID of uuid.Nil means all nodes.
type CompareQuery struct {
	OrgID  uuid.UUID
	NodeID uuid.UUID
	Metric string
	Window time.Duration
}

// Validate checks the window and the metric.
func (q CompareQuery) Validate() error {
	if q.Window < MinWindow || q.Window > MaxWindow {
		return ErrBadWindow
	}
	if !compareMetrics[q.Metric] {
		return clickhouse.ErrUnknownMetric
	}
	return nil
}

// Compare gives the metric of each host in the window that ends now and
// in the window before it. The plan clamps both windows. A previous
// window fully outside the plan is empty. The hosts with the biggest
// change are first.
func (s *Service) Compare(ctx context.Context, q CompareQuery) (wire.Compare, error) {
	if err := q.Validate(); err != nil {
		return wire.Compare{}, err
	}
	if s.ch == nil {
		return wire.Compare{}, ErrNoHistory
	}
	now := s.now().UTC()
	curFrom, c1 := s.Clamp(ctx, q.OrgID, now.Add(-q.Window), now)
	prevTo := now.Add(-q.Window)
	prevFrom, c2 := s.Clamp(ctx, q.OrgID, now.Add(-2*q.Window), now)
	prevEmpty := !prevFrom.Before(prevTo)
	if prevEmpty {
		prevTo = prevFrom
	}
	out := wire.Compare{
		Metric:   q.Metric,
		Current:  wire.Window{From: curFrom, To: now},
		Previous: wire.Window{From: prevFrom, To: prevTo},
		Clamped:  c1 || c2,
		Hosts:    []wire.CompareHost{},
	}
	stats := func(from, to time.Time) (map[uuid.UUID]clickhouse.NodeStat, error) {
		rows, err := s.ch.WindowStats(ctx, clickhouse.StatsQuery{OrgID: q.OrgID, NodeID: q.NodeID, Metric: q.Metric, From: from, To: to})
		m := make(map[uuid.UUID]clickhouse.NodeStat, len(rows))
		for _, r := range rows {
			m[r.NodeID] = r
		}
		return m, err
	}
	cur, err := stats(curFrom, now)
	if err != nil {
		return wire.Compare{}, err
	}
	prev := map[uuid.UUID]clickhouse.NodeStat{}
	if !prevEmpty {
		if prev, err = stats(prevFrom, prevTo); err != nil {
			return wire.Compare{}, err
		}
	}
	names, err := s.HostNames(ctx, q.OrgID)
	if err != nil {
		return wire.Compare{}, err
	}
	ids := map[uuid.UUID]bool{}
	for id := range cur {
		ids[id] = true
	}
	for id := range prev {
		ids[id] = true
	}
	for id := range ids {
		h := wire.CompareHost{NodeID: id, Name: names[id]}
		c, hasCur := cur[id]
		if hasCur {
			h.Current = wire.Stat{Avg: c.Avg, Max: c.Max, Points: c.Points}
		}
		if p, ok := prev[id]; ok {
			h.Previous = &wire.Stat{Avg: p.Avg, Max: p.Max, Points: p.Points}
			if hasCur {
				h.DeltaAvg = ptr(c.Avg - p.Avg)
			}
		}
		out.Hosts = append(out.Hosts, h)
	}
	slices.SortFunc(out.Hosts, func(a, b wire.CompareHost) int {
		if c := cmp.Compare(absDelta(b), absDelta(a)); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.NodeID.String(), b.NodeID.String())
	})
	return out, nil
}

// absDelta is the size of the change. A host with no change value is last.
func absDelta(h wire.CompareHost) float64 {
	if h.DeltaAvg == nil {
		return -1
	}
	return math.Abs(*h.DeltaAvg)
}

// HostNames maps the node IDs of the org to the display names.
func (s *Service) HostNames(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]string, error) {
	nodes, err := s.nodes.ListNodes(ctx, orgID)
	if err != nil {
		return nil, err
	}
	m := make(map[uuid.UUID]string, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n.DisplayName
	}
	return m, nil
}

// ResolveHost finds a node of the org by ID, then by display name with
// no case. A name that matches more than one node gives
// ErrHostAmbiguous with the IDs.
func (s *Service) ResolveHost(ctx context.Context, orgID uuid.UUID, ref string) (store.Node, error) {
	ref = strings.TrimSpace(ref)
	nodes, err := s.nodes.ListNodes(ctx, orgID)
	if err != nil {
		return store.Node{}, err
	}
	if id, err := uuid.Parse(ref); err == nil {
		for _, n := range nodes {
			if n.ID == id {
				return n, nil
			}
		}
	}
	var found []store.Node
	for _, n := range nodes {
		if ref != "" && strings.EqualFold(n.DisplayName, ref) {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 0:
		return store.Node{}, fmt.Errorf("%w: %q", ErrHostNotFound, ref)
	case 1:
		return found[0], nil
	}
	ids := make([]string, len(found))
	for i, n := range found {
		ids[i] = n.ID.String()
	}
	return store.Node{}, fmt.Errorf("%w: %q matches %s", ErrHostAmbiguous, ref, strings.Join(ids, ", "))
}
