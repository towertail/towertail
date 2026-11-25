package clickhouse

import (
	"strings"
	"testing"
	"time"
)

func TestHistoryTier(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		span, age time.Duration
		want      tier
	}{
		{time.Hour, 0, tierRaw},
		{6 * time.Hour, 0, tierRaw},
		{7 * time.Hour, 0, tier1m},
		{7 * day, 0, tier1m},
		{8 * day, 0, tier5m},
		{30 * day, 0, tier5m},
		{90 * day, 0, tier1h},
		{time.Hour, 2 * day, tierRaw},
		{time.Hour, 8 * day, tier1m},
		{time.Hour, 40 * day, tier5m},
		{time.Hour, 100 * day, tier1h},
	} {
		if got := historyTier(tc.span, tc.age); got != tc.want {
			t.Errorf("historyTier(%v, %v) = %s, want %s", tc.span, tc.age, got.table, tc.want.table)
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
		sql, err := c.historySQL(tc.tier, familySamples, []string{"cpu"}, tc.step, false)
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
	if _, err := c.historySQL(tier1m, familySamples, []string{"nope"}, 0, false); err != ErrUnknownMetric {
		t.Fatalf("err = %v, want ErrUnknownMetric", err)
	}
}

func TestHistorySQLMultiMetricRaw(t *testing.T) {
	c := &Client{db: "towertail"}
	sql, err := c.historySQL(tierRaw, familySamples, []string{"cpu", "mem"}, 0, false)
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
	sql, err = c.historySQL(tierRaw, familySamples, []string{"cpu", "mem"}, time.Minute, false)
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
		sql, err := c.historySQL(tr, familySamples, []string{"cpu"}, time.Minute, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"node_id IN ?", "SELECT node_id, ", "GROUP BY node_id, t", "ORDER BY node_id, t"} {
			if !strings.Contains(sql, want) {
				t.Errorf("%s: want %q in %s", tr.table, want, sql)
			}
		}
	}
	sql, err := c.historySQL(tierRaw, familySamples, []string{"cpu"}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "IN ?") || strings.Contains(sql, "SELECT node_id") {
		t.Errorf("single node query has a node list: %s", sql)
	}
}

func TestHistorySQLProcsRollups(t *testing.T) {
	c := &Client{db: "towertail"}
	sql, err := c.historySQL(tierRaw, familySamples, []string{"procs"}, time.Minute, false)
	if err != nil || !strings.Contains(sql, "procs_total") {
		t.Fatalf("raw: sql %q, err %v", sql, err)
	}
	for _, tr := range []tier{tier1m, tier5m, tier1h} {
		sql, err := c.historySQL(tr, familyHealth, []string{"procs"}, 0, false)
		if err != nil || !strings.Contains(sql, "towertail.health_"+strings.TrimPrefix(tr.table, "samples_")) || !strings.Contains(sql, "avgMerge(avg_procs)") {
			t.Errorf("%s: sql %q, err %v", tr.table, sql, err)
		}
		// A metric of another family is an error.
		if _, err := c.historySQL(tr, familySamples, []string{"procs"}, 0, false); err == nil {
			t.Errorf("%s: want an error for the wrong family", tr.table)
		}
	}
}

func TestHistorySQLHealthFamily(t *testing.T) {
	c := &Client{db: "towertail"}
	sql, err := c.historySQL(tier5m, familyHealth, []string{"zombies", "pids", "load_1"}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FROM towertail.health_5m", "avgMerge(avg_zombies)", "maxMerge(max_zombies)", "avgMerge(avg_pids)",
		"avgMerge(avg_load_1)", "ts_5m", "node_id IN ?", "GROUP BY node_id, t", "toNullable("} {
		if !strings.Contains(sql, want) {
			t.Errorf("want %q in %s", want, sql)
		}
	}
	if strings.Contains(sql, "samples_5m") {
		t.Errorf("health query reads samples: %s", sql)
	}
	raw, err := c.historySQL(tierRaw, familyHealth, []string{"pids", "psi_io_full"}, 0, false)
	if err != nil || !strings.Contains(raw, "FROM towertail.samples_raw") || !strings.Contains(raw, "pids_used") || !strings.Contains(raw, "psi_io_full") {
		t.Errorf("raw health: %s, %v", raw, err)
	}
}

func TestMetricFamily(t *testing.T) {
	for _, tc := range []struct {
		tier   tier
		metric string
		want   string
		err    error
	}{
		{tier1m, "cpu", familySamples, nil},
		{tier1m, "load", familyHealth, nil},
		{tier1h, "load_1", familyHealth, nil},
		{tier5m, "psi_mem_full", familyHealth, nil},
		{tierRaw, "zombies", familySamples, nil},
		{tier1m, "nope", "", ErrUnknownMetric},
		{tierRaw, "nope", "", ErrUnknownMetric},
	} {
		got, err := metricFamily(tc.tier, tc.metric)
		if got != tc.want || err != tc.err {
			t.Errorf("metricFamily(%s, %s) = %q, %v; want %q, %v", tc.tier.table, tc.metric, got, err, tc.want, tc.err)
		}
	}
	// Every metric that the raw tier has also has a rollup.
	for m := range rawExprs {
		if _, ok := rollupCols[m]; !ok {
			t.Errorf("metric %s has no rollup", m)
		}
	}
}
