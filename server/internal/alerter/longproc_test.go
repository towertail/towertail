package alerter

import (
	"testing"
	"time"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

// The same cases as LongProcTrackerTests in the Mac app.

var (
	lpT0      = time.Unix(1_800_000_000, 0).UTC()
	lpStarted = time.Unix(1_799_000_000, 0).UTC()
	lpRule    = store.LongProcRule{Enabled: true, CPUPct: 50, SustainSeconds: 1800, Notify: "all"}
)

func lpProc(cpu float32, pid int32, name string, start time.Time) clickhouse.ProcRow {
	return clickhouse.ProcRow{PID: pid, Name: name, Cmd: "/usr/bin/" + name, CPUPct: cpu, StartTS: start}
}

func orb(cpu float32) clickhouse.ProcRow { return lpProc(cpu, 812, "OrbStack Helper", lpStarted) }

func lpAt(m int) time.Time { return lpT0.Add(time.Duration(m) * time.Minute) }

// feed sends one sample each minute from..through and gives all changes.
func feed(t *procTracker, from, through int, rule store.LongProcRule, ignored map[string]bool, items func(int) []clickhouse.ProcRow) []procChange {
	var out []procChange
	for m := from; m <= through; m++ {
		out = append(out, t.observe(items(m), lpAt(m), rule, ignored)...)
	}
	return out
}

// flagged gives the reported set as pids.
func (t *procTracker) flaggedPIDs() []int32 {
	var out []int32
	for k := range t.reported {
		out = append(out, k.pid)
	}
	return out
}

func (t *procTracker) changePoints() int {
	n := 0
	for _, e := range t.entries {
		n += len(e.points)
	}
	return n
}

func TestProcTracker_SteadyHighFlagsAfterSustain(t *testing.T) {
	tr := newProcTracker()
	if c := feed(tr, 0, 29, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} }); len(c) != 0 {
		t.Fatalf("before the sustain time: %+v", c)
	}
	c := feed(tr, 30, 30, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	if len(c) != 1 || !c[0].flagged || c[0].proc.PID != 812 || !c[0].highSince.Equal(lpT0) {
		t.Fatalf("changes = %+v", c)
	}
	if got := c[0].detail(lpAt(30)); got != "OrbStack Helper (pid 812) at 60% CPU for 30m." {
		t.Errorf("detail = %q", got)
	}
	if c := feed(tr, 31, 40, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} }); len(c) != 0 {
		t.Errorf("later samples: %+v", c)
	}
}

func TestProcTracker_TwentyPercentDipsStayFlagged(t *testing.T) {
	tr := newProcTracker()
	c := feed(tr, 0, 90, lpRule, nil, func(m int) []clickhouse.ProcRow {
		if m%5 == 4 {
			return []clickhouse.ProcRow{orb(10)}
		}
		return []clickhouse.ProcRow{orb(60)}
	})
	if len(c) != 1 || !c[0].flagged || !c[0].highSince.Equal(lpT0) {
		t.Fatalf("a dip in the first window keeps the run start: %+v", c)
	}
}

func TestProcTracker_ThirtyPercentDipsDoNotFlag(t *testing.T) {
	tr := newProcTracker()
	c := feed(tr, 0, 120, lpRule, nil, func(m int) []clickhouse.ProcRow {
		if m%10 >= 7 {
			return []clickhouse.ProcRow{orb(10)}
		}
		return []clickhouse.ProcRow{orb(60)}
	})
	if len(c) != 0 {
		t.Fatalf("changes = %+v", c)
	}
}

func TestProcTracker_BelowLimitNeverFlags(t *testing.T) {
	tr := newProcTracker()
	feed(tr, 0, 120, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(40)} })
	if !tr.empty() {
		t.Fatalf("entries = %d", len(tr.entries))
	}
}

func TestProcTracker_ReusedPIDWithNewStartIsNewRun(t *testing.T) {
	tr := newProcTracker()
	feed(tr, 0, 20, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	other := lpStarted.Add(500 * time.Second)
	next := func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{lpProc(60, 812, "OrbStack Helper", other)} }
	if c := feed(tr, 21, 40, lpRule, nil, next); len(c) != 0 {
		t.Fatalf("the new process has only 19 minutes: %+v", c)
	}
	c := feed(tr, 41, 51, lpRule, nil, next)
	if len(c) != 1 || !c[0].highSince.Equal(lpAt(21)) {
		t.Fatalf("changes = %+v", c)
	}
	if got := c[0].key.metric(); got != "long_proc/812/1799000500" {
		t.Errorf("metric = %q", got)
	}
}

func TestProcTracker_ProcessThatLeavesClears(t *testing.T) {
	tr := newProcTracker()
	feed(tr, 0, 30, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	c := feed(tr, 31, 31, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{lpProc(1, 1, "other", lpStarted)} })
	if len(c) != 1 || c[0].flagged {
		t.Fatalf("a killed process clears at once: %+v", c)
	}
	feed(tr, 32, 70, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{lpProc(1, 1, "other", lpStarted)} })
	if !tr.empty() {
		t.Fatalf("no high point in the window: entries = %d", len(tr.entries))
	}
}

