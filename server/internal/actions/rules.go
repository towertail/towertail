package actions

import (
	"context"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

// RestoreMode tells how Restore treats the targets of a backup.
type RestoreMode int

// RestoreAll writes the old rules of the backup to all of its targets.
const RestoreAll RestoreMode = iota

// Replace replaces the org rules and the rules of the named hosts at once,
// then tells the engine and the dashboards. The origin of ch comes from the
// actor. It writes nothing when one part is bad.
func (s *Service) Replace(ctx context.Context, a Actor, ch store.RulesChange) (RulesResult, error) {
	ch.Origin = a.Origin(s.now())
	if err := ch.Validate(); err != nil {
		return RulesResult{}, badInput(err)
	}
	backup, err := s.st.ReplaceRules(ctx, a.OrgID, ch)
	if err != nil {
		return RulesResult{}, err
	}
	res := RulesResult{Backup: backup}
	if ch.Org != nil {
		s.invalidateSettings(a.OrgID)
		if cfg, err := s.st.GetSettings(ctx, a.OrgID); err == nil {
			s.broadcastSettings(a.OrgID, cfg)
			res.Settings = &cfg
		}
	}
	for id := range ch.Hosts {
		if n, err := s.st.GetNode(ctx, a.OrgID, id); err == nil {
			s.broadcastNode(a.OrgID, n)
			res.Nodes = append(res.Nodes, n)
		}
	}
	return res, nil
}

// Restore writes the old rules of a backup. The restore makes a new backup
// of the rules that it replaces. A host that no longer exists is skipped.
func (s *Service) Restore(ctx context.Context, a Actor, backupID uuid.UUID, mode RestoreMode) (RulesResult, error) {
	backup, err := s.st.GetRulesBackup(ctx, a.OrgID, backupID)
	if err != nil {
		return RulesResult{}, err
	}
	nodes, err := s.st.ListNodes(ctx, a.OrgID)
	if err != nil {
		return RulesResult{}, err
	}
	hosts := make(map[uuid.UUID]store.HostRules, len(backup.Hosts))
	for _, n := range nodes {
		if h, ok := backup.Hosts[n.ID]; ok {
			hosts[n.ID] = h
		}
	}
	return s.Replace(ctx, a, store.RulesChange{Org: backup.Org, Hosts: hosts})
}
