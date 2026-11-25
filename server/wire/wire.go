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
	ID uuid.UUID `json:"id"`
	// AlertID is the id of the incident. It is the same for each event from
	// the change out of ok to the change back to ok.
	AlertID uuid.UUID `json:"alertId"`
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind"`
	NodeID  uuid.UUID `json:"nodeId"`
	Metric  string    `json:"metric,omitempty"`
	Tint    string    `json:"tint,omitempty"`
	// Notify is true when the server sends a notification for the event.
	Notify bool   `json:"notify,omitempty"`
	Title  string `json:"title,omitempty"`
	Text   string `json:"text,omitempty"`
}

// WSMessage is a tagged envelope for WebSocket frames. The frame types
// hello, summary, detail and lease are for lease mode (docs/wire.md §4).
type WSMessage struct {
	Type    string          `json:"type"`
	NodeID  *uuid.UUID      `json:"nodeId,omitempty"`
	Sample  json.RawMessage `json:"sample,omitempty"`
	Event   *Event          `json:"event,omitempty"`
	Node    json.RawMessage `json:"node,omitempty"`
	Nodes   json.RawMessage `json:"nodes,omitempty"`
	Setting json.RawMessage `json:"settings,omitempty"`
	Job     *DiskScanJob    `json:"job,omitempty"`
	Hello   *StreamHello    `json:"hello,omitempty"`
	Summary *LiveSummary    `json:"summary,omitempty"`
	Tab     DetailTab       `json:"tab,omitempty"`
	Detail  json.RawMessage `json:"detail,omitempty"`
	Lease   *LeaseNotice    `json:"lease,omitempty"`
	Alert   *Alert          `json:"alert,omitempty"`
}

// ClientMessage is sent client → server: subscribe (legacy mode), view
// (lease mode) and ping.
type ClientMessage struct {
	Type    string      `json:"type"`
	NodeIDs []uuid.UUID `json:"node_ids,omitempty"`
	Hosts   []uuid.UUID `json:"hosts,omitempty"`
	Detail  *ViewDetail `json:"detail,omitempty"`
	// TTL is the lease time in seconds.
	TTL int `json:"ttl,omitempty"`
}

// SubscribeMessage is the old name of ClientMessage.
type SubscribeMessage = ClientMessage

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

// EnrollRequest / EnrollResponse for /v1/sampler/enroll. A NodeID that
// does not exist makes the node with that ID. An empty NodeID with a
// known MachineID binds to the node of that machine.
type EnrollRequest struct {
	DisplayName string    `json:"display_name" validate:"required"`
	MachineID   string    `json:"machine_id,omitempty"`
	NodeID      uuid.UUID `json:"node_id,omitempty"`
}

type EnrollResponse struct {
	NodeID       uuid.UUID `json:"node_id"`
	SamplerToken string    `json:"sampler_token"`
	Endpoint     string    `json:"endpoint"`
}

// IngestResponse is the 202 body of POST /v1/ingest/samples. Interval
// is a Go duration ("5s"). Update is the version that the sampler
// should run. Both are left out when the server has no order.
type IngestResponse struct {
	Accepted int    `json:"accepted"`
	Rejected int    `json:"rejected"`
	Error    string `json:"error,omitempty"`
	Interval string `json:"interval,omitempty"`
	Update   string `json:"update,omitempty"`
	// ProcCwd tells the sampler to send process folders. The server always sets it.
	ProcCwd *bool `json:"proc_cwd,omitempty"`
}

// IngestPlanFull is the 402 body of POST /v1/ingest/samples. The sampler
// pauses for RetryAfter seconds.
type IngestPlanFull struct {
	RetryAfter int `json:"retry_after"`
}

// IngestGone is the 410 body of POST /v1/ingest/samples. The sampler
// exits. Action is always "exit".
type IngestGone struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// Reasons of IngestGone.
const (
	GoneHostRemoved    = "host_removed"
	GoneAccountDeleted = "account_deleted"
	GoneCloudOff       = "cloud_off"
)

// JoinTokenRequest is the body of POST /v1/sampler/join-tokens.
type JoinTokenRequest struct {
	DisplayName string `json:"display_name,omitempty"`
}

