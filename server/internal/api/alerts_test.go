package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/internal/query"
)

func TestRecentSince(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name        string
		limits      Limits
		query       string
		want        time.Time
		wantClamped bool
		wantCode    int
	}{
		{"clamped", fixedLimits(1), "days=7", now.Add(-day), true, 0},
		{"inside plan", fixedLimits(30), "days=7", now.Add(-7 * day), false, 0},
		{"no limits", nil, "days=7", now.Add(-7 * day), false, 0},
		{"zero days of plan", fixedLimits(0), "days=7", now.Add(-7 * day), false, 0},
		{"default", fixedLimits(30), "", now.Add(-7 * day), false, 0},
		{"days 0", nil, "days=0", time.Time{}, false, http.StatusBadRequest},
		{"days 91", nil, "days=91", time.Time{}, false, http.StatusBadRequest},
		{"days word", nil, "days=x", time.Time{}, false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{query: query.NewService(nil, nil, tc.limits)}
			since, clamped, err := s.recentSince(rangeCtx(t, tc.query), now)
			if tc.wantCode != 0 {
				he, ok := err.(*echo.HTTPError)
				if !ok || he.Code != tc.wantCode {
					t.Fatalf("err = %v, want code %d", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !since.Equal(tc.want) || clamped != tc.wantClamped {
				t.Fatalf("since = %v clamped = %v, want %v %v", since, clamped, tc.want, tc.wantClamped)
			}
		})
	}
}
