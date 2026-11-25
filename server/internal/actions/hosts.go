package actions

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

// SnoozeHost sets the snooze of a host. A nil until clears it.
func (s *Service) SnoozeHost(ctx context.Context, a Actor, nodeID uuid.UUID, until *time.Time) (store.Node, error) {
	if until != nil {
		u, err := s.future(until)
		if err != nil {
			return store.Node{}, err
		}
		until = u
	}
	n, err := s.st.EditNode(ctx, a.OrgID, nodeID, func(n *store.Node) error {
		n.SnoozedUntil = until
		return nil
	})
	if err != nil {
		return store.Node{}, err
	}
	s.broadcastNode(a.OrgID, n)
	return n, nil
}

// MuteResult holds the changed host (a host mute) or the changed settings
// (an org mute).
type MuteResult struct {
	Node     *store.Node
	Settings *store.ServerSettings
	Rules    *RulesResult
}

// SetMute sets or clears the mute of one metric on a host, or on the org
// when nodeID is nil. The engine closes a muted open alert. A host mute
// makes no backup. An org mute makes one when the mute changes.
func (s *Service) SetMute(ctx context.Context, a Actor, metric store.ThresholdMetric, nodeID *uuid.UUID, muted bool) (MuteResult, error) {
	if !metric.Valid() {
		return MuteResult{}, ErrBadMetric
	}
	if nodeID != nil {
		n, err := s.st.EditNode(ctx, a.OrgID, *nodeID, func(n *store.Node) error {
			n.MutedSignals = n.MutedSignals.Set(metric, muted)
			return nil
		})
		if err != nil {
			return MuteResult{}, err
		}
		s.broadcastNode(a.OrgID, n)
		return MuteResult{Node: &n}, nil
	}
	res, err := s.EditRules(ctx, a, RulesEdit{
		Org: true,
		Apply: func(cfg *store.ServerSettings, _ map[uuid.UUID]*store.Node) error {
			cfg.Muted = cfg.Muted.Set(metric, muted)
			return nil
		},
	})
	if err != nil {
		return MuteResult{}, err
	}
	return MuteResult{Settings: res.Settings, Rules: &res}, nil
}

// ReplaceHostRules replaces the rule fields of one host and makes a backup.
// A field that h leaves out clears that override.
func (s *Service) ReplaceHostRules(ctx context.Context, a Actor, nodeID uuid.UUID, h store.HostRules) (store.Node, RulesResult, error) {
	res, err := s.Replace(ctx, a, store.RulesChange{Hosts: map[uuid.UUID]store.HostRules{nodeID: h}})
	if err != nil {
		return store.Node{}, RulesResult{}, err
	}
	if len(res.Nodes) == 0 {
		return store.Node{}, RulesResult{}, store.ErrNotFound
	}
	return res.Nodes[0], res, nil
}
