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
| Client ↔ Server stream | WSS, text frames | JSON `WSMessage` envelopes. Query `?lease=1` selects lease mode (§4) | `Authorization: Bearer <user_token>` at connect |

Error envelope for every REST 4xx/5xx:

```json
{ "error": { "code": "not_found", "message": "...", "request_id": "..." } }
```

Codes: `unauthorized`, `forbidden`, `not_found`, `bad_request`, `conflict`, `plan_limit` (402, a new node above the plan limit of a cloud org), `rate_limited`, `unavailable`, `not_implemented`, `internal`.

---

## 2. Authentication

Five token kinds, all opaque strings with a `tt_` prefix. Raw tokens are only ever returned at issue time; the server stores SHA-256 hashes.

| Kind | Issued by | Allowed groups |
|---|---|---|
| `user` | `POST /v1/auth/login` | `/v1/*` except ingest + control |
| `admin` | `towertail-server token issue --kind=admin` (CLI) | everything in `user` + token management |
| `api` | `POST /v1/api-keys`, or `towertail-server token issue --kind=api` | The read endpoints of §3 and `/mcp`. The key reads as a `viewer`. It gets 403 on the stream, on the write routes and on `/v1/api-keys`. Each key has a budget of 5 requests each second with a burst of 20 (`http.api_key_rate_limit_rps` and `http.api_key_rate_limit_burst`). A call above the budget gets 429 `rate_limited`. The per-IP limit also applies |
| `sampler` | `POST /v1/sampler/enroll`, `POST /v1/sampler/join` | `/v1/ingest/*`, `/v1/control/next`, `/v1/disk-scans/{job}/progress`, `/v1/disk-scans/{job}/result` |
| `join` | `POST /v1/sampler/join-tokens` | `POST /v1/sampler/join` only (in the body). The token starts with `tt_join_`, works for 1 hour and for one call. Any bearer route gives 403 for it |

Sampler tokens are bound to one `node_id`. Ingest/control endpoints reject tokens without a binding.

### Roles

A user has a role in an org. The roles from the highest to the lowest rank are `owner`, `admin`, `member` and `viewer`. A role allows each route of the same rank or a lower rank. The table in §3 gives the lowest role of each route.

| Role | Can |
|---|---|
| `viewer` | Read nodes, history, processes, disks, settings, alerts, disk scans and the stream. Log out. |
| `member` | Everything of `viewer`, and ack, snooze or unsnooze an alert, snooze a host, mute a metric on a host, and request or cancel a disk scan. |
| `admin` | Everything of `member`, and change nodes and settings, ignore a process, mute a metric for the org, use the channels (also read them, because a channel holds a secret URL), kill a process, enroll samplers, and manage sampler tokens and join tokens. |
| `owner` | Everything of `admin`. An extension (the cloud) uses it for billing and the org. |

Self-hosted mode has no role source. There, each `user` and `admin` token has the `owner` role. A token with no user also has the `owner` role, for example the bootstrap admin token. A server with a role source (the cloud, through `app.WithRoles`) finds the role of the token user in the org of the token. A user with no role in the org gets 403 `no access to this org`. A role that is too low gives 403 "role <role> needed". `sampler` and `join` tokens have no role. An `api` token always has the `viewer` role. When it has a user and the server has a role source, the user must still have a role in the org, else 403.

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
| POST | `/v1/sampler/join` | `JoinRequest` | `EnrollResponse`. No bearer token: the join token is in the body. 401 for an unknown, used or expired token. 402 when the plan is full (the join token stays). 10 calls each minute for each IP |

### User / admin

