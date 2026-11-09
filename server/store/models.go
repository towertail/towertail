package store

import (
	"encoding/json"
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
	Tags             []string          `json:"tags"`
	Enabled          bool              `json:"enabled"`
	IconOnWarn       bool              `json:"iconOnWarn"`
	IconOnCritical   bool              `json:"iconOnCritical"`
	NotifyOnWarn     bool              `json:"notifyOnWarn"`
	NotifyOnCritical bool              `json:"notifyOnCritical"`
	CustomThresholds *MetricThresholds `json:"customThresholds,omitempty"`
	SnoozedUntil     *time.Time        `json:"snoozedUntil,omitempty"`
	Favorite         bool              `json:"favorite"`
	// AllowDiskScan lets a user start a disk scan on the node. It is off by default.
	AllowDiskScan bool `json:"allowDiskScan"`
	// CustomLongProc replaces the org rule for this node.
	CustomLongProc *LongProcRule `json:"customLongProc,omitempty"`
	// IgnoredProcs is added to the ignore lists of the rule and the org.
	IgnoredProcs []string `json:"ignoredProcs,omitempty"`
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

// MetricThresholds mirrors the Swift MetricThresholds struct. Values are
// 0..1 ratios except for raw-percent fields that stay percents on the
// Swift side — keep that contract identical.
type MetricThresholds struct {
	CPUWarn      float64 `json:"cpuWarn"`
	CPUCritical  float64 `json:"cpuCritical"`
	MemWarn      float64 `json:"memWarn"`
	MemCritical  float64 `json:"memCritical"`
	DiskWarn     float64 `json:"diskWarn"`
	DiskCritical float64 `json:"diskCritical"`
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
}

// UnmarshalJSON sets the default long-process rule when the JSON has no
// longProc key and the target has no rule yet.
func (s *ServerSettings) UnmarshalJSON(b []byte) error {
	type alias ServerSettings
	a := alias(*s)
	if a.LongProc.CPUPct == 0 {
		a.LongProc = DefaultLongProcRule()
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
}

type TokenKind string

const (
	TokenKindUser    TokenKind = "user"
	TokenKindSampler TokenKind = "sampler"
	TokenKindAdmin   TokenKind = "admin"
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
