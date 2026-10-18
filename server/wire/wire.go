// Package wire contains the REST and WebSocket message shapes shared
// by the server and its clients (Mac app RemoteBackend, sampler).
// Types here are authoritative; clients must match these exactly.
//
// Metric-shape types (Sample, Host, CPU, …) are re-exported from
// github.com/towertail/sampler/pkg/schema so the wire contract has one source of truth.
// The sampler emits schema.Sample on stdout/push; the server decodes
// the same struct here. Types below that aren't re-exports are
// server-only (API envelopes, WebSocket framing, auth/enroll).
package wire

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/towertail/sampler/pkg/schema"
)

// --- Metric shapes re-exported from schema ---

type (
	Host         = schema.HostInfo
	CPU          = schema.CPUInfo
	MemSwap      = schema.MemInfo
	Disk         = schema.DiskSample
	DiskIO       = schema.DiskIOInfo
	DiskIODevice = schema.DiskIODeviceInfo
	Net          = schema.NetInfo
	Procs        = schema.ProcList
	Proc         = schema.ProcSample
)

// Sample wraps schema.Sample so the server can carry the original
// wire bytes alongside the decoded fields (we re-emit them on the
// WebSocket fan-out without re-marshalling). The JSON shape on the
// wire is identical to schema.Sample — see ingest/TestDecodeSample.
type Sample struct {
	schema.Sample
	Raw json.RawMessage `json:"-"`
}

// --- Server-only types ---

// Tint is the threshold state of one metric.
type Tint string

const (
	TintOK       Tint = "ok"
	TintWarn     Tint = "warn"
	TintCritical Tint = "critical"
)

// Event is a BackendEvent equivalent.
type Event struct {
	ID     uuid.UUID `json:"id"`
	TS     time.Time `json:"ts"`
	Kind   string    `json:"kind"`
	NodeID uuid.UUID `json:"nodeId"`
	Metric string    `json:"metric,omitempty"`
	Tint   string    `json:"tint,omitempty"`
}

// WSMessage is a tagged envelope for WebSocket frames.
type WSMessage struct {
	Type    string          `json:"type"`
	NodeID  *uuid.UUID      `json:"nodeId,omitempty"`
	Sample  json.RawMessage `json:"sample,omitempty"`
	Event   *Event          `json:"event,omitempty"`
	Node    json.RawMessage `json:"node,omitempty"`
	Nodes   json.RawMessage `json:"nodes,omitempty"`
	Setting json.RawMessage `json:"settings,omitempty"`
	Job     *DiskScanJob    `json:"job,omitempty"`
}

// SubscribeMessage is sent client → server to narrow the subscription
// to a set of nodes.
type SubscribeMessage struct {
	Type    string      `json:"type"`
	NodeIDs []uuid.UUID `json:"node_ids,omitempty"`
}

// APIError is the uniform error envelope.
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

type APIErrorEnvelope struct {
	Error APIError `json:"error"`
	// Job is the active disk scan of the node on a 409 for a second scan.
	Job *DiskScanJob `json:"job,omitempty"`
}

// LoginRequest / LoginResponse for /v1/auth/login.
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// EnrollRequest / EnrollResponse for /v1/sampler/enroll.
type EnrollRequest struct {
	DisplayName string    `json:"display_name" validate:"required"`
	MachineID   string    `json:"machine_id,omitempty"`
	NodeID      uuid.UUID `json:"node_id,omitempty"` // optional: existing node to bind to
}

type EnrollResponse struct {
	NodeID       uuid.UUID `json:"node_id"`
	SamplerToken string    `json:"sampler_token"`
	Endpoint     string    `json:"endpoint"`
}

// SamplerToken is one sampler token in GET /v1/sampler/tokens. It has no
// secret.
type SamplerToken struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	NodeID     *uuid.UUID `json:"nodeId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// ControlNextResponse is returned from the sampler long-poll. When no
// message is available, HTTP 204 is returned with no body.
type ControlNextResponse struct {
	ID      uuid.UUID       `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// KillProcessPayload is the JSON body of a kill_process control message.
type KillProcessPayload struct {
	PID int32 `json:"pid"`
}

// NodeHistoryPoint is one bucket of a history range response.
type NodeHistoryPoint struct {
	TS    time.Time `json:"ts"`
	Value float64   `json:"value"`
	Max   float64   `json:"max,omitempty"`
}

// NodeHistoryResponse wraps a series of points.
type NodeHistoryResponse struct {
	NodeID     uuid.UUID `json:"node_id"`
	Metric     string    `json:"metric"`
	Resolution string    `json:"resolution"`
	// Clamped is true when the plan history limit moved "from" later.
	Clamped bool               `json:"clamped,omitempty"`
	Points  []NodeHistoryPoint `json:"points"`
}

// TopProcess is one process of a node over a range: a name and a cmd.
type TopProcess struct {
	Name      string  `json:"name"`
	Cmd       string  `json:"cmd"`
	CPUPctAvg float64 `json:"cpu_pct_avg"`
	CPUPctMax float64 `json:"cpu_pct_max"`
	RSSMax    uint64  `json:"rss_max"`
}

// NodeProcessesResponse is the top processes of a node, by average CPU.
type NodeProcessesResponse struct {
	NodeID    uuid.UUID    `json:"node_id"`
	From      time.Time    `json:"from"`
	To        time.Time    `json:"to"`
	Clamped   bool         `json:"clamped,omitempty"`
	Processes []TopProcess `json:"processes"`
}

// DiskPoint is the use of a mount at the end of one bucket.
type DiskPoint struct {
	TS    time.Time `json:"ts"`
	Used  uint64    `json:"used"`
	Total uint64    `json:"total"`
}

