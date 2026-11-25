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
	// write stores one event. A zero alert id means the id of the event.
	write := func(org uuid.UUID, metric, tint string, ts time.Time, alert uuid.UUID) uuid.UUID {
		t.Helper()
		id := uuid.New()
		err := client.WriteEvent(ctx, EventRow{
			TS: ts, OrgID: org, NodeID: node, EventID: id, AlertID: alert, Kind: "threshold_crossed",
			Metric: metric, Tint: tint, Payload: `{"value":0.95}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	cpu := write(org, "cpu", "warn", now.Add(-3*time.Minute), uuid.Nil)
	open := write(org, "cpu", "critical", now.Add(-2*time.Minute), cpu)
	closed := write(org, "mem", "critical", now.Add(-2*time.Minute), uuid.Nil)
	write(org, "mem", "ok", now.Add(-time.Minute), closed)
	write(otherOrg, "cpu", "critical", now, uuid.Nil)

	alerts, err := client.OpenAlerts(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].EventID != open || alerts[0].AlertID != cpu || alerts[0].Tint != "critical" || alerts[0].Kind != "threshold_crossed" {
		t.Fatalf("open alerts = %+v", alerts)
	}
	// An alert is found by its alert id and by the event id of an old client.
	for _, id := range []uuid.UUID{cpu, open} {
		a, err := client.Alert(ctx, org, id)
		if err != nil || a.EventID != open || a.AlertID != cpu {
			t.Fatalf("Alert(%s) = %+v, %v", id, a, err)
		}
	}
	// A closed alert is found, with the tint ok.
	if a, err := client.Alert(ctx, org, closed); err != nil || a.Tint != "ok" {
		t.Fatalf("closed alert = %+v, %v", a, err)
	}
	// An id that no event has gives ErrNoAlert.
	if _, err := client.Alert(ctx, org, uuid.New()); err != ErrNoAlert {
		t.Fatalf("unknown alert: err = %v, want ErrNoAlert", err)
	}
	if _, err := client.Alert(ctx, otherOrg, open); err != ErrNoAlert {
		t.Fatalf("alert of other org: err = %v, want ErrNoAlert", err)
	}

	by := uuid.New()
	until := now.Add(time.Hour)
	err = client.WriteAlertLines(ctx, []AlertLine{
		{TS: now, OrgID: org, NodeID: node, AlertID: cpu, Kind: "acked", User: &by},
		{TS: now.Add(time.Second), OrgID: org, NodeID: node, AlertID: cpu, Kind: "snoozed", User: &by, Until: &until},
	})
	if err != nil {
		t.Fatal(err)
	}
	marks, err := client.AlertMarks(ctx, org, []uuid.UUID{cpu, closed})
	if err != nil {
		t.Fatal(err)
	}
	m := marks[cpu]
	if m.AckedBy == nil || *m.AckedBy != by || m.AckedAt == nil || !m.AckedAt.Equal(now) ||
		m.SnoozedUntil == nil || !m.SnoozedUntil.Equal(until) || len(marks) != 1 {
		t.Fatalf("marks after ack and snooze: %+v", marks)
	}

	recent, err := client.RecentAlerts(ctx, org, now.Add(-150*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 || recent[0].Tint != "ok" || recent[0].Metric != "mem" {
		t.Fatalf("recent alerts = %+v", recent)
	}
	if recent, _ := client.RecentAlerts(ctx, org, now.Add(-time.Hour), 2); len(recent) != 2 {
		t.Fatalf("recent alerts with limit 2 = %d rows", len(recent))
	}

	tints, err := client.LastTints(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	alertOf := map[string]uuid.UUID{}
	for _, r := range tints {
		if r.OrgID == org {
			got[r.Metric] = r.Tint
			alertOf[r.Metric] = r.AlertID
		}
	}
	if got["cpu"] != "critical" || got["mem"] != "ok" || len(got) != 2 || alertOf["cpu"] != cpu || alertOf["mem"] != closed {
		t.Fatalf("last tints = %v, alerts = %v", got, alertOf)
	}
}

func TestAlertLogMarks(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node, user := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	line := func(alert uuid.UUID, kind string, at time.Duration, until *time.Time) AlertLine {
		return AlertLine{TS: now.Add(at), OrgID: org, NodeID: node, AlertID: alert, Kind: kind, User: &user, Until: until}
	}
	acked, unacked, snoozed, plain, legacy := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	first, last := now.Add(time.Hour), now.Add(3*time.Hour)
	err := client.WriteAlertLines(ctx, []AlertLine{
		line(acked, "acked", -3*time.Second, nil),
		line(unacked, "acked", -3*time.Second, nil),
		line(unacked, "unacked", -2*time.Second, nil),
		line(snoozed, "snoozed", -3*time.Second, &first),
		line(snoozed, "snoozed", -2*time.Second, &last),
		line(plain, "notified", -time.Second, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	// An alert from before the log has its state in the old columns.
	ackAt, snoozeUntil := now.Add(-time.Minute), now.Add(2*time.Hour)
	err = client.WriteEvent(ctx, EventRow{
		TS: now.Add(-time.Hour), OrgID: org, NodeID: node, EventID: legacy, Kind: "threshold_crossed",
		Metric: "disk", Tint: "warn", Payload: `{}`, AckedBy: &user, AckedAt: &ackAt, SnoozedUntil: &snoozeUntil,
	})
	if err != nil {
		t.Fatal(err)
	}

	marks, err := client.AlertMarks(ctx, org, []uuid.UUID{acked, unacked, snoozed, plain, legacy})
	if err != nil {
		t.Fatal(err)
	}
	if m := marks[acked]; m.AckedBy == nil || *m.AckedBy != user || m.SnoozedUntil != nil {
		t.Errorf("acked = %+v", m)
	}
	if m := marks[unacked]; m.AckedBy != nil || m.AckedAt != nil {
		t.Errorf("acked then unacked = %+v, want no ack", m)
	}
	if m := marks[snoozed]; m.SnoozedUntil == nil || !m.SnoozedUntil.Equal(last) || m.AckedBy != nil {
		t.Errorf("snoozed twice = %+v, want the last until", m)
	}
	if m, ok := marks[plain]; ok && (m.AckedBy != nil || m.SnoozedUntil != nil) {
		t.Errorf("notified only = %+v", m)
	}
	if m := marks[legacy]; m.AckedBy == nil || *m.AckedBy != user || m.AckedAt == nil || !m.AckedAt.Equal(ackAt) ||
		m.SnoozedUntil == nil || !m.SnoozedUntil.Equal(snoozeUntil) {
		t.Errorf("old columns = %+v", m)
	}

	// An unack after an old ack removes it. The old column does not come back.
	err = client.WriteAlertLines(ctx, []AlertLine{line(legacy, "unacked", 0, nil)})
	if err != nil {
		t.Fatal(err)
	}
	marks, err = client.AlertMarks(ctx, org, []uuid.UUID{legacy})
	if err != nil {
		t.Fatal(err)
	}
	if m := marks[legacy]; m.AckedBy != nil || m.SnoozedUntil == nil {
		t.Errorf("old ack, then unack = %+v", m)
	}

	open, err := client.OpenMarks(ctx, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	byAlert := map[uuid.UUID]AlertMark{}
	for _, m := range open {
		byAlert[m.AlertID] = m
	}
	if m := byAlert[acked]; m.OrgID != org || m.AckedBy == nil {
		t.Errorf("open marks, acked = %+v", m)
	}
	if m := byAlert[unacked]; m.AckedBy != nil {
		t.Errorf("open marks, unacked = %+v", m)
	}
}

// Each process of a long_process event is its own open alert.
func TestLongProcessAlerts(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	write := func(metric, tint string, ts time.Time) uuid.UUID {
		t.Helper()
		id := uuid.New()
		err := client.WriteEvent(ctx, EventRow{
			TS: ts, OrgID: org, NodeID: node, EventID: id, Kind: "long_process",
			Metric: metric, Tint: tint, Payload: `{"value":85,"pid":812}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	open := write("long_proc/812/1799000000", "warn", now.Add(-2*time.Minute))
	write("long_proc/9/1799000000", "warn", now.Add(-2*time.Minute))
	write("long_proc/9/1799000000", "ok", now.Add(-time.Minute))

	alerts, err := client.OpenAlerts(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].EventID != open || alerts[0].Kind != "long_process" {
		t.Fatalf("open alerts = %+v", alerts)
	}
	if recent, _ := client.RecentAlerts(ctx, org, now.Add(-time.Hour), 10); len(recent) != 3 {
		t.Fatalf("recent alerts = %+v", recent)
	}
}

func TestRegistrations(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, ts := range []time.Time{now.Add(-time.Hour), now.Add(-time.Minute)} {
		err := client.WriteEvent(ctx, EventRow{
			TS: ts, OrgID: org, NodeID: node, EventID: uuid.New(), Kind: "registered",
			Metric: "reachability", Tint: "ok",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := client.Registrations(ctx, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OrgID != org || got[0].NodeID != node || !got[0].TS.Equal(now.Add(-time.Minute)) {
		t.Fatalf("registrations = %+v", got)
	}
}