| Method | Path | Request | Response | Role |
|---|---|---|---|---|
| POST | `/v1/auth/logout` | — | 204 | viewer |
| GET | `/v1/nodes` | — | `[Node]` | viewer |
| POST | `/v1/nodes` | `Node` | `Node` (201) | admin |
| GET | `/v1/nodes/{id}` | — | `Node` | viewer |
| PUT | `/v1/nodes/{id}` | `Node` | `Node` | admin |
| DELETE | `/v1/nodes/{id}` | — | 204 | admin |
| POST | `/v1/nodes/{id}/kill-process?pid=…` | — | 202 | admin |
| GET | `/v1/nodes/{id}/history?metric=cpu&from=…&to=…&step=1m` | — | `NodeHistoryResponse` | viewer |
| GET | `/v1/nodes/{id}/history?metrics=cpu,mem&from=…&to=…&step=1m` | — | `NodeHistoryMultiResponse`. `metrics` is a list of 8 names at most. It replaces `metric`. | viewer |
| GET | `/v1/history?nodes=<ids>&metrics=<names>&from=…&to=…&step=1m` | — | `FleetHistoryResponse`. `nodes` is a list of 200 IDs at most. `metrics` is a list of 8 names at most. The default range is 30 min. A range longer than 24 h gives 400. | viewer |
| GET | `/v1/fleet/hosts?sort=cpu&limit=100` | — | `FleetHosts`: each node with the latest values of the last 10 minutes, the highest first. `sort` is `cpu`, `mem`, `disk` or `net` (default `cpu`). `limit` is 1 to 500. A node with no sample in 10 minutes has `online: false` and no values, and is last. | viewer |
| GET | `/v1/fleet/processes?by=cpu&from=…&to=…&node=<id>&limit=20` | — | `FleetProcesses`: the top processes of the fleet, or of one host with `node`. `by` is `cpu` or `mem` (default `cpu`). The default range is 1 h. The plan limit moves `from` and sets `clamped`. `limit` is 1 to 50. | viewer |
| GET | `/v1/fleet/compare?metric=cpu&window=1h&node=<id>` | — | `Compare`: the average and the highest value of each host in the last `window` and in the window before it. `metric` is `cpu`, `mem`, `net_rx`, `net_tx`, `disk_read` or `disk_write`. `window` is a Go duration from 15m to 720h. The plan limit moves the start of each window and sets `clamped`. A previous window fully outside the plan is empty: it has `from` equal to `to`, and no host has `previous`. | viewer |
| GET | `/v1/api-keys` | — | `[APIKey]`: an admin or owner sees all keys of the org. Other roles see their own keys. | viewer |
| POST | `/v1/api-keys` | `APIKeyRequest` | `APIKeyCreated` (201). The `token` shows only this one time. 409 when the user has 20 keys. An `api` token gets 403. | viewer |
| DELETE | `/v1/api-keys/{id}` | — | 204. 404 for an unknown ID or a token of another kind. 403 when a non-admin revokes the key of another user. | viewer |
| GET | `/v1/nodes/{id}/processes?from=…&to=…&limit=10&by=cpu` | — | `NodeProcessesResponse`. `by` is `cpu` or `mem`. | viewer |
| GET | `/v1/nodes/{id}/disks?from=…&to=…` | — | `NodeDisksResponse` | viewer |
| GET | `/v1/settings` | — | `ServerSettings` | viewer |
| PUT | `/v1/settings` | `ServerSettings` | `ServerSettings` | admin |
| GET | `/v1/alerts` | — | `AlertList`: the open alerts of the org, the newest first | viewer |
| GET | `/v1/alerts/recent?days=7` | — | `AlertList`: the alert events of the org in the last `days` (1 to 90, default 7), the newest first, 200 at most. A change back to ok has the tint `ok`. The plan history limit can move the start later. Then `clamped` is `true` | viewer |
| POST | `/v1/alerts/{id}/ack` | — | `Alert` (404 if the alert is not open, 503 if ClickHouse is not ready). An ack stops all notifications of the alert, except the one for the return to ok. | member |
| DELETE | `/v1/alerts/{id}/ack` | — | `Alert`. Takes the ack back. 404 if the alert is not open. | member |
| POST | `/v1/alerts/{id}/snooze?until=RFC3339` | — | `Alert` (400 if `until` is not RFC3339 or not in the future, 404 if the alert is not open). A snooze stops all notifications of the alert until `until`. When it ends and the alert is still open, the server sends one reminder. | member |
| DELETE | `/v1/alerts/{id}/snooze` | — | `Alert`. Takes the snooze back. 404 if the alert is not open. | member |
| POST | `/v1/alerts/{id}/ignore?scope=host\|all` | — | `Alert`. For a `long_process` alert only: adds `proc.name` to `Node.ignoredProcs` (`host`) or to `ServerSettings.longProc.ignore` (`all`). The open alert closes with no notification. 400 for another kind or a bad scope, 404 if the alert is not known. | admin |
| DELETE | `/v1/alerts/{id}/ignore?scope=host\|all` | — | `Alert`. Removes the name from the list. | admin |
| POST | `/v1/nodes/{id}/snooze?until=RFC3339` | — | `Node`. Sets `snoozedUntil`. 400 if `until` is not RFC3339 or not in the future, 404 if the node is not known. | member |
| DELETE | `/v1/nodes/{id}/snooze` | — | `Node`. Clears `snoozedUntil`. | member |
| PUT | `/v1/mutes` | `{ metric, nodeId?, muted }` | `Node` (with `nodeId`) or `ServerSettings` (without). `metric` is `cpu`, `mem`, `disk` or `long`. Sets or clears the mute. A muted open alert closes with no notification and a `reason` of `muted` in the event payload. A host mute needs `member`, an org mute needs `admin` (403). 400 for an unknown metric, 404 if the node is not known. | member |
| GET | `/v1/channels` | — | `[Channel]` | admin |
| POST | `/v1/channels` | `ChannelRequest` | `Channel` (201, 400 if not valid, 402 if the plan does not permit the kind) | admin |
| GET | `/v1/channels/{id}` | — | `Channel` | admin |
| PUT | `/v1/channels/{id}` | `ChannelRequest` | `Channel` | admin |
| DELETE | `/v1/channels/{id}` | — | 204 | admin |
| POST | `/v1/channels/{id}/test` | — | `ChannelTestResponse` (200, also when the send fails) | admin |
| POST | `/v1/sampler/enroll` | `EnrollRequest` | `EnrollResponse` (see §3.0.1) | admin |
| POST | `/v1/sampler/join-tokens` | `JoinTokenRequest` | `JoinTokenResponse` (201). 402 when the plan is full | admin |
| GET | `/v1/sampler/versions` | — | `{ "linux-amd64": "sha256:…", … }` | viewer |
| GET | `/v1/sampler/tokens` | — | `[SamplerToken]`: the sampler tokens of the org, with no secret | admin |
| DELETE | `/v1/sampler/tokens/{id}` | — | 204. The node stays. Its sampler cannot send again until it enrolls again | admin |
| POST | `/v1/nodes/{id}/disk-scans` | `DiskScanRequest` | `DiskScanJob` (202) | member |
| GET | `/v1/nodes/{id}/disk-scans` | — | `{ "jobs": [DiskScanJob] }`, the newest first | viewer |
| GET | `/v1/disk-scans/{job}` | — | `DiskScanJob` | viewer |
| GET | `/v1/disk-scans/{job}/result` | — | the scan result line, gzip (see §3.1) | viewer |
| POST | `/v1/disk-scans/{job}/cancel` | — | `DiskScanJob` | member |

