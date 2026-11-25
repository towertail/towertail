package actions

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

// IgnoreScope tells where an ignored process name applies: one host, or
// all hosts of the org.
type IgnoreScope string

const (
	ScopeHost IgnoreScope = "host"
	ScopeAll  IgnoreScope = "all"
)

var errScope = errors.New("scope must be host or all")

// Valid tells if the scope is host or all.
func (s IgnoreScope) Valid() bool { return s == ScopeHost || s == ScopeAll }

// IgnoreItem is a process name to add to the ignore list of a node.
type IgnoreItem struct {
	NodeID uuid.UUID
	Name   string
}

// IgnoreProcesses adds the names to the ignore lists, or removes them. The
// []error gives one error for each item. The error is for a bad scope.
func (s *Service) IgnoreProcesses(ctx context.Context, a Actor, items []IgnoreItem, scope IgnoreScope, undo bool) (RulesResult, []error, error) {
	if !scope.Valid() {
		return RulesResult{}, nil, badInput(errScope)
	}
	_, errs, res := s.writeIgnores(ctx, a, scope, undo, items)
	return res, errs, nil
}

// IgnoreEdit is the rules edit that adds the names of the items to the
// ignore lists, or removes them. With ScopeAll it edits the org list. With
// ScopeHost it edits the list of each host of the items.
func IgnoreEdit(scope IgnoreScope, items []IgnoreItem, undo bool) RulesEdit {
	edit := func(l []string, names []string) []string {
		l = slices.DeleteFunc(slices.Clone(l), func(x string) bool {
			return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(x, n) })
		})
		if undo {
			return l
		}
		for _, n := range names {
			if !slices.ContainsFunc(l, func(x string) bool { return strings.EqualFold(x, n) }) {
				l = append(l, n)
			}
		}
		return l
	}
	if scope == ScopeAll {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.Name
		}
		return RulesEdit{
			Org: true,
			Apply: func(cfg *store.ServerSettings, _ map[uuid.UUID]*store.Node) error {
				cfg.LongProc.Ignore = edit(cfg.LongProc.Ignore, names)
				return nil
			},
		}
	}
	var hosts []uuid.UUID
	names := map[uuid.UUID][]string{}
	for _, it := range items {
		if _, ok := names[it.NodeID]; !ok {
			hosts = append(hosts, it.NodeID)
		}
		names[it.NodeID] = append(names[it.NodeID], it.Name)
	}
	return RulesEdit{
		Hosts: hosts,
		Apply: func(_ *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error {
			for id, n := range nodes {
				n.IgnoredProcs = edit(n.IgnoredProcs, names[id])
			}
			return nil
		},
	}
}

// writeIgnores adds the names to the ignore lists, or removes them, with
// one rules edit for all items: one write, and one backup. It gives the log
// text and the error of each item. An item with an unknown host gets
// ErrNotFound and the others go on. A failed edit gives its error to each
// item that it holds.
func (s *Service) writeIgnores(ctx context.Context, a Actor, scope IgnoreScope, undo bool, items []IgnoreItem) (text []string, errs []error, res RulesResult) {
	text, errs = make([]string, len(items)), make([]error, len(items))
	var valid []IgnoreItem
	if scope == ScopeAll {
		valid = items
		for i, it := range items {
			text[i] = it.Name + " ignored on all hosts"
		}
	} else {
		known := map[uuid.UUID]bool{}
		for i, it := range items {
			if !known[it.NodeID] {
				if _, err := s.st.GetNode(ctx, a.OrgID, it.NodeID); err != nil {
					errs[i] = err
					continue
				}
				known[it.NodeID] = true
			}
			valid = append(valid, it)
		}
		if len(valid) == 0 {
			return text, errs, res
		}
	}
	res, err := s.EditRules(ctx, a, IgnoreEdit(scope, valid, undo))
	if err != nil {
		for i := range items {
			if errs[i] == nil {
				errs[i] = err
			}
		}
		if scope == ScopeAll {
			return nil, errs, RulesResult{}
		}
		return text, errs, RulesResult{}
	}
	if scope != ScopeAll {
		for i, it := range items {
			if errs[i] != nil {
				continue
			}
			for _, n := range res.Nodes {
				if n.ID == it.NodeID {
					text[i] = it.Name + " ignored on " + n.DisplayName
				}
			}
		}
	}
	return text, errs, res
}
