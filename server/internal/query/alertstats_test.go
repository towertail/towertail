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

type fakeChannels []store.Channel

func (f fakeChannels) ListChannels(context.Context, uuid.UUID) ([]store.Channel, error) {
	return f, nil
}

func statsRequest(mod func(*AlertStatsRequest)) AlertStatsRequest {
	r := AlertStatsRequest{From: now.Add(-7 * 24 * time.Hour), To: now}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestAlertStats(t *testing.T) {
	cpuA, memA, cpuB, longB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nodes := []store.Node{
		{ID: hostA, DisplayName: "web-1", ThresholdOverrides: &store.ThresholdOverrides{CPU: &store.ThresholdPair{Warn: 0.9, Critical: 0.95}}},
		{ID: hostB, DisplayName: "db-2"},
	}
	r := &fakeReader{
		incidents: []clickhouse.IncidentRow{
			incident(cpuA, hostA, "threshold_crossed", "cpu", "warn", 3, nil),
			incident(memA, hostA, "threshold_crossed", "mem", "critical", 5, nil),
			incident(cpuB, hostB, "threshold_crossed", "cpu", "warn", 10, nil),
			incident(longB, hostB, "long_process", "long_proc/9/1", "warn", 6, nil),
		},
		notified: []clickhouse.NotifiedRow{
			{AlertID: cpuA, Sends: 2, Failed: 1, Channels: map[string]int{"Email ops": 1, "Slack": 1}, FailedChannels: map[string]int{"Slack": 1}},
			{AlertID: memA, Sends: 1, Channels: map[string]int{"Email ops": 1}},
			{AlertID: uuid.New(), Sends: 9, Channels: map[string]int{"Slack": 9}},
		},
	}
	s := newService(r, nodes, 0)
	s.channels = fakeChannels{
		{Kind: store.ChannelEmail, Name: "Email ops"},
		{Kind: store.ChannelSlack, Name: "Slack"},
	}
	got, err := s.AlertStats(context.Background(), statsRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Alerts != 4 || got.Sends != 3 || got.EmailSends != 2 || got.Clamped || got.Truncated {
		t.Fatalf("totals = %+v", got)
	}
	if len(got.ByHost) != 2 || got.ByHost[0].Name != "db-2" && got.ByHost[0].Name != "web-1" {
		t.Fatalf("by host = %+v", got.ByHost)
	}
	for _, h := range got.ByHost {
		switch h.Name {
		case "web-1":
			if h.Alerts != 2 || h.Critical != 1 || h.Warn != 1 || h.Sends != 3 {
				t.Errorf("web-1 = %+v", h)
			}
		case "db-2":
			if h.Alerts != 2 || h.Warn != 2 || h.Sends != 0 {
				t.Errorf("db-2 = %+v", h)
			}
		}
	}
	kinds := map[string]CountRow{}
	for _, k := range got.ByKind {
		kinds[k.Key] = k
	}
	if kinds["cpu"].Alerts != 2 || kinds["mem"].Critical != 1 || kinds["long"].Alerts != 1 || kinds["cpu"].Sends != 2 {
		t.Fatalf("by kind = %+v", got.ByKind)
	}
	rules := map[string]RuleRow{}
	for _, r := range got.ByRule {
		name := r.Kind + "/" + string(r.Source)
		if r.NodeID != nil {
			name += "/" + r.Host
		}
		rules[name] = r
	}
	if len(rules) != 4 {
		t.Fatalf("by rule = %+v", got.ByRule)
	}
	if r := rules["cpu/host/web-1"]; r.Warn != 0.9 || r.Critical != 0.95 || r.Alerts != 1 || r.Sends != 2 || *r.NodeID != hostA {
		t.Errorf("host rule = %+v", r)
	}
	if r := rules["cpu/org"]; r.Warn != store.DefaultServerSettings().Rules().Thresholds.Pair(store.MetricCPU).Warn || r.Alerts != 1 || r.NodeID != nil {
		t.Errorf("org rule = %+v", r)
	}
	if r := rules["mem/org"]; r.Sends != 1 || r.Notify == "" {
		t.Errorf("mem rule = %+v", r)
	}
	if len(got.ByChannel) != 2 || got.ByChannel[0] != (ChannelRow{Name: "Email ops", Kind: store.ChannelEmail, Sends: 2}) ||
		got.ByChannel[1] != (ChannelRow{Name: "Slack", Kind: store.ChannelSlack, Sends: 1, Failed: 1}) {
		t.Fatalf("by channel = %+v", got.ByChannel)
	}
	if len(got.Bins) != statsBins {
		t.Fatalf("bins = %d", len(got.Bins))
	}
	if !r.gotNote.From.Equal(got.From) || !r.gotNote.To.Equal(now) {
		t.Fatalf("notified query = %+v", r.gotNote)
	}
}

func TestAlertStatsClampAndFilters(t *testing.T) {
	r := &fakeReader{incidents: []clickhouse.IncidentRow{
		incident(uuid.New(), hostA, "threshold_crossed", "cpu", "warn", 3, nil),
		incident(uuid.New(), hostA, "threshold_crossed", "mem", "warn", 4, nil),
	}}
	s := newService(r, []store.Node{{ID: hostA, DisplayName: "web-1"}}, 2)
	got, err := s.AlertStats(context.Background(), statsRequest(func(r *AlertStatsRequest) {
		r.Kinds, r.Hosts = []string{"cpu"}, []uuid.UUID{hostA}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Clamped || !got.From.Equal(now.Add(-48*time.Hour)) || got.Alerts != 1 || len(r.gotIncs.NodeIDs) != 1 {
		t.Fatalf("plan clamp: %+v, query %+v", got, r.gotIncs)
	}

	got, err = newService(r, nil, 0).AlertStats(context.Background(), statsRequest(func(r *AlertStatsRequest) {
		r.From = now.Add(-120 * 24 * time.Hour)
	}))
	if err != nil || !got.Clamped || !got.From.Equal(now.Add(-90*24*time.Hour)) {
		t.Fatalf("90 d clamp: %+v, %v", got, err)
	}
}

func TestAlertStatsErrors(t *testing.T) {
	s := newService(&fakeReader{}, nil, 0)
	for _, mod := range []func(*AlertStatsRequest){
		func(r *AlertStatsRequest) { r.Top = 21 },
		func(r *AlertStatsRequest) { r.Top = -1 },
		func(r *AlertStatsRequest) { r.Kinds = []string{"gpu"} },
		func(r *AlertStatsRequest) { r.From = r.To },
	} {
		if _, err := s.AlertStats(context.Background(), statsRequest(mod)); !errors.Is(err, ErrBadAlertQuery) {
			t.Errorf("err = %v, want ErrBadAlertQuery", err)
		}
	}
	if _, err := NewService(nil, nil, nil, nil).AlertStats(context.Background(), statsRequest(nil)); !errors.Is(err, ErrNoHistory) {
		t.Errorf("no ClickHouse: %v", err)
	}
}

func TestAlertStatsTop(t *testing.T) {
	var rows []clickhouse.IncidentRow
	var nodes []store.Node
	for i := range 5 {
		id := uuid.New()
		nodes = append(nodes, store.Node{ID: id, DisplayName: string(rune('a' + i))})
		rows = append(rows, incident(uuid.New(), id, "threshold_crossed", "cpu", "warn", 3, nil))
	}
	got, err := newService(&fakeReader{incidents: rows}, nodes, 0).AlertStats(context.Background(), statsRequest(func(r *AlertStatsRequest) { r.Top = 2 }))
	if err != nil || len(got.ByHost) != 2 || got.Alerts != 5 {
		t.Fatalf("got %d hosts, %d alerts, %v", len(got.ByHost), got.Alerts, err)
	}
}

func TestAlertStatsFlapping(t *testing.T) {
	series := func(host uuid.UUID, n int, length time.Duration) alertSet {
		var set alertSet
		for i := range n {
			a := mkAlert(host, "web-1", "threshold_crossed", "disk/root", wire.TintWarn, float64(i+1))
			end := a.Start.Add(length)
			a.End, a.State = &end, alertResolved
			set = append(set, a)
		}
		return set
	}
	other := uuid.New()
	for _, tc := range []struct {
		name   string
		set    alertSet
		flaps  int
		median time.Duration
	}{
		{"short and many", series(hostA, 4, 5*time.Minute), 1, 5 * time.Minute},
		{"too few", series(hostA, 2, 5*time.Minute), 0, 0},
		{"long", series(hostA, 4, 30*time.Minute), 0, 0},
		{"two hosts", append(series(hostA, 3, time.Minute), series(other, 3, time.Minute)...), 2, time.Minute},
	} {
		got := tc.set.flapping(now)
		if len(got) != tc.flaps {
			t.Fatalf("%s: flapping = %+v", tc.name, got)
		}
		if tc.flaps > 0 && (got[0].MedianDuration != tc.median || got[0].Metric != "disk") {
			t.Errorf("%s: row = %+v", tc.name, got[0])
		}
	}
}