### Sampler-only

| Method | Path | Request | Response |
|---|---|---|---|
| POST | `/v1/ingest/samples` | NDJSON of `Sample` lines (optionally gzipped) | 202 `IngestResponse`, 402 `IngestPlanFull` or 410 `IngestGone` (see §3.0) |
| GET | `/v1/control/next` | — | `ControlNextResponse` or 204 |
| POST | `/v1/disk-scans/{job}/progress` | one scan line: `progress`, `error` or `cancelled` | `{ "cancel": bool }` |
| PUT | `/v1/disk-scans/{job}/result` | the scan `result` line, gzip, 16 MiB at most | 204 |

A sampler token can use the disk-scan endpoints only for jobs of its own node. Other jobs give 403.

The sampler sends `net.rx_bps` and `net.tx_bps` as 0, with the counters `rx_cum` and `tx_cum`. At ingest, the server sets the two rates from the counter delta to the last sample of the node. The first sample after a server start, a gap over 5 minutes and a counter reset give 0. The stored row and the stream frames have the rates.

### 3.0 Ingest reply (orders for the sampler)

The server sends its orders for a sampler in the reply to `POST /v1/ingest/samples`. The sampler needs no WebSocket and no extra poll. The server checks the node of the token before it reads the body.

| Status | Body | The sampler must |
|---|---|---|
| 202 | `{ "accepted": 3, "rejected": 0, "error": "…", "interval": "5s", "update": "0.7.1", "proc_cwd": true }` | Go on. `error` is the first reject reason and is left out when empty. |
| 402 | `{ "retry_after": 3600 }` and the header `Retry-After: 3600` | Send nothing for `retry_after` seconds, then try again. |
| 410 | `{ "action": "exit", "reason": "host_removed" }` | Stop and exit. Do not start again. |
| 401 and 5xx | the error envelope | Retry with a back-off. A revoked token gives 401. |

- `interval` is a Go duration. The sampler collects at this interval until a later reply says other. An order of the host (for example from the cloud plan) wins. Else the server sends `stream.live_interval` (default `5s`) while a live view lease covers the host, and `stream.idle_interval` (default `30s`) when no lease covers it. The server sends the key in each reply, so the sampler keeps no state. It is left out when the server has no order. A new interval starts at the next post after a collection, so it can take up to one old interval to apply.
- `update` is the version that the sampler should run. The server sets it when `sampler.latest_version` is set and is newer than the version in the header `User-Agent: towertail-sampler/<version>`. A sampler with no such header gets no `update`.
- `proc_cwd` tells the sampler whether to send the working folder, repo and parent of the processes. The server sends the key in each 202 reply. It is `false` when the node has `hideProcFolders: true`. A sampler that does not know the key ignores it.
- 410 `reason` is `host_removed` (the node does not exist), `cloud_off` (the node has `enabled: false`) or `account_deleted`. Towertail Cloud sends `account_deleted` when the account of the node is deleted. The token stays in a tombstone for 90 days. The self-host server does not send it.
- 402 means that the plan is full. The nodes of the org are sorted by `createdAt`, then id. A node at or above the `MaxNodes` rank gets 402. The oldest hosts keep their place.
- The order bodies are flat JSON. They do not use the error envelope.

