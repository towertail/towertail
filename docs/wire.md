# Towertail wire contract (v1)

Authoritative description of every message that flows between:

- **Sampler → Server** — NDJSON ingest + heartbeat long-poll control channel.
- **Server → Mac/Windows client** — REST + WebSocket.

See also: [`wire-format.md`](https://github.com/towertail/sampler/blob/main/docs/wire-format.md) for the `Sample` schema (unchanged), [`server-plan.md`](server-plan.md) for the server architecture.

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
| `sampler` | `POST /v1/sampler/enroll` | `/v1/ingest/*`, `/v1/control/next`, `/v1/disk-scans/{job}/progress`, `/v1/disk-scans/{job}/result` |

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
| POST | `/v1/nodes/{id}/disk-scans` | `DiskScanRequest` | `DiskScanJob` (202) |
| GET | `/v1/nodes/{id}/disk-scans` | — | `{ "jobs": [DiskScanJob] }`, the newest first |
| GET | `/v1/disk-scans/{job}` | — | `DiskScanJob` |
| GET | `/v1/disk-scans/{job}/result` | — | the scan result line, gzip (see §3.1) |
| POST | `/v1/disk-scans/{job}/cancel` | — | `DiskScanJob` |

### Sampler-only

| Method | Path | Request | Response |
|---|---|---|---|
| POST | `/v1/ingest/samples` | NDJSON of `Sample` lines (optionally gzipped) | `{ accepted, rejected, error }` |
| GET | `/v1/control/next` | — | `ControlNextResponse` or 204 |
| POST | `/v1/disk-scans/{job}/progress` | one scan line: `progress`, `error` or `cancelled` | `{ "cancel": bool }` |
| PUT | `/v1/disk-scans/{job}/result` | the scan `result` line, gzip, 16 MiB at most | 204 |

A sampler token can use the disk-scan endpoints only for jobs of its own node. Other jobs give 403.

### 3.1 Disk scans

A disk scan finds what uses the disk space below a folder of a node. The sampler of the node runs it (`towertail-sampler scan`, see [`wire-format.md`](https://github.com/towertail/sampler/blob/main/docs/wire-format.md) §3.4). The scan is read-only. The server keeps the job and the result. It never runs a cleanup command.

**Opt-in on two sides.** A scan runs only when both are true:

- The node has `allowDiskScan: true` (a `Node` field, default `false`). Else `POST /v1/nodes/{id}/disk-scans` gives 403 `forbidden` with the message `disk scans are off for this node`.
- The sampler runs with `--allow-disk-scan` (`towertail-sampler push` and `service install`). Else the sampler posts an `error` line with the message `disk scans are off on this host: run the sampler with --allow-disk-scan`, and the job becomes `failed`.

**Request.**

```json
{ "root": "/", "max_depth": 6, "min_bytes": 50000000 }
```

All keys are optional. An empty `root` is the home folder of the user that runs the sampler. `max_depth` and `min_bytes` default to the sampler defaults.

**Job.**

```json
{
  "id": "uuid",
  "node_id": "uuid",
  "state": "running",
  "root": "/",
  "max_depth": 6,
  "min_bytes": 50000000,
  "progress": { "t": "progress", "v": 1, "files": 412318, "dirs": 30211, "bytes": 90412000000,
                "no_access_dirs": 7, "current": "/var/lib", "elapsed_ms": 37000,
                "used_bytes": 195400000000, "top": [] },
  "error": "",
  "cancel_requested": false,
  "created_ts": "2026-10-15T09:12:00Z",
  "started_ts": "2026-10-15T09:12:04Z",
  "finished_ts": null,
  "result_bytes": 0
}
```

| Key | Meaning |
|---|---|
| `state` | `queued`, `running`, `done`, `failed`, `cancelled` or `expired`. |
| `progress` | The last progress line from the sampler, or `null`. |
| `error` | The reason for `failed` or `expired`. Else empty. |
| `cancel_requested` | A user asked to cancel a running job. The sampler gets `cancel: true` on its next progress post. |
| `started_ts` | Time of the first progress line, or `null`. |
| `finished_ts` | Time of the final state, or `null`. |
| `result_bytes` | Size of the gzip result. `0` until the job is `done`. |

**States.**

```
queued ──first progress──▶ running ──result PUT──▶ done
  │                          │ └──error line or no progress for 2 min──▶ failed
  │                          └──cancelled line──▶ cancelled
  ├──cancel──▶ cancelled
  └──no sampler in 10 min──▶ expired
```

`done`, `failed`, `cancelled` and `expired` are final. A job in a final state does not change.

**Rules.**

- One job in `queued` or `running` state for each node. A second request gives 409 `conflict`. The error envelope then has the current job in a `job` key: `{ "error": {…}, "job": DiskScanJob }`.
- `POST …/cancel` on a `queued` job makes it `cancelled`. On a `running` job it sets `cancel_requested`. On a job in a final state it changes nothing and returns the job.
- Progress post on a job in a final state: the response is `{ "cancel": true }`, so that the sampler stops.
- The result PUT body is gzip (`Content-Encoding: gzip`, `Content-Type: application/json`). It is 16 MiB at most, else 413. The server decodes it as a scan `result` line before it keeps it, else 400.
- `GET …/result` returns the gzip bytes with `Content-Encoding: gzip` and `Content-Type: application/json`. It gives 404 when the job has no result.
- The server keeps the last 3 jobs with a result for each node, and deletes results older than 7 days.
- The audit log has an entry for each scan request and each cancel.

**Control message.** `POST …/disk-scans` puts a `disk_scan` message on the control queue of the node:

```json
{ "id": "uuid", "kind": "disk_scan",
  "payload": { "job_id": "uuid", "root": "/", "max_depth": 6, "min_bytes": 50000000 } }
```

The sampler runs one scan at a time. It posts a progress line each 2 s, and the final `error` or `cancelled` line, or it PUTs the result.

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
{ "type": "disk_scan_updated", "nodeId": "uuid", "job": { DiskScanJob } }
{ "type": "pong" }
```

`disk_scan_updated` goes to the clients that see the node. The server sends it on each state change, and for progress at most once each 2 s for each job.

On connect the server immediately replays the most recent `sample` for every node the client can see, then begins streaming new frames as they arrive. Event replays are not implemented today (clients refetch `/v1/alerts` at connect).

---

## 5. Go types (reference)

The server's [`internal/wire`](../server/internal/wire/wire.go) package is the source of truth. Notable cross-module shapes:

- `Sample` — identical to `pkg/schema/sample.go` Sample in [towertail/sampler](https://github.com/towertail/sampler), just relaxed around optional fields.
- `Event` — `{ id, ts, kind, nodeId, metric?, tint? }` where `kind` ∈ `threshold_crossed | reachability_changed | sampler_version` and `tint` ∈ `ok | warn | critical`.
- `Node` — camelCase mirror of the Swift `Node` struct (`apps/mac/Sources/State/Node.swift`).
- `ServerSettings` — camelCase mirror of the Swift `ServerSettings` class.
- `LoginRequest`, `LoginResponse`.
- `EnrollRequest`, `EnrollResponse`.
- `ControlNextResponse`, `KillProcessPayload`, `DiskScanPayload`.
- `DiskScanRequest`, `DiskScanJob`, `DiskScanProgressResponse`. The scan lines are `pkg/schema/scan.go` in [towertail/sampler](https://github.com/towertail/sampler).
- `NodeHistoryPoint`, `NodeHistoryResponse`.

---

## 6. Schema versioning

- `Sample.v` tracks the sampler schema (see [`wire-format.md`](https://github.com/towertail/sampler/blob/main/docs/wire-format.md) §11). Server rejects samples with unknown `v`; it ingests `v=1` today.
- REST + WS do not carry a version field. Breaking changes bump the path prefix (`/v2`).
- Bolt and ClickHouse storage are internal; schema migrations run on boot.
