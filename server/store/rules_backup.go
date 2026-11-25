package store

import (
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
