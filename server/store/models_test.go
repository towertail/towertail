package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/towertail/sampler/pkg/schema"
	"github.com/towertail/towertail/server/wire"
)

func TestServerSettingsWithoutLongProcGetsDefaults(t *testing.T) {
	var s ServerSettings
	if err := json.Unmarshal([]byte(`{"notifyWarn":true}`), &s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.LongProc, DefaultLongProcRule()) {
		t.Fatalf("longProc = %+v", s.LongProc)
	}
}

func TestLongProcRuleDecodesEachField(t *testing.T) {
	var r LongProcRule
	if err := json.Unmarshal([]byte(`{"cpuPct":5000,"sustainSeconds":"bad","notify":"off","ignore":["postgres"]}`), &r); err != nil {
		t.Fatal(err)
	}
	want := LongProcRule{Enabled: true, CPUPct: 800, SustainSeconds: 1800, Notify: "off", Ignore: []string{"postgres"}}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("rule = %+v, want %+v", r, want)
	}
	if err := json.Unmarshal([]byte(`{"enabled":false,"cpuPct":1,"sustainSeconds":5,"notify":"critical","ignore":null}`), &r); err != nil {
		t.Fatal(err)
	}
	want = LongProcRule{Enabled: false, CPUPct: 10, SustainSeconds: 60, Notify: "all", Ignore: []string{}}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("rule = %+v, want %+v", r, want)
	}
}

// A decode into stored settings keeps the stored rule when the JSON has
// no longProc key.
func TestServerSettingsKeepsRuleOfTarget(t *testing.T) {
	s := DefaultServerSettings()
	s.LongProc.Ignore = []string{"postgres"}
	if err := json.Unmarshal([]byte(`{"notifyWarn":false}`), &s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.LongProc.Ignore, []string{"postgres"}) {
		t.Fatalf("longProc = %+v", s.LongProc)
	}
}

func TestNodeLongProcFields(t *testing.T) {
	var n Node
	if err := json.Unmarshal([]byte(`{"customLongProc":{"cpuPct":90},"ignoredProcs":["node"]}`), &n); err != nil {
		t.Fatal(err)
	}
	if n.CustomLongProc == nil || n.CustomLongProc.CPUPct != 90 || n.CustomLongProc.SustainSeconds != 1800 || n.IgnoredProcs[0] != "node" {
		t.Fatalf("node = %+v", n)
	}
	b, _ := json.Marshal(Node{})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["customLongProc"]; ok {
		t.Errorf("empty node has customLongProc: %s", b)
	}
}

func TestLongProcIgnoreSet(t *testing.T) {
	r := LongProcRule{Ignore: []string{"Postgres"}}
	got := r.IgnoreSet([]string{"NODE"}, nil)
	if !reflect.DeepEqual(got, map[string]bool{"postgres": true, "node": true}) {
		t.Fatalf("set = %v", got)
	}
}

func TestRoleAllows(t *testing.T) {
	order := []Role{RoleViewer, RoleMember, RoleAdmin, RoleOwner}
	for i, have := range order {
		for j, need := range order {
			if got, want := have.Allows(need), i >= j; got != want {
				t.Errorf("%s.Allows(%s) = %v, want %v", have, need, got, want)
			}
		}
	}
	for _, need := range order {
		if Role("").Allows(need) {
			t.Errorf("an empty role allows %s", need)
		}
	}
}

func TestParseRole(t *testing.T) {
	for _, name := range []string{"owner", "admin", "member", "viewer"} {
		r, err := ParseRole(name)
		if err != nil || string(r) != name {
			t.Errorf("ParseRole(%q) = %q, %v", name, r, err)
		}
	}
	if _, err := ParseRole("root"); err == nil {
		t.Error("ParseRole(root) has no error")
	}
}

func TestMutedSignalsSkipsUnknownAndSorts(t *testing.T) {
	var m MutedSignals
	if err := json.Unmarshal([]byte(`["mem","nope","cpu","cpu"]`), &m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, MutedSignals{MetricCPU, MetricMem}) {
		t.Fatalf("muted = %v", m)
	}
	m = m.Set(MetricDisk, true).Set(MetricCPU, false).Set(MetricDisk, true)
	if !reflect.DeepEqual(m, MutedSignals{MetricDisk, MetricMem}) {
		t.Fatalf("after set: %v", m)
	}
	if !m.Has(MetricDisk) || m.Has(MetricCPU) {
		t.Fatalf("has: %v", m)
	}
	var none MutedSignals
	if b, _ := json.Marshal(none); string(b) != "[]" {
		t.Errorf("nil marshals as %s, want []", b)
	}
	b, _ := json.Marshal(Node{})
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if string(raw["mutedSignals"]) != "[]" {
		t.Errorf("node mutedSignals = %s, want []", raw["mutedSignals"])
	}
}

