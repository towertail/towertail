package query

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// ErrBadAlertQuery is returned for an alert query that is not valid. The
// message names the field.
var ErrBadAlertQuery = errors.New("query: bad alert query")

func badAlertQuery(field string) error { return fmt.Errorf("%w: %s", ErrBadAlertQuery, field) }

const (
	// MaxIncidents is the most alerts that one page request reads. The
	// counts and the facets cover only these alerts.
	MaxIncidents = 10000
	// DefaultAlertLimit and MaxAlertLimit are the limits of the page rows.
	DefaultAlertLimit = 500
	MaxAlertLimit     = 1000
	// MaxAlertBins is the most bins of the start strip.
	MaxAlertBins = 60
)

// Values of the alert states.
const (
	alertOpen     = "open"
	alertAcked    = "acked"
	alertSnoozed  = "snoozed"
	alertResolved = "resolved"
)

// The values that an alert query accepts.
var (
	AlertStates = []string{"open", "action", "resolved", "all"}
	AlertTints  = []string{"critical", "warn"}
	AlertKinds  = []string{"cpu", "mem", "disk", "health", "long", "nodata"}
	AlertSorts  = []string{"sev", "title", "host", "kind", "peak", "start", "duration", "state"}
	validFacets = []string{"state", "tint", "kind", "host"}
)

// AlertQuery is a request for the alerts page. State is one of open (not
// resolved), action (open, not acked, not snoozed), resolved and all.
// Tints is the worst tint of the alert. StartedFrom and StartedTo keep the
// alerts that started in [StartedFrom, StartedTo). Now is the time of the
// request. It is zero for the clock of the service.
type AlertQuery struct {
	OrgID                  uuid.UUID
	State                  string
	Tints, Kinds           []string
	Hosts                  []uuid.UUID
	Q                      string
	From, To               time.Time
	StartedFrom, StartedTo *time.Time
	Bins                   int
	Facets                 []string
	Sort                   string
	Desc                   bool
	Limit                  int
	Now                    time.Time
}

// Validate checks the query and sets the default state and sort. It cuts
// a range that is longer than MaxAlertDays: it moves From and reports it.
func (q *AlertQuery) Validate() (clamped bool, err error) {
	if q.State == "" {
		q.State = alertOpen
	}
	if q.Sort == "" {
		q.Sort = "sev"
	}
	for _, c := range []struct {
		field string
		got   []string
		ok    []string
	}{
		{"state", []string{q.State}, AlertStates},
		{"tint", q.Tints, AlertTints},
		{"kind", q.Kinds, AlertKinds},
		{"facets", q.Facets, validFacets},
		{"sort", []string{q.Sort}, AlertSorts},
	} {
		for _, v := range c.got {
			if !slices.Contains(c.ok, v) {
				return false, badAlertQuery(c.field)
			}
		}
	}
	if q.Bins < 0 || q.Bins > MaxAlertBins {
		return false, badAlertQuery("bins")
	}
	if q.Limit < 1 || q.Limit > MaxAlertLimit {
		return false, badAlertQuery("limit")
	}
	if q.From.IsZero() || q.To.IsZero() || !q.From.Before(q.To) {
		return false, badAlertQuery("from")
	}
	if oldest := q.To.Add(-MaxAlertDays * 24 * time.Hour); q.From.Before(oldest) {
		q.From, clamped = oldest, true
	}
	return clamped, nil
}

// alertSet is a list of alerts that the page counts, filters and sorts.
type alertSet []wire.Alert

// kindOf is the kind of the filter menu: cpu, mem, disk, health, long or
// nodata.
func kindOf(a wire.Alert) string {
	switch a.Kind {
	case "long_process":
		return "long"
	case "reachability_changed":
		return "nodata"
	case "threshold_crossed":
		switch m, _, _ := strings.Cut(a.Metric, "/"); m {
		case "cpu", "mem", "disk":
			return m
		}
		return "health"
	}
	if strings.HasPrefix(a.Kind, "health") {
		return "health"
	}
	return a.Kind
}

// closeSuperseded ends an open alert at the start of the next alert of the
// same node and metric. An alert from before alert_id has one alert for
// each event, and the alerts of a process have no change back to ok after
// a restart.
func (s alertSet) closeSuperseded() alertSet {
	out := slices.Clone(s)
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int {
		x, y := out[a], out[b]
		return cmp.Or(
			bytes.Compare(x.NodeID[:], y.NodeID[:]),
			strings.Compare(x.Metric, y.Metric),
			x.Start.Compare(*y.Start),
		)
	})
	for n := 0; n+1 < len(idx); n++ {
		cur, next := &out[idx[n]], &out[idx[n+1]]
		if cur.NodeID != next.NodeID || cur.Metric != next.Metric || cur.End != nil || !next.Start.After(*cur.Start) {
			continue
		}
		end := *next.Start
		cur.End, cur.State = &end, alertResolved
	}
	return out
}

