// Package wire contains the REST and WebSocket message shapes shared
// by the server and its clients (Mac app RemoteBackend, sampler).
// Types here are authoritative; clients must match these exactly.
//
// Metric-shape types (Sample, Host, CPU, …) are re-exported from
// sampler/pkg/schema so the wire contract has one source of truth.
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

// --- Metric shapes re-exported from sampler/pkg/schema ---

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
	NodeID     uuid.UUID          `json:"node_id"`
	Metric     string             `json:"metric"`
	Resolution string             `json:"resolution"`
	Points     []NodeHistoryPoint `json:"points"`
}
