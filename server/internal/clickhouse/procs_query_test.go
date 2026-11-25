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

func TestSeriesStep(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		span, age time.Duration
		n         int
		want      time.Duration
	}{
		{time.Hour, 0, 120, 30 * time.Second},
		{time.Minute, 0, 120, 10 * time.Second},
		{time.Hour, 3 * time.Hour, 120, time.Minute},
		{2 * time.Hour, 3 * time.Hour, 10, 12 * time.Minute},
		{2 * day, 0, 120, 24 * time.Minute},
		{30 * day, 0, 120, 6 * time.Hour},
		{time.Hour, 8 * day, 60, time.Hour},
	} {
		if got := SeriesStep(tc.span, tc.age, tc.n); got != tc.want {
			t.Errorf("SeriesStep(%v, %v, %d) = %v, want %v", tc.span, tc.age, tc.n, got, tc.want)
		}
	}
}

func TestFitStep(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		span, age, step, want time.Duration
	}{
		{time.Hour, 0, time.Second, 10 * time.Second},
		{time.Hour, 0, time.Minute, time.Minute},
		{time.Hour, 3 * time.Hour, 90 * time.Second, 2 * time.Minute},
		{day, 8 * day, 5 * time.Minute, time.Hour},
		{2 * day, 0, 90 * time.Minute, 90 * time.Minute},
	} {
		if got := FitStep(tc.span, tc.age, tc.step); got != tc.want {
			t.Errorf("FitStep(%v, %v, %v) = %v, want %v", tc.span, tc.age, tc.step, got, tc.want)
		}
	}
}
