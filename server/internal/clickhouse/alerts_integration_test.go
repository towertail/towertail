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

// incidentEnv writes events of one org for the tests of the incident reads.
type incidentEnv struct {
	t      *testing.T
	client *Client
	org    uuid.UUID
	now    time.Time
}

// event writes one event. A zero alert id means the id of the event.
func (e *incidentEnv) event(node uuid.UUID, metric, tint string, ago time.Duration, alert uuid.UUID, payload string) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	err := e.client.WriteEvent(context.Background(), EventRow{
		TS: e.now.Add(-ago), OrgID: e.org, NodeID: node, EventID: id, AlertID: alert,
		Kind: "threshold_crossed", Metric: metric, Tint: tint, Payload: payload,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestIncidents(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()
	e := &incidentEnv{t: t, client: client, org: uuid.New(), now: time.Now().UTC().Truncate(time.Millisecond)}
	node, node2 := uuid.New(), uuid.New()
	h := time.Hour

	// ok -> warn -> critical -> warn -> ok: one incident.
	cpu := e.event(node, "cpu", "warn", 3*h, uuid.Nil, `{"value":0.85,"warn":0.8,"critical":0.95}`)
	e.event(node, "cpu", "critical", 2*h, cpu, `{"value":0.97}`)
	e.event(node, "cpu", "warn", 90*time.Minute, cpu, `{"value":0.9}`)
	e.event(node, "cpu", "ok", h, cpu, `{"value":0.2,"peak":0.97}`)
	// An open alert has no end.
	mem := e.event(node, "mem", "warn", 30*time.Minute, uuid.Nil, `{"value":0.9}`)
	// An open alert that started before the range, and a resolved one.
	disk := e.event(node2, "disk", "critical", 3*24*h, uuid.Nil, `{"value":0.97}`)
	oldDone := e.event(node2, "cpu", "warn", 10*24*h, uuid.Nil, `{"value":0.9}`)
	e.event(node2, "cpu", "ok", 9*24*h, oldDone, `{"value":0.1}`)
	// An alert from before alert_id: each event is its own alert.
	legacy := e.event(node2, "mem", "warn", 5*h, uuid.Nil, `{"value":0.9}`)
	legacyOK := e.event(node2, "mem", "ok", 4*h, uuid.Nil, `{"value":0.1}`)
	otherOrg := &incidentEnv{t: t, client: client, org: uuid.New(), now: e.now}
	otherOrg.event(node, "cpu", "critical", h, uuid.Nil, `{}`)

	by := uuid.New()
	err := client.WriteAlertLines(ctx, []AlertLine{{TS: e.now.Add(-100 * time.Minute), OrgID: e.org, NodeID: node, AlertID: cpu, Kind: "acked", User: &by}})
	if err != nil {
		t.Fatal(err)
	}

	rows, truncated, err := client.Incidents(ctx, IncidentQuery{OrgID: e.org, From: e.now.Add(-24 * h), To: e.now, Limit: 100})
	if err != nil || truncated {
		t.Fatalf("incidents: truncated = %v, err = %v", truncated, err)
	}
	byID := map[uuid.UUID]IncidentRow{}
	for _, r := range rows {
		byID[r.AlertID] = r
	}
	// cpu, mem, disk (still open), and the two rows of the old alert.
	if len(rows) != 5 {
		t.Fatalf("rows = %d: %+v", len(rows), rows)
	}
	if _, ok := byID[oldDone]; ok {
		t.Fatal("an alert that ended before the range is in the rows")
	}
	c := byID[cpu]
	if c.End == nil || !c.End.Equal(e.now.Add(-h)) || !c.Start.Equal(e.now.Add(-3*h)) || c.Severity != "critical" || c.Tint != "ok" ||
		c.Peak != 0.97 || c.Value != 0.9 || c.NodeID != node || c.Kind != "threshold_crossed" || c.Metric != "cpu" ||
		c.Payload != `{"value":0.85,"warn":0.8,"critical":0.95}` || c.Mark.AckedBy == nil || *c.Mark.AckedBy != by {
		t.Fatalf("cpu incident = %+v", c)
	}
	if m := byID[mem]; m.End != nil || m.Severity != "warn" || m.Tint != "warn" || m.Mark.AckedBy != nil {
		t.Fatalf("open incident = %+v", m)
	}
	if d := byID[disk]; d.End != nil || d.Severity != "critical" {
		t.Fatalf("incident from before the range = %+v", d)
	}
	if l := byID[legacy]; l.End != nil || l.Severity != "warn" {
		t.Fatalf("old alert = %+v", l)
	}
	if l := byID[legacyOK]; l.End == nil || l.Severity != "ok" {
		t.Fatalf("old change back to ok = %+v", l)
	}
	if rows[0].AlertID != mem {
		t.Fatalf("first row = %+v, want the newest start", rows[0])
	}

	// A wider range finds the old resolved alert.
	wide, _, err := client.Incidents(ctx, IncidentQuery{OrgID: e.org, From: e.now.Add(-11 * 24 * h), To: e.now, Limit: 100})
	if err != nil || len(wide) != 6 {
		t.Fatalf("wide range: %d rows, %v", len(wide), err)
	}
	// A range that ends before an alert started leaves it out.
	early, _, err := client.Incidents(ctx, IncidentQuery{OrgID: e.org, From: e.now.Add(-11 * 24 * h), To: e.now.Add(-6 * 24 * h), Limit: 100})
	if err != nil || len(early) != 1 || early[0].AlertID != oldDone {
		t.Fatalf("early range: %d rows, %v", len(early), err)
	}
	only, _, err := client.Incidents(ctx, IncidentQuery{OrgID: e.org, From: e.now.Add(-24 * h), To: e.now, NodeIDs: []uuid.UUID{node}, Limit: 100})
	if err != nil || len(only) != 2 {
		t.Fatalf("one node: %d rows, %v", len(only), err)
	}
	// Limit+1 rows give truncated.
	cut, truncated, err := client.Incidents(ctx, IncidentQuery{OrgID: e.org, From: e.now.Add(-24 * h), To: e.now, Limit: 3})
	if err != nil || !truncated || len(cut) != 3 {
		t.Fatalf("limit 3: %d rows, truncated = %v, %v", len(cut), truncated, err)
	}
	exact, truncated, err := client.Incidents(ctx, IncidentQuery{OrgID: e.org, From: e.now.Add(-24 * h), To: e.now, Limit: 5})
	if err != nil || truncated || len(exact) != 5 {
		t.Fatalf("limit 5: %d rows, truncated = %v, %v", len(exact), truncated, err)
	}
}

func TestAlertsByIDAndEvents(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()
	e := &incidentEnv{t: t, client: client, org: uuid.New(), now: time.Now().UTC().Truncate(time.Millisecond)}
	node := uuid.New()
	h := time.Hour

	first := e.event(node, "cpu", "warn", 3*h, uuid.Nil, `{"value":0.85}`)
	last := e.event(node, "cpu", "critical", 2*h, first, `{"value":0.97}`)
	// A later alert of the same key replaces the first one.
	second := e.event(node, "cpu", "warn", h, uuid.Nil, `{"value":0.85}`)
	other := e.event(node, "mem", "warn", h, uuid.Nil, `{}`)
	unknown := uuid.New()

	got, err := client.AlertsByID(ctx, e.org, []uuid.UUID{second, other, unknown, first})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[second].EventID != second || got[other].Metric != "mem" {
		t.Fatalf("by id = %+v", got)
	}
	// The first alert is replaced, so only its alert id of the old form
	// misses. An event id of the open alert finds it.
	got, err = client.AlertsByID(ctx, e.org, []uuid.UUID{second})
	if err != nil || got[second].AlertID != second {
		t.Fatalf("by alert id = %+v, %v", got, err)
	}
	if got, _ := client.AlertsByID(ctx, uuid.New(), []uuid.UUID{second}); len(got) != 0 {
		t.Fatalf("other org = %+v", got)
	}
	if got, err := client.AlertsByID(ctx, e.org, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids = %+v, %v", got, err)
	}

	// Events: by alert id, and by any event id of the alert.
	for _, id := range []uuid.UUID{first, last} {
		evs, err := client.AlertEvents(ctx, e.org, id)
		if err != nil || len(evs) != 2 || evs[0].EventID != first || evs[1].EventID != last || evs[1].Tint != "critical" || evs[0].AlertID != first {
			t.Fatalf("events of %s = %+v, %v", id, evs, err)
		}
	}
	if _, err := client.AlertEvents(ctx, e.org, unknown); err != ErrNoAlert {
		t.Fatalf("unknown alert: %v", err)
	}
	if _, err := client.AlertEvents(ctx, uuid.New(), first); err != ErrNoAlert {
		t.Fatalf("other org: %v", err)
	}

	by := uuid.New()
	until := e.now.Add(h)
	err = client.WriteAlertLines(ctx, []AlertLine{
		{TS: e.now.Add(-time.Minute), OrgID: e.org, NodeID: node, AlertID: first, Kind: "snoozed", User: &by, Until: &until, Text: "t"},
		{TS: e.now.Add(-2 * time.Minute), OrgID: e.org, NodeID: node, AlertID: first, Kind: "notified", Text: "Slack"},
		{TS: e.now, OrgID: e.org, NodeID: node, AlertID: second, Kind: "acked", User: &by},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines, err := client.AlertLines(ctx, e.org, first)
	if err != nil || len(lines) != 2 || lines[0].Kind != "notified" || lines[1].Kind != "snoozed" || lines[1].Until == nil || !lines[1].Until.Equal(until) || *lines[1].User != by {
		t.Fatalf("lines = %+v, %v", lines, err)
	}
	if lines, _ := client.AlertLines(ctx, e.org, unknown); len(lines) != 0 {
		t.Fatalf("lines of an unknown alert = %+v", lines)
	}
}

func TestNotifiedCounts(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, other, node := uuid.New(), uuid.New(), uuid.New()
	a1, a2 := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	line := func(org, alert uuid.UUID, kind, text string, at time.Time) AlertLine {
		return AlertLine{TS: at, OrgID: org, NodeID: node, AlertID: alert, Kind: kind, Text: text}
	}
	err := client.WriteAlertLines(ctx, []AlertLine{
		line(org, a1, "notified", "Email ops, Slack (failed)", now.Add(-time.Hour)),
		line(org, a1, "notified", "Email ops", now.Add(-30*time.Minute)),
		line(org, a2, "notified", "Slack", now.Add(-20*time.Minute)),
		line(org, a2, "acked", "", now.Add(-10*time.Minute)),
		line(org, a2, "notified", "Slack", now.Add(-48*time.Hour)),
		line(other, a1, "notified", "Email ops", now.Add(-time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.NotifiedCounts(ctx, NotifiedQuery{OrgID: org, From: now.Add(-24 * time.Hour), To: now})
	if err != nil {
		t.Fatal(err)
	}
	by := map[uuid.UUID]NotifiedRow{}
	for _, r := range rows {
		by[r.AlertID] = r
	}
	r1 := by[a1]
	if len(rows) != 2 || r1.Sends != 3 || r1.Failed != 1 || r1.FailedChannels["Slack"] != 1 || len(r1.FailedChannels) != 1 || r1.Channels["Email ops"] != 2 || r1.Channels["Slack"] != 1 || len(r1.Channels) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if r2 := by[a2]; r2.Sends != 1 || r2.Failed != 0 || r2.Channels["Slack"] != 1 {
		t.Fatalf("a2 = %+v", r2)
	}
	none, err := client.NotifiedCounts(ctx, NotifiedQuery{OrgID: org, From: now.Add(-24 * time.Hour), To: now, NodeIDs: []uuid.UUID{uuid.New()}})
	if err != nil || len(none) != 0 {
		t.Fatalf("other node: rows = %+v, err = %v", none, err)
	}
}

func TestRuleSends(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node, otherNode := uuid.New(), uuid.New(), uuid.New()
	cpu, proc, mem, other := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	env := &incidentEnv{t: t, client: client, org: org, now: now}
	env.event(node, "cpu", "warn", 2*time.Hour, cpu, "{}")
	env.event(node, "long_proc/x", "warn", 3*time.Hour, proc, "{}")
	env.event(node, "mem", "warn", 3*time.Hour, mem, "{}")
	env.event(otherNode, "cpu", "warn", 2*time.Hour, other, "{}")
	line := func(n, alert uuid.UUID, text string, ago time.Duration) AlertLine {
		return AlertLine{TS: now.Add(-ago), OrgID: org, NodeID: n, AlertID: alert, Kind: "notified", Text: text}
	}
	err := client.WriteAlertLines(ctx, []AlertLine{
		line(node, cpu, "Email ops, Slack", time.Hour),
		line(node, cpu, "Email ops (failed)", 30*time.Minute),
		line(node, cpu, "Email ops", 40*24*time.Hour),
		line(node, proc, "Email ops", 2*time.Hour),
		line(node, mem, "Email ops", 2*time.Hour),
		line(otherNode, other, "Email ops", time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	count := func(metric string, channels ...string) int {
		t.Helper()
		n, err := client.RuleSends(ctx, RuleSendsQuery{
			OrgID: org, NodeID: node, Metric: metric, Channels: channels, Since: now.Add(-30 * 24 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, tc := range []struct {
		metric   string
		channels []string
		want     int
	}{
		{"cpu", []string{"Email ops"}, 1},
		{"long_proc", []string{"Email ops"}, 1},
		{"mem", []string{"Email ops"}, 1},
		{"cp", []string{"Email ops"}, 0},
		{"disk", []string{"Email ops"}, 0},
		{"cpu", []string{"Email dev"}, 0},
		{"cpu", nil, 0},
	} {
		if got := count(tc.metric, tc.channels...); got != tc.want {
			t.Errorf("RuleSends(%s, %v) = %d, want %d", tc.metric, tc.channels, got, tc.want)
		}
	}
}

func TestHostFacts(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	sample := func(at time.Time, kernel string, uptime uint64) IngestBundle {
		return IngestBundle{Sample: SampleRow{
			TS: at, OrgID: org, NodeID: node, SchemaV: 1, HostOS: "linux", HostArch: "arm64", HostKernel: kernel,
			HostSampler: "1.2.3+abc", HostUptimeS: uptime, CPUCores: 8, MemTotal: 16 << 30, Errors: []string{},
		}}
	}
	if err := client.insert(ctx, []IngestBundle{
		sample(now.Add(-2*time.Hour), "6.1.0", 100),
		sample(now.Add(-time.Minute), "6.8.0", 7200),
	}); err != nil {
		t.Fatal(err)
	}
	f, ok, err := client.HostFacts(ctx, org, node, now.Add(-24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("HostFacts = %+v, %v, %v", f, ok, err)
	}
	if f.Kernel != "6.8.0" || f.OS != "linux" || f.Arch != "arm64" || f.Sampler != "1.2.3+abc" || f.Cores != 8 || f.MemTotal != 16<<30 || f.UptimeS != 7200 {
		t.Fatalf("facts = %+v", f)
	}
	if _, ok, err := client.HostFacts(ctx, org, uuid.New(), now.Add(-24*time.Hour)); err != nil || ok {
		t.Fatalf("unknown node: ok = %v, err = %v", ok, err)
	}
	if _, ok, err := client.HostFacts(ctx, org, node, now); err != nil || ok {
		t.Fatalf("no sample since: ok = %v, err = %v", ok, err)
	}
}
