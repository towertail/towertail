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
| GET | `/v1/nodes/{id}/processes?from=…&to=…&limit=10` | — | `NodeProcessesResponse` |
| GET | `/v1/nodes/{id}/disks?from=…&to=…` | — | `NodeDisksResponse` |
| GET | `/v1/settings` | — | `ServerSettings` |
| PUT | `/v1/settings` | `ServerSettings` | `ServerSettings` |
| GET | `/v1/alerts` | — | `AlertList`: the open alerts of the org, the newest first |
| POST | `/v1/alerts/{id}/ack` | — | `Alert` (404 if the alert is not open) |
| POST | `/v1/alerts/{id}/snooze?until=RFC3339` | — | `Alert` (404 if the alert is not open) |
| GET | `/v1/channels` | — | `[Channel]` |
| POST | `/v1/channels` | `ChannelRequest` | `Channel` (201, 400 if not valid) |
| GET | `/v1/channels/{id}` | — | `Channel` |
| PUT | `/v1/channels/{id}` | `ChannelRequest` | `Channel` |
| DELETE | `/v1/channels/{id}` | — | 204 |
| POST | `/v1/channels/{id}/test` | — | `ChannelTestResponse` (200, also when the send fails) |
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

- The node has `allowDiskScan: true` (a `Node` field, default `false`). Else `POST /v1/nodes/{id}/disk-scans` gives 403 `forbidden` with the message `disk scans are off for this node`. A `PUT /v1/nodes/{id}` body without the `allowDiskScan` key keeps the stored value.
- The sampler runs with `--allow-disk-scan` (`towertail-sampler push` and `service install`). Else the sampler posts an `error` line with the message `disk scans are off on this host: run the sampler with --allow-disk-scan`, and the job becomes `failed`.

**Request.**

```json
{ "root": "/", "max_depth": 6, "min_bytes": 50000000 }
```

All keys are optional. An empty `root` is the home folder of the user that runs the sampler. `max_depth` and `min_bytes` default to the sampler defaults.

**Refresh of a folder.** A refresh scans one folder again and the apps put the result into an earlier scan. It is a normal job with two more keys:

```json
{ "root": "/home/dev/Downloads", "max_depth": 5, "min_bytes": 50000000, "min_frac": 0, "refresh_of": "~" }
```

| Key | Meaning |
|---|---|
| `min_frac` | Passed to the sampler as `--min-frac`. A refresh sends `0`, so that the floor is `min_bytes` (the `limits.min_bytes` of the earlier result). Must be in [0, 1). Left out: the sampler default. |
| `refresh_of` | The scope of the earlier scan: `~` or an absolute path. `root` must be below it, else 400. With `~`, `root` must be absolute or start with `~`. |

The job has the same keys. An app that follows the node merges a `done` job with `refresh_of` into the stored result of that scope. It does not show the job as a separate scope. The one-active-job rule applies to a refresh too.

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

The sampler can post the `error` or `cancelled` line, or PUT the result, before its first progress line. Then a `queued` job goes directly to `failed`, `cancelled` or `done`.

**Rules.**

- One job in `queued` or `running` state for each node. A second request gives 409 `conflict`. The error envelope then has the current job in a `job` key: `{ "error": {…}, "job": DiskScanJob }`.
- `POST …/cancel` on a `queued` job makes it `cancelled`. On a `running` job it sets `cancel_requested`. On a job in a final state it changes nothing and returns the job.
- Progress post on a job in a final state: the response is `{ "cancel": true }`, so that the sampler stops.
- The result PUT body is gzip (`Content-Encoding: gzip`, `Content-Type: application/json`). It is 16 MiB at most, else 413. The server decodes it as a scan `result` line before it keeps it, else 400. A result PUT for a job in a final state gives 409.
- `GET …/result` returns the gzip bytes with `Content-Encoding: gzip` and `Content-Type: application/json`. It gives 404 when the job has no result.
- The server keeps the last 3 jobs with a result for each node, and deletes results older than 7 days.
- The audit log has an entry for each scan request and each cancel.

**Control message.** `POST …/disk-scans` puts a `disk_scan` message on the control queue of the node:

```json
{ "id": "uuid", "kind": "disk_scan",
  "payload": { "job_id": "uuid", "root": "/", "max_depth": 6, "min_bytes": 50000000 } }
```

The payload has no `max_depth`, `min_bytes` or `min_frac` key when the request did not set it. Then the sampler uses its default. A `min_frac` of `0` is sent.

The sampler runs one scan at a time. It posts a progress line each 2 s, and the final `error` or `cancelled` line, or it PUTs the result.

---

### 3.2 Alert channels

Each org has its own alert channels. When the alerter sends an alert (the notify flags of the settings and of the node, the snooze and the cooldown apply), the server sends it to each enabled channel whose route matches. It sends to all channels at the same time. A failed send gets 2 more attempts after 1 s and 3 s. A 4xx reply (not 408 or 429) and an SMTP 5xx reply are final.

| `kind` | `target` | What the server sends |
|---|---|---|
| `slack` | incoming webhook URL | `text` and one attachment in the color of the tint |
| `discord` | webhook URL | one embed in the color of the tint, no mentions |
| `pagerduty` | the routing key (32 characters) of an Events API v2 integration | `trigger` with severity `warning` or `critical`; a change to ok sends `resolve`. `dedup_key` is `<org>:<node>:<metric>`. |
| `webhook` | URL | `AlertPayload` (below) |
| `email` | 1 to 20 addresses, comma-separated | plain text mail through `notify.smtp`. The kind is available only when `notify.smtp.host` is set, or when an extension gives a mailer (`app.WithMailer`; the cloud does this). |

