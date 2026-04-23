# Towertail wire contract (v1)

Authoritative description of every message that flows between:

- **Sampler → Server** — NDJSON ingest + heartbeat long-poll control channel.
- **Server → Mac/Windows client** — REST + WebSocket.

See also: [`sampler.md`](sampler.md) for the `Sample` schema (unchanged), [`server-plan.md`](server-plan.md) for the server architecture.

---

## 1. Transport

| Direction | Protocol | Framing | Auth |
|---|---|---|---|
| Sampler → Server ingest | HTTPS POST | NDJSON body (`application/x-ndjson`), optional `Content-Encoding: gzip` | `Authorization: Bearer <sampler_token>` |
| Sampler → Server control | HTTPS GET long-poll | JSON (single message) or 204 No Content | `Authorization: Bearer <sampler_token>` |
| Client → Server REST | HTTPS GET/POST/PUT/DELETE | JSON | `Authorization: Bearer <user_token>` (or admin) |
| Client ↔ Server stream | WSS, text frames | JSON `WSMessage` envelopes | `Authorization: Bearer <user_token>` at connect |

Error envelope for every REST 4xx/5xx:

```json
{ "error": { "code": "not_found", "message": "...", "request_id": "..." } }
```

Codes: `unauthorized`, `forbidden`, `not_found`, `bad_request`, `conflict`, `rate_limited`, `unavailable`, `not_implemented`, `internal`.

---

## 2. Authentication

Three token kinds, all opaque strings with a `tt_` prefix. Raw tokens are only ever returned at issue time; the server stores SHA-256 hashes.

| Kind | Issued by | Allowed groups |
|---|---|---|
| `user` | `POST /v1/auth/login` | `/v1/*` except ingest + control |
| `admin` | `towertail-server token issue --kind=admin` (CLI) | everything in `user` + token management |
| `sampler` | `POST /v1/sampler/enroll` | `/v1/ingest/*`, `/v1/control/next` |

Sampler tokens are bound to one `node_id`. Ingest/control endpoints reject tokens without a binding.

---

## 3. REST endpoints

Base path `/v1`. Authed unless otherwise marked.

### Public

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/healthz` | — | `200 ok` |
| GET | `/readyz` | — | `200 ready` or `503 starting` |
| GET | `/metrics` | — | Prometheus text |
| POST | `/v1/auth/login` | `LoginRequest` | `LoginResponse` |

### User / admin

| Method | Path | Request | Response |
|---|---|---|---|
| POST | `/v1/auth/logout` | — | 204 |
| GET | `/v1/nodes` | — | `[Node]` |
| POST | `/v1/nodes` | `Node` | `Node` (201) |
| GET | `/v1/nodes/{id}` | — | `Node` |
| PUT | `/v1/nodes/{id}` | `Node` | `Node` |
| DELETE | `/v1/nodes/{id}` | — | 204 |
| POST | `/v1/nodes/{id}/kill-process?pid=…` | — | 202 |
| GET | `/v1/nodes/{id}/history?metric=cpu&from=…&to=…&step=1m` | — | `NodeHistoryResponse` |
| GET | `/v1/settings` | — | `ServerSettings` |
| PUT | `/v1/settings` | `ServerSettings` | `ServerSettings` |
| GET | `/v1/alerts?state=active` | — | `{ alerts: [...] }` |
| POST | `/v1/alerts/{id}/ack` | — | 200 |
| POST | `/v1/alerts/{id}/snooze?until=RFC3339` | — | 200 |
| POST | `/v1/sampler/enroll` | `EnrollRequest` | `EnrollResponse` |
| GET | `/v1/sampler/versions` | — | `{ "linux-amd64": "sha256:…", … }` |

### Sampler-only

| Method | Path | Request | Response |
|---|---|---|---|
| POST | `/v1/ingest/samples` | NDJSON of `Sample` lines (optionally gzipped) | `{ accepted, rejected, error }` |
| GET | `/v1/control/next` | — | `ControlNextResponse` or 204 |

---

## 4. WebSocket messages

Endpoint: `/v1/stream`. Client presents bearer token in the `Authorization` header on the upgrade request.

### Client → server

```json
{ "type": "subscribe", "node_ids": ["uuid", "uuid"] }   // narrow subscription
{ "type": "ping" }
```

An empty `node_ids` removes the filter and subscribes to every node in the org.

### Server → client

Every frame is a `WSMessage` with a `type` discriminator:

```json
{ "type": "sample",           "nodeId": "uuid", "sample": { ... }  }
{ "type": "event",            "event":  { ... } }
{ "type": "node_updated",     "node":   { ... } }
{ "type": "settings_updated", "settings": { ... } }
{ "type": "pong" }
```

On connect the server immediately replays the most recent `sample` for every node the client can see, then begins streaming new frames as they arrive. Event replays are not implemented today (clients refetch `/v1/alerts` at connect).

---

## 5. Go types (reference)

The server's [`internal/wire`](../server/internal/wire/wire.go) package is the source of truth. Notable cross-module shapes:

- `Sample` — identical to `sampler/internal/schema/sample.go` Sample, just relaxed around optional fields.
- `Event` — `{ id, ts, kind, nodeId, metric?, tint? }` where `kind` ∈ `threshold_crossed | reachability_changed | sampler_version` and `tint` ∈ `ok | warn | critical`.
- `Node` — camelCase mirror of the Swift `Node` struct (`app/mac/Sources/State/Node.swift`).
- `ServerSettings` — camelCase mirror of the Swift `ServerSettings` class.
- `LoginRequest`, `LoginResponse`.
- `EnrollRequest`, `EnrollResponse`.
- `ControlNextResponse`, `KillProcessPayload`.
- `NodeHistoryPoint`, `NodeHistoryResponse`.

---

## 6. Schema versioning

- `Sample.v` tracks the sampler schema (see [`sampler.md`](sampler.md) §11). Server rejects samples with unknown `v`; it ingests `v=1` today.
- REST + WS do not carry a version field. Breaking changes bump the path prefix (`/v2`).
- Bolt and ClickHouse storage are internal; schema migrations run on boot.
