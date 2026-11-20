package clickhouse

import (
	"strings"
	"testing"
	"time"
)

func TestHistoryTier(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		span time.Duration
		want tier
	}{
		{time.Hour, tierRaw},
		{6 * time.Hour, tierRaw},
		{7 * time.Hour, tier1m},
		{7 * day, tier1m},
		{8 * day, tier5m},
		{30 * day, tier5m},
		{90 * day, tier1h},
	} {
		if got := historyTier(tc.span); got != tc.want {
			t.Errorf("historyTier(%v) = %s, want %s", tc.span, got.table, tc.want.table)
		}
	}
}

func TestHistorySQLStep(t *testing.T) {
	c := &Client{db: "towertail"}
	for _, tc := range []struct {
		tier     tier
		step     time.Duration
		grouped  bool
		interval string
	}{
		{tierRaw, 0, false, ""},
		{tierRaw, time.Minute, true, "INTERVAL 60 SECOND"},
		{tier1m, time.Minute, true, ""},
		{tier1m, 15 * time.Minute, true, "INTERVAL 900 SECOND"},
		{tier1h, time.Minute, true, ""},
	} {
		sql, err := c.historySQL(tc.tier, []string{"cpu"}, tc.step, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sql, "GROUP BY") != tc.grouped {
			t.Errorf("%s step %v: %s", tc.tier.table, tc.step, sql)
		}
		if tc.interval != "" && !strings.Contains(sql, tc.interval) {
			t.Errorf("%s step %v: want %s in %s", tc.tier.table, tc.step, tc.interval, sql)
		}
		if tc.interval == "" && strings.Contains(sql, "toStartOfInterval") {
			t.Errorf("%s step %v: want no regroup in %s", tc.tier.table, tc.step, sql)
		}
	}
	if _, err := c.historySQL(tier1m, []string{"nope"}, 0, false); err != ErrUnknownMetric {
		t.Fatalf("err = %v, want ErrUnknownMetric", err)
	}
}

func TestHistorySQLMultiMetricRaw(t *testing.T) {
	c := &Client{db: "towertail"}
	sql, err := c.historySQL(tierRaw, []string{"cpu", "mem"}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AS v0", "AS m0", "AS v1", "AS m1", "cpu_pct", "mem_used"} {
		if !strings.Contains(sql, want) {
			t.Errorf("want %q in %s", want, sql)
		}
	}
	if strings.Count(sql, "SELECT") != 1 || strings.Contains(sql, "GROUP BY") {
		t.Errorf("want one SELECT and no GROUP BY: %s", sql)
	}
	sql, err = c.historySQL(tierRaw, []string{"cpu", "mem"}, time.Minute, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "GROUP BY t") || !strings.Contains(sql, "avg(") {
		t.Errorf("want a grouped query: %s", sql)
	}
}

func TestHistorySQLMultiNode(t *testing.T) {
	c := &Client{db: "towertail"}
	for _, tr := range []tier{tierRaw, tier1m} {
		sql, err := c.historySQL(tr, []string{"cpu"}, time.Minute, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"node_id IN ?", "SELECT node_id, ", "GROUP BY node_id, t", "ORDER BY node_id, t"} {
			if !strings.Contains(sql, want) {
				t.Errorf("%s: want %q in %s", tr.table, want, sql)
			}
		}
	}
	sql, err := c.historySQL(tierRaw, []string{"cpu"}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "IN ?") || strings.Contains(sql, "SELECT node_id") {
		t.Errorf("single node query has a node list: %s", sql)
	}
}

func TestHistorySQLProcsRawOnly(t *testing.T) {
	c := &Client{db: "towertail"}
	sql, err := c.historySQL(tierRaw, []string{"procs"}, time.Minute, false)
	if err != nil || !strings.Contains(sql, "procs_total") {
		t.Fatalf("raw: sql %q, err %v", sql, err)
	}
	for _, tr := range []tier{tier1m, tier5m, tier1h} {
		if _, err := c.historySQL(tr, []string{"cpu", "procs"}, 0, false); err != ErrNoRollup {
			t.Errorf("%s: err = %v, want ErrNoRollup", tr.table, err)
		}
	}
}
