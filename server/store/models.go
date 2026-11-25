package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Node mirrors the Swift-side Node struct in apps/mac/Sources/State/Node.swift.
// Keys are camelCase to match the existing on-disk and wire shape.
type Node struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"displayName"`
	Kind        string    `json:"kind"` // "local" | "ssh"
	SSHUser     *string   `json:"sshUser,omitempty"`
	SSHHost     *string   `json:"sshHost,omitempty"`
	// Tags is serialized via MarshalJSON so nil → `[]` (not `null`).
	// Swift's RemoteNode decodes `tags` as non-optional `[String]` and a
	// bare `null` breaks the whole node list decode.
	Tags             []string `json:"tags"`
	Enabled          bool     `json:"enabled"`
	IconOnWarn       bool     `json:"iconOnWarn"`
	IconOnCritical   bool     `json:"iconOnCritical"`
	NotifyOnWarn     bool     `json:"notifyOnWarn"`
	NotifyOnCritical bool     `json:"notifyOnCritical"`
	// ThresholdOverrides replaces the org pair of each metric that it sets.
	ThresholdOverrides *ThresholdOverrides `json:"thresholdOverrides,omitempty"`
	// AlertOverrides replaces the org timing of each rule that it sets.
	AlertOverrides *AlertOverrides `json:"alertOverrides,omitempty"`
	SnoozedUntil   *time.Time      `json:"snoozedUntil,omitempty"`
	Favorite       bool            `json:"favorite"`
	// AllowDiskScan lets a user start a disk scan on the node. It is off by default.
	AllowDiskScan bool `json:"allowDiskScan"`
	// HideProcFolders tells push samplers not to send process folders.
	HideProcFolders bool `json:"hideProcFolders"`
	// CustomLongProc replaces the org rule for this node.
	CustomLongProc *LongProcRule `json:"customLongProc,omitempty"`
	// IgnoredProcs is added to the ignore lists of the rule and the org.
	IgnoredProcs []string `json:"ignoredProcs,omitempty"`
	// MutedSignals are the metrics that never alert on this node.
	MutedSignals MutedSignals `json:"mutedSignals"`
	// RulesOrigin tells which client set the rules of the node last.
	RulesOrigin *RulesOrigin `json:"rulesOrigin,omitempty"`
	// CreatedAt is set by the store on the first PutNode.
	CreatedAt time.Time `json:"createdAt"`
	// MachineID is the machine id of the host that enrolled for the node.
	MachineID string `json:"machineId,omitempty"`
}

// MarshalJSON ensures Tags is always an array on the wire — never
// `null` — so Swift clients with a non-optional `tags: [String]` field
// decode cleanly.
func (n Node) MarshalJSON() ([]byte, error) {
	type alias Node
	a := alias(n)
	if a.Tags == nil {
		a.Tags = []string{}
	}
	return json.Marshal(a)
}

// UnmarshalJSON reads the old customThresholds key as overrides of cpu,
// mem and disk, unless the JSON has thresholdOverrides. The server never
// writes customThresholds again.
func (n *Node) UnmarshalJSON(b []byte) error {
	type alias Node
	a := alias(*n)
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	var old struct {
		Custom *MetricThresholds `json:"customThresholds"`
	}
	if err := json.Unmarshal(b, &old); err != nil {
		return err
	}
	if old.Custom != nil && a.ThresholdOverrides == nil {
		o := LegacyOverrides(*old.Custom)
		a.ThresholdOverrides = &o
	}
	*n = Node(a)
	return nil
}

// MutedSignals is a sorted list of muted metrics with no duplicates.
type MutedSignals []ThresholdMetric

// Has reports whether x is muted.
func (m MutedSignals) Has(x ThresholdMetric) bool { return slices.Contains(m, x) }

// Set gives the list with x muted or not. The receiver does not change.
func (m MutedSignals) Set(x ThresholdMetric, muted bool) MutedSignals {
	out := slices.DeleteFunc(slices.Clone(m), func(y ThresholdMetric) bool { return y == x })
	if muted {
		out = append(out, x)
	}
	return normalizeMuted(out)
}

// Union gives the sorted union of both lists.
func (m MutedSignals) Union(o MutedSignals) MutedSignals {
	return normalizeMuted(append(slices.Clone(m), o...))
}

func normalizeMuted(m MutedSignals) MutedSignals {
	m = slices.DeleteFunc(m, func(x ThresholdMetric) bool { return !x.Valid() })
	slices.Sort(m)
	return slices.Compact(m)
}

// MarshalJSON writes [] for a nil list.
func (m MutedSignals) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]ThresholdMetric(m))
}

// UnmarshalJSON skips unknown names and sorts the rest. A null leaves the
// list as it is.
func (m *MutedSignals) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return err
	}
	out := make(MutedSignals, 0, len(names))
	for _, n := range names {
		out = append(out, ThresholdMetric(n))
	}
	if out = normalizeMuted(out); len(out) == 0 {
		out = nil
	}
	*m = out
	return nil
}

// ServerSettings mirrors Swift ServerSettings. Units: ratios for
// thresholds, seconds for intervals/debounce/grace.
type ServerSettings struct {
	Thresholds                  MetricThresholds `json:"thresholds"`
	LocalPollingIntervalSeconds int              `json:"localPollingIntervalSeconds"`
	SSHPollingIntervalSeconds   int              `json:"sshPollingIntervalSeconds"`
	NotificationsEnabled        bool             `json:"notificationsEnabled"`
	NotifyWarn                  bool             `json:"notifyWarn"`
	NotifyCritical              bool             `json:"notifyCritical"`
	NotifyDebounceSeconds       int              `json:"notifyDebounceSeconds"`
	// NotifyCooldownSeconds is the minimum time between two alerts with
	// the same node, metric and tint. 0 means the default (10 min).
	NotifyCooldownSeconds     int          `json:"notifyCooldownSeconds"`
	AutoUpdateSamplersEnabled bool         `json:"autoUpdateSamplersEnabled"`
	PostWakeGraceSeconds      int          `json:"postWakeGraceSeconds"`
	LongProc                  LongProcRule `json:"longProc"`
	// Muted are the metrics that never alert on any node of the org.
	Muted MutedSignals `json:"muted"`
	// Alerts holds the sustain time and the notify level of each metric.
	Alerts AlertRules `json:"alerts"`
	// RulesOrigin tells which client set the org rules last.
	RulesOrigin *RulesOrigin `json:"rulesOrigin,omitempty"`
}

// MutedSet gives the metrics that are muted on the node: the union of the
// org list and the node list. The node can be nil.
func (s ServerSettings) MutedSet(n *Node) MutedSignals {
	if n == nil {
		return s.Muted.Union(nil)
	}
	return s.Muted.Union(n.MutedSignals)
}

// UnmarshalJSON sets the default long-process rule and the default alert
// rules when the target has none yet. A key that the JSON has not
// changes only the keys of the JSON.
func (s *ServerSettings) UnmarshalJSON(b []byte) error {
	type alias ServerSettings
	a := alias(*s)
	if a.LongProc.CPUPct == 0 {
		a.LongProc = DefaultLongProcRule()
	}
	if a.Alerts == (AlertRules{}) {
		a.Alerts = DefaultAlertRules()
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*s = ServerSettings(a)
	return nil
}

// LongProcRule finds a process that stays over a CPU limit for a long
// time. It mirrors the Swift LongProcRule.
type LongProcRule struct {
	Enabled        bool    `json:"enabled"`
	CPUPct         float64 `json:"cpuPct"`
	SustainSeconds int     `json:"sustainSeconds"`
	// Notify is "off" or "all".
	Notify string `json:"notify"`
	// Ignore holds process names, matched exactly without case.
	Ignore []string `json:"ignore"`
}

// LongProcTolerance is the fraction of the window that must be high.
const LongProcTolerance = 0.8

const (
	longProcCPUMin, longProcCPUMax         = 10.0, 800.0
	longProcSustainMin, longProcSustainMax = 60, 86_400
)

func DefaultLongProcRule() LongProcRule {
	return LongProcRule{Enabled: true, CPUPct: 50, SustainSeconds: 1800, Notify: "all", Ignore: []string{}}
}

// Clamped gives the rule with the values in their ranges.
func (r LongProcRule) Clamped() LongProcRule {
	r.CPUPct = min(max(r.CPUPct, longProcCPUMin), longProcCPUMax)
	r.SustainSeconds = min(max(r.SustainSeconds, longProcSustainMin), longProcSustainMax)
	if r.Notify != "off" {
		r.Notify = "all"
	}
	if r.Ignore == nil {
		r.Ignore = []string{}
	}
	return r
}

func (r LongProcRule) Notifies() bool { return r.Notify != "off" }

// IgnoreSet gives the lowercase union of the rule list and the other lists.
func (r LongProcRule) IgnoreSet(other ...[]string) map[string]bool {
	out := map[string]bool{}
	for _, l := range append([][]string{r.Ignore}, other...) {
		for _, n := range l {
			out[strings.ToLower(n)] = true
		}
	}
	return out
}

// UnmarshalJSON decodes each field on its own. A missing or bad field
// gets the default, so an old or partial rule stays usable.
func (r *LongProcRule) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := DefaultLongProcRule()
	field := func(key string, v any) {
		if m, ok := raw[key]; ok {
			_ = json.Unmarshal(m, v)
		}
	}
	field("enabled", &out.Enabled)
	field("cpuPct", &out.CPUPct)
	field("sustainSeconds", &out.SustainSeconds)
	field("notify", &out.Notify)
	field("ignore", &out.Ignore)
	*r = out.Clamped()
	return nil
}

// DefaultServerSettings returns the factory defaults documented in
// docs/PLAN.md §1.
func DefaultServerSettings() ServerSettings {
	return ServerSettings{
		Thresholds: MetricThresholds{
			CPUWarn: 0.75, CPUCritical: 0.90,
			MemWarn: 0.75, MemCritical: 0.90,
			DiskWarn: 0.85, DiskCritical: 0.95,
			ProcsWarn: DefaultProcsWarn, ProcsCritical: DefaultProcsCritical,
			ZombiesWarn: DefaultZombiesWarn, ZombiesCritical: DefaultZombiesCritical,
			Health: DefaultHealthLimits(),
		},
		LocalPollingIntervalSeconds: 5,
		SSHPollingIntervalSeconds:   30,
		NotificationsEnabled:        true,
		NotifyWarn:                  true,
		NotifyCritical:              true,
		NotifyDebounceSeconds:       60,
		NotifyCooldownSeconds:       600,
		AutoUpdateSamplersEnabled:   true,
		PostWakeGraceSeconds:        30,
		LongProc:                    DefaultLongProcRule(),
		Alerts:                      DefaultAlertRules(),
	}
}

// Token represents an issued auth token. Raw tokens are never stored;
// only the SHA-256 hash lives in the database.
type Token struct {
	ID     uuid.UUID  `json:"id"`
	OrgID  uuid.UUID  `json:"orgId"`
	Kind   TokenKind  `json:"kind"`
	Label  string     `json:"label"`
	Hash   []byte     `json:"hash"`
	NodeID *uuid.UUID `json:"nodeId,omitempty"`
	// UserID is the user that the token acts for, when it is known.
	UserID     *uuid.UUID `json:"userId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	// ExpiresAt is set on join tokens. Verify rejects the token after it.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type TokenKind string