func TestServerSettingsMutedSet(t *testing.T) {
	s := ServerSettings{Muted: MutedSignals{MetricMem, MetricCPU}}
	n := Node{MutedSignals: MutedSignals{MetricCPU, MetricDisk, "Bogus"}}
	if got := s.MutedSet(&n); !reflect.DeepEqual(got, MutedSignals{MetricCPU, MetricDisk, MetricMem}) {
		t.Errorf("union = %v", got)
	}
	if got := s.MutedSet(nil); !reflect.DeepEqual(got, MutedSignals{MetricCPU, MetricMem}) {
		t.Errorf("nil node = %v", got)
	}
	var old ServerSettings
	if err := json.Unmarshal([]byte(`{"muted":["CPU","disk"]}`), &old); err != nil {
		t.Fatal(err)
	}
	if !old.Muted.Has(MetricDisk) || old.Muted.Has(MetricCPU) {
		t.Errorf("decoded = %v", old.Muted)
	}
}

func TestNodeReadsLegacyCustomThresholds(t *testing.T) {
	var n Node
	body := `{"customThresholds":{"cpuWarn":0.5,"cpuCritical":0.6,"memWarn":0.55,"memCritical":0.65,"diskWarn":0.7,"diskCritical":0.8}}`
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatal(err)
	}
	o := n.ThresholdOverrides
	if o == nil || o.CPU.Warn != 0.5 || o.Mem.Critical != 0.65 || o.Disk.Warn != 0.7 || o.PIDs != nil {
		t.Fatalf("overrides = %+v", o)
	}
	b, _ := json.Marshal(n)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if _, ok := raw["customThresholds"]; ok {
		t.Errorf("node writes customThresholds: %s", b)
	}
	if _, ok := raw["thresholdOverrides"]; !ok {
		t.Errorf("node has no thresholdOverrides: %s", b)
	}
}

func TestNodeNewKeyWinsOverLegacy(t *testing.T) {
	var n Node
	body := `{"customThresholds":{"cpuWarn":0.5,"cpuCritical":0.6,"memWarn":0.5,"memCritical":0.6,"diskWarn":0.5,"diskCritical":0.6},
		"thresholdOverrides":{"pids":{"warn":0.5,"critical":0.6}}}`
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatal(err)
	}
	if o := n.ThresholdOverrides; o.CPU != nil || o.PIDs == nil {
		t.Fatalf("overrides = %+v", o)
	}
}

func TestServerSettingsWithoutAlertsGetsDefaults(t *testing.T) {
	var s ServerSettings
	if err := json.Unmarshal([]byte(`{"thresholds":{"cpuWarn":0.5,"cpuCritical":0.6,"memWarn":0.5,"memCritical":0.6,"diskWarn":0.5,"diskCritical":0.6}}`), &s); err != nil {
		t.Fatal(err)
	}
	th := s.Thresholds
	if s.Alerts != DefaultAlertRules() || th.Health != DefaultHealthLimits() ||
		th.ProcsWarn != 5000 || th.ProcsCritical != 20000 || th.ZombiesWarn != 200 || th.ZombiesCritical != 2000 {
		t.Fatalf("settings = %+v", s)
	}
}

