//go:build integration

package clickhouse

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func aiRow(org uuid.UUID, ts time.Time, tool, status string) AICall {
	return AICall{
		TS: ts, CallID: uuid.New(), OrgID: org, TokenID: uuid.New(), Via: "oauth", Client: "Claude",
		Tool: tool, Input: `{"x":1}`, Status: status, DurationMs: 10, Rows: 2, Bytes: 100,
	}
}

func TestAICallsRoundTrip(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, other, user := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	grant := uuid.New()
	a := aiRow(org, now.Add(-3*time.Minute), "list_hosts", "ok")
	a.UserID, a.GrantID = &user, &grant
	b := aiRow(org, now.Add(-2*time.Minute), "alerts", "error")
	b.Error = "bad input"
	b.Input = strings.Repeat("é", 300)
	c := aiRow(org, now.Add(-time.Minute), "host_metrics", "ok")
	c.Clamped = true
	d := aiRow(org, now.Add(-time.Minute), "fleet_summary", "denied")
	d.Client = "Cursor"
	d.Change = `{"targets":[]}`
	if err := client.WriteAICalls(ctx, []AICall{a, b, c, d, aiRow(other, now, "whoami", "ok")}); err != nil {
		t.Fatal(err)
	}

	q := AICallQuery{OrgID: org, From: now.Add(-time.Hour), To: now.Add(time.Minute), Limit: 2}
	page1, err := client.AICalls(ctx, q)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page 1 = %d rows, %v", len(page1), err)
	}
	// c and d share a timestamp, so the call ID decides their order.
	if page1[0].TS != c.TS || page1[1].TS != c.TS {
		t.Fatalf("page 1 is not the newest: %v %v", page1[0].TS, page1[1].TS)
	}
	last := page1[1]
	q.BeforeTS, q.BeforeID = last.TS, last.CallID
	page2, err := client.AICalls(ctx, q)
	if err != nil || len(page2) != 2 || page2[0].CallID != b.CallID || page2[1].CallID != a.CallID {
		t.Fatalf("page 2 = %+v, %v", page2, err)
	}
	if page2[1].UserID == nil || *page2[1].UserID != user || page2[1].GrantID == nil || *page2[1].GrantID != grant {
		t.Fatalf("user and grant did not round trip: %+v", page2[1])
	}
	if got := []rune(page2[0].Input); len(got) != aiCallListInput {
		t.Fatalf("list input has %d characters, want %d", len(got), aiCallListInput)
	}
	if page2[0].Change != "" || page2[0].Status != "error" || page2[0].Error != "bad input" || page2[0].Via != "oauth" {
		t.Fatalf("row = %+v", page2[0])
	}

	count := func(f func(*AICallQuery)) int {
		t.Helper()
		q := AICallQuery{OrgID: org, From: now.Add(-time.Hour), To: now.Add(time.Minute)}
		f(&q)
		rows, err := client.AICalls(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if n := count(func(q *AICallQuery) {}); n != 4 {
		t.Errorf("all = %d, want 4", n)
	}
	if n := count(func(q *AICallQuery) { q.Filter = AICallErrors }); n != 2 {
		t.Errorf("errors = %d, want 2", n)
	}
	if n := count(func(q *AICallQuery) { q.Filter = AICallClamped }); n != 1 {
		t.Errorf("clamped = %d, want 1", n)
	}
	if n := count(func(q *AICallQuery) { q.Client = "Cursor" }); n != 1 {
		t.Errorf("client = %d, want 1", n)
	}
	if n := count(func(q *AICallQuery) { q.Tool = "alerts" }); n != 1 {
		t.Errorf("tool = %d, want 1", n)
	}
	if n := count(func(q *AICallQuery) { q.UserID = &user }); n != 1 {
		t.Errorf("user = %d, want 1", n)
	}
	if n := count(func(q *AICallQuery) { q.To = now.Add(-90 * time.Second) }); n != 2 {
		t.Errorf("range = %d, want 2", n)
	}

	full, err := client.AICall(ctx, org, d.CallID)
	if err != nil || full.Change != d.Change || full.Status != "denied" {
		t.Fatalf("AICall = %+v, %v", full, err)
	}
	if full, err := client.AICall(ctx, org, b.CallID); err != nil || len([]rune(full.Input)) != 300 {
		t.Fatalf("full input = %d characters, %v", len([]rune(full.Input)), err)
	}
	if _, err := client.AICall(ctx, org, uuid.New()); err != ErrNoAICall {
		t.Fatalf("unknown call: err = %v, want ErrNoAICall", err)
	}
	if _, err := client.AICall(ctx, other, a.CallID); err != ErrNoAICall {
		t.Fatalf("call of other org: err = %v, want ErrNoAICall", err)
	}
}

func TestAICallStats(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, u1, u2 := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	var rows []AICall
	for i := range 20 {
		r := aiRow(org, now.Add(-time.Duration(i)*time.Minute), "list_hosts", "ok")
		r.DurationMs = uint32(i + 1)
		r.UserID = &u1
		if i%2 == 1 {
			r.UserID = &u2
		}
		switch i {
		case 0:
			r.Status, r.Client, r.Tool = "error", "Cursor", "alerts"
		case 1:
			r.Status = "denied"
		case 2:
			r.Clamped = true
		}
		rows = append(rows, r)
	}
	if err := client.WriteAICalls(ctx, rows); err != nil {
		t.Fatal(err)
	}
	q := AICallQuery{OrgID: org, From: now.Add(-time.Hour), To: now.Add(time.Minute)}
	st, err := client.AICallStats(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if st.Calls != 20 || st.People != 2 || st.Errors != 2 || st.Clamped != 1 || st.P95Ms < 19 || st.P95Ms > 20 {
		t.Fatalf("stats = %+v", st)
	}
	if len(st.Clients) != 2 || st.Clients[0] != "Claude" || st.Clients[1] != "Cursor" ||
		len(st.Tools) != 2 || st.Tools[0] != "alerts" || st.Tools[1] != "list_hosts" {
		t.Fatalf("clients = %v, tools = %v", st.Clients, st.Tools)
	}
	// A filter shrinks the totals, not the two lists.
	q.Client = "Cursor"
	st, err = client.AICallStats(ctx, q)
	if err != nil || st.Calls != 1 || len(st.Clients) != 2 {
		t.Fatalf("filtered stats = %+v, %v", st, err)
	}
	// No rows give zeros.
	st, err = client.AICallStats(ctx, AICallQuery{OrgID: uuid.New(), From: now.Add(-time.Hour)})
	if err != nil || st.Calls != 0 || st.P95Ms != 0 {
		t.Fatalf("empty stats = %+v, %v", st, err)
	}
}

func TestAICallUsage(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, user := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	g1, g2, key := uuid.New(), uuid.New(), uuid.New()
	var rows []AICall
	add := func(n int, grant *uuid.UUID, token uuid.UUID, u *uuid.UUID, age time.Duration) {
		for range n {
			r := aiRow(org, now.Add(-age), "whoami", "ok")
			r.GrantID, r.TokenID, r.UserID = grant, token, u
			rows = append(rows, r)
		}
	}
	// Two tokens of one grant count together.
	add(2, &g1, uuid.New(), &user, time.Minute)
	add(1, &g1, uuid.New(), &user, time.Minute)
	add(1, &g2, uuid.New(), nil, 2*time.Minute)
	add(3, nil, key, &user, 3*time.Minute)
	add(5, nil, uuid.New(), nil, 40*24*time.Hour)
	if err := client.WriteAICalls(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := client.AICallUsage(ctx, org, nil, now.Add(-time.Hour))
	if err != nil || len(got) != 3 {
		t.Fatalf("usage = %+v, %v", got, err)
	}
	by := map[uuid.UUID]AICallUsage{}
	for _, u := range got {
		k := u.GrantID
		if k == uuid.Nil {
			k = u.TokenID
		}
		by[k] = u
	}
	if by[g1].Calls != 3 || by[g1].TokenID != uuid.Nil || by[g2].Calls != 1 || by[key].Calls != 3 || by[key].GrantID != uuid.Nil {
		t.Fatalf("usage = %+v", by)
	}
	if !by[g1].LastAt.Equal(now.Add(-time.Minute)) {
		t.Errorf("last at = %v", by[g1].LastAt)
	}
	own, err := client.AICallUsage(ctx, org, &user, now.Add(-time.Hour))
	if err != nil || len(own) != 2 {
		t.Fatalf("own usage = %+v, %v", own, err)
	}
}

func TestAICallsTTL(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	var ddl string
	if err := client.conn.QueryRow(context.Background(), "SHOW CREATE TABLE "+client.table("ai_calls")).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	// ClickHouse prints INTERVAL 30 DAY as toIntervalDay(30).
	if !strings.Contains(ddl, "toIntervalDay(30)") {
		t.Fatalf("ai_calls has no 30 day TTL:\n%s", ddl)
	}
}
