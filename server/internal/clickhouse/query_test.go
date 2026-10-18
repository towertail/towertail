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
		sql, err := c.historySQL(tc.tier, "cpu", tc.step)
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
	if _, err := c.historySQL(tier1m, "nope", 0); err != ErrUnknownMetric {
		t.Fatalf("err = %v, want ErrUnknownMetric", err)
	}
}
