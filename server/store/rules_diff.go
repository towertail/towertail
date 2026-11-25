package store

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// FieldChange is one rule field that differs. Path is the JSON path of
// the field (for example thresholds.cpuWarn). Before is empty for a field
// that was not set, and After is empty for a field that is not set now.
// Target names the org ("org") or the host (its id). Diff leaves it empty.
type FieldChange struct {
	Target string          `json:"target,omitempty"`
	Path   string          `json:"path"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

// Diff gives the leaf fields that differ from the rules in to, sorted by
// path. The origin does not count.
func (r OrgRules) Diff(to OrgRules) []FieldChange {
	r.Origin, to.Origin = nil, nil
	return diffLeaves(r, to)
}

// Diff gives the leaf fields that differ from the rules in to, sorted by
// path. The origin does not count.
func (r HostRules) Diff(to HostRules) []FieldChange {
	r.Origin, to.Origin = nil, nil
	return diffLeaves(r, to)
}

// Equal tells if both rules have the same values. The origin does not count.
func (r OrgRules) Equal(o OrgRules) bool { return len(r.Diff(o)) == 0 }

// Equal tells if both rules have the same values. The origin does not count.
func (r HostRules) Equal(o HostRules) bool { return len(r.Diff(o)) == 0 }

func diffLeaves(from, to any) []FieldChange {
	a, b := leaves(from), leaves(to)
	paths := make([]string, 0, len(a)+len(b))
	for p := range a {
		paths = append(paths, p)
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	var out []FieldChange
	for _, p := range paths {
		if !bytes.Equal(a[p], b[p]) {
			out = append(out, FieldChange{Path: p, Before: a[p], After: b[p]})
		}
	}
	return out
}

// leaves gives the JSON of each leaf value by path. A list is one leaf.
// A null and an empty list are not leaves, so they equal a missing key.
func leaves(v any) map[string]json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil
	}
	out := map[string]json.RawMessage{}
	walkLeaves(out, "", tree)
	return out
}

func walkLeaves(out map[string]json.RawMessage, path string, v any) {
	switch x := v.(type) {
	case nil:
	case map[string]any:
		for k, c := range x {
			walkLeaves(out, strings.TrimPrefix(path+"."+k, "."), c)
		}
	case []any:
		if len(x) > 0 {
			out[path], _ = json.Marshal(x)
		}
	default:
		out[path], _ = json.Marshal(x)
	}
}

func (r OrgRules) clone() OrgRules {
	r.Muted = slices.Clone(r.Muted)
	r.LongProc.Ignore = slices.Clone(r.LongProc.Ignore)
	if r.Origin != nil {
		o := *r.Origin
		r.Origin = &o
	}
	return r
}

func (r HostRules) clone() HostRules {
	if r.Thresholds != nil {
		t := *r.Thresholds
		for _, m := range allPairMetrics() {
			if p := t.Pair(m); p != nil {
				c := *p
				*t.slot(m) = &c
			}
		}
		r.Thresholds = &t
	}
	if r.Alerts != nil {
		a := *r.Alerts
		for _, p := range []**AlertRule{&a.CPU, &a.Mem, &a.Disk, &a.Health} {
			if *p != nil {
				c := **p
				*p = &c
			}
		}
		if a.Tolerance != nil {
			c := *a.Tolerance
			a.Tolerance = &c
		}
		r.Alerts = &a
	}
	if r.LongProc != nil {
		l := *r.LongProc
		l.Ignore = slices.Clone(l.Ignore)
		r.LongProc = &l
	}
	r.Muted = slices.Clone(r.Muted)
	r.IgnoredProcs = slices.Clone(r.IgnoredProcs)
	if r.Origin != nil {
		o := *r.Origin
		r.Origin = &o
	}
	return r
}