const (
	TokenKindUser    TokenKind = "user"
	TokenKindSampler TokenKind = "sampler"
	TokenKindAdmin   TokenKind = "admin"
	// TokenKindAPI is a read-only key of a user. It reads the read routes.
	TokenKindAPI  TokenKind = "api"
	TokenKindJoin TokenKind = "join"
)

// User logs in with email and password. In the cloud, PasswordHash is
// empty, because users log in with a magic link or OAuth. Self-hosted
// users are in ZeroOrg.
type User struct {
	ID           uuid.UUID `json:"id"`
	OrgID        uuid.UUID `json:"orgId"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ControlMessage is an out-of-band command for a specific node's sampler.
// Samplers long-poll /v1/control/next to pick these up.
type ControlMessage struct {
	ID        uuid.UUID  `json:"id"`
	NodeID    uuid.UUID  `json:"nodeId"`
	Kind      string     `json:"kind"` // "kill_process" | "disk_scan"
	Payload   []byte     `json:"payload"`
	CreatedAt time.Time  `json:"createdAt"`
	ClaimedAt *time.Time `json:"claimedAt,omitempty"`
}

// ZeroOrg is the synthetic org id used in self-hosted mode where
// multi-tenancy is not active.
var ZeroOrg = uuid.UUID{}

// Role is the rank of a user in an org.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

func (r Role) rank() int {
	switch r {
	case RoleOwner:
		return 4
	case RoleAdmin:
		return 3
	case RoleMember:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// Valid reports whether r is one of the four roles.
func (r Role) Valid() bool { return r.rank() > 0 }

// Allows reports whether r has the rank of need or a higher rank. An
// invalid role allows nothing.
func (r Role) Allows(need Role) bool { return r.Valid() && r.rank() >= need.rank() }

// ParseRole converts a role name.
func ParseRole(s string) (Role, error) {
	if r := Role(s); r.Valid() {
		return r, nil
	}
	return "", fmt.Errorf("unknown role %q", s)
}
