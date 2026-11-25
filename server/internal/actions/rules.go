package actions

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

// RestoreMode tells how Restore treats the targets of a backup.
type RestoreMode int

const (
	// RestoreAll writes the old rules of the backup to all of its targets.
	RestoreAll RestoreMode = iota
	// RestoreIfUnchanged writes nothing when a target changed after the
	// backup. It gives a *ConflictError that names the targets.
	RestoreIfUnchanged
)

// ConflictError means that a target of a backup changed after the backup.
type ConflictError struct{ Targets []string }

func (e *ConflictError) Error() string {
	return "rules changed after the backup: " + strings.Join(e.Targets, ", ")
}

// RulesEdit is a change of the rules of the org (when Org is set) and of the
// named hosts. Apply changes the targets in place. It gets a nil settings
// value when Org is not set.
type RulesEdit struct {
	Org   bool
	Hosts []uuid.UUID
	Apply func(s *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error
}

const orgTarget = "org"

// EditRules runs the edit in one store transaction. The store makes one
// backup of the rules that changed. The service then tells the engine and
// the dashboards. Diff lists the fields that changed. Diff is empty and
// Backup.ID is uuid.Nil when no rule changed.
func (s *Service) EditRules(ctx context.Context, a Actor, ed RulesEdit) (RulesResult, error) {
	var newOrg store.OrgRules
	newHosts := map[uuid.UUID]store.HostRules{}
	backup, err := s.st.EditRules(ctx, a.OrgID, store.RulesEdit{
		Org: ed.Org, Hosts: ed.Hosts, Origin: a.Origin(s.now()),
		Apply: func(cfg *store.ServerSettings, nodes map[uuid.UUID]*store.Node) error {
			if err := ed.Apply(cfg, nodes); err != nil {
				return err
			}
			if cfg != nil {
				newOrg = cfg.Rules()
			}
			for id, n := range nodes {
				newHosts[id] = n.Rules()
			}
			return nil
		},
	})
	if err != nil {
		return RulesResult{}, err
	}
	res := RulesResult{Backup: backup}
	if backup.Org != nil {
		res.Diff = tagged(orgTarget, backup.Org.Diff(newOrg))
	}
	for _, id := range dedupeIDs(ed.Hosts) {
		if old, ok := backup.Hosts[id]; ok {
			res.Diff = append(res.Diff, tagged(id.String(), old.Diff(newHosts[id]))...)
		}
	}
	s.publish(ctx, a.OrgID, ed.Org, ed.Hosts, &res)
	return res, nil
}

// Preview runs the edit on copies in memory and gives the Diff. It writes
// nothing, makes no backup and sends no event.
func (s *Service) Preview(ctx context.Context, a Actor, ed RulesEdit) (RulesResult, error) {
	var cfg *store.ServerSettings
	var oldOrg store.OrgRules
	if ed.Org {
		c, err := s.st.GetSettings(ctx, a.OrgID)
		if err != nil {
			return RulesResult{}, err
		}
		oldOrg = c.Rules()
		if cfg, err = deepCopy(&c); err != nil {
			return RulesResult{}, err
		}
	}
	oldHosts := map[uuid.UUID]store.HostRules{}
	nodes := map[uuid.UUID]*store.Node{}
	for _, id := range dedupeIDs(ed.Hosts) {
		n, err := s.st.GetNode(ctx, a.OrgID, id)
		if err != nil {
			return RulesResult{}, err
		}
		oldHosts[id] = n.Rules()
		if nodes[id], err = deepCopy(&n); err != nil {
			return RulesResult{}, err
		}
	}
	if err := ed.Apply(cfg, nodes); err != nil {
		return RulesResult{}, err
	}
	var res RulesResult
	if cfg != nil {
		cur := cfg.Rules()
		if err := cur.Validate(); err != nil {
			return RulesResult{}, err
		}
		res.Diff = tagged(orgTarget, oldOrg.Diff(cur))
	}
	for _, id := range dedupeIDs(ed.Hosts) {
		cur := nodes[id].Rules()
		if err := cur.Validate(); err != nil {
			return RulesResult{}, err
		}
		res.Diff = append(res.Diff, tagged(id.String(), oldHosts[id].Diff(cur))...)
	}
	return res, nil
}

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
	hosts := make([]uuid.UUID, 0, len(ch.Hosts))
	for id := range ch.Hosts {
		hosts = append(hosts, id)
	}
	slices.SortFunc(hosts, func(x, y uuid.UUID) int { return slices.Compare(x[:], y[:]) })
	res := RulesResult{Backup: backup}
	s.publish(ctx, a.OrgID, ch.Org != nil, hosts, &res)
	return res, nil
}

