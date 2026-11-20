package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestParseRange(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		rng, from, to string
		wantFrom      time.Time
		wantTo        time.Time
		wantErr       string
	}{
		{name: "default", wantFrom: now.Add(-time.Hour), wantTo: now},
		{name: "1h", rng: "1h", wantFrom: now.Add(-time.Hour), wantTo: now},
		{name: "90m", rng: "90m", wantFrom: now.Add(-90 * time.Minute), wantTo: now},
		{name: "7d", rng: "7d", wantFrom: now.Add(-7 * 24 * time.Hour), wantTo: now},
		{name: "365d", rng: "365d", wantFrom: now.Add(-365 * 24 * time.Hour), wantTo: now},
		{
			name: "from and to", from: "2026-11-20T08:00:00Z", to: "2026-11-20T09:30:00+00:00",
			wantFrom: now.Add(-4 * time.Hour), wantTo: now.Add(-150 * time.Minute),
		},
		{name: "from only", from: "2026-11-20T08:00:00Z", wantFrom: now.Add(-4 * time.Hour), wantTo: now},
		{
			name: "to in the future", from: "2026-11-20T08:00:00Z", to: "2026-11-21T08:00:00Z",
			wantFrom: now.Add(-4 * time.Hour), wantTo: now,
		},
		{name: "400d", rng: "400d", wantErr: "range must be 15m to 365d"},
		{name: "5m", rng: "5m", wantErr: "range must be 15m to 365d"},
		{name: "word", rng: "soon", wantErr: "bad duration"},
		{name: "negative", rng: "-1h", wantErr: "bad duration"},
		{name: "from after to", from: "2026-11-20T09:00:00Z", to: "2026-11-20T08:00:00Z", wantErr: "from must be before to"},
		{name: "from in the future", from: "2026-11-21T08:00:00Z", wantErr: "from must be before to"},
		{name: "span over a year", from: "2024-01-01T00:00:00Z", wantErr: "at most 365d"},
		{name: "bad from", from: "yesterday", wantErr: "from must be RFC 3339"},
		{name: "bad to", from: "2026-11-20T08:00:00Z", to: "today", wantErr: "to must be RFC 3339"},
		{name: "to without from", to: "2026-11-20T08:00:00Z", wantErr: "to needs from"},
		{name: "range and from", rng: "1h", from: "2026-11-20T08:00:00Z", wantErr: "not both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to, err := parseRange(tc.rng, tc.from, tc.to, now)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !from.Equal(tc.wantFrom) || !to.Equal(tc.wantTo) {
				t.Fatalf("range = %v to %v, want %v to %v", from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}
