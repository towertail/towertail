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