// filter keeps the alerts that match all filters except the one that skip
// names: state, tint, kind, host or bin. The text search always applies.
func (s alertSet) filter(q AlertQuery, skip string) alertSet {
	needle := strings.ToLower(strings.TrimSpace(q.Q))
	out := make(alertSet, 0, len(s))
	for _, a := range s {
		switch {
		case skip != "state" && !stateMatches(q.State, a):
		case skip != "tint" && len(q.Tints) > 0 && !slices.Contains(q.Tints, string(a.Severity)):
		case skip != "kind" && len(q.Kinds) > 0 && !slices.Contains(q.Kinds, kindOf(a)):
		case skip != "host" && len(q.Hosts) > 0 && !slices.Contains(q.Hosts, a.NodeID):
		case skip != "bin" && !startedIn(q, a):
		case needle != "" && !matchesText(a, needle):
		default:
			out = append(out, a)
		}
	}
	return out
}

func stateMatches(tab string, a wire.Alert) bool {
	switch tab {
	case "open":
		return a.State != alertResolved
	case "action":
		return a.State == alertOpen
	case "resolved":
		return a.State == alertResolved
	}
	return true
}

func startedIn(q AlertQuery, a wire.Alert) bool {
	if q.StartedFrom != nil && a.Start.Before(*q.StartedFrom) {
		return false
	}
	return q.StartedTo == nil || a.Start.Before(*q.StartedTo)
}

// matchesText looks for the lowercase needle in the host name, the metric
// and the name and command of the process.
func matchesText(a wire.Alert, needle string) bool {
	fields := []string{a.NodeName, a.Metric}
	if a.Proc != nil {
		fields = append(fields, a.Proc.Name, a.Proc.Cmd)
	}
	return slices.ContainsFunc(fields, func(f string) bool { return strings.Contains(strings.ToLower(f), needle) })
}

// facets counts each requested facet over the alerts that match all other
// filters.
func (s alertSet) facets(q AlertQuery) wire.AlertFacets {
	var out wire.AlertFacets
	for _, f := range q.Facets {
		counts := map[string]int{}
		for _, a := range s.filter(q, f) {
			switch f {
			case "state":
				counts[a.State]++
			case "tint":
				counts[string(a.Severity)]++
			case "kind":
				counts[kindOf(a)]++
			case "host":
				counts[a.NodeID.String()]++
			}
		}
		switch f {
		case "state":
			out.State = counts
		case "tint":
			out.Tint = counts
		case "kind":
			out.Kind = counts
		case "host":
			out.Host = counts
		}
	}
	return out
}

// counts counts the alerts of each state tab, with all other filters on.
func (s alertSet) counts(q AlertQuery) wire.AlertCounts {
	var c wire.AlertCounts
	for _, a := range s.filter(q, "state") {
		c.All++
		switch a.State {
		case alertResolved:
			c.Resolved++
		case alertOpen:
			c.Action++
			c.Open++
		default:
			c.Open++
		}
	}
	return c
}

// bins counts the starts of the alerts in Bins equal bins of the range.
// A start on the edge of two bins goes to the later bin. The bin filter is
// off, so a click on a bar does not change the bars.
func (s alertSet) bins(q AlertQuery) []wire.AlertBin {
	if q.Bins == 0 {
		return nil
	}
	width := q.To.Sub(q.From) / time.Duration(q.Bins)
	out := make([]wire.AlertBin, q.Bins)
	for i := range out {
		out[i].Start = q.From.Add(time.Duration(i) * width)
		out[i].End = q.From.Add(time.Duration(i+1) * width)
	}
	out[q.Bins-1].End = q.To
	for _, a := range s.filter(q, "bin") {
		if a.Start.Before(q.From) || a.Start.After(q.To) {
			continue
		}
		i := min(int(a.Start.Sub(q.From)/width), q.Bins-1)
		if a.Severity == wire.TintCritical {
			out[i].Critical++
		} else {
			out[i].Warn++
		}
	}
	return out
}

// sorted orders the alerts by the key, then by start, the newest first.
func (s alertSet) sorted(q AlertQuery) alertSet {
	out := slices.Clone(s)
	now := q.Now
	key := func(a wire.Alert) (n float64, t string) {
		switch q.Sort {
		case "sev":
			if a.Severity == wire.TintCritical {
				return 2, ""
			}
			return 1, ""
		case "title":
			return 0, strings.ToLower(alertTitle(a))
		case "host":
			return 0, strings.ToLower(a.NodeName)
		case "kind":
			return 0, kindOf(a)
		case "peak":
			if a.Peak != nil {
				return *a.Peak, ""
			}
		case "start":
			return float64(a.Start.UnixNano()), ""
		case "duration":
			end := now
			if a.End != nil {
				end = *a.End
			}
			return float64(end.Sub(*a.Start)), ""
		case "state":
			return float64(slices.Index([]string{alertOpen, alertAcked, alertSnoozed, alertResolved}, a.State)), ""
		}
		return 0, ""
	}
	slices.SortStableFunc(out, func(a, b wire.Alert) int {
		an, at := key(a)
		bn, bt := key(b)
		c := cmp.Or(cmp.Compare(an, bn), strings.Compare(at, bt))
		if q.Desc {
			c = -c
		}
		return cmp.Or(c, b.Start.Compare(*a.Start))
	})
	return out
}

// alertTitle is the name that the sort by title uses: the process name, or
// the metric.
func alertTitle(a wire.Alert) string {
	if a.Proc != nil && a.Proc.Name != "" {
		return a.Proc.Name
	}
	return a.Metric
}