// JoinTokenResponse holds the one-use join token. It works for one hour.
type JoinTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// JoinRequest is the body of POST /v1/sampler/join. The reply is an
// EnrollResponse.
type JoinRequest struct {
	JoinToken   string `json:"join_token" validate:"required"`
	DisplayName string `json:"display_name" validate:"required"`
	MachineID   string `json:"machine_id,omitempty"`
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

// NodeHistorySeries is the series of many metrics of one node. Each key
// of Series is a metric name.
type NodeHistorySeries struct {
	NodeID uuid.UUID                     `json:"node_id"`
	Series map[string][]NodeHistoryPoint `json:"series"`
}

// NodeHistoryMultiResponse is the answer of the node history route with
// "metrics".
type NodeHistoryMultiResponse struct {
	NodeHistorySeries
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Resolution string    `json:"resolution"`
	// Clamped is true when the plan history limit moved "from" later.
	Clamped bool `json:"clamped,omitempty"`
}

// FleetHistoryResponse is the answer of the fleet history route. A node
// with no points in the range has no entry.
type FleetHistoryResponse struct {
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Resolution string    `json:"resolution"`
	// Clamped is true when the plan history limit moved "from" later.
	Clamped bool                `json:"clamped,omitempty"`
	Nodes   []NodeHistorySeries `json:"nodes"`
}

// TopProcess is one process of a node over a range: a name and a cmd.
type TopProcess struct {
	Key       ProcessKey  `json:"key"`
	Name      string      `json:"name"`
	Cmd       string      `json:"cmd"`
	Repo      string      `json:"repo,omitempty"`
	Cwd       string      `json:"cwd,omitempty"`
	Parent    string      `json:"parent,omitempty"`
	CPUPctAvg float64     `json:"cpu_pct_avg"`
	CPUPctMax float64     `json:"cpu_pct_max"`
	RSSMax    uint64      `json:"rss_max"`
	IOBpsAvg  float64     `json:"io_bps_avg,omitempty"`
	Series    *ProcSeries `json:"series,omitempty"`
}

// ProcSeries is the history of one process. Point i is at series_from +
// i * series_step_s of the response. A bucket with no row is 0.
type ProcSeries struct {
	CPU []float64 `json:"cpu"`
	RSS []uint64  `json:"rss"`
	IO  []float64 `json:"io,omitempty"`
}

// NodeProcessesResponse is the top processes of a node, by average CPU.
// SeriesFrom and SeriesStepS are set when the request has "series".
type NodeProcessesResponse struct {
	NodeID      uuid.UUID    `json:"node_id"`
	From        time.Time    `json:"from"`
	To          time.Time    `json:"to"`
	Clamped     bool         `json:"clamped,omitempty"`
	SeriesFrom  *time.Time   `json:"series_from,omitempty"`
	SeriesStepS int          `json:"series_step_s,omitempty"`
	Processes   []TopProcess `json:"processes"`
}

// ProcessSnapshot is the top processes of a node at one time.
type ProcessSnapshot struct {
	NodeID    uuid.UUID         `json:"node_id"`
	TS        time.Time         `json:"ts"`       // the sample or bucket start
	Tier      string            `json:"tier"`     // "raw", "1m", "1h"
	BucketS   int               `json:"bucket_s"` // 0 for raw
	By        string            `json:"by"`
	Total     int               `json:"total,omitempty"` // raw only
	Prev      *time.Time        `json:"prev,omitempty"`
	Next      *time.Time        `json:"next,omitempty"`
	Clamped   bool              `json:"clamped,omitempty"`
	Processes []SnapshotProcess `json:"processes"`
}

// SnapshotProcess is one process of a ProcessSnapshot. PID, PPID, Parent,
// User, Cwd, Threads, State and StartTS come from raw samples only. On a
// rollup, CPUPct is the average and RSS is the maximum.
type SnapshotProcess struct {
	Key       ProcessKey `json:"key"`
	PID       int32      `json:"pid,omitempty"`
	PPID      int32      `json:"ppid,omitempty"`
	Parent    string     `json:"parent,omitempty"`
	User      string     `json:"user,omitempty"`
	Name      string     `json:"name"`
	Cmd       string     `json:"cmd"`
	Repo      string     `json:"repo,omitempty"`
	Cwd       string     `json:"cwd,omitempty"`
	CPUPct    float64    `json:"cpu_pct"`
	CPUPctMax float64    `json:"cpu_pct_max,omitempty"`
	RSS       uint64     `json:"rss"`
	IOBps     float64    `json:"io_bps,omitempty"`
	Threads   int32      `json:"threads,omitempty"`
	State     string     `json:"state,omitempty"`
	StartTS   *time.Time `json:"start_ts,omitempty"`
}

// FleetHost is one host with its latest values. A host with no sample in
// the last 10 minutes is offline and has no values. CPU is a percent. Mem
// and Disk are ratios from 0 to 1. NetRx and NetTx are bytes each second.
type FleetHost struct {
	NodeID   uuid.UUID  `json:"node_id"`
	Name     string     `json:"name"`
	Tags     []string   `json:"tags,omitempty"`
	Online   bool       `json:"online"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
	CPU      *float64   `json:"cpu,omitempty"`
	Mem      *float64   `json:"mem,omitempty"`
	Disk     *float64   `json:"disk,omitempty"`
	Load1    *float64   `json:"load1,omitempty"`
	NetRx    *uint64    `json:"net_rx,omitempty"`
	NetTx    *uint64    `json:"net_tx,omitempty"`
}

// FleetHosts is the body of GET /v1/fleet/hosts.
type FleetHosts struct {
	Hosts []FleetHost `json:"hosts"`
}

// FleetProcess is one process of a host over a range.
type FleetProcess struct {
	NodeID    uuid.UUID  `json:"node_id"`
	NodeName  string     `json:"node_name"`
	Key       ProcessKey `json:"key"`
	Name      string     `json:"name"`
	Cmd       string     `json:"cmd"`
	Repo      string     `json:"repo,omitempty"`
	Cwd       string     `json:"cwd,omitempty"`
	Parent    string     `json:"parent,omitempty"`
	CPUPctAvg float64    `json:"cpu_pct_avg"`
	CPUPctMax float64    `json:"cpu_pct_max"`
	RSSMax    uint64     `json:"rss_max"`
}

// FleetProcesses is the body of GET /v1/fleet/processes. By is cpu, mem or io.
type FleetProcesses struct {
	From      time.Time      `json:"from"`
	To        time.Time      `json:"to"`
	Clamped   bool           `json:"clamped,omitempty"`
	By        string         `json:"by"`
	Processes []FleetProcess `json:"processes"`
}

// Window is a time range.
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Stat is the average and the highest value of a metric in a window.
type Stat struct {
	Avg    float64 `json:"avg"`
	Max    float64 `json:"max"`
	Points int     `json:"points"`
}

// CompareHost is the change of one host between two windows. Previous
// and DeltaAvg are empty when the host has no data in the previous window.
type CompareHost struct {
	NodeID   uuid.UUID `json:"node_id"`
	Name     string    `json:"name"`
	Current  Stat      `json:"current"`
	Previous *Stat     `json:"previous,omitempty"`
	DeltaAvg *float64  `json:"delta_avg,omitempty"`
}

// Compare is the body of GET /v1/fleet/compare. Clamped is true when the
// plan history limit cuts the previous window.
type Compare struct {
	Metric   string        `json:"metric"`
	Current  Window        `json:"current"`
	Previous Window        `json:"previous"`
	Clamped  bool          `json:"clamped,omitempty"`
	Hosts    []CompareHost `json:"hosts"`
}

// APIKey is a read-only API key. The secret is not part of it.
type APIKey struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	UserID     *uuid.UUID `json:"userId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// APIKeyCreated is the answer of POST /v1/api-keys. Token shows only one
// time.
type APIKeyCreated struct {
	APIKey
	Token string `json:"token"`
}

// APIKeyRequest is the body of POST /v1/api-keys.
type APIKeyRequest struct {
	Label string `json:"label" validate:"required,max=80"`
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
// tint is not ok. ID is the alert id: the event id of the change out of ok.
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
	Proc         *AlertProc `json:"proc,omitempty"`
}

// AlertProc is the process of a long_process alert, from the event
// payload. StartTS is nil when the sampler did not send a start time.
type AlertProc struct {
	Key       ProcessKey `json:"key"`
	PID       int32      `json:"pid"`
	Name      string     `json:"name"`
	Cmd       string     `json:"cmd"`
	StartTS   *time.Time `json:"start_ts,omitempty"`
	HighSince *time.Time `json:"high_since,omitempty"`
	CPUPct    float64    `json:"cpu_pct"`
	Cwd       string     `json:"cwd,omitempty"`
	Repo      string     `json:"repo,omitempty"`
	Parent    string     `json:"parent,omitempty"`
}

// AlertList is the body of GET /v1/alerts.
type AlertList struct {
	Alerts []Alert `json:"alerts"`
	// Clamped is true when the plan history limit moved the start later.
	Clamped bool `json:"clamped,omitempty"`
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
	// Blocked is true when the plan does not permit the kind. The server
	// sends no alert to a blocked channel.
	Blocked bool `json:"blocked,omitempty"`
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
