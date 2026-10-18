//go:build integration

package clickhouse

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAlertsAckSnoozeRoundTrip(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, otherOrg, node := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	write := func(org uuid.UUID, metric, tint string, ts time.Time) uuid.UUID {
		t.Helper()
		id := uuid.New()
		err := client.WriteEvent(ctx, EventRow{
			TS: ts, OrgID: org, NodeID: node, EventID: id, Kind: "threshold_crossed",
			Metric: metric, Tint: tint, Payload: `{"value":0.95}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	write(org, "cpu", "warn", now.Add(-3*time.Minute))
	open := write(org, "cpu", "critical", now.Add(-2*time.Minute))
	closed := write(org, "mem", "critical", now.Add(-2*time.Minute))
	write(org, "mem", "ok", now.Add(-time.Minute))
	write(otherOrg, "cpu", "critical", now)

	alerts, err := client.OpenAlerts(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].EventID != open || alerts[0].Tint != "critical" || alerts[0].Kind != "threshold_crossed" {
		t.Fatalf("open alerts = %+v", alerts)
	}
	if _, err := client.Alert(ctx, org, closed); err != ErrNoAlert {
		t.Fatalf("closed alert: err = %v, want ErrNoAlert", err)
	}
	if _, err := client.Alert(ctx, otherOrg, open); err != ErrNoAlert {
		t.Fatalf("alert of other org: err = %v, want ErrNoAlert", err)
	}

	by := uuid.New()
	if err := client.AckAlert(ctx, org, open, by, now); err != nil {
		t.Fatal(err)
	}
	until := now.Add(time.Hour)
	if err := client.SnoozeAlert(ctx, org, open, until); err != nil {
		t.Fatal(err)
	}
	a, err := client.Alert(ctx, org, open)
	if err != nil {
		t.Fatal(err)
	}
	if a.AckedBy == nil || *a.AckedBy != by || a.AckedAt == nil || !a.AckedAt.Equal(now) ||
		a.SnoozedUntil == nil || !a.SnoozedUntil.Equal(until) {
		t.Fatalf("after ack and snooze: %+v", a)
	}

	tints, err := client.LastTints(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range tints {
		if r.OrgID == org {
			got[r.Metric] = r.Tint
		}
	}
	if got["cpu"] != "critical" || got["mem"] != "ok" || len(got) != 2 {
		t.Fatalf("last tints = %v", got)
	}
}
