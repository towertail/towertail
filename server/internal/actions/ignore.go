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
	_, errs, res := s.writeIgnores(ctx, a.OrgID, scope, undo, items)
	return res, errs, nil
}

// writeIgnores adds the names to the ignore lists, or removes them, with
// one write for each node (host) or one write for the org (all). It gives
// the log text and the error of each item. A failed write gives the error
// to each item of its node.
func (s *Service) writeIgnores(ctx context.Context, org uuid.UUID, scope IgnoreScope, undo bool, items []IgnoreItem) (text []string, errs []error, res RulesResult) {
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
	text, errs = make([]string, len(items)), make([]error, len(items))
	if scope == ScopeAll {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.Name
			text[i] = it.Name + " ignored on all hosts"
		}
		cfg, err := s.st.EditSettings(ctx, org, func(cfg *store.ServerSettings) error {
			cfg.LongProc.Ignore = edit(cfg.LongProc.Ignore, names)
			return nil
		})
		if err != nil {
			return nil, slices.Repeat([]error{err}, len(items)), res
		}
		s.invalidateSettings(org)
		s.broadcastSettings(org, cfg)
		res.Settings = &cfg
		return text, errs, res
	}
	var nodes []uuid.UUID
	names := map[uuid.UUID][]string{}
	for _, it := range items {
		if _, ok := names[it.NodeID]; !ok {
			nodes = append(nodes, it.NodeID)
		}
		names[it.NodeID] = append(names[it.NodeID], it.Name)
	}
	for _, id := range nodes {
		n, err := s.st.EditNode(ctx, org, id, func(n *store.Node) error {
			n.IgnoredProcs = edit(n.IgnoredProcs, names[id])
			return nil
		})
		for i, it := range items {
			switch {
			case it.NodeID != id:
			case err != nil:
				errs[i] = err
			default:
				text[i] = it.Name + " ignored on " + n.DisplayName
			}
		}
		if err == nil {
			s.broadcastNode(org, n)
			res.Nodes = append(res.Nodes, n)
		}
	}
	return text, errs, res
}