### 3.0.1 Enroll and join

`POST /v1/sampler/enroll` and `POST /v1/sampler/join` bind a sampler to a node and give it a token. The rules are the same:

- `node_id` set and the node exists: bind to it.
- `node_id` set and the node does not exist: make the node with that ID (the Mac app sends its local node ID). A full plan gives 402.
- `node_id` empty and `machine_id` set: bind to the node of the org with that `machineId`. If none exists, make a node and keep the `machine_id` in it. A second install on the same host makes no second node. `/v1/sampler/join` has no `node_id`.
- Else make a node. A full plan gives 402.
- `endpoint` in the reply is `http.public_url` of the server config. When it is empty, it is the scheme and host of the request.
- A `PUT /v1/nodes/{id}` body without the `machineId` key keeps the stored value.

A join token is for a host that has no user token. A user makes it with `POST /v1/sampler/join-tokens`, and the host sends it with `POST /v1/sampler/join`. The reply of `join-tokens` is `{ "token": "tt_join_…", "expires_at": "…" }`. The request of `join` is `{ "join_token", "display_name", "machine_id"? }`.

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

**Plan.** The plan of the org can block a kind (in the cloud, only the Team plan gives Slack, Discord, PagerDuty and webhooks; email is open on all plans). A create, a test, and an update that changes the kind to a blocked kind give 402 `plan_limit`. An update that keeps the kind passes, so the owner can edit or disable a blocked channel. `Channel` has `blocked: true` for a blocked kind, and the server sends no alert to that channel. A new plan brings it back.

**Route.** `minTint` is `warn` (the default) or `critical`. A change back to ok matches when the tint that it ends matches. `metrics` (`cpu`, `mem`, `disk`, `reachability`, `long_proc`) and `tags` (the node must have one of them) are empty for all.

**Webhook body.** `AlertPayload` is `{ v: 1, type: "alert" | "test", eventId, orgId, nodeId, nodeName, tags, metric, from, to, value, at, title, text }`. `value` is a ratio (0..1) for `cpu`, `mem` and `disk`, the seconds since the last sample for `reachability`, and the CPU percent of the process (top convention) for `long_proc`. For `long_proc`, `text` names the process, for example "OrbStack Helper (pid 812) at 85% CPU for 32m.". The header `X-Towertail-Event` is the type. With a secret, the server also sends `X-Towertail-Timestamp` (Unix seconds, the time of the send) and `X-Towertail-Signature: sha256=<hex>`, the HMAC-SHA256 of `<timestamp>.<body>` with the secret. Check the signature, and refuse a timestamp older than 5 minutes.

**Private targets.** With `notify.block_private_targets=true` (the cloud), the server does not connect to loopback, private, link-local, CGNAT (`100.64.0.0/10`) or multicast addresses. It checks the address after the DNS lookup. It does not follow redirects.

## 4. WebSocket messages

Endpoint: `/v1/stream`. Client presents bearer token in the `Authorization` header on the upgrade request.

A connection has one of two modes. The client selects the mode at connect.

| Mode | Query | Sample data |
|---|---|---|
| Lease | `?lease=1` | Only for the hosts that the client has leased (a `view` frame). A short `summary` for each host. A `detail` for the open tab. |
| Legacy | none | The full `sample` of each node, as before. Old apps and old web builds use this mode. |

The mode does not change after connect. The server cannot stop the replay of full samples after the first frame, so a `view` frame does not switch a legacy connection to lease mode.

The config key `stream.legacy_samples` (env `TT_STREAM__LEGACY_SAMPLES`, default `true`) keeps the legacy mode. With `false`, a legacy client gets events only and no replay.

The key `stream.compression` (env `TT_STREAM__COMPRESSION`, default `no_context_takeover`) sets the WebSocket compression. The server offers `permessage-deflate` without context takeover. Frames under 512 B are not compressed. A client that does not offer `permessage-deflate` gets plain frames. With `off`, the server does not offer it.

The Mac app sends an empty `Sec-WebSocket-Extensions` header, so it gets plain frames. `URLSessionWebSocketTask` fails with "Protocol error" on the frame after a deflated frame. The cloud sets `off`, because older Mac releases offer `permessage-deflate`.

### Client → server

```json
{ "type": "subscribe", "node_ids": ["uuid", "uuid"] }   // legacy mode only
{ "type": "view", "hosts": ["uuid"], "detail": { "host": "uuid", "tab": "processes" }, "ttl": 60 }  // lease mode only
{ "type": "ping" }
```

**`subscribe`** (legacy mode only). It narrows the `sample`, `event` and `disk_scan_updated` frames to a set of nodes. An empty `node_ids` removes the filter. In lease mode the server ignores it.

**`view`** (lease mode only). It sets the lease of the connection. A new `view` replaces the old one. The client sends it every 30 s while the user sees the data, and sends an empty view (`{ "type": "view" }`) at once when the data is hidden. On a legacy connection the server ignores it.

