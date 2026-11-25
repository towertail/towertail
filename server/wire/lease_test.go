package wire

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the files in testdata/fixtures")

const (
	linuxSample = "../testdata/load/sample-linux.json"
	fixtureDir  = "../testdata/fixtures"
)

func loadSample(t *testing.T, raw []byte) Sample {
	t.Helper()
	var s Sample
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func linux(t *testing.T) Sample {
	t.Helper()
	raw, err := os.ReadFile(linuxSample)
	if err != nil {
		t.Fatal(err)
	}
	return loadSample(t, raw)
}

// full has ports and health, which the Linux load fixture does not.
const full = `{"v":1,"ts":"2026-10-17T12:00:00.000Z",
"host":{"name":"h","os":"darwin","arch":"arm64","kernel":"24.0","uptime_s":5,"sampler":"0.9.0+abc"},
"cpu":{"pct":10,"load_1":0.5,"load_5":0,"load_15":0,"cores":8},
"mem":{"used":50,"total":200},"swap":{"used":0,"total":0},
"disks":[{"mount":"/","fs":"apfs","used":1,"total":4,"inodes_used":90,"inodes_total":100},
 {"mount":"/data","fs":"apfs","used":3,"total":4,"inodes_used":10,"inodes_total":100}],
"disk_io":{"read_bps":1,"write_bps":2,"read_cum":3,"write_cum":4},
"procs":{"root":true,"top_n":1,"total":321,"visible":321,"items":[{"pid":1,"name":"init","cpu_pct":1,"rss":2}]},
"ports":{"root":true,"collected_ts":"2026-10-17T12:00:00.000Z","max_conn":10,"total":0,"items":[]},
"health":{"procs":321,"zombies":2,"zombie_parents":[{"pid":9,"name":"x","count":2}],"pids_used":5,"pids_max":10},
"errors":[]}`

func TestSampleSummary(t *testing.T) {
	s := linux(t)
	sum := s.Summary()
	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-4 {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	near("cpu", sum.CPU, s.CPU.Pct)
	near("mem", sum.Mem, float64(s.Mem.Used)/float64(s.Mem.Total))
	var fullest float64
	for _, d := range *s.Disks {
		fullest = math.Max(fullest, float64(d.Used)/float64(d.Total))
	}
	near("disk", sum.Disk, fullest)
	near("load", sum.Load, s.CPU.Load1)
	if sum.Rx != s.Net.RxBps || sum.Tx != s.Net.TxBps {
		t.Errorf("net = %d/%d", sum.Rx, sum.Tx)
	}
	if sum.TS != s.TS || sum.Host != nil || sum.Health != nil {
		t.Errorf("summary: %+v", sum)
	}
	b, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) >= 300 {
		t.Errorf("summary is %d bytes: %s", len(b), b)
	}
}

func TestSampleSummaryNoNetNoDisks(t *testing.T) {
	s := loadSample(t, []byte(`{"v":1,"ts":"t","host":{},"cpu":{},"mem":{},"swap":{},"errors":[]}`))
	sum := s.Summary()
	if sum.Mem != 0 || sum.Disk != 0 || sum.Rx != 0 || sum.Tx != 0 || sum.Procs != 0 {
		t.Errorf("summary: %+v", sum)
	}
	if _, ok := s.Detail(DetailProcesses); ok {
		t.Error("detail of an empty sample")
	}
}

func TestSampleSummaryHealthAndProcs(t *testing.T) {
	s := loadSample(t, []byte(full))
	sum := s.Summary()
	if sum.Procs != 321 {
		t.Errorf("procs = %d", sum.Procs)
	}
	h := sum.Health
	if h == nil || h.Procs != 321 || *h.Zombies != 2 || h.Inodes != 0.9 {
		t.Fatalf("health: %+v", h)
	}
	b, _ := json.Marshal(h)
	if bytes.Contains(b, []byte("zombie_parents")) {
		t.Errorf("brief has zombie parents: %s", b)
	}
	if got := s.Facts(); got.OS != "darwin" || got.Cores != 8 || got.MemTotal != 200 || got.Sampler != "0.9.0+abc" {
		t.Errorf("facts: %+v", got)
	}
}

func TestSampleDetailTabs(t *testing.T) {
	s := loadSample(t, []byte(full))
	keys := func(tab DetailTab) map[string]bool {
		t.Helper()
		d, ok := s.Detail(tab)
		if !ok {
			t.Fatalf("%s: no parts", tab)
		}
		b, _ := json.Marshal(d)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for k := range m {
			out[k] = true
		}
		return out
	}
	cases := map[DetailTab][]string{
		DetailProcesses: {"ts", "procs"},
		DetailPorts:     {"ts", "ports"},
		DetailDisks:     {"ts", "disks", "disk_io", "procs"},
		DetailHealth:    {"ts", "health", "procs"},
	}
	for tab, want := range cases {
		got := keys(tab)
		if len(got) != len(want) {
			t.Errorf("%s: keys %v, want %v", tab, got, want)
		}
		for _, k := range want {
			if !got[k] {
				t.Errorf("%s: no key %s", tab, k)
			}
		}
	}

	noPorts := linux(t)
	if _, ok := noPorts.Detail(DetailPorts); ok {
		t.Error("ports on a sample without ports")
	}
	if _, ok := noPorts.Detail(DetailHealth); !ok {
		t.Error("health tab has the process list")
	}
}

func TestDetailTabValid(t *testing.T) {
	for _, tab := range []DetailTab{DetailProcesses, DetailPorts, DetailDisks, DetailHealth} {
		if !tab.Valid() {
			t.Errorf("%s: not valid", tab)
		}
	}
	for _, tab := range []DetailTab{"", "network", "Processes"} {
		if tab.Valid() {
			t.Errorf("%q: valid", tab)
		}
	}
}

func TestHostFactsSame(t *testing.T) {
	a := HostFacts{OS: "linux", Sampler: "1", UptimeS: 1}
	b := a
	b.UptimeS = 99
	if !a.Same(b) {
		t.Error("uptime must not count")
	}
	b.Sampler = "2"
	if a.Same(b) {
		t.Error("sampler must count")
	}
}

// TestSummaryFixtures keeps testdata/fixtures in step with Summary and
// Detail. The web and Mac tests decode the same files. Run with -update
// to rewrite them.
func TestSummaryFixtures(t *testing.T) {
	s := linux(t)
	sum := s.Summary()
	facts := s.Facts()
	sum.Host = &facts
	detail, ok := s.Detail(DetailProcesses)
	if !ok {
		t.Fatal("no processes")
	}
	files := map[string]any{
		"summary_linux.json":          sum,
		"detail_processes_linux.json": detail,
	}
	for name, v := range files {
		want, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, '\n')
		path := filepath.Join(fixtureDir, name)
		if *update {
			if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date; run go test ./wire -update", name)
		}
	}
}

