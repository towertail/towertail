package clickhouse

import (
	"testing"
	"time"
)

func TestProcsSource(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		span, age time.Duration
		want      string
	}{
		{time.Minute, 10 * time.Minute, "procs_topn"},
		{time.Minute, 2 * time.Hour, "procs_top_1m"},
		{time.Minute, 8 * day, "procs_top_1h"},
		{2 * day, 0, "procs_top_1m"},
		{8 * day, 0, "procs_top_1h"},
	} {
		if got, _ := procsSource(tc.span, tc.age); got != tc.want {
			t.Errorf("procsSource(%v, %v) = %s, want %s", tc.span, tc.age, got, tc.want)
		}
	}
}