| Key | Meaning |
|---|---|
| `hosts` | The node IDs that the client watches. At most 500; the server cuts a longer list. |
| `detail` | Optional. The one host and tab that the client has open. The host counts as a leased host. A tab that is not in the table below is dropped. |
| `ttl` | Seconds that the lease lasts. Default 60 (when missing or 0). The server clamps it to 10 .. 120. |

The lease ends when `ttl` passes without a new `view` (sleep, crash, lost network), or when the client sends an empty view, or when it disconnects. A restart of the server ends all leases: the client sends `view` again after it reconnects.

### Server → client

Every frame is a `WSMessage` with a `type` discriminator:

```json
{ "type": "sample",           "nodeId": "uuid", "sample": { ... }  }
{ "type": "event",            "event":  { ... } }
{ "type": "alert_updated",    "nodeId": "uuid", "alert": { Alert } }
{ "type": "node_updated",     "node":   { ... } }
{ "type": "settings_updated", "settings": { ... } }
{ "type": "disk_scan_updated", "nodeId": "uuid", "job": { DiskScanJob } }
{ "type": "pong" }
{ "type": "hello",   "hello":   { "lease": true, "renew_s": 30, "max_ttl_s": 120 } }
{ "type": "summary", "nodeId": "uuid", "summary": { LiveSummary } }
{ "type": "detail",  "nodeId": "uuid", "tab": "processes", "detail": { DetailParts } }
{ "type": "lease",   "lease":   { "state": "ended", "reason": "limit" } }
```

`sample` is for legacy mode. `hello`, `summary`, `detail` and `lease` are for lease mode.

Events (`event`, `alert_updated`, `node_updated`, `settings_updated`, `disk_scan_updated`) always reach a lease client, with or without a lease. A legacy client keeps its `subscribe` filter. `disk_scan_updated` goes to the clients that see the node. The server sends it on each state change, and for progress at most once each 2 s for each job.

**`hello`.** The first frame of a lease connection. `renew_s` is the time between two `view` frames. `max_ttl_s` is the largest `ttl`. A client that asked for lease mode and gets no `hello` knows that the server is old. Then it uses the `sample` frames.

**`summary`.** About 200 bytes. The server makes it one time for each sample, and sends it to each client whose lease covers the node (a host in `hosts`, or the detail host).

| Key | Meaning |
|---|---|
| `ts` | The `ts` of the sample. |
| `cpu` | Percent, 0..100 (`cpu.pct`). |
| `mem` | Used / total, ratio 0..1. |
| `disk` | The fullest mount, ratio 0..1. |
| `rx`, `tx` | Bytes per second. |
| `load` | `cpu.load_1`. |
| `procs` | Process count. Missing when the sample has no process count. |
| `health` | Short health data (`HealthBrief`: the keys of `health` without `zombie_parents`, plus `inodes`, the worst inode ratio of all mounts). Only when it changed since the last summary of the node, and in the replay frame. The client keeps the last value. |
| `host` | Slow facts: `os`, `arch`, `kernel`, `cores`, `mem_total`, `uptime_s`, `sampler`. Only in the replay frame and after a change of the facts (`uptime_s` does not count). The client keeps the last value. |

Ratios have 4 decimals.

**`detail`.** Sent to a client whose lease has `detail.host` equal to the node, in addition to the summary. It has the `ts` and the parts of the sample for the open tab. The keys have the names and shapes of the same keys in the sample. A sample that has none of the parts of the tab gives no `detail` frame (for example, the sampler sends `ports` only each `--ports-interval`).

| `tab` | Keys of `detail` |
|---|---|
| `processes` | `procs` |
| `ports` | `ports` |
| `disks` | `disks`, `disk_io`, `procs` |
| `health` | `health`, `procs` |

**`lease`.** `state` is `ended`. `reason` is `limit` when the server ends the lease because of a limit of leases for each user. The server ends the lease with the oldest renewal of the user when the user starts a new lease over the limit. The new lease works. The client does not renew on its timer after `limit`. It sends a new `view` only after a user action (the window or tab becomes visible, a click, a change of host). This rule stops two visible clients from ending each other in turns. A server with no limit never sends this frame.

**Replay.**

- Legacy mode: on connect the server immediately replays the most recent `sample` for every node the client can see (nothing when `stream.legacy_samples` is `false`), then begins streaming new frames as they arrive.
- Lease mode: on connect the server sends only `hello`. After each `view` that changes the lease, the server replays the last `summary` (with `host` and `health`) of each host that the old lease did not cover. If the detail host or tab changed, it also replays the last `detail` of the tab. For `ports` this is the last sample that had ports. A `view` that renews the same lease sends no replay.
- Event replays are not implemented today (clients refetch `/v1/alerts` at connect).