// DiskSeries is the use of one mount over a range.
type DiskSeries struct {
	Mount  string      `json:"mount"`
	FS     string      `json:"fs"`
	Points []DiskPoint `json:"points"`
}

// NodeDisksResponse is the use of each mount of a node over a range.
type NodeDisksResponse struct {
	NodeID     uuid.UUID    `json:"node_id"`
	From       time.Time    `json:"from"`
	To         time.Time    `json:"to"`
	Resolution string       `json:"resolution"`
	Clamped    bool         `json:"clamped,omitempty"`
	Mounts     []DiskSeries `json:"mounts"`
}

// Alert is an open alert: the last event of a (node, metric) when its
// tint is not ok. ID is the event id.
type Alert struct {
	ID           uuid.UUID  `json:"id"`
	NodeID       uuid.UUID  `json:"node_id"`
	Kind         string     `json:"kind"`
	Metric       string     `json:"metric"`
	Tint         Tint       `json:"tint"`
	TS           time.Time  `json:"ts"`
	Value        float64    `json:"value"`
	AckedBy      *uuid.UUID `json:"acked_by,omitempty"`
	AckedAt      *time.Time `json:"acked_at,omitempty"`
	SnoozedUntil *time.Time `json:"snoozed_until,omitempty"`
}

// AlertList is the body of GET /v1/alerts.
type AlertList struct {
	Alerts []Alert `json:"alerts"`
}

// DiskScanRequest is the body of POST /v1/nodes/{id}/disk-scans. All
// keys are optional. Zero values tell the sampler to use its defaults.
type DiskScanRequest struct {
	Root     string `json:"root"`
	MaxDepth int    `json:"max_depth"`
	MinBytes int64  `json:"min_bytes"`
	// MinFrac is nil for the sampler default. A refresh sends 0.
	MinFrac *float64 `json:"min_frac,omitempty"`
	// RefreshOf is the scope that this scan refreshes, for example ~.
	// The apps merge the result into that scope.
	RefreshOf string `json:"refresh_of,omitempty"`
}

// DiskScanJob is a disk scan as the app sees it (docs/wire.md §3.1).
type DiskScanJob struct {
	ID              uuid.UUID            `json:"id"`
	NodeID          uuid.UUID            `json:"node_id"`
	State           string               `json:"state"`
	Root            string               `json:"root"`
	MaxDepth        int                  `json:"max_depth"`
	MinBytes        int64                `json:"min_bytes"`
	MinFrac         *float64             `json:"min_frac,omitempty"`
	RefreshOf       string               `json:"refresh_of,omitempty"`
	Progress        *schema.ScanProgress `json:"progress"`
	Error           string               `json:"error"`
	CancelRequested bool                 `json:"cancel_requested"`
	CreatedTS       time.Time            `json:"created_ts"`
	StartedTS       *time.Time           `json:"started_ts"`
	FinishedTS      *time.Time           `json:"finished_ts"`
	ResultBytes     int64                `json:"result_bytes"`
}

// DiskScanJobList is the body of GET /v1/nodes/{id}/disk-scans.
type DiskScanJobList struct {
	Jobs []DiskScanJob `json:"jobs"`
}

// DiskScanPayload is the JSON body of a disk_scan control message. The
// sampler uses its defaults for keys that are not there.
type DiskScanPayload struct {
	JobID    uuid.UUID `json:"job_id"`
	Root     string    `json:"root"`
	MaxDepth int       `json:"max_depth,omitempty"`
	MinBytes int64     `json:"min_bytes,omitempty"`
	MinFrac  *float64  `json:"min_frac,omitempty"`
}

// DiskScanProgressResponse is the reply to a progress post. Cancel
// tells the sampler to stop the scan.
type DiskScanProgressResponse struct {
	Cancel bool `json:"cancel"`
}

// ChannelRoute selects the alerts of a channel. minTint is warn (the
// default) or critical. Empty metrics or tags match all.
type ChannelRoute struct {
	MinTint Tint     `json:"minTint,omitempty"`
	Metrics []string `json:"metrics,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

// Channel is an alert channel of the org. The secret is never sent.
type Channel struct {
	ID        uuid.UUID    `json:"id"`
	Kind      string       `json:"kind"` // slack | discord | pagerduty | webhook | email
	Name      string       `json:"name"`
	Enabled   bool         `json:"enabled"`
	Target    string       `json:"target"`
	HasSecret bool         `json:"hasSecret"`
	Route     ChannelRoute `json:"route"`
	CreatedAt time.Time    `json:"createdAt"`
}

// ChannelRequest is the body of POST /v1/channels and PUT
// /v1/channels/{id}. On PUT, a missing enabled, target or secret keeps
// the stored value. An empty secret removes it.
type ChannelRequest struct {
	Kind    string       `json:"kind"`
	Name    string       `json:"name"`
	Enabled *bool        `json:"enabled,omitempty"`
	Target  *string      `json:"target,omitempty"`
	Secret  *string      `json:"secret,omitempty"`
	Route   ChannelRoute `json:"route"`
}

// ChannelTestResponse is the result of POST /v1/channels/{id}/test.
type ChannelTestResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AlertPayload is the body that a generic webhook channel gets. Type is
// alert or test.
type AlertPayload struct {
	V        int       `json:"v"`
	Type     string    `json:"type"`
	EventID  uuid.UUID `json:"eventId"`
	OrgID    uuid.UUID `json:"orgId"`
	NodeID   uuid.UUID `json:"nodeId"`
	NodeName string    `json:"nodeName"`
	Tags     []string  `json:"tags"`
	Metric   string    `json:"metric"`
	From     Tint      `json:"from"`
	To       Tint      `json:"to"`
	Value    float64   `json:"value"`
	At       time.Time `json:"at"`
	Title    string    `json:"title"`
	Text     string    `json:"text"`
}
