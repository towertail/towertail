package store

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOrgRulesCooldownRoundTrip(t *testing.T) {
	s := DefaultServerSettings()
	s.NotifyCooldownSeconds = 1800
	back := ServerSettings{}
	back.SetRules(s.Rules())
	if back.NotifyCooldownSeconds != 1800 {
		t.Errorf("cooldown = %d, want 1800", back.NotifyCooldownSeconds)
	}
	var r OrgRules
	if err := json.Unmarshal([]byte(`{"thresholds":{"cpuWarn":0.6,"cpuCritical":0.9}}`), &r); err != nil || r.NotifyCooldownSeconds != 600 {
		t.Errorf("missing key: %d, %v, want 600", r.NotifyCooldownSeconds, err)
	}

	st, err := OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	org := uuid.New()
	changed := DefaultServerSettings().Rules()
	changed.NotifyCooldownSeconds = 1800
	bk, err := st.ReplaceRules(ctx, org, RulesChange{Org: &changed, Origin: RulesOrigin{Client: ClientWeb, At: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if bk.Org == nil || bk.Org.NotifyCooldownSeconds != 600 {
		t.Fatalf("backup cooldown: %+v", bk.Org)
	}
	if cfg, _ := st.GetSettings(ctx, org); cfg.NotifyCooldownSeconds != 1800 {
		t.Fatalf("settings cooldown = %d", cfg.NotifyCooldownSeconds)
	}
	if _, err := st.ReplaceRules(ctx, org, RulesChange{Org: bk.Org, Origin: RulesOrigin{Client: ClientWeb, At: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := st.GetSettings(ctx, org); cfg.NotifyCooldownSeconds != 600 {
		t.Errorf("restored cooldown = %d, want 600", cfg.NotifyCooldownSeconds)
	}
	bad := changed
	bad.NotifyCooldownSeconds = 90000
	if _, err := st.ReplaceRules(ctx, org, RulesChange{Org: &bad}); !errors.Is(err, ErrInvalidRules) {
		t.Errorf("cooldown 90000: %v, want ErrInvalidRules", err)
	}
}

func TestRulesDiff(t *testing.T) {
	from := DefaultServerSettings().Rules()
	to := from
	to.Thresholds.CPUWarn = 0.85
	to.Origin = &RulesOrigin{Client: ClientAI, At: time.Now()}
	got := from.Diff(to)
	if len(got) != 1 || got[0].Path != "thresholds.cpuWarn" || string(got[0].Before) != "0.75" || string(got[0].After) != "0.85" {
		t.Fatalf("org diff = %+v", got)
	}
	if from.Equal(to) {
		t.Error("Equal: rules with a different cpuWarn are equal")
	}
	only := from
	only.Origin = &RulesOrigin{Client: ClientWeb}
	if !from.Equal(only) || len(from.Diff(only)) != 0 {
		t.Error("the origin must not count")
	}
	to = from
	to.Alerts.CPU.SustainSeconds = 60
	to.NotifyCooldownSeconds = 900
	to.LongProc.Ignore = []string{"node"}
	paths := []string{}
	for _, c := range from.Diff(to) {
		paths = append(paths, c.Path)
	}
	want := []string{"alerts.cpu.sustainSeconds", "longProc.ignore", "notifyCooldownSeconds"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	// A null list and an empty list are the same.
	nilList, emptyList := from, from
	nilList.LongProc.Ignore, emptyList.LongProc.Ignore = nil, []string{}
	if !nilList.Equal(emptyList) {
		t.Error("nil and empty ignore lists differ")
	}

	h := HostRules{}
	set := HostRules{Thresholds: &ThresholdOverrides{CPU: &ThresholdPair{Warn: 0.5, Critical: 0.6}}, Muted: MutedSignals{MetricDisk}}
	got = h.Diff(set)
	if len(got) != 3 || got[0].Path != "muted" || got[1].Path != "thresholds.cpu.critical" || got[2].Path != "thresholds.cpu.warn" {
		t.Fatalf("host diff = %+v", got)
	}
	if got[1].Before != nil || string(got[1].After) != "0.6" {
		t.Errorf("a new field has a before: %+v", got[1])
	}
	back := set.Diff(h)
	if len(back) != 3 || back[2].After != nil || string(back[2].Before) != "0.5" {
		t.Errorf("a removed field has an after: %+v", back)
	}
	if !(HostRules{Thresholds: &ThresholdOverrides{}}).Equal(HostRules{}) {
		t.Error("empty overrides differ from none")
	}
}

func TestRulesBackupChangedSince(t *testing.T) {
	at := time.Now().UTC()
	a, b, gone := uuid.New(), uuid.New(), uuid.New()
	origin := &RulesOrigin{Client: ClientAI, At: at}
	bk := RulesBackup{
		ID: uuid.New(), At: at, Org: &OrgRules{},
		Hosts: map[uuid.UUID]HostRules{a: {}, b: {}},
	}
	cfg := ServerSettings{RulesOrigin: origin}
	nodes := []Node{
		{ID: a, DisplayName: "web-1", RulesOrigin: origin},
		{ID: b, DisplayName: "web-2", RulesOrigin: origin},
	}
	if got := bk.ChangedSince(cfg, nodes); len(got) != 0 {
		t.Errorf("equal origin: %v", got)
	}
	nodes[1].RulesOrigin = &RulesOrigin{At: at.Add(time.Second)}
	if got := bk.ChangedSince(cfg, nodes); len(got) != 1 || got[0] != "web-2" {
		t.Errorf("later origin: %v", got)
	}
	cfg.RulesOrigin = &RulesOrigin{At: at.Add(time.Minute)}
	nodes[0].RulesOrigin = nil
	if got := bk.ChangedSince(cfg, nodes); len(got) != 3 || got[0] != "org" || got[1] != "web-1" || got[2] != "web-2" {
		t.Errorf("org, no origin and later origin: %v", got)
	}
	// A host that does not exist any more is a target. Its id names it.
	bk.Hosts[gone] = HostRules{}
	kept := []Node{{ID: a, DisplayName: "web-1", RulesOrigin: origin}}
	want := []string{b.String(), gone.String()}
	slices.Sort(want)
	if got := bk.ChangedSince(ServerSettings{RulesOrigin: origin}, kept); !slices.Equal(got, want) {
		t.Errorf("deleted hosts: %v, want %v", got, want)
	}
	bk.Org = nil
	if got := bk.ChangedSince(ServerSettings{}, kept); slices.Contains(got, "org") {
		t.Errorf("a backup with no org rules names the org: %v", got)
	}
}

func TestNormalizeClientAI(t *testing.T) {
	for in, want := range map[string]string{"ai": "ai", "web": "web", "foo": "api", "": "api"} {
		if got := NormalizeClient(in); got != want {
			t.Errorf("NormalizeClient(%q) = %q, want %q", in, got, want)
		}
	}
}
