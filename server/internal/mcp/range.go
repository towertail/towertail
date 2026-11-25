package mcp

import (
	"strconv"
	"strings"
	"time"
)

// Limits of a range.
const (
	minRange     = 15 * time.Minute
	maxRange     = 365 * 24 * time.Hour
	defaultRange = time.Hour
)

// parseSpan reads a Go duration, or a number with "d" for days.
func parseSpan(text string) (time.Duration, error) {
	text = strings.TrimSpace(text)
	if days, ok := strings.CutSuffix(text, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil || n <= 0 || n > 36500 {
			return 0, badf("bad duration %q: use for example 90m, 24h or 7d", text)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 {
		return 0, badf("bad duration %q: use for example 90m, 24h or 7d", text)
	}
	return d, nil
}

// parseRange gives the start and the end of a range. Give either rangeText
// (the last span up to now, 1h by default) or from, with an optional to
// in RFC 3339. An end in the future moves to now.
func parseRange(rangeText, from, to string, now time.Time) (time.Time, time.Time, error) {
	if from == "" && to != "" {
		return time.Time{}, time.Time{}, badf("to needs from")
	}
	if from == "" {
		if rangeText == "" {
			rangeText = defaultRange.String()
		}
		d, err := parseSpan(rangeText)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if d < minRange || d > maxRange {
			return time.Time{}, time.Time{}, badf("range must be 15m to 365d")
		}
		return now.Add(-d), now, nil
	}
	if rangeText != "" {
		return time.Time{}, time.Time{}, badf("use range, or from and to, but not both")
	}
	start, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return time.Time{}, time.Time{}, badf("from must be RFC 3339, for example 2026-11-20T08:00:00Z")
	}
	end := now
	if to != "" {
		if end, err = time.Parse(time.RFC3339, to); err != nil {
			return time.Time{}, time.Time{}, badf("to must be RFC 3339, for example 2026-11-20T09:00:00Z")
		}
	}
	start, end = start.UTC(), end.UTC()
	if end.After(now) {
		end = now
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, badf("from must be before to")
	}
	if end.Sub(start) > maxRange {
		return time.Time{}, time.Time{}, badf("range must be at most 365d")
	}
	return start, end, nil
}

// parseAt reads a point in time: RFC 3339, "now", or "now-" and a span,
// for example now-90m or now-2d. A time in the future moves to now.
func parseAt(text string, now time.Time) (time.Time, error) {
	text = strings.TrimSpace(text)
	if rest, ok := strings.CutPrefix(text, "now"); ok {
		if rest == "" {
			return now, nil
		}
		if span, ok := strings.CutPrefix(rest, "-"); ok {
			d, err := parseSpan(span)
			if err != nil {
				return time.Time{}, err
			}
			return now.Add(-d), nil
		}
	}
	t, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, badf("at must be RFC 3339, now or now-<span>, for example 2026-11-20T08:00:00Z or now-2h")
	}
	if t = t.UTC(); t.After(now) {
		t = now
	}
	return t, nil
}