func TestDetailKeepsProcFolders(t *testing.T) {
	s := loadSample(t, []byte(`{"v":1,"ts":"t","host":{},"cpu":{},"mem":{},"swap":{},
"procs":{"root":true,"top_n":1,"total":1,"visible":1,"items":[
 {"pid":10,"name":"node","cpu_pct":5,"rss":2,"cwd":"/srv/app/api","repo":"/srv/app","parent":"zsh","read_bps":2048,"write_bps":0}]},
"errors":[]}`))
	for _, tab := range []DetailTab{DetailProcesses, DetailDisks, DetailHealth} {
		d, ok := s.Detail(tab)
		if !ok {
			t.Fatalf("%s: no parts", tab)
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"cwd":"/srv/app/api"`, `"repo":"/srv/app"`, `"parent":"zsh"`, `"read_bps":2048`, `"write_bps":0`} {
			if !bytes.Contains(b, []byte(want)) {
				t.Errorf("%s: %s is missing %s", tab, b, want)
			}
		}
	}
}

func TestDetailProcessesHaveKey(t *testing.T) {
	s := loadSample(t, []byte(`{"v":1,"ts":"t","host":{},"cpu":{},"mem":{},"swap":{},
"procs":{"root":true,"top_n":1,"total":1,"visible":1,"items":[{"pid":10,"name":"node","cmd":"node app.js","cpu_pct":5,"rss":2}]},
"errors":[]}`))
	d, ok := s.Detail(DetailProcesses)
	if !ok {
		t.Fatal("no parts")
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Procs struct {
			Total int
			Items []struct {
				Key ProcessKey
				PID int
			}
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if want := NewProcessKey("node", "node app.js"); got.Procs.Total != 1 || len(got.Procs.Items) != 1 || got.Procs.Items[0].Key != want || got.Procs.Items[0].PID != 10 {
		t.Fatalf("frame: %s", b)
	}
}
