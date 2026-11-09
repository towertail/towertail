package notify

import (
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

func TestAlertText(t *testing.T) {
	for _, tc := range []struct {
		a           Alert
		title, text string
	}{
		{Alert{NodeName: "web-1", Metric: "cpu", From: wire.TintWarn, To: wire.TintCritical, Value: 0.934}, "web-1: CPU critical", "CPU 93% (was warn)"},
		{Alert{NodeName: "web-1", Metric: "mem", From: wire.TintWarn, To: wire.TintOK, Value: 0.5}, "web-1: Memory back to ok", "Memory 50% (was warn)"},
		{Alert{NodeName: "db", Metric: "reachability", From: wire.TintOK, To: wire.TintCritical, Value: 150.4}, "db: unreachable", "No sample for 2m30s."},
		{Alert{NodeName: "db", Metric: "reachability", From: wire.TintCritical, To: wire.TintOK}, "db: reachable again", "Samples come in again."},
		{Alert{NodeName: "db", Metric: "disk", To: wire.TintWarn, Test: true}, "[test] db: Disk warn", "Disk 0% (was )"},
		{Alert{NodeName: "mbp", Metric: "long_proc", From: wire.TintOK, To: wire.TintWarn, Value: 85, Detail: "OrbStack Helper (pid 812) at 85% CPU for 32m."}, "mbp: long-running process", "OrbStack Helper (pid 812) at 85% CPU for 32m."},
		{Alert{NodeName: "mbp", Metric: "long_proc", From: wire.TintWarn, To: wire.TintOK, Detail: "x"}, "mbp: long-running process ended", "x"},
	} {
		if got := tc.a.Title(); got != tc.title {
			t.Errorf("title: %q, want %q", got, tc.title)
		}
		if got := tc.a.Text(); got != tc.text {
			t.Errorf("text: %q, want %q", got, tc.text)
		}
	}
	id := uuid.New()
	if got := (Alert{NodeID: id, Metric: "cpu", To: wire.TintWarn}).Title(); got != id.String()+": CPU warn" {
		t.Errorf("no name: %q", got)
	}
}

func TestDedupKeyInstance(t *testing.T) {
	a := Alert{Metric: "long_proc", Instance: "812/1799000000"}
	b := a
	b.Instance = "9/1799000000"
	if a.DedupKey() == b.DedupKey() {
		t.Fatal("two processes share a dedup key")
	}
	if got := (Alert{Metric: "cpu"}).DedupKey(); got != uuid.Nil.String()+":"+uuid.Nil.String()+":cpu" {
		t.Errorf("cpu key = %q", got)
	}
}
