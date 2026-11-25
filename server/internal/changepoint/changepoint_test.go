package changepoint

import (
	"math"
	"testing"
)

// noisy gives n values around the levels, with a repeatable wobble.
func noisy(n int, level func(i int) float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = level(i) + 0.5*math.Sin(float64(i)*12.9898)*math.Cos(float64(i)*4.1414)
	}
	return out
}

func TestDetectorFind(t *testing.T) {
	d := NewDetector(5, 3)

	t.Run("step", func(t *testing.T) {
		for name, vals := range map[string][]float64{
			"clean": noisy(60, func(i int) float64 {
				if i < 30 {
					return 10
				}
				return 50
			}),
			"exact": append(repeat(10, 30), repeat(50, 30)...),
		} {
			got := d.Find(vals)
			if len(got) != 1 || got[0].Index != 30 {
				t.Fatalf("%s: points = %+v, want one at 30", name, got)
			}
			if math.Abs(got[0].Before-10) > 1 || math.Abs(got[0].After-50) > 1 {
				t.Errorf("%s: before %v after %v", name, got[0].Before, got[0].After)
			}
		}
	})

	t.Run("flat", func(t *testing.T) {
		if got := d.Find(noisy(120, func(int) float64 { return 20 })); len(got) != 0 {
			t.Fatalf("points = %+v, want none", got)
		}
		if got := d.Find(repeat(7, 50)); len(got) != 0 {
			t.Fatalf("constant: points = %+v, want none", got)
		}
	})

	t.Run("short", func(t *testing.T) {
		if got := d.Find(append(repeat(1, 4), repeat(9, 5)...)); got != nil {
			t.Fatalf("points = %+v, want none", got)
		}
	})

	t.Run("largest first and capped", func(t *testing.T) {
		vals := noisy(90, func(i int) float64 {
			switch {
			case i < 30:
				return 10
			case i < 60:
				return 15
			}
			return 60
		})
		got := d.Find(vals)
		if len(got) != 2 || got[0].Index != 60 || got[1].Index != 30 {
			t.Fatalf("points = %+v, want 60 then 30", got)
		}
		if got := NewDetector(5, 1).Find(vals); len(got) != 1 || got[0].Index != 60 {
			t.Fatalf("capped = %+v", got)
		}
		if got := NewDetector(5, 0).Find(vals); got != nil {
			t.Fatalf("zero cap = %+v", got)
		}
	})

}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}
