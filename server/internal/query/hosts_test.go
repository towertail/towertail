package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

type fakeConfig struct {
	nodes    []store.Node
	settings store.ServerSettings
}

func (f fakeConfig) GetNode(_ context.Context, _, id uuid.UUID) (store.Node, error) {
	for _, n := range f.nodes {
		if n.ID == id {
			return n, nil
		}
	}
	return store.Node{}, store.ErrNotFound
}

func (f fakeConfig) GetSettings(context.Context, uuid.UUID) (store.ServerSettings, error) {
	return f.settings, nil
}

func TestFleetSummary(t *testing.T) {
	web, db, off := uuid.New(), uuid.New(), uuid.New()
	nodes := []store.Node{
		{ID: web, DisplayName: "web", Tags: []string{"prod", "web"}},
		{ID: db, DisplayName: "db", Tags: []string{"prod"}, ThresholdOverrides: &store.ThresholdOverrides{CPU: &store.ThresholdPair{Warn: 0.9, Critical: 0.95}}},
		{ID: off, DisplayName: "old", Tags: []string{"web"}},
	}
	r := &fakeReader{
		latest: []clickhouse.NodeLatest{
			{NodeID: web, TS: now, CPUPct: 80, MemRatio: 0.95},
			{NodeID: db, TS: now, CPUPct: 80, MemRatio: 0.4},
		},
		disks: map[uuid.UUID]float64{web: 0.5, db: 0.9},
		open: []clickhouse.AlertRow{
			{AlertID: uuid.New(), NodeID: web, Kind: "threshold_crossed", Metric: "mem", Tint: "critical"},
			{AlertID: uuid.New(), NodeID: db, Kind: "threshold_crossed", Metric: "disk", Tint: "warn"},
			{AlertID: uuid.New(), NodeID: db, Kind: "threshold_crossed", Metric: "cpu", Tint: "warn"},
		},
		incidents: []clickhouse.IncidentRow{
			{AlertID: uuid.New(), NodeID: web, Kind: "threshold_crossed", Metric: "mem", Tint: "critical", Severity: "critical", Start: now.Add(-time.Hour), LastTS: now, Payload: `{}`},
			{AlertID: uuid.New(), NodeID: db, Kind: "threshold_crossed", Metric: "disk", Tint: "warn", Severity: "warn", Start: now.Add(-2 * time.Hour), LastTS: now, Payload: `{}`},
		},
		notified: []clickhouse.NotifiedRow{{AlertID: uuid.New(), Sends: 3}, {AlertID: uuid.New(), Sends: 2}},
	}
	s := newService(r, nodes, 0)
	s.config = fakeConfig{nodes: nodes, settings: store.DefaultServerSettings()}
	got, err := s.FleetSummary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if got.Hosts != 3 || got.Online != 2 || got.Offline != 1 {
		t.Errorf("hosts = %d, online = %d, offline = %d", got.Hosts, got.Online, got.Offline)
	}
	if got.OpenAlerts[wire.TintCritical] != 1 || got.OpenAlerts[wire.TintWarn] != 2 {
		t.Errorf("open alerts = %v", got.OpenAlerts)
	}
	if got.Alerts24h != 2 || got.Notifications24h != 5 {
		t.Errorf("alerts 24h = %d, notifications 24h = %d", got.Alerts24h, got.Notifications24h)
	}
	if !r.gotNote.To.Equal(now) || !r.gotNote.From.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("notified query = %+v", r.gotNote)
	}
	if got.Tags["prod"] != 2 || got.Tags["web"] != 2 {
		t.Errorf("tags = %v", got.Tags)
	}
	// web: cpu 0.8 is over the org warn 0.75, and mem 0.95 is critical.
	// db: cpu 0.8 is under its host warn 0.9, and disk 0.9 is over the org warn 0.85.
	want := []HostOver{
		{web, "web", "mem", 0.95, wire.TintCritical},
		{db, "db", "disk", 0.9, wire.TintWarn},
		{web, "web", "cpu", 80, wire.TintWarn},
	}
	if len(got.Over) != len(want) {
		t.Fatalf("over = %+v, want %+v", got.Over, want)
	}
	for i := range want {
		if got.Over[i] != want[i] {
			t.Errorf("over[%d] = %+v, want %+v", i, got.Over[i], want[i])
		}
	}
	if len(got.TopCPU) != 2 || len(got.TopMem) != 2 || got.TopMem[0].Name != "web" || got.TopDisk[0].Name != "db" {
		t.Errorf("tops = %+v %+v %+v", got.TopCPU, got.TopMem, got.TopDisk)
	}
}

func TestFleetSummaryMuted(t *testing.T) {
	web := uuid.New()
	nodes := []store.Node{{ID: web, DisplayName: "web", MutedSignals: store.MutedSignals{store.MetricCPU}}}
	r := &fakeReader{latest: []clickhouse.NodeLatest{{NodeID: web, TS: now, CPUPct: 99}}}
	got, err := newService(r, nodes, 0).FleetSummary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Over) != 0 {
		t.Fatalf("over = %+v, want none for a muted metric", got.Over)
	}
}