---

## 5. Go types (reference)

The server's [`wire`](../server/wire/wire.go) package is the source of truth. Notable cross-module shapes:

- `Sample` — identical to `pkg/schema/sample.go` Sample in [towertail/sampler](https://github.com/towertail/sampler), just relaxed around optional fields.
- `Event` — `{ id, alertId, ts, kind, nodeId, metric?, tint?, notify?, title?, text? }`. `alertId` is the alert id (see `Alert`). `notify` is `true` when the server sends a notification for the event, so a client can show its own notification. `title` and `text` are the words of that notification. `kind` ∈ `threshold_crossed | reachability_changed | sampler_version | long_process` and `tint` ∈ `ok | warn | critical`.
- `Node` — camelCase mirror of the Swift `Node` struct (`apps/mac/Sources/State/Node.swift`). The server also sets `createdAt` (it keeps the first value on each PUT) and `machineId` (optional, from enroll or join). `mutedSignals` (a sorted list of `cpu`, `mem`, `disk` and `long`; unknown names are skipped; always sent, `[]` when empty) mutes these metrics on the node. A `PUT /v1/nodes/{id}` body without `mutedSignals` keeps the stored list. `customLongProc` (a `LongProcRule`, optional) replaces the org rule for the node. `ignoredProcs` (process names) is added to the org ignore list. `hideProcFolders` (bool, default `false`) tells push samplers not to send process folders (see §3.0, `proc_cwd`). A `PUT /v1/nodes/{id}` body without the `hideProcFolders` key keeps the stored value. A `PUT /v1/nodes/{id}` body without the `ignoredProcs` key keeps the stored `ignoredProcs` and `customLongProc`, so an old client does not clear them. A current client always sends `ignoredProcs`.
- `ServerSettings` — camelCase mirror of the Swift `ServerSettings` class. The server adds `notifyCooldownSeconds`: the minimum time between two alerts with the same node, metric and tint (0 means 600). `longProc` is the org `LongProcRule`. A `PUT /v1/settings` body without `longProc` keeps the stored rule. `muted` is the list of metrics muted for the org (same form as `Node.mutedSignals`). The effective mutes of a node are the union of both lists. A `PUT /v1/settings` body without `muted` keeps the stored list.
- `LongProcRule` — `{ enabled, cpuPct, sustainSeconds, notify, ignore }`. Defaults: `true`, `50`, `1800`, `"all"`, `[]`. `cpuPct` is 10..800 (top convention), `sustainSeconds` is 60..86400, `notify` is `"off"` or `"all"`. A missing or bad field gets its default. A process (key `pid` + `start_ts`) is flagged when, over the last `sustainSeconds`, it is at or above `cpuPct` for at least 80% of the time. A process that is not in the sample counts as low. Names in the ignore lists match without case. The server emits a `long_process` event with tint `warn` when a process is flagged, and with tint `ok` when it is cleared (also when the node becomes unreachable; that event sends no notification). The event payload is `{ metric: "long_proc", previous, value, pid, name, cmd, cpu_pct, high_since, start_ts, cwd?, repo?, parent? }`. The alert text names the process with its folder and script, for example `node · sling (.wt/SLING-40) · vitest run (pid 812) at 85% CPU for 32m.`. `notify: "off"` keeps the events but sends no notification.
- `Alert` — `{ id, node_id, kind, metric, tint, ts, value, acked_by?, acked_at?, snoozed_until?, proc? }`. `id` is the alert id: the event id of the change out of `ok`. All events of one incident (until the change back to `ok`) have this id. `acked_by` is the user ID when the token has a user, else the token ID. An alert is open while the last event of its node and metric has a tint other than `ok`. `metric` is `reachability` when a node sends no sample for 3 intervals, or no first sample 10 minutes after it enrolled. The stored event payload has `reason` (`stopped_reporting` or `never_reported`) and `since`. When the node reports again, `value` is the down time in seconds. The server also stores a `registered` event for each new node. It is not an alert and no client gets it. For `kind: long_process`, `metric` is `long_proc/<pid>/<start_unix>`, so each process is its own alert, and `value` is the CPU percent. For `kind: long_process`, `proc` is `{ pid, name, cmd, start_ts?, high_since?, cpu_pct, cwd?, repo?, parent? }` from the event payload. `cpu_pct` and `value` are in the top convention (100 is one core). `proc` is absent for the other kinds, and for an event with no process in its payload. `AlertList` is `{ alerts: [Alert], clamped? }`. `clamped` is `true` on `/v1/alerts/recent` when the plan history limit moved the start later.
- `Channel` — `{ id, kind, name, enabled, target, hasSecret, route: { minTint?, metrics?, tags? }, createdAt, blocked? }`. An alert channel of the org (§3.2).
- `ChannelRequest` — `{ kind, name, enabled?, target?, secret?, route }`. On PUT, a missing `enabled`, `target` or `secret` keeps the stored value. `"secret": ""` removes the secret. `ChannelTestResponse` is `{ ok, error? }`.
- `AlertPayload` — the body of a generic webhook (§3.2).
- `ClientMessage` (`SubscribeMessage` is an alias), `StreamHello`, `LiveSummary`, `HealthBrief`, `HostFacts`, `DetailTab`, `ViewDetail`, `DetailParts`, `LeaseNotice` — the lease mode of the stream (§4).
- `LoginRequest`, `LoginResponse`.
- `EnrollRequest`, `EnrollResponse`, `JoinTokenRequest`, `JoinTokenResponse`, `JoinRequest`. `SamplerToken` is `{ id, label, nodeId?, createdAt, lastUsedAt? }`.
- `ControlNextResponse`, `KillProcessPayload`, `DiskScanPayload`.
- `DiskScanRequest`, `DiskScanJob`, `DiskScanProgressResponse`. The scan lines are `pkg/schema/scan.go` in [towertail/sampler](https://github.com/towertail/sampler).
- `NodeHistoryPoint`, `NodeHistoryResponse` — `{ node_id, metric, resolution, clamped?, points: [{ ts, value, max? }] }`. `metric` is `cpu`, `mem`, `net_rx`, `net_tx`, `disk_read`, `disk_write` or `procs`. The length of the range selects the table: 6 h or less reads the raw samples, 7 d or less the 1 min rollup, 30 d or less the 5 min rollup, else the 1 h rollup. A `step` larger than the bucket of the table groups the points. `resolution` is `raw` (one point for each sample) or the bucket, for example `5m0s`.
- `NodeProcessesResponse` — `{ node_id, from, to, clamped?, processes: [TopProcess] }`. `TopProcess` is `{ name, cmd, repo?, cwd?, parent?, cpu_pct_avg, cpu_pct_max, rss_max }`, the highest `cpu_pct_avg` first. A process is a name, a cmd and a repo. `cwd` and `parent` come only from ranges of 1 h or less. 1 h or less reads each sample. 7 d or less reads the top 10 of each minute. Longer ranges read the top 5 of each hour. `limit` is 10 by default and 50 at most.
- `NodeDisksResponse` — `{ node_id, from, to, resolution, clamped?, mounts: [{ mount, fs, points: [{ ts, used, total }] }] }`. Each point is the last value in its bucket. The bucket gives about 240 points, 1 min at least. The disk rows stay 7 days.
- `NodeHistoryMultiResponse` — `{ node_id, series: { <metric>: [{ ts, value, max? }] }, from, to, resolution, clamped? }`. One query gives all metrics, with the same table and `step` rules as `NodeHistoryResponse`. `FleetHistoryResponse` — `{ from, to, resolution, clamped?, nodes: [{ node_id, series }] }`. The org of the token filters the nodes. A node with no points in the range, or of another org, has no entry in `nodes`. The 24 h limit uses the requested range, before the plan limit. The plan limit then moves `from` and sets `clamped`.
- The `procs` metric is the process count (`procs_total`). It has raw samples only, so it works for ranges of 6 h or less. For a longer range, a request with `procs` gives 400 `metric has no rollup for this range`. An unknown metric gives 400 `unknown metric`.
- `FleetHosts` — `{ hosts: [{ node_id, name, tags?, online, last_seen?, cpu?, mem?, disk?, load1?, net_rx?, net_tx? }] }`. `cpu` is a percent. `mem` and `disk` are ratios 0..1 (`disk` is the fullest mount). `net_rx` and `net_tx` are bytes each second. `FleetProcesses` — `{ from, to, clamped?, by, processes: [{ node_id, node_name, name, cmd, repo?, cwd?, parent?, cpu_pct_avg, cpu_pct_max, rss_max }] }`. A process is a node, a name, a cmd and a repo. `cwd` and `parent` come only from ranges of 1 h or less.
- `Compare` — `{ metric, current: { from, to }, previous: { from, to }, clamped?, hosts: [{ node_id, name, current: { avg, max, points }, previous?, delta_avg? }] }`. `delta_avg` is the current average minus the previous average. It is left out when a window has no data for the host. The hosts with the biggest change are first. On a rollup table, `avg` is the mean of the bucket averages. `max` is the highest bucket max. Only `cpu` has a max in the rollups, so for the other metrics `max` is the highest bucket average.
- `APIKey` — `{ id, label, userId?, createdAt, lastUsedAt? }`. `lastUsedAt` is updated at most one time in 5 minutes. `APIKeyCreated` is `APIKey` with `token`. `APIKeyRequest` is `{ label }`, 1 to 80 characters.
- History range: `from` and `to` are RFC 3339 or `now`, `now-1h`. The default `to` is now. The default range is 1 h (history, processes) or 24 h (disks). The plan of the org can limit the history days. Then the server moves `from` to the oldest allowed time and sets `clamped: true`.

---

## 6. Schema versioning

- `Sample.v` tracks the sampler schema (see [`wire-format.md`](https://github.com/towertail/sampler/blob/main/docs/wire-format.md) §11). Server rejects samples with unknown `v`; it ingests `v=1` today.
- REST + WS do not carry a version field. Breaking changes bump the path prefix (`/v2`).
- Bolt and ClickHouse storage are internal; schema migrations run on boot.

---

## 7. MCP

The server has a read-only [Model Context Protocol](https://modelcontextprotocol.io) server at `POST /mcp`. It is part of the Go server, so a self-hosted server has it too. The cloud shows it at `https://api.towertail.com/mcp`. `TT_MCP__ENABLED=false` (`mcp.enabled`) turns it off. Then `/mcp` gives 404.

- **Transport:** streamable HTTP in stateless mode with JSON replies. Send `Content-Type: application/json` and `Accept: application/json, text/event-stream`. Each reply is one JSON body. There is no session and no stream. `GET` and `DELETE` give 405.
- **Auth:** `Authorization: Bearer tt_...` with an `api` token. `user` and `admin` tokens also pass. Other kinds give 403. The role is `viewer`. The key budget of §2 applies (429 `rate_limited`). OAuth is not available yet.
- **Reads:** the tools call the same query code as the fleet routes of §3, so the plan history limit is the same. A tool that reads history sets `clamped: true` when the limit moved the start later.
- **Errors:** a bad input or a failed query is a tool error (`isError: true`) with a short text. A server with no ClickHouse gives the tool error `query: history is off`. A call with no token on the context gives `no token`.
- **Host:** a tool takes a host by ID or by display name (no case). A name that matches two hosts gives an error with the two IDs.
- **Range:** `range` is a Go duration or a number with `d` for days, from `15m` to `365d` (`1h`, `90m`, `7d`). `from` and `to` are RFC 3339 and replace `range`. A `to` in the future moves to now.

| Tool | Input | Output |
|---|---|---|
| `list_hosts` | `sort` (`cpu`, `mem`, `disk`, `net`; default `cpu`), `limit` (1 to 200; default 50) | `hosts`: `id`, `name`, `tags?`, `online`, `last_seen?`, `cpu_pct?`, `mem_ratio?`, `disk_ratio?`, `load1?`, `net_rx_bps?`, `net_tx_bps?`. An offline host has no values. |
| `host_metrics` | `host`, `metrics` (up to 6 of `cpu`, `mem`, `net_rx`, `net_tx`, `disk_read`, `disk_write`; default `cpu`), `range` (default `1h`) or `from` and `to`, `resolution` (`auto`, `raw`, `1m`, `5m`, `1h`; default `auto`) | `host`, `host_id`, `from`, `to`, `clamped?`, `metrics`: for each metric `metric`, `resolution`, `count`, `min`, `avg`, `max`, `last` and `points` (`ts`, `value`, `max?`). |
| `top_processes` | `host` (empty = fleet), `by` (`cpu`, `mem`; default `cpu`), `range` (default `1h`), `limit` (1 to 50; default 20) | `from`, `to`, `by`, `clamped?`, `processes`: `host`, `host_id`, `name`, `command`, `repo?`, `cpu_pct_avg`, `cpu_pct_max`, `rss_max_bytes`. |
| `alerts` | `state` (`open`, `recent`; default `open`), `days` (1 to 90; default 7; `recent` only), `host` (empty = fleet) | `clamped?`, `alerts`: `id`, `host`, `host_id`, `kind`, `metric`, `tint`, `value`, `start`, `acked_by?`, `acked_at?`, `snoozed_until?`. A `recent` alert can have the tint `ok`: the change back to normal. At most 200 recent alerts. |
| `disks` | `host`, `range` (default `24h`) | `host`, `host_id`, `from`, `to`, `clamped?`, `mounts`: `mount`, `fs`, `used_bytes`, `total_bytes`, `ratio`, `change_bytes` (used now minus used at the start). |
| `compare` | `metric` (default `cpu`), `window` (`15m` to `30d`; default `1h`), `host` (empty = fleet) | `metric`, `current`, `previous` (each `from`, `to`), `clamped?`, `total_hosts`, `hosts` (at most 50, the biggest change first): `host`, `host_id`, `current` and `previous?` (`avg`, `max`, `points`), `change_avg?`. |

`host_metrics` gives at most 300 points for each metric. When the range has more, the server raises the step and then merges the points: `value` is the average, `max` is the highest. `min`, `avg`, `max` and `last` come from all the points before the merge. A `count` of 0 means no data.

All tools are read-only. Tools that change a host (for example `kill_process`) are not part of this version.
