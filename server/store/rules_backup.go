package store

import (
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RulesBackupTTL is how long the store keeps the old rules after a replace.
const RulesBackupTTL = 30 * 24 * time.Hour

// RulesChange replaces the rules of the org and of some hosts at once.
// Origin.At is the time of the change. The backup uses it as its time.
type RulesChange struct {
	Org    *OrgRules
	Hosts  map[uuid.UUID]HostRules
	Origin RulesOrigin
}

// RulesBackup holds the rules that a RulesChange replaced.
type RulesBackup struct {
	ID     uuid.UUID               `json:"id"`
	At     time.Time               `json:"at"`
	Origin RulesOrigin             `json:"origin"`
	Org    *OrgRules               `json:"org,omitempty"`
	Hosts  map[uuid.UUID]HostRules `json:"hosts,omitempty"`
}

// Validate checks all rules of the change.
func (ch RulesChange) Validate() error {
	if ch.Org != nil {
		if err := ch.Org.Validate(); err != nil {
			return err
		}
	}
	for _, h := range ch.Hosts {
		if err := h.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (ch RulesChange) at() time.Time {
	if ch.Origin.At.IsZero() {
		return time.Now().UTC()
	}
	return ch.Origin.At.UTC()
}

// apply writes the new rules into s (when the change has org rules) and
// into the nodes. It gives the backup of the old rules. A nil s is left
// alone.
func (ch RulesChange) apply(s *ServerSettings, nodes map[uuid.UUID]*Node) RulesBackup {
	origin := ch.Origin
	origin.At = ch.at()
	b := RulesBackup{ID: uuid.New(), At: origin.At, Origin: origin}
	if ch.Org != nil && s != nil {
		old := s.Rules()
		b.Org = &old
		org := *ch.Org
		org.Origin = &origin
		s.SetRules(org)
	}
	if len(nodes) > 0 {
		b.Hosts = make(map[uuid.UUID]HostRules, len(nodes))
	}
	for id, n := range nodes {
		b.Hosts[id] = n.Rules()
		h := ch.Hosts[id]
		h.Origin = &origin
		n.SetRules(h)
	}
	return b
}

// RulesEdit changes the rules of the org (when Org is set) and of the named
// hosts in one transaction. Origin is the origin that each changed target
// gets. Origin.At is the time of the change. The backup uses it as its time.
// Apply changes the targets in place. It gets a nil settings value when Org
// is not set.
type RulesEdit struct {
	Org    bool
	Hosts  []uuid.UUID
	Origin RulesOrigin
	Apply  func(s *ServerSettings, nodes map[uuid.UUID]*Node) error
}

func (ed RulesEdit) at() time.Time {
	if ed.Origin.At.IsZero() {
		return time.Now().UTC()
	}
	return ed.Origin.At.UTC()
}

// run applies the edit to the loaded targets and validates the new rules.
// A target whose rules changed gets the origin. A target whose rules did not
// change keeps its origin. It gives the backup of the old rules of the
// changed targets, or a zero backup when no rule changed. A nil s is left
// alone.
func (ed RulesEdit) run(s *ServerSettings, nodes map[uuid.UUID]*Node) (RulesBackup, error) {
	var oldOrg OrgRules
	if s != nil {
		oldOrg = s.Rules().clone()
	}
	oldHosts := make(map[uuid.UUID]HostRules, len(nodes))
	for id, n := range nodes {
		oldHosts[id] = n.Rules().clone()
	}
	if err := ed.Apply(s, nodes); err != nil {
		return RulesBackup{}, err
	}
	origin := ed.Origin
	origin.At = ed.at()
	b := RulesBackup{ID: uuid.New(), At: origin.At, Origin: origin}
	if s != nil {
		cur := s.Rules()
		if err := cur.Validate(); err != nil {
			return RulesBackup{}, err
		}
		if cur.Equal(oldOrg) {
			s.RulesOrigin = oldOrg.Origin
		} else {
			o := origin
			s.RulesOrigin = &o
			b.Org = &oldOrg
		}
	}
	for id, n := range nodes {
		cur := n.Rules()
		if err := cur.Validate(); err != nil {
			return RulesBackup{}, err
		}
		if cur.Equal(oldHosts[id]) {
			n.RulesOrigin = oldHosts[id].Origin
			continue
		}
		o := origin
		n.RulesOrigin = &o
		if b.Hosts == nil {
			b.Hosts = map[uuid.UUID]HostRules{}
		}
		b.Hosts[id] = oldHosts[id]
	}
	if b.Org == nil && len(b.Hosts) == 0 {
		return RulesBackup{}, nil
	}
	return b, nil
}

// ChangedSince gives the targets of the backup whose rules changed after
// the backup: the origin time is not the time of the backup, or the host
// does not exist any more. A target is "org", the display name of a host,
// or the id of a host that does not exist any more. A host mute that the
// REST route sets does not change the origin, so this does not see it.
func (b RulesBackup) ChangedSince(s ServerSettings, nodes []Node) []string {
	var out []string
	if b.Org != nil && (s.RulesOrigin == nil || !s.RulesOrigin.At.Equal(b.At)) {
		out = append(out, "org")
	}
	byID := make(map[uuid.UUID]Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	var hosts []string
	for id := range b.Hosts {
		n, ok := byID[id]
		switch {
		case !ok:
			hosts = append(hosts, id.String())
		case n.RulesOrigin == nil || !n.RulesOrigin.At.Equal(b.At):
			hosts = append(hosts, n.DisplayName)
		}
	}
	slices.SortFunc(hosts, strings.Compare)
	return append(out, hosts...)
}