func TestThresholdsDecodeKeepsStoredValues(t *testing.T) {
	s := DefaultServerSettings()
	s.Thresholds.Health.PIDs = ThresholdPair{0.5, 0.6}
	s.Alerts.Mem.SustainSeconds = 7
	if err := json.Unmarshal([]byte(`{"thresholds":{"cpuWarn":0.4,"cpuCritical":0.5},"alerts":{"cpu":{"notify":"all"}}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Thresholds.CPUWarn != 0.4 || s.Thresholds.MemWarn != 0.75 || s.Thresholds.Health.PIDs.Warn != 0.5 {
		t.Errorf("thresholds = %+v", s.Thresholds)
	}
	if s.Alerts.Mem.SustainSeconds != 7 || s.Alerts.CPU.Notify != NotifyAll || s.Alerts.CPU.SustainSeconds != 300 {
		t.Errorf("alerts = %+v", s.Alerts)
	}
}

func TestThresholdOverridesApplied(t *testing.T) {
	g := DefaultServerSettings().Thresholds
	got := ThresholdOverrides{Mem: &ThresholdPair{0.85, 0.95}}.Applied(g)
	want := g
	want.MemWarn, want.MemCritical = 0.85, 0.95
	if got != want {
		t.Errorf("mem override: %+v", got)
	}
	p := ThresholdPair{98, 99}
	got = ThresholdOverrides{MemPressure: &p}.Applied(g)
	if got.Health.MemPressure != p || got.Health.MacMemPressure != p || got.CPUWarn != g.CPUWarn {
		t.Errorf("memPressure override: %+v", got.Health)
	}
	z := ThresholdOverrides{Procs: &ThresholdPair{100, 200}}.Applied(g)
	if z.ProcsWarn != 100 || z.ProcsCritical != 200 {
		t.Errorf("procs override: %+v", z)
	}
}

func TestAlertOverridesApplied(t *testing.T) {
	g := DefaultAlertRules()
	tol := 0.9
	got := AlertOverrides{CPU: &AlertRule{600, NotifyAll}, Tolerance: &tol}.Applied(g)
	want := g
	want.CPU = AlertRule{600, NotifyAll}
	want.Tolerance = 0.9
	if got != want {
		t.Errorf("alerts = %+v", got)
	}
}

func TestAlertNotifyAllows(t *testing.T) {
	cases := []struct {
		n          AlertNotify
		warn, crit bool
	}{{NotifyOff, false, false}, {NotifyCritical, false, true}, {NotifyAll, true, true}, {"", false, false}}
	for _, c := range cases {
		if c.n.Allows(wire.TintWarn) != c.warn || c.n.Allows(wire.TintCritical) != c.crit || c.n.Allows(wire.TintOK) {
			t.Errorf("%q: warn %v critical %v ok %v", c.n, c.n.Allows(wire.TintWarn), c.n.Allows(wire.TintCritical), c.n.Allows(wire.TintOK))
		}
	}
}

func TestRulesValidate(t *testing.T) {
	d := DefaultServerSettings().Rules()
	if err := d.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	cases := map[string]func(r *OrgRules){
		"warn over critical": func(r *OrgRules) { r.Thresholds.CPUWarn = 0.95 },
		"fraction over 1":    func(r *OrgRules) { r.Thresholds.DiskCritical = 1.5 },
		"percent over 100":   func(r *OrgRules) { r.Thresholds.Health.IOPressure.Critical = 120 },
		"sustain 3601":       func(r *OrgRules) { r.Alerts.CPU.SustainSeconds = 3601 },
		"tolerance 0.4":      func(r *OrgRules) { r.Alerts.Tolerance = 0.4 },
		"unknown notify":     func(r *OrgRules) { r.Alerts.Mem.Notify = "loud" },
		"zero warn":          func(r *OrgRules) { r.Thresholds.Health.PIDs.Warn = 0 },
	}
	for name, mutate := range cases {
		r := d
		mutate(&r)
		if err := r.Validate(); !errors.Is(err, ErrInvalidRules) {
			t.Errorf("%s: %v, want ErrInvalidRules", name, err)
		}
	}
	h := HostRules{Thresholds: &ThresholdOverrides{PIDs: &ThresholdPair{0.9, 0.8}}}
	if err := h.Validate(); !errors.Is(err, ErrInvalidRules) {
		t.Errorf("host pair: %v", err)
	}
	h = HostRules{Alerts: &AlertOverrides{CPU: &AlertRule{10, "x"}}}
	if err := h.Validate(); !errors.Is(err, ErrInvalidRules) {
		t.Errorf("host alert: %v", err)
	}
}

func TestOrgRulesForSources(t *testing.T) {
	org := DefaultServerSettings().Rules()
	org.Muted = MutedSignals{MetricZombies}
	host := HostRules{
		Thresholds: &ThresholdOverrides{Mem: &ThresholdPair{0.85, 0.95}},
		Muted:      MutedSignals{MetricDisk},
		Alerts:     &AlertOverrides{CPU: &AlertRule{600, NotifyAll}},
	}
	e := org.For(host)
	if e.Thresholds.MemWarn != 0.85 || e.Sources.Thresholds[MetricMem] != SourceHost || e.Sources.Thresholds[MetricCPU] != SourceOrg {
		t.Errorf("thresholds: %+v", e.Sources.Thresholds)
	}
	if e.Sources.Muted[MetricDisk] != SourceHost || e.Sources.Muted[MetricZombies] != SourceOrg || len(e.Sources.Muted) != 2 {
		t.Errorf("muted: %+v", e.Sources.Muted)
	}
	if !e.Muted.Has(MetricDisk) || !e.Muted.Has(MetricZombies) {
		t.Errorf("muted set: %v", e.Muted)
	}
	if e.Alerts.CPU.SustainSeconds != 600 || e.Sources.Alerts["cpu"] != SourceHost || e.Sources.Alerts["mem"] != SourceOrg {
		t.Errorf("alerts: %+v %+v", e.Alerts, e.Sources.Alerts)
	}
	if e.Sources.LongProc != SourceOrg {
		t.Errorf("longProc source: %s", e.Sources.LongProc)
	}
}

func ip(v int) *int      { return &v }
func i64(v int64) *int64 { return &v }

func TestHealthChecks(t *testing.T) {
	th := DefaultServerSettings().Thresholds
	psi := func(memSome, memFull, ioFull float64) *schema.PSIInfo {
		return &schema.PSIInfo{MemSome: memSome, MemFull: memFull, IOFull: ioFull}
	}
	cases := []struct {
		name  string
		h     wire.HealthBrief
		muted MutedSignals
		crit  bool
		want  []HealthCheck
	}{
		{"quiet", wire.HealthBrief{Procs: 300, Zombies: ip(0)}, nil, false, nil},
		{"procs and zombies warn", wire.HealthBrief{Procs: 5000, Zombies: ip(200)}, nil, false, []HealthCheck{
			{MetricProcs, wire.TintWarn, "5,000 processes"}, {MetricZombies, wire.TintWarn, "200 zombies"}}},
		{"zombies critical first", wire.HealthBrief{Procs: 6000, Zombies: ip(33817)}, nil, false, []HealthCheck{
			{MetricZombies, wire.TintCritical, "33,817 zombies"}, {MetricProcs, wire.TintWarn, "6,000 processes"}}},
		{"pids critical", wire.HealthBrief{PidsUsed: i64(95), PidsMax: i64(100)}, nil, false, []HealthCheck{
			{MetricPIDs, wire.TintCritical, "PIDs 95% of limit"}}},
		{"files warn", wire.HealthBrief{FilesUsed: i64(81), FilesMax: i64(100)}, nil, false, []HealthCheck{
			{MetricFiles, wire.TintWarn, "Open files 81% of limit"}}},
		{"mac pressure warn", wire.HealthBrief{MemPressurePct: ip(85)}, nil, false, []HealthCheck{
			{MetricMemPressure, wire.TintWarn, "Memory pressure 85%"}}},
		{"mac pressure wins over kernel level", wire.HealthBrief{MemPressurePct: ip(58), MemPressure: ip(2)}, nil, false, nil},
		{"psi mem full critical", wire.HealthBrief{PSI: psi(50, 25, 0)}, nil, false, []HealthCheck{
			{MetricMemPressure, wire.TintCritical, "Memory pressure 25%"}}},
		{"psi mem some warn", wire.HealthBrief{PSI: psi(12, 1, 0)}, nil, false, []HealthCheck{
			{MetricMemPressure, wire.TintWarn, "Memory pressure 12%"}}},
		{"kernel level 4", wire.HealthBrief{MemPressure: ip(4)}, nil, false, []HealthCheck{
			{MetricMemPressure, wire.TintCritical, "Memory pressure critical"}}},
		{"kernel level 2", wire.HealthBrief{MemPressure: ip(2)}, nil, false, []HealthCheck{
			{MetricMemPressure, wire.TintWarn, "Memory pressure high"}}},
		{"kernel level 2 critical only", wire.HealthBrief{MemPressure: ip(2)}, nil, true, nil},
		{"kernel level 4 critical only", wire.HealthBrief{MemPressure: ip(4)}, nil, true, []HealthCheck{
			{MetricMemPressure, wire.TintCritical, "Memory pressure critical"}}},
		{"psi io warn", wire.HealthBrief{PSI: psi(0, 0, 25)}, nil, false, []HealthCheck{
			{MetricIOPressure, wire.TintWarn, "I/O pressure 25%"}}},
		{"inodes critical", wire.HealthBrief{Inodes: 0.96}, nil, false, []HealthCheck{
			{MetricInodes, wire.TintCritical, "Inodes 96%"}}},
		{"muted check", wire.HealthBrief{Procs: 6000, Zombies: ip(5000)}, MutedSignals{MetricZombies}, false, []HealthCheck{
			{MetricProcs, wire.TintWarn, "6,000 processes"}}},
	}
	for _, c := range cases {
		got := th.HealthChecks(c.h, c.muted, c.crit)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	over := ThresholdOverrides{MemPressure: &ThresholdPair{98, 99}}.Applied(th)
	if got := over.HealthChecks(wire.HealthBrief{MemPressurePct: ip(97), MemPressure: ip(4)}, nil, true); got != nil {
		t.Errorf("mac override: %+v", got)
	}
}
