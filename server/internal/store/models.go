package store

import (
	"time"

	"github.com/google/uuid"
)

// Node mirrors the Swift-side Node struct in app/mac/Sources/State/Node.swift.
// Keys are camelCase to match the existing on-disk and wire shape.
type Node struct {
	ID                uuid.UUID         `json:"id"`
	DisplayName       string            `json:"displayName"`
	Kind              string            `json:"kind"` // "local" | "ssh"
	SSHUser           *string           `json:"sshUser,omitempty"`
	SSHHost           *string           `json:"sshHost,omitempty"`
	Tags              []string          `json:"tags"`
	Enabled           bool              `json:"enabled"`
	IconOnWarn        bool              `json:"iconOnWarn"`
	IconOnCritical    bool              `json:"iconOnCritical"`
	NotifyOnWarn      bool              `json:"notifyOnWarn"`
	NotifyOnCritical  bool              `json:"notifyOnCritical"`
	CustomThresholds  *MetricThresholds `json:"customThresholds,omitempty"`
	SnoozedUntil      *time.Time        `json:"snoozedUntil,omitempty"`
	Favorite          bool              `json:"favorite"`
}

// MetricThresholds mirrors the Swift MetricThresholds struct. Values are
// 0..1 ratios except for raw-percent fields that stay percents on the
// Swift side — keep that contract identical.
type MetricThresholds struct {
	CPUWarn     float64 `json:"cpuWarn"`
	CPUCritical float64 `json:"cpuCritical"`
	MemWarn     float64 `json:"memWarn"`
	MemCritical float64 `json:"memCritical"`
	DiskWarn    float64 `json:"diskWarn"`
	DiskCritical float64 `json:"diskCritical"`
}

// ServerSettings mirrors Swift ServerSettings. Units: ratios for
// thresholds, seconds for intervals/debounce/grace.
type ServerSettings struct {
	Thresholds                    MetricThresholds `json:"thresholds"`
	LocalPollingIntervalSeconds   int              `json:"localPollingIntervalSeconds"`
	SSHPollingIntervalSeconds     int              `json:"sshPollingIntervalSeconds"`
	NotificationsEnabled          bool             `json:"notificationsEnabled"`
	NotifyWarn                    bool             `json:"notifyWarn"`
	NotifyCritical                bool             `json:"notifyCritical"`
	NotifyDebounceSeconds         int              `json:"notifyDebounceSeconds"`
	AutoUpdateSamplersEnabled     bool             `json:"autoUpdateSamplersEnabled"`
	PostWakeGraceSeconds          int              `json:"postWakeGraceSeconds"`
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
		AutoUpdateSamplersEnabled:   true,
		PostWakeGraceSeconds:        30,
	}
}

// Token represents an issued auth token. Raw tokens are never stored;
// only the SHA-256 hash lives in the database.
type Token struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"orgId"`
	Kind       TokenKind  `json:"kind"`
	Label      string     `json:"label"`
	Hash       []byte     `json:"hash"`
	NodeID     *uuid.UUID `json:"nodeId,omitempty"`
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

// User is the stubbed cloud-managed user record. Self-hosted mode
// uses a single synthetic user with ZeroUUID.
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
	Kind      string     `json:"kind"` // "kill_process"
	Payload   []byte     `json:"payload"`
	CreatedAt time.Time  `json:"createdAt"`
	ClaimedAt *time.Time `json:"claimedAt,omitempty"`
}

// ZeroOrg is the synthetic org id used in self-hosted mode where
// multi-tenancy is not active.
var ZeroOrg = uuid.UUID{}