func TestProcTracker_ShortAbsenceKeepsTheRun(t *testing.T) {
	tr := newProcTracker()
	feed(tr, 0, 30, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	feed(tr, 31, 32, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{lpProc(1, 1, "other", lpStarted)} })
	c := feed(tr, 33, 33, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	if len(c) != 1 || !c[0].flagged || !c[0].highSince.Equal(lpT0) {
		t.Fatalf("changes = %+v", c)
	}
}

func TestProcTracker_IgnoredNamesNeverFlag(t *testing.T) {
	tr := newProcTracker()
	ignored := lpRule.IgnoreSet([]string{"ORBSTACK helper"})
	if c := feed(tr, 0, 40, lpRule, ignored, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(90)} }); len(c) != 0 {
		t.Fatalf("changes = %+v", c)
	}
	if !tr.empty() {
		t.Fatalf("entries = %d", len(tr.entries))
	}
}

func TestProcTracker_IgnoreRemovesATrackedProcess(t *testing.T) {
	tr := newProcTracker()
	feed(tr, 0, 30, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	c := feed(tr, 31, 31, lpRule, map[string]bool{"orbstack helper": true}, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	if len(c) != 1 || c[0].flagged || !tr.empty() {
		t.Fatalf("changes = %+v, entries = %d", c, len(tr.entries))
	}
}

func TestProcTracker_SteadyDayKeepsFewChangePoints(t *testing.T) {
	tr := newProcTracker()
	day := lpRule
	day.SustainSeconds = 86_400
	feed(tr, 0, 24*60, day, nil, func(m int) []clickhouse.ProcRow {
		cpu := float32(60)
		if m%30 == 15 {
			cpu = 10
		}
		return []clickhouse.ProcRow{orb(cpu), lpProc(70, 2, "node", lpStarted)}
	})
	if got := len(tr.flaggedPIDs()); got != 2 {
		t.Fatalf("flagged = %d", got)
	}
	if n := tr.changePoints(); n >= 200 {
		t.Errorf("change points = %d", n)
	}
	feed(tr, 1441, 1441, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	if n := tr.changePoints(); n >= 100 {
		t.Errorf("a short rule trims the window: change points = %d", n)
	}
}

func TestProcTracker_DisabledRuleClears(t *testing.T) {
	tr := newProcTracker()
	feed(tr, 0, 30, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	off := lpRule
	off.Enabled = false
	c := tr.observe([]clickhouse.ProcRow{orb(60)}, lpAt(31), off, nil)
	if len(c) != 1 || c[0].flagged || !tr.empty() {
		t.Fatalf("changes = %+v", c)
	}
}

func TestProcTracker_NewKeysSortedByCPU(t *testing.T) {
	tr := newProcTracker()
	c := feed(tr, 0, 30, lpRule, nil, func(int) []clickhouse.ProcRow {
		return []clickhouse.ProcRow{orb(60), lpProc(90, 2, "node", lpStarted)}
	})
	if len(c) != 2 || c[0].proc.Name != "node" || c[1].proc.Name != "OrbStack Helper" {
		t.Fatalf("changes = %+v", c)
	}
}

func TestProcTracker_KeyWithoutStartUsesName(t *testing.T) {
	a := newProcKey(clickhouse.ProcRow{PID: 5, Name: "a"})
	b := newProcKey(clickhouse.ProcRow{PID: 5, Name: "b"})
	if a == b {
		t.Fatal("keys without a start time must differ by name")
	}
	if newProcKey(orb(1)) != newProcKey(lpProc(99, 812, "renamed", lpStarted)) {
		t.Fatal("keys with a start time ignore the name")
	}
	if got := a.metric(); got != "long_proc/5/a" {
		t.Errorf("metric = %q", got)
	}
}

func TestProcTracker_TimeBackwardResets(t *testing.T) {
	tr := newProcTracker()
	feed(tr, 0, 30, lpRule, nil, func(int) []clickhouse.ProcRow { return []clickhouse.ProcRow{orb(60)} })
	c := tr.observe([]clickhouse.ProcRow{orb(60)}, lpAt(-5), lpRule, nil)
	if len(c) != 1 || c[0].flagged {
		t.Fatalf("changes = %+v", c)
	}
}

func TestProcChange_ShortName(t *testing.T) {
	c := procChange{proc: clickhouse.ProcRow{PID: 7, Name: "com.apple.WebKit.WebContent.Extra"}}
	if got := c.shortName(); got != "com.apple.WebKit.WebCon…" {
		t.Errorf("shortName = %q", got)
	}
	if got := (procChange{proc: clickhouse.ProcRow{PID: 7}}).shortName(); got != "pid 7" {
		t.Errorf("shortName = %q", got)
	}
}