**Route.** `minTint` is `warn` (the default) or `critical`. A change back to ok matches when the tint that it ends matches. `metrics` (`cpu`, `mem`, `disk`, `reachability`) and `tags` (the node must have one of them) are empty for all.

**Webhook body.** `AlertPayload` is `{ v: 1, type: "alert" | "test", eventId, orgId, nodeId, nodeName, tags, metric, from, to, value, at, title, text }`. `value` is a ratio (0..1) for `cpu`, `mem` and `disk`, and the seconds since the last sample for `reachability`. The header `X-Towertail-Event` is the type. With a secret, the server also sends `X-Towertail-Timestamp` (Unix seconds, the time of the send) and `X-Towertail-Signature: sha256=<hex>`, the HMAC-SHA256 of `<timestamp>.<body>` with the secret. Check the signature, and refuse a timestamp older than 5 minutes.

**Private targets.** With `notify.block_private_targets=true` (the cloud), the server does not connect to loopback, private, link-local, CGNAT (`100.64.0.0/10`) or multicast addresses. It checks the address after the DNS lookup. It does not follow redirects.

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

The server's [`wire`](../server/wire/wire.go) package is the source of truth. Notable cross-module shapes:

- `Sample` — identical to `pkg/schema/sample.go` Sample in [towertail/sampler](https://github.com/towertail/sampler), just relaxed around optional fields.
- `Event` — `{ id, ts, kind, nodeId, metric?, tint? }` where `kind` ∈ `threshold_crossed | reachability_changed | sampler_version` and `tint` ∈ `ok | warn | critical`.
- `Node` — camelCase mirror of the Swift `Node` struct (`apps/mac/Sources/State/Node.swift`).
- `ServerSettings` — camelCase mirror of the Swift `ServerSettings` class. The server adds `notifyCooldownSeconds`: the minimum time between two alerts with the same node, metric and tint (0 means 600).
- `Alert` — `{ id, node_id, kind, metric, tint, ts, value, acked_by?, acked_at?, snoozed_until? }`. `id` is the event id. An alert is open while the last event of its node and metric has a tint other than `ok`. `metric` is `reachability` when a node sends no sample for 3 intervals. `AlertList` is `{ alerts: [Alert] }`.
- `Channel` — `{ id, kind, name, enabled, target, hasSecret, route: { minTint?, metrics?, tags? }, createdAt }`. An alert channel of the org (§3.2).
- `ChannelRequest` — `{ kind, name, enabled?, target?, secret?, route }`. On PUT, a missing `enabled`, `target` or `secret` keeps the stored value. `"secret": ""` removes the secret. `ChannelTestResponse` is `{ ok, error? }`.
- `AlertPayload` — the body of a generic webhook (§3.2).
- `LoginRequest`, `LoginResponse`.
- `EnrollRequest`, `EnrollResponse`.
- `ControlNextResponse`, `KillProcessPayload`, `DiskScanPayload`.
- `DiskScanRequest`, `DiskScanJob`, `DiskScanProgressResponse`. The scan lines are `pkg/schema/scan.go` in [towertail/sampler](https://github.com/towertail/sampler).
- `NodeHistoryPoint`, `NodeHistoryResponse` — `{ node_id, metric, resolution, clamped?, points: [{ ts, value, max? }] }`. `metric` is `cpu`, `mem`, `net_rx`, `net_tx`, `disk_read` or `disk_write`. The length of the range selects the table: 6 h or less reads the raw samples, 7 d or less the 1 min rollup, 30 d or less the 5 min rollup, else the 1 h rollup. A `step` larger than the bucket of the table groups the points. `resolution` is `raw` (one point for each sample) or the bucket, for example `5m0s`.
- `NodeProcessesResponse` — `{ node_id, from, to, clamped?, processes: [TopProcess] }`. `TopProcess` is `{ name, cmd, cpu_pct_avg, cpu_pct_max, rss_max }`, the highest `cpu_pct_avg` first. A process is a name and a cmd. 1 h or less reads each sample. 7 d or less reads the top 10 of each minute. Longer ranges read the top 5 of each hour. `limit` is 10 by default and 50 at most.
- `NodeDisksResponse` — `{ node_id, from, to, resolution, clamped?, mounts: [{ mount, fs, points: [{ ts, used, total }] }] }`. Each point is the last value in its bucket. The bucket gives about 240 points, 1 min at least. The disk rows stay 7 days.
- History range: `from` and `to` are RFC 3339 or `now`, `now-1h`. The default `to` is now. The default range is 1 h (history, processes) or 24 h (disks). The plan of the org can limit the history days. Then the server moves `from` to the oldest allowed time and sets `clamped: true`.

---

## 6. Schema versioning

- `Sample.v` tracks the sampler schema (see [`wire-format.md`](https://github.com/towertail/sampler/blob/main/docs/wire-format.md) §11). Server rejects samples with unknown `v`; it ingests `v=1` today.
- REST + WS do not carry a version field. Breaking changes bump the path prefix (`/v2`).
- Bolt and ClickHouse storage are internal; schema migrations run on boot.
