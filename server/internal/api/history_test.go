package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
)

type fixedLimits int

func (d fixedLimits) HistoryDays(context.Context, uuid.UUID) int { return int(d) }
func (d fixedLimits) MaxNodes(context.Context, uuid.UUID) int    { return -1 }

func rangeCtx(t *testing.T, query string) echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/nodes/x/history?"+query, nil)
	c := e.NewContext(req, httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues(uuid.NewString())
	authctx.Set(c, store.Token{OrgID: uuid.New(), Kind: store.TokenKindUser})
	return c
}

func TestParseRangeClampsToPlan(t *testing.T) {
	s := &Server{limits: fixedLimits(30)}
	r, err := s.parseRange(rangeCtx(t, "from=now-2160h"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Clamped {
		t.Fatal("clamped = false, want true")
	}
	if got := time.Since(r.From); got > 30*24*time.Hour+time.Minute || got < 30*24*time.Hour-time.Minute {
		t.Fatalf("from is %v ago, want 30 days", got)
	}
}

func TestParseRangeInsidePlan(t *testing.T) {
	s := &Server{limits: fixedLimits(30)}
	r, err := s.parseRange(rangeCtx(t, "from=now-24h"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r.Clamped {
		t.Fatal("clamped = true, want false")
	}
}

func TestParseRangeNoLimits(t *testing.T) {
	for _, s := range []*Server{{}, {limits: fixedLimits(0)}} {
		r, err := s.parseRange(rangeCtx(t, "from=now-2160h"), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if r.Clamped || time.Since(r.From) < 89*24*time.Hour {
			t.Fatalf("range = %+v, want no clamp", r)
		}
	}
}

// A range that ends before the oldest allowed time becomes empty.
func TestParseRangeToBeforeLimit(t *testing.T) {
	s := &Server{limits: fixedLimits(30)}
	r, err := s.parseRange(rangeCtx(t, "from=now-2160h&to=now-1440h"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Clamped || !r.To.Equal(r.From) {
		t.Fatalf("range = %+v, want an empty clamped range", r)
	}
}
