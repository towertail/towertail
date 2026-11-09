package store

import (
	"encoding/json"
	"reflect"
	"testing"
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