func TestHostDetail(t *testing.T) {
	id := uuid.New()
	node := store.Node{ID: id, DisplayName: "web", ThresholdOverrides: &store.ThresholdOverrides{Mem: &store.ThresholdPair{Warn: 0.5, Critical: 0.6}}}
	cfg := fakeConfig{nodes: []store.Node{node}, settings: store.DefaultServerSettings()}
	facts := &clickhouse.HostFacts{SeenAt: now, OS: "linux", Arch: "arm64", Cores: 4}
	r := &fakeReader{
		latest: []clickhouse.NodeLatest{{NodeID: id, TS: now, CPUPct: 12}},
		facts:  facts,
		open:   []clickhouse.AlertRow{{AlertID: uuid.New(), NodeID: id, Kind: "threshold_crossed", Metric: "mem", Tint: "warn"}, {AlertID: uuid.New(), NodeID: uuid.New()}},
	}
	s := newService(r, cfg.nodes, 0)
	s.config = cfg
	got, err := s.HostDetail(context.Background(), uuid.New(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Node.ID != id || got.Latest == nil || got.Latest.CPU == nil || *got.Latest.CPU != 12 {
		t.Errorf("node = %v, latest = %+v", got.Node.ID, got.Latest)
	}
	if got.Facts == nil || got.Facts.OS != "linux" || got.Facts.Cores != 4 {
		t.Errorf("facts = %+v", got.Facts)
	}
	if len(got.OpenAlerts) != 1 || got.OpenAlerts[0].Metric != "mem" {
		t.Errorf("open alerts = %+v", got.OpenAlerts)
	}
	if p := got.Effective.Thresholds.Pair(store.MetricMem); p.Warn != 0.5 || got.Effective.Sources.Thresholds[store.MetricMem] != store.SourceHost {
		t.Errorf("effective mem = %+v, sources = %v", p, got.Effective.Sources.Thresholds)
	}
	if _, err := s.HostDetail(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown node: err = %v, want ErrNotFound", err)
	}
}

func TestHostDetailNoHistory(t *testing.T) {
	id := uuid.New()
	nodes := []store.Node{{ID: id, DisplayName: "web"}}
	got, err := NewService(nil, fakeNodes(nodes), nil, nil).HostDetail(context.Background(), uuid.New(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Node.ID != id || got.Latest != nil || got.Facts != nil || len(got.OpenAlerts) != 0 {
		t.Fatalf("detail = %+v, want the node and the rules only", got)
	}
	if got.Effective.Thresholds.CPUWarn != 0.75 {
		t.Errorf("effective cpu warn = %v, want the default 0.75", got.Effective.Thresholds.CPUWarn)
	}
}

func TestHistoryDays(t *testing.T) {
	if got := NewService(nil, nil, fixedLimits(30), nil).HistoryDays(context.Background(), uuid.New()); got != 30 {
		t.Errorf("HistoryDays = %d, want 30", got)
	}
	if got := NewService(nil, nil, nil, nil).HistoryDays(context.Background(), uuid.New()); got != 0 {
		t.Errorf("HistoryDays with no limits = %d, want 0", got)
	}
}

func TestRules(t *testing.T) {
	id := uuid.New()
	snooze := now.Add(time.Hour)
	node := store.Node{
		ID: id, DisplayName: "web", NotifyOnWarn: true, SnoozedUntil: &snooze,
		ThresholdOverrides: &store.ThresholdOverrides{CPU: &store.ThresholdPair{Warn: 0.5, Critical: 0.6}},
	}
	settings := store.DefaultServerSettings()
	settings.NotifyCooldownSeconds = 0
	cfg := fakeConfig{nodes: []store.Node{node}, settings: settings}
	s := newService(&fakeReader{}, cfg.nodes, 0)
	s.config = cfg

	org, err := s.Rules(context.Background(), uuid.New(), uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if org.Host != nil || org.Effective != nil || org.Notify.HostWarn != nil || org.Org.Thresholds.CPUWarn != 0.75 {
		t.Errorf("org rules = %+v, want the org rules only", org)
	}
	if org.Cooldown != defaultCooldown || org.Debounce != time.Duration(settings.NotifyDebounceSeconds)*time.Second {
		t.Errorf("cooldown = %v, debounce = %v", org.Cooldown, org.Debounce)
	}
	if org.Notify.Enabled != settings.NotificationsEnabled || org.Notify.Warn != settings.NotifyWarn {
		t.Errorf("notify = %+v", org.Notify)
	}

	got, err := s.Rules(context.Background(), uuid.New(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host == nil || got.Effective == nil || got.Effective.Thresholds.CPUWarn != 0.5 || got.Effective.Sources.Thresholds[store.MetricCPU] != store.SourceHost {
		t.Fatalf("host rules = %+v", got)
	}
	if got.Org.Thresholds.CPUWarn != 0.75 {
		t.Errorf("org cpu warn = %v, want 0.75", got.Org.Thresholds.CPUWarn)
	}
	n := got.Notify
	if n.HostWarn == nil || !*n.HostWarn || n.HostCritical == nil || *n.HostCritical || n.HostSnoozedUntil == nil || !n.HostSnoozedUntil.Equal(snooze) {
		t.Errorf("host notify flags = %+v", n)
	}
	if _, err := s.Rules(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown node: err = %v, want ErrNotFound", err)
	}
}
