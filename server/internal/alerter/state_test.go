package alerter

import (
	"testing"
	"time"
)

var t0 = time.Unix(1700000000, 0).UTC()

// fill records one level each step from the start, with n points.
func fill(w *sustainWindow, t Tint, step time.Duration, n int, keep time.Duration) {
	for i := range n {
		w.record(t, t0.Add(time.Duration(i)*step), keep)
	}
}

func TestSustainWindow_NotReadyUntilCovered(t *testing.T) {
	var w sustainWindow
	keep := 5 * time.Minute
	fill(&w, TintCritical, 10*time.Second, 25, keep) // 0..240 s
	if _, ok := w.level(keep, 0.8); ok {
		t.Fatal("4 min of critical is ready for 5 min")
	}
	w.record(TintCritical, t0.Add(300*time.Second), keep)
	if got, ok := w.level(keep, 0.8); !ok || got != TintCritical {
		t.Fatalf("5 min: %v, %v", got, ok)
	}
}

func TestSustainWindow_Tolerance(t *testing.T) {
	var w sustainWindow
	keep := 5 * time.Minute
	// Critical for 255 s of 300 s (85 %), then ok for 45 s.
	w.record(TintCritical, t0, keep)
	w.record(TintOK, t0.Add(255*time.Second), keep)
	w.record(TintOK, t0.Add(300*time.Second), keep)
	if got, ok := w.level(keep, 0.8); !ok || got != TintCritical {
		t.Fatalf("tolerance 0.8: %v, %v", got, ok)
	}
	if got, ok := w.level(keep, 0.9); !ok || got != TintOK {
		t.Fatalf("tolerance 0.9: %v, %v", got, ok)
	}
}

func TestSustainWindow_WarnCountsInWarnLevel(t *testing.T) {
	var w sustainWindow
	keep := time.Minute
	w.record(TintWarn, t0, keep)
	w.record(TintCritical, t0.Add(30*time.Second), keep)
	w.record(TintCritical, t0.Add(60*time.Second), keep)
	if got, _ := w.level(keep, 0.8); got != TintWarn {
		t.Fatalf("half critical, all warn or worse: %v", got)
	}
}

func TestSustainWindow_TimeGoesBack(t *testing.T) {
	var w sustainWindow
	keep := time.Minute
	fill(&w, TintCritical, 10*time.Second, 7, keep)
	w.record(TintOK, t0.Add(5*time.Second), keep)
	if len(w.points) != 1 {
		t.Fatalf("points = %d, want 1", len(w.points))
	}
	if _, ok := w.level(keep, 0.8); ok {
		t.Fatal("a cleared window is ready")
	}
}

func TestSustainWindow_ZeroSustain(t *testing.T) {
	var w sustainWindow
	if _, ok := w.level(0, 0.8); ok {
		t.Fatal("an empty window is ready")
	}
	w.record(TintCritical, t0, 0)
	w.record(TintWarn, t0.Add(time.Second), 0)
	if got, ok := w.level(0, 0.8); !ok || got != TintWarn {
		t.Fatalf("zero sustain: %v, %v", got, ok)
	}
	if len(w.points) != 1 {
		t.Fatalf("points = %d, want 1", len(w.points))
	}
}