// Restore writes the old rules of a backup. The restore makes a new backup
// of the rules that it replaces. With RestoreAll, a host that no longer
// exists is skipped. With RestoreIfUnchanged, a target that changed after
// the backup, or a host that no longer exists, gives a *ConflictError and
// nothing is written.
func (s *Service) Restore(ctx context.Context, a Actor, backupID uuid.UUID, mode RestoreMode) (RulesResult, error) {
	backup, err := s.st.GetRulesBackup(ctx, a.OrgID, backupID)
	if err != nil {
		return RulesResult{}, err
	}
	nodes, err := s.st.ListNodes(ctx, a.OrgID)
	if err != nil {
		return RulesResult{}, err
	}
	if mode == RestoreIfUnchanged {
		return s.restoreIfUnchanged(ctx, a, backup, nodes)
	}
	hosts := make(map[uuid.UUID]store.HostRules, len(backup.Hosts))
	for _, n := range nodes {
		if h, ok := backup.Hosts[n.ID]; ok {
			hosts[n.ID] = h
		}
	}
	return s.Replace(ctx, a, store.RulesChange{Org: backup.Org, Hosts: hosts})
}

// RestoreEdit is the rules edit that writes the old rules of a backup. The
// edit gives a *ConflictError when a target changed after the backup, also
// when it runs inside the store transaction.
func (s *Service) RestoreEdit(ctx context.Context, a Actor, backupID uuid.UUID) (RulesEdit, error) {
	backup, err := s.st.GetRulesBackup(ctx, a.OrgID, backupID)
	if err != nil {
		return RulesEdit{}, err
	}
	return restoreEdit(backup), nil
}

func restoreEdit(backup store.RulesBackup) RulesEdit {
	hosts := make([]uuid.UUID, 0, len(backup.Hosts))
	for id := range backup.Hosts {
		hosts = append(hosts, id)
	}
	slices.SortFunc(hosts, func(x, y uuid.UUID) int { return slices.Compare(x[:], y[:]) })
	return RulesEdit{
		Org: backup.Org != nil, Hosts: hosts,
		Apply: func(c *store.ServerSettings, locked map[uuid.UUID]*store.Node) error {
			cur := store.ServerSettings{}
			if c != nil {
				cur = *c
			}
			var all []store.Node
			for _, n := range locked {
				all = append(all, *n)
			}
			if changed := backup.ChangedSince(cur, all); len(changed) > 0 {
				return &ConflictError{Targets: changed}
			}
			if c != nil {
				c.SetRules(*backup.Org)
			}
			for id, n := range locked {
				n.SetRules(backup.Hosts[id])
			}
			return nil
		},
	}
}

// restoreIfUnchanged checks the targets first, then again inside the
// transaction, so a change between the check and the write is also a
// conflict.
func (s *Service) restoreIfUnchanged(ctx context.Context, a Actor, backup store.RulesBackup, nodes []store.Node) (RulesResult, error) {
	cfg := store.ServerSettings{}
	if backup.Org != nil {
		var err error
		if cfg, err = s.st.GetSettings(ctx, a.OrgID); err != nil {
			return RulesResult{}, err
		}
	}
	if changed := backup.ChangedSince(cfg, nodes); len(changed) > 0 {
		return RulesResult{}, &ConflictError{Targets: changed}
	}
	return s.EditRules(ctx, a, restoreEdit(backup))
}

// publish tells the engine and the dashboards about the targets, and puts
// the new values in res. A value that it cannot read is left out.
func (s *Service) publish(ctx context.Context, org uuid.UUID, withOrg bool, hosts []uuid.UUID, res *RulesResult) {
	if withOrg {
		s.invalidateSettings(org)
		if cfg, err := s.st.GetSettings(ctx, org); err == nil {
			s.broadcastSettings(org, cfg)
			res.Settings = &cfg
		}
	}
	for _, id := range dedupeIDs(hosts) {
		if n, err := s.st.GetNode(ctx, org, id); err == nil {
			s.broadcastNode(org, n)
			res.Nodes = append(res.Nodes, n)
		}
	}
}

func tagged(target string, ch []store.FieldChange) []store.FieldChange {
	for i := range ch {
		ch[i].Target = target
	}
	return ch
}

// deepCopy copies a value through its JSON form.
func deepCopy[T any](v *T) (*T, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := new(T)
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, err
	}
	return out, nil
}
