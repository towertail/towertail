// Package changepoint finds the points where the level of a series
// shifts.
package changepoint

import (
	"cmp"
	"math"
	"slices"
)

// Detector finds mean shifts with PELT (Pruned Exact Linear Time). The
// cost is the squared error to the mean of a segment. The penalty of one
// change is the BIC value 2·σ²·ln n.
type Detector struct {
	minSegment, maxPoints int
}

// NewDetector makes a detector. A segment has at least minSegment values
// (1 or more). Find gives at most maxPoints change points.
func NewDetector(minSegment, maxPoints int) Detector {
	return Detector{minSegment: max(minSegment, 1), maxPoints: maxPoints}
}

// Point is a change of level. Index is the first value of the new level.
// Before and After are the means of the segments on each side.
type Point struct {
	Index         int
	Before, After float64
}

// Find gives the change points of the values, the largest shift first.
// Fewer than 2*minSegment values give none.
func (d Detector) Find(values []float64) []Point {
	n := len(values)
	if n < 2*d.minSegment || d.maxPoints < 1 {
		return nil
	}
	sums := newPrefix(values)
	penalty := 2 * sigma(values) * sigma(values) * math.Log(float64(n))
	cuts := d.pelt(sums, n, penalty)
	if len(cuts) == 0 {
		return nil
	}
	bounds := append(append([]int{0}, cuts...), n)
	out := make([]Point, 0, len(cuts))
	for i, c := range cuts {
		out = append(out, Point{Index: c, Before: sums.mean(bounds[i], c), After: sums.mean(c, bounds[i+2])})
	}
	slices.SortStableFunc(out, func(a, b Point) int {
		return cmp.Compare(math.Abs(b.After-b.Before), math.Abs(a.After-a.Before))
	})
	return out[:min(len(out), d.maxPoints)]
}

// pelt gives the cut indexes in ascending order. The pruning is the
// standard one. With a minimum segment it can drop a cut that a later
// short segment would need, so the result is exact only for long segments.
func (d Detector) pelt(sums prefix, n int, penalty float64) []int {
	best := make([]float64, n+1)
	last := make([]int, n+1)
	for i := range best {
		best[i] = math.Inf(1)
	}
	best[0] = -penalty
	cands := []int{0}
	for t := d.minSegment; t <= n; t++ {
		for _, s := range cands {
			if t-s < d.minSegment {
				continue
			}
			if c := best[s] + sums.cost(s, t) + penalty; c < best[t] {
				best[t], last[t] = c, s
			}
		}
		if math.IsInf(best[t], 1) {
			continue
		}
		cands = slices.DeleteFunc(cands, func(s int) bool {
			return t-s >= d.minSegment && best[s]+sums.cost(s, t) > best[t]
		})
		cands = append(cands, t)
	}
	if math.IsInf(best[n], 1) {
		return nil
	}
	var cuts []int
	for t := n; t > 0; t = last[t] {
		if last[t] > 0 {
			cuts = append(cuts, last[t])
		}
	}
	slices.Reverse(cuts)
	return cuts
}

// prefix holds the running sums of the values and of their squares.
type prefix struct{ sum, sq []float64 }

func newPrefix(values []float64) prefix {
	p := prefix{sum: make([]float64, len(values)+1), sq: make([]float64, len(values)+1)}
	for i, v := range values {
		p.sum[i+1], p.sq[i+1] = p.sum[i]+v, p.sq[i]+v*v
	}
	return p
}

func (p prefix) mean(a, b int) float64 { return (p.sum[b] - p.sum[a]) / float64(b-a) }

// cost is the squared error of values[a:b] to their mean.
func (p prefix) cost(a, b int) float64 {
	s := p.sum[b] - p.sum[a]
	return max(p.sq[b]-p.sq[a]-s*s/float64(b-a), 0)
}

// sigma estimates the noise from the first differences, so a level shift
// and a slow drift do not inflate it. It uses the MAD, and the root mean
// square when the MAD is 0. A floor keeps a flat series from splitting.
func sigma(values []float64) float64 {
	diffs := make([]float64, len(values)-1)
	var scale, sq float64
	for i := range diffs {
		diffs[i] = values[i+1] - values[i]
		sq += diffs[i] * diffs[i]
	}
	for _, v := range values {
		scale = max(scale, math.Abs(v))
	}
	med := median(diffs)
	devs := make([]float64, len(diffs))
	for i, d := range diffs {
		devs[i] = math.Abs(d - med)
	}
	floor := 1e-6 * max(scale, 1)
	s := 1.4826 * median(devs) / math.Sqrt2
	if s < floor {
		s = math.Sqrt(sq / float64(len(diffs)) / 2)
	}
	return max(s, floor)
}

func median(v []float64) float64 {
	s := slices.Sorted(slices.Values(v))
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
