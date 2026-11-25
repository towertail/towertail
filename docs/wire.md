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

Six token kinds, all opaque strings with a `tt_` prefix. Raw tokens are only ever returned at issue time; the server stores SHA-256 hashes.

| Kind | Issued by | Allowed groups |
|---|---|---|
| `user` | `POST /v1/auth/login` | `/v1/*` except ingest + control |
| `admin` | `towertail-server token issue --kind=admin` (CLI) | everything in `user` + token management |
| `api` | `POST /v1/api-keys`, or `towertail-server token issue --kind=api` | The read endpoints of §3 and `/mcp`. The key reads as a `viewer`. It gets 403 on the stream, on the write routes and on `/v1/api-keys`. Each key has a budget of 5 requests each second with a burst of 20 (`http.api_key_rate_limit_rps` and `http.api_key_rate_limit_burst`). A call above the budget gets 429 `rate_limited`. The per-IP limit also applies |
| `oauth` | The authorization server of the cloud, after a user allows an AI client | `/mcp` and `/mcp/v1` only. Each other bearer route gives 403 "token kind not allowed". The token lasts 1 hour. It has `scopes`, an `audience` and a `grantId` (see "Scopes" below). It needs a user, else 401 `invalid token`. The role is the role of the user in the org. The key budget applies to each grant |
| `sampler` | `POST /v1/sampler/enroll`, `POST /v1/sampler/join` | `/v1/ingest/*`, `/v1/control/next`, `/v1/disk-scans/{job}/progress`, `/v1/disk-scans/{job}/result` |
| `join` | `POST /v1/sampler/join-tokens` | `POST /v1/sampler/join` only (in the body). The token starts with `tt_join_`, works for 1 hour and for one call. Any bearer route gives 403 for it |

Sampler tokens are bound to one `node_id`. Ingest/control endpoints reject tokens without a binding.

### Roles

A user has a role in an org. The roles from the highest to the lowest rank are `owner`, `admin`, `member` and `viewer`. A role allows each route of the same rank or a lower rank. The table in §3 gives the lowest role of each route.

| Role | Can |
|---|---|
| `viewer` | Read nodes, history, processes, disks, settings, rules, alerts, disk scans and the stream. Log out. |
| `member` | Everything of `viewer`, and ack, snooze or unsnooze an alert, snooze a host, mute a metric on a host, and request or cancel a disk scan. |
| `admin` | Everything of `member`, and change nodes, settings and rules, import and restore rules, ignore a process, mute a metric for the org, use the channels (also read them, because a channel holds a secret URL), kill a process, enroll samplers, and manage sampler tokens and join tokens. |
| `owner` | Everything of `admin`. An extension (the cloud) uses it for billing and the org. |

Self-hosted mode has no role source. There, each `user` and `admin` token has the `owner` role. A token with no user also has the `owner` role, for example the bootstrap admin token. A server with a role source (the cloud, through `app.WithRoles`) finds the role of the token user in the org of the token. A user with no role in the org gets 403 `no access to this org`. A role that is too low gives 403 "role <role> needed". `sampler` and `join` tokens have no role. An `api` token always has the `viewer` role. When it has a user and the server has a role source, the user must still have a role in the org, else 403.

### Scopes

An `oauth` token holds scopes: `fleet:read` (always held), `alerts:write` and `rules:write`. The effective right of a call is the scopes of the token, the role of the user and the org setting `mcp.aiChanges`, all three together. A scope is not enough alone. A `user` or `admin` token holds all scopes, and an `api` token holds `fleet:read` only. The MCP server checks the token kind before it uses a scope, so a `user` token stays read-only at `/mcp`.

The key `mcp` of `ServerSettings` holds the org rules for AI clients:

| Key | Meaning | Default |
|---|---|---|
| `membersConnect` | Members, not only admins, can connect an AI client. | `true` |
| `anyClient` | Any OAuth client can connect, not only known clients. | `true` |
| `apiKeys` | API keys can use `/mcp`. When `false`, an API key gets 403 "API keys are off for MCP in this org". | `true` |
| `signInDays` | Days that an AI client stays signed in: 7, 30 or 90. | `30` |
| `aiChanges` | `off` (read only), `alerts` (also `alerts:write`) or `alerts_and_rules` (also `rules:write`). | `alerts` |

A `PUT /v1/settings` body with another `signInDays` or `aiChanges` gives 400. A body with no `mcp` keeps the stored value. The `settings_updated` message carries the key.

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
| GET | `/v1/nodes/{id}/rules` | — | `NodeRules` | viewer |
| PUT | `/v1/nodes/{id}/rules` | `HostRules` | `Node` | admin |
| DELETE | `/v1/nodes/{id}` | — | 204 | admin |
| POST | `/v1/nodes/{id}/kill-process?pid=…` | — | 202 | admin |
| GET | `/v1/nodes/{id}/history?metric=cpu&from=…&to=…&step=1m` | — | `NodeHistoryResponse` | viewer |
| GET | `/v1/nodes/{id}/history?metrics=cpu,mem&from=…&to=…&step=1m` | — | `NodeHistoryMultiResponse`. `metrics` is a list of 16 names at most. It replaces `metric`. | viewer |
| GET | `/v1/history?nodes=<ids>&metrics=<names>&from=…&to=…&step=1m` | — | `FleetHistoryResponse`. `nodes` is a list of 200 IDs at most. `metrics` is a list of 8 names at most (the node route takes 16). The default range is 30 min. A range longer than 24 h gives 400. | viewer |
| GET | `/v1/fleet/hosts?sort=cpu&limit=100` | — | `FleetHosts`: each node with the latest values of the last 10 minutes, the highest first. `sort` is `cpu`, `mem`, `disk` or `net` (default `cpu`). `limit` is 1 to 500. A node with no sample in 10 minutes has `online: false` and no values, and is last. | viewer |
| GET | `/v1/fleet/processes?by=cpu&from=…&to=…&node=<id>&limit=20` | — | `FleetProcesses`: the top processes of the fleet, or of one host with `node`. `by` is `cpu`, `mem` or `io` (default `cpu`). The default range is 1 h. The plan limit moves `from` and sets `clamped`. `limit` is 1 to 50. | viewer |
| GET | `/v1/fleet/compare?metric=cpu&window=1h&node=<id>` | — | `Compare`: the average and the highest value of each host in the last `window` and in the window before it. `metric` is `cpu`, `mem`, `net_rx`, `net_tx`, `disk_read` or `disk_write`. `window` is a Go duration from 15m to 720h. The plan limit moves the start of each window and sets `clamped`. A previous window fully outside the plan is empty: it has `from` equal to `to`, and no host has `previous`. | viewer |
| GET | `/v1/api-keys` | — | `[APIKey]`: an admin or owner sees all keys of the org. Other roles see their own keys. | viewer |
| POST | `/v1/api-keys` | `APIKeyRequest` | `APIKeyCreated` (201). The `token` shows only this one time. 409 when the user has 20 keys. An `api` token gets 403. | viewer |
| DELETE | `/v1/api-keys/{id}` | — | 204. 404 for an unknown ID or a token of another kind. 403 when a non-admin revokes the key of another user. | viewer |
| GET | `/v1/nodes/{id}/processes?from=…&to=…&limit=10&by=cpu&series=60` | — | `NodeProcessesResponse`. `by` is `cpu`, `mem` or `io`. `series` is 0 to 120 (default 0, no series). With `series`, each row has a `series`. 400 for a bad `series`. | viewer |
| GET | `/v1/nodes/{id}/processes/at?ts=…&by=cpu&limit=20` | — | `ProcessSnapshot`: the top processes of the node at one time. `ts` is RFC 3339 or `now-…` (default now). `by` is `cpu`, `mem` or `io`. `limit` is 1 to 50 (default 20). 400 for a bad `ts`, `by` or `limit`. A `ts` older than the plan limit gives `clamped: true` and no processes. | viewer |
| GET | `/v1/nodes/{id}/processes/{key}?from=…&to=…&step=1m` | — | `ProcessDetail`: the points and the facts of one process. `{key}` is a `ProcessKey`. The default range is 1 h. `step` is raised to the bucket of the table and to a value that gives 300 points at most. 400 for a bad key, 404 when the process has no row in the range, 503 when ClickHouse is off. | viewer |
| GET | `/v1/nodes/{id}/disks?from=…&to=…` | — | `NodeDisksResponse` | viewer |
| GET | `/v1/settings` | — | `ServerSettings` | viewer |
| PUT | `/v1/settings` | `ServerSettings` | `ServerSettings` | admin |
| GET | `/v1/rules` | — | `RulesView` | viewer |
| POST | `/v1/rules/import` | `RulesImport` | `{ backupId, hosts, org }` | admin |
| GET | `/v1/rules/backups` | — | `[RulesBackup]` | admin |
| POST | `/v1/rules/backups/{id}/restore` | `{ client?, device? }` (optional) | `{ backupId, hosts, org }` | admin |
| GET | `/v1/alerts` | — | `AlertList`: the open alerts of the org, the newest first. With the parameter `state`, the route gives `AlertPage` (see below). | viewer |
| GET | `/v1/alerts/recent?days=7` | — | `AlertList`: the alert events of the org in the last `days` (1 to 90, default 7), the newest first, 200 at most. A change back to ok has the tint `ok`. The plan history limit can move the start later. Then `clamped` is `true` | viewer |
| GET | `/v1/alerts/{id}` | — | `AlertDetail`: the alert, its limits and its timeline. `id` is an alert id or an event id. 404 if the alert is not known, 503 if ClickHouse is not ready. | viewer |
| POST | `/v1/alerts/bulk` | `{ action, ids, until?, scope? }` | `{ results: [{ id, alert?, error? }] }`. `action` is `ack`, `unack`, `snooze`, `unsnooze`, `ignore` or `unignore`. `ids` has 1 to 200 alert ids. `until` is RFC 3339 and in the future (`snooze` only). `scope` is `host` or `all` (`ignore` and `unignore` only, role `admin`). The status is 200 when some ids fail. Each id has a result with the new `alert` or an `error`, in the order of `ids` with the repeated ids dropped. The server writes one journal batch, and one edit for each node. 400 for a bad body, 403 for `ignore` without role `admin`. | member |
| POST | `/v1/alerts/{id}/ack` | — | `Alert` (404 if the alert is not open, 503 if ClickHouse is not ready). An ack stops all notifications of the alert, except the one for the return to ok. | member |
| DELETE | `/v1/alerts/{id}/ack` | — | `Alert`. Takes the ack back. 404 if the alert is not open. | member |
| POST | `/v1/alerts/{id}/snooze?until=RFC3339` | — | `Alert` (400 if `until` is not RFC3339 or not in the future, 404 if the alert is not open). A snooze stops all notifications of the alert until `until`. When it ends and the alert is still open, the server sends one reminder. | member |
| DELETE | `/v1/alerts/{id}/snooze` | — | `Alert`. Takes the snooze back. 404 if the alert is not open. | member |
| POST | `/v1/alerts/{id}/ignore?scope=host\|all` | — | `Alert`. For a `long_process` alert only: adds `proc.name` to `Node.ignoredProcs` (`host`) or to `ServerSettings.longProc.ignore` (`all`). The open alert closes with no notification. 400 for another kind or a bad scope, 404 if the alert is not known. | admin |
| DELETE | `/v1/alerts/{id}/ignore?scope=host\|all` | — | `Alert`. Removes the name from the list. | admin |
| POST | `/v1/nodes/{id}/snooze?until=RFC3339` | — | `Node`. Sets `snoozedUntil`. 400 if `until` is not RFC3339 or not in the future, 404 if the node is not known. | member |
| DELETE | `/v1/nodes/{id}/snooze` | — | `Node`. Clears `snoozedUntil`. | member |
| PUT | `/v1/mutes` | `{ metric, nodeId?, muted }` | `Node` (with `nodeId`) or `ServerSettings` (without). `metric` is `cpu`, `mem`, `disk`, `procs`, `zombies`, `pids`, `files`, `inodes`, `memPressure`, `ioPressure` or `long`. Sets or clears the mute. A muted open alert closes with no notification and a `reason` of `muted` in the event payload. A host mute needs `member`, an org mute needs `admin` (403). 400 for an unknown metric, 404 if the node is not known. | member |
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

**Alerts page.** `GET /v1/alerts` with `state` gives an `AlertPage`: the alerts of a range, one row for each incident, with counts, facets and bins. The parameters are:

| Parameter | Meaning |
|---|---|
| `state` | `open` (not resolved), `action` (open, not acked, not snoozed), `resolved` or `all`. |
| `from`, `to` | The range. RFC 3339 or `now-…` with a Go duration (for example `now-168h`). Default: the last 7 days. An alert is in the range when it overlaps the range. The range has at most 90 days. A longer range moves `from` later and sets `clamped`. |
| `started_from`, `started_to` | RFC 3339. Keep the alerts that started in `[started_from, started_to)`. |
| `tint` | List (comma separated) of `critical` and `warn`. It matches the worst tint of the alert. |
| `kind` | List of `cpu`, `mem`, `disk`, `health`, `long` and `nodata`. |
| `host` | List of node ids. |
| `q` | Text. It matches the title and the name of the host, with no case. |
| `facets` | List of `state`, `tint`, `kind` and `host`. The server returns a count for each value. A facet leaves out its own filter. |
| `bins` | 0 to 60. The number of bins of the start strip. |
| `sort`, `dir` | `sort` is `sev` (default), `title`, `host`, `kind`, `peak`, `start`, `duration` or `state`. `dir` is `desc` (default) or `asc`. |
| `limit` | 1 to 1000 (default 500). The number of rows. |

400 for a bad value. The server reads at most 10,000 incidents of the range, the newest first. Then `truncated` is `true` and the counts and facets cover only these incidents.

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
- The sampler runs with `--allow-disk-scan` (`towertail-sampler push` and `start`). Else the sampler posts an `error` line with the message `disk scans are off on this host: run the sampler with --allow-disk-scan`, and the job becomes `failed`.

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

**`detail`.** Sent to a client whose lease has `detail.host` equal to the node, in addition to the summary. It has the `ts` and the parts of the sample for the open tab. The keys have the names and shapes of the same keys in the sample. Each item of `procs.items` also has `key`, the `ProcessKey` of its name and cmd. A sample that has none of the parts of the tab gives no `detail` frame (for example, the sampler sends `ports` only each `--ports-interval`).

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
- `Event` — `{ id, alertId, ts, kind, nodeId, metric?, tint?, notify?, title?, text? }`. `alertId` is the alert id (see `Alert`). `notify` is `true` when the server sends a notification for the event, so a client can show its own notification. `title` and `text` are the words of that notification. `kind` ∈ `threshold_crossed | reachability_changed | sampler_version | long_process` and `tint` ∈ `ok | warn | critical`. For `threshold_crossed`, `metric` is `cpu`, `mem`, `disk` or `health`. A `health` event holds all tripped health checks in `text`, for example `5,000 zombies · Inodes 96%`, or `All health checks are back to normal.` for `ok`. The engine uses the time of the sample as the event time. A rule `sustainSeconds` above 0 delays a change out of `ok` until the level holds for that time (see `alerts` in `ServerSettings`). Inode checks never wait. A rule `notify` of `off` or `critical` sets `notify` to `false` on the events that it hides. The event is still stored. A mute closes an open alert with an `ok` event, `notify: false` and `reason: "muted"` in the stored payload.
- `Node` — camelCase mirror of the Swift `Node` struct (`apps/mac/Sources/State/Node.swift`). The server also sets `createdAt` (it keeps the first value on each PUT) and `machineId` (optional, from enroll or join). `mutedSignals` (a sorted list of `cpu`, `mem`, `disk`, `procs`, `zombies`, `pids`, `files`, `inodes`, `memPressure`, `ioPressure` and `long`; unknown names are skipped; always sent, `[]` when empty) mutes these metrics on the node. A `PUT /v1/nodes/{id}` body without `mutedSignals`, `thresholdOverrides`, `alertOverrides` or `rulesOrigin` keeps the stored value. A key with `null` clears it. A bad rule gives 400. When the body has `rulesOrigin`, the server takes `client` and `device` and sets `userId` and `at`. A body with `customThresholds` and no `thresholdOverrides` changes only the `cpu`, `mem` and `disk` overrides and keeps the others. `customLongProc` (a `LongProcRule`, optional) replaces the org rule for the node. `ignoredProcs` (process names) is added to the org ignore list. `hideProcFolders` (bool, default `false`) tells push samplers not to send process folders (see §3.0, `proc_cwd`). A `PUT /v1/nodes/{id}` body without the `hideProcFolders` key keeps the stored value. A `PUT /v1/nodes/{id}` body without the `ignoredProcs` key keeps the stored `ignoredProcs` and `customLongProc`, so an old client does not clear them. A current client always sends `ignoredProcs`. `thresholdOverrides` (optional) holds a `{ warn, critical }` pair for each metric that the host changes: `cpu`, `mem`, `disk`, `procs`, `zombies`, `pids`, `files`, `inodes`, `memPressure`, `ioPressure`. A `memPressure` pair also sets the macOS pair. `alertOverrides` (optional) holds `{ sustainSeconds, notify }` for `cpu`, `mem`, `disk` and `health`, and `tolerance`. `rulesOrigin` (optional) is `{ client, device?, userId, at }`: the client that set the rules last (`web`, `mac`, `windows` or `api`). The old key `customThresholds` is read once, for an old client: it becomes `thresholdOverrides` of `cpu`, `mem` and `disk`. The server never sends it.
- `ServerSettings` — camelCase mirror of the Swift `ServerSettings` class. The server adds `notifyCooldownSeconds`: the minimum time between two alerts with the same node, metric and tint (0 means 600). `longProc` is the org `LongProcRule`. A `PUT /v1/settings` body without `longProc` keeps the stored rule. `muted` is the list of metrics muted for the org (same form as `Node.mutedSignals`). The effective mutes of a node are the union of both lists. A `PUT /v1/settings` body without `muted` keeps the stored list. `thresholds` has, in addition to the six usage values, `procsWarn`, `procsCritical`, `zombiesWarn`, `zombiesCritical` (counts; defaults 5000, 20000, 200, 2000) and `health`: `{ pids, files, inodes, memPressure, ioPressure, macMemPressure }`, each `{ warn, critical }` (defaults `0.7/0.9`, `0.8/0.95`, `0.85/0.95`, `10/20`, `20/40`, `80/90`; pids, files and inodes are ratios, the others are percents). A missing key keeps its stored value (the default for a new org). `alerts` is `{ cpu, mem, disk, health, tolerance }`. Each rule is `{ sustainSeconds, notify }`: `sustainSeconds` is 0 to 3600, `notify` is `off`, `critical` or `all`. `tolerance` is 0.5 to 1.0. The defaults are the Mac defaults: cpu 300 s `critical`, mem 120 s `all`, disk 0 s `all`, health 300 s `all`, tolerance 0.8. An org with no stored `alerts` gets the defaults. `rulesOrigin` is the same as on `Node`. A `PUT /v1/settings` body changes only the keys that it has, also for the rule keys, so a client that sends only the six usage values keeps the other values. A bad rule gives 400 and writes nothing. When the body changes a rule, the server sets `rulesOrigin`: `client` (default `api`) and `device` come from the `rulesOrigin` of the body, and the server sets `userId` and `at`. `userId` is the user of the token, or the token id when the token has no user.
- `HostRules` — `{ thresholds?, alerts?, muted, longProc?, ignoredProcs?, origin? }`. `thresholds` is `thresholdOverrides` of `Node`, `alerts` is `alertOverrides`, `muted` is `mutedSignals`, `longProc` is `customLongProc` and `origin` is `rulesOrigin`. `PUT /v1/nodes/{id}/rules` replaces all rule fields of the host and leaves the other fields of the `Node`. A field that the body leaves out or sets to `null` clears that override. The server sets `userId` and `at` of the origin. It takes `client` (`web`, `mac`, `windows` or `api`; default `api`) and `device` from the `origin` of the body. 400 for a bad rule (`warn` above `critical`, a percent above 100, `sustainSeconds` out of range, an unknown `notify`), 404 for an unknown host. The server sends `node_updated`.
- `OrgRules` — `{ thresholds, alerts, muted, longProc, origin? }`: the rule fields of `ServerSettings`. A key that a body leaves out has its default value.
- `EffectiveRules` — the rules that the engine applies to one host: `{ thresholds, alerts, muted, longProc, ignore, sources }`. `muted` is the union of the org list and the host list. `ignore` is the org ignore list with the host names, without duplicates. `sources` says `org` or `host` for each rule: `thresholds.<metric>`, `alerts.<cpu|mem|disk|health|tolerance>`, `muted.<metric>` (only for the muted metrics) and `longProc`.
- `RulesView` — `{ org: OrgRules, hosts: [{ nodeId, host: HostRules, effective: EffectiveRules }] }`, one entry for each host. `NodeRules` (`GET /v1/nodes/{id}/rules`) is `{ org, nodeId, host, effective }`.
- `RulesImport` — `{ org?, hosts: [{ nodeId, rules: HostRules }], client?, device? }`. The server checks all parts, then replaces the org rules (when `org` is set) and the rules of the named hosts at once, and sets the origin on each one. A host that the body does not name keeps its rules. 400 for a bad rule, a repeated `nodeId` or a body with no `org` and no `hosts` (nothing is written), 404 for an unknown host (nothing is written). The reply `hosts` is the number of hosts and `org` tells if the org rules changed. The server sends one `settings_updated` and one `node_updated` for each host.
- `RulesBackup` — `{ id, at, origin, org?, hosts? }`: the rules that an import or a restore replaced. `at` is the time of the change. The server keeps a backup for 30 days. `GET /v1/rules/backups` gives them, newest first (`[]` when none). A restore writes the old rules back, skips a host that no longer exists, and makes a new backup of the rules that it replaces. 404 for an unknown or old backup.
- `LongProcRule` — `{ enabled, cpuPct, sustainSeconds, notify, ignore }`. Defaults: `true`, `50`, `1800`, `"all"`, `[]`. `cpuPct` is 10..800 (top convention), `sustainSeconds` is 60..86400, `notify` is `"off"` or `"all"`. A missing or bad field gets its default. A process (key `pid` + `start_ts`) is flagged when, over the last `sustainSeconds`, it is at or above `cpuPct` for at least 80% of the time. A process that is not in the sample counts as low. Names in the ignore lists match without case. The server emits a `long_process` event with tint `warn` when a process is flagged, and with tint `ok` when it is cleared (also when the node becomes unreachable; that event sends no notification). The event payload is `{ metric: "long_proc", previous, value, pid, name, cmd, cpu_pct, high_since, start_ts, cwd?, repo?, parent? }`. The alert text names the process with its folder and script, for example `node · sling (.wt/SLING-40) · vitest run (pid 812) at 85% CPU for 32m.`. `notify: "off"` keeps the events but sends no notification.
- `Alert` — `{ id, node_id, kind, metric, tint, ts, value, acked_by?, acked_at?, snoozed_until?, proc? }`. `id` is the alert id: the event id of the change out of `ok`. All events of one incident (until the change back to `ok`) have this id. `acked_by` is the user ID when the token has a user, else the token ID. An alert is open while the last event of its node and metric has a tint other than `ok`. `metric` is `reachability` when a node sends no sample for 3 intervals, or no first sample 10 minutes after it enrolled. The stored event payload has `reason` (`stopped_reporting` or `never_reported`) and `since`. When the node reports again, `value` is the down time in seconds. The server also stores a `registered` event for each new node. It is not an alert and no client gets it. For `kind: long_process`, `metric` is `long_proc/<pid>/<start_unix>`, so each process is its own alert, and `value` is the CPU percent. For `kind: long_process`, `proc` is `{ key, pid, name, cmd, start_ts?, high_since?, cpu_pct, cwd?, repo?, parent? }` from the event payload. `key` is the `ProcessKey` of the name and cmd. `cpu_pct` and `value` are in the top convention (100 is one core). `proc` is absent for the other kinds, and for an event with no process in its payload. `AlertList` is `{ alerts: [Alert], clamped? }`. `clamped` is `true` on `/v1/alerts/recent` when the plan history limit moved the start later. The alerts page and `GET /v1/alerts/{id}` also fill these fields: `start` (the first event of the alert), `end` (the change back to ok, absent while the alert is open), `peak` (the highest value), `severity` (the worst tint), `state` (`open`, `acked`, `snoozed` or `resolved`) and `node_name`. The old lists leave them out. A new alert has the words `warn` and `critical` (the limits of the rule) in its event payload, and the change back to ok has `peak`. A new alert of the same node and metric ends the old open alert at its start. The server does not fill old alerts back. The peak of an open alert comes from the memory of the alerter, so a restart resets it until the next sample.
- `AlertPage` — `{ alerts: [Alert], total, truncated?, clamped?, counts, facets?, bins?, from, to }`. `total` is the number of alerts that match all filters. `counts` is `{ open, action, resolved, all }`: the counts of the range with the filters other than `state`. `facets` is `{ state?, tint?, kind?, host? }`, each a map of value to count (the key of `host` is the node id). `bins` is `[{ start, end, critical, warn }]`: the number of alerts that started in each bin, by severity.
- `AlertDetail` — `{ alert: Alert, limits?, timeline: [AlertLine] }`. `limits` is `{ warn, critical }` and is absent when the events have none. `timeline` is oldest first.
- `AlertLine` — `{ ts, kind, tint?, user?, until?, text? }`. `kind` ∈ `opened | changed | notified | escalated | acked | unacked | snoozed | unsnoozed | recovered | closed`. `user` is the user of an action. `until` is for `snoozed`. `text` names the channels of a `notified` line, and the reason of a `closed` line.
- `Channel` — `{ id, kind, name, enabled, target, hasSecret, route: { minTint?, metrics?, tags? }, createdAt, blocked? }`. An alert channel of the org (§3.2).
- `ChannelRequest` — `{ kind, name, enabled?, target?, secret?, route }`. On PUT, a missing `enabled`, `target` or `secret` keeps the stored value. `"secret": ""` removes the secret. `ChannelTestResponse` is `{ ok, error? }`.
- `AlertPayload` — the body of a generic webhook (§3.2).
- `ClientMessage`, `StreamHello`, `LiveSummary`, `HealthBrief`, `HostFacts`, `DetailTab`, `ViewDetail`, `DetailParts`, `LeaseNotice` — the lease mode of the stream (§4).
- `LoginRequest`, `LoginResponse`.
- `EnrollRequest`, `EnrollResponse`, `JoinTokenRequest`, `JoinTokenResponse`, `JoinRequest`. `SamplerToken` is `{ id, label, nodeId?, createdAt, lastUsedAt? }`.
- `ControlNextResponse`, `KillProcessPayload`, `DiskScanPayload`.
- `DiskScanRequest`, `DiskScanJob`, `DiskScanProgressResponse`. The scan lines are `pkg/schema/scan.go` in [towertail/sampler](https://github.com/towertail/sampler).
- `JoinRequest`, `EnrollResponse`, `ControlNextResponse`, `KillProcessPayload`, `DiskScanPayload`, `DiskScanProgressResponse` and the ingest replies (`IngestResponse`, `IngestPlanFull`, `IngestGone`) are `pkg/schema/push.go` in [towertail/sampler](https://github.com/towertail/sampler). The JSON does not change.
- `NodeHistoryPoint`, `NodeHistoryResponse` — `{ node_id, metric, resolution, clamped?, points: [{ ts, value, max? }] }`. `metric` is one name of the list below. The length of the range selects the table: 6 h or less reads the raw samples, 7 d or less the 1 min rollup, 30 d or less the 5 min rollup, else the 1 h rollup. The age of `from` also selects the table: raw samples keep 7 d, the 1 min rollup 30 d and the 5 min rollup 90 d. The server reads the coarser table of the two choices. A 1 h window that starts 8 d ago reads the 1 min rollup. A `step` larger than the bucket of the table groups the points. `resolution` is `raw` (one point for each sample) or the bucket, for example `5m0s`.
- `NodeProcessesResponse` — `{ node_id, from, to, clamped?, processes: [TopProcess] }`. `TopProcess` is `{ key, name, cmd, repo?, cwd?, parent?, cpu_pct_avg, cpu_pct_max, rss_max, io_bps_avg? }`, the highest `cpu_pct_avg` first (or by `by`). `io_bps_avg` is the average of read plus write bytes each second. It is 0 for rollup rows from before migration 00011. On a rollup, `by=mem` and `by=io` sort only the rows that the rollup kept. A process is a name, a cmd and a repo. `cwd` and `parent` come only from ranges of 1 h or less. 1 h or less reads each sample. 7 d or less reads the top 10 of each minute. Longer ranges read the top 5 of each hour. The age of `from` also selects the table: a window that starts more than 1 h ago reads the top 10 of each minute, and a window that starts more than 7 d ago reads the top 5 of each hour. `limit` is 10 by default and 50 at most. With `series=N`, the response has `series_from` and `series_step_s`, and each row has `series: { cpu: [..], rss: [..], io: [..] }`. Point i is at `series_from + i × series_step_s`. The step is the range divided by N, rounded up to a whole bucket of the table (10 s raw, 1 min, 1 h), and the buckets sit on multiples of the step since the Unix epoch. The arrays have N or N+1 points. `cpu` is the average, `rss` is the maximum and `io` is the average read plus write bytes each second. A bucket with no row is 0. The client computes "time at 50 % CPU or more" from `cpu`. Two rows that share a key (a split by repo) get the same series.
- `ProcessKey` is the text `<16 hex digits of cityHash64(cmd)>.<base64url(name), no padding>`. It names a process across samples and rollups. It has no `/` and always one `.`. The server sends it as `key` in `TopProcess`, `FleetProcess`, `SnapshotProcess`, `AlertProc` and each process of the stream "processes" detail (`procs.items[].key`). The clients do not compute the hash.
- `ProcessDetail` — `{ node_id, key, name, cmd, repo?, from, to, resolution, clamped?, points: [ProcPoint], facts? }`. The age and the length of the range select the table, as for `NodeProcessesResponse`. `resolution` is the step. `ProcPoint` is `{ ts, cpu_pct, rss, io_bps? }`: `cpu_pct` is the average, `rss` is the maximum and `io_bps` is the average read plus write bytes each second. A bucket with no row has no point. `cmd` and `repo` come from the newest row of the range. `facts` is `{ seen_at, pid, ppid?, parent?, user?, cwd?, state?, threads?, start_ts?, listen_tcp?, listen_udp? }`. The facts come from the newest raw row at or before `to`, so they exist only when `to` is less than 1 h ago and the process ran in the last hour. `listen_tcp` and `listen_udp` come from the newest ports row of the same PID and name since the process started. The server keeps ports for 1 day, only for samples that carry ports and only for processes that listen.
- `ProcessSnapshot` — `{ node_id, ts, tier, bucket_s, by, total?, prev?, next?, clamped?, processes: [SnapshotProcess] }`. `tier` is `raw`, `1m` or `1h`. The age of `ts` selects the table. `raw` (1 h or less) gives the newest sample that is at or before `ts` and not older than 2 min: `ts` is the sample time, `total` is the process count of the sample, and `prev` and `next` are the times of the neighbor samples (1 h at most). `1m` (7 d or less) gives the top 10 of the minute of `ts`. `1h` gives the top 5 of the hour. On a rollup, `ts` is the start of the bucket, `bucket_s` is 60 or 3600, and `prev` and `next` are the neighbor buckets (`next` is left out for the future). An empty bucket gives `processes: []`. `SnapshotProcess` is `{ key, pid?, ppid?, parent?, user?, name, cmd, repo?, cwd?, cpu_pct, cpu_pct_max?, rss, io_bps?, threads?, state?, start_ts? }`. `pid`, `ppid`, `parent`, `user`, `cwd`, `threads`, `state` and `start_ts` come from raw samples only. On a rollup, `cpu_pct` is the average, `cpu_pct_max` is the maximum and `rss` is the maximum.
- `NodeDisksResponse` — `{ node_id, from, to, resolution, clamped?, mounts: [{ mount, fs, points: [{ ts, used, total, inodes_used?, inodes_total? }] }] }`. Each point is the last value in its bucket. The inode counts are the highest in the bucket. They are left out for a filesystem with no inodes and for rows from before migration 00013. The bucket gives about 240 points, 1 min at least. The disk rows stay 7 days.
- History metrics: `cpu` (percent), `mem` (ratio 0..1), `net_rx`, `net_tx`, `disk_read`, `disk_write` (bytes each second), `load_1` (the 1 min load; `load` is the same), `procs` (the process count), `disk` (ratio 0..1 of the fullest mount), `inodes` (ratio 0..1 of the mount with the highest inode use), `zombies` (a count), `pids` and `files` (ratios 0..1 of the used PIDs and open files to the limit), `psi_cpu`, `psi_mem`, `psi_mem_full`, `psi_io` and `psi_io_full` (Linux pressure in percent) and `mem_pressure` (Mac memory pressure, 0..100). All metrics have the raw table and the three rollups. A metric that a host does not report (for example PSI on a Mac), and a bucket with no value, have no points. The rollups of `load_1`, `procs` and the health metrics start at the time of migration 00013. Older rollup buckets have no points of them. Raw samples before the migration have no points of the health metrics, except `load_1` and `procs`. `max` of a rollup point is the highest sample of the bucket. For `mem`, `net_rx`, `net_tx`, `disk_read` and `disk_write` on a rollup, `max` equals `value`. An unknown name gives 400.
- `NodeHistoryMultiResponse` — `{ node_id, series: { <metric>: [{ ts, value, max? }] }, from, to, resolution, clamped? }`. One query gives all metrics, with the same table and `step` rules as `NodeHistoryResponse`. `FleetHistoryResponse` — `{ from, to, resolution, clamped?, nodes: [{ node_id, series }] }`. The org of the token filters the nodes. A node with no points in the range, or of another org, has no entry in `nodes`. The 24 h limit uses the requested range, before the plan limit. The plan limit then moves `from` and sets `clamped`.
- The `procs` metric is the process count (`procs_total`). It has raw samples only, so it works for ranges of 6 h or less. For a longer range, a request with `procs` gives 400 `metric has no rollup for this range`. An unknown metric gives 400 `unknown metric`.
- `FleetHosts` — `{ hosts: [{ node_id, name, tags?, online, last_seen?, cpu?, mem?, disk?, load1?, net_rx?, net_tx? }] }`. `cpu` is a percent. `mem` and `disk` are ratios 0..1 (`disk` is the fullest mount). `net_rx` and `net_tx` are bytes each second. `FleetProcesses` — `{ from, to, clamped?, by, processes: [{ node_id, node_name, key, name, cmd, repo?, cwd?, parent?, cpu_pct_avg, cpu_pct_max, rss_max, io_bps_avg? }] }`. `io_bps_avg` is the average of read plus write bytes each second. A process is a node, a name, a cmd and a repo. `cwd` and `parent` come only from ranges of 1 h or less.
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

The server has a read-only [Model Context Protocol](https://modelcontextprotocol.io) server at `POST /mcp`. It is part of the Go server, so a self-hosted server has it too. The cloud shows it at `https://api.towertail.com/mcp`. `TT_MCP__ENABLED=false` (`mcp.enabled`) turns it off. Then `/mcp` and `/mcp/v1` give 404.

- **Transport:** streamable HTTP in stateless mode with JSON replies. Send `Content-Type: application/json` and `Accept: application/json, text/event-stream`. Each reply is one JSON body. There is no session and no stream. `GET` and `DELETE` give 405.
- **Auth:** `Authorization: Bearer tt_...` with an `api` token. `user` and `admin` tokens also pass. Other kinds give 403. The role is `viewer`. The key budget of §2 applies (429 `rate_limited`). `oauth` tokens also pass (see OAuth below).
- **Paths:** `POST /mcp` serves the newest tools. `POST /mcp/v1` serves the same tools as `/mcp` today. Its schemas are for the directory reviews (for example Google), so they freeze before the first submission and then change only on purpose. A tool change that breaks a client goes to `/mcp` only. Both paths have the same auth and replies.
- **OAuth:** OAuth is off until both `TT_MCP__RESOURCE_URL` (`mcp.resource_url`, for example `https://api.towertail.com/mcp`, with no trailing slash) and `TT_MCP__AUTHORIZATION_SERVER` (`mcp.authorization_server`, the issuer URL) are set. Load refuses only one of them and any URL that is not http or https. `TT_MCP__DOCS_URL` (`mcp.docs_url`) is the documentation page of the metadata. `TT_MCP__APP_URL` (`mcp.app_url`) is the base URL of the dashboard. It is for the `url` of a tool result. Empty means no `url`.
  - **Metadata:** `GET /.well-known/oauth-protected-resource/mcp` (also `/.well-known/oauth-protected-resource`) and `GET /.well-known/oauth-protected-resource/mcp/v1` give the RFC 9728 document: `resource` (exactly `mcp.resource_url`, plus `/v1` on the v1 path), `authorization_servers`, `scopes_supported`, `bearer_methods_supported` (`["header"]`), `resource_name` and `resource_documentation`. With OAuth off, they give 404.
  - **Challenge:** each 401 of `/mcp` and `/mcp/v1` has `WWW-Authenticate: Bearer resource_metadata="<origin>/.well-known/oauth-protected-resource<path>", scope="fleet:read"`. When the request had a token, the value also has `error="invalid_token"`. With OAuth off, the value is `Bearer realm="towertail"`.
  - **Audience:** the `audience` of an `oauth` token must be `mcp.resource_url` or `mcp.resource_url` + `/v1`. Both paths accept either one. Else 401 "token audience does not match".
  - **API keys:** when the org setting `mcp.apiKeys` is `false`, an `api` token gets 403 on both paths. `oauth`, `user` and `admin` tokens still pass.
  - **Rate limit:** the key budget of §2 also applies to `oauth` tokens. The access tokens of one grant share one budget.
  - **CORS:** `/mcp`, `/mcp/v1`, `/.well-known/` and `/oauth/` allow any origin with no credentials, so browser clients on other sites can call them. They allow the headers `Authorization`, `Content-Type`, `Mcp-Protocol-Version` and `Mcp-Session-Id`, and expose `WWW-Authenticate`. All other routes keep `http.cors_allow_origins`.
- **Tool metadata:** each tool in `tools/list` has `title` and `annotations`: `title`, `readOnlyHint`, `destructiveHint`, `idempotentHint` and `openWorldHint`. Every hint is explicit. A read tool has `readOnlyHint: true` and `idempotentHint: true`. No tool is destructive or open-world.
- **Instructions:** the `initialize` result has `instructions`. The text (2 KB or less) gives facts: the host rule, the range syntax, the units and the plan limit. It gives no orders to the model.
- **Result:** a good call gives `structuredContent` and one `text` block with the same JSON. Clients that read only text, such as Google, use that block.
- **Reads:** the tools call the same query code as the fleet routes of §3, so the plan history limit is the same. A tool that reads history sets `clamped: true` when the limit moved the start later.
- **Errors:** a bad input or a failed query is a tool error (`isError: true`) with a short text. A server with no ClickHouse gives the tool error `query: history is off`. A call with no token on the context gives `no token`.
- **Host:** a tool takes a host by ID or by display name (no case). A name that matches two hosts gives an error with the two IDs.
- **Range:** `range` is a Go duration or a number with `d` for days, from `15m` to `365d` (`1h`, `90m`, `7d`). `from` and `to` are RFC 3339 and replace `range`. A `to` in the future moves to now.

| Tool | Input | Output |
|---|---|---|
| `list_hosts` | `sort` (`cpu`, `mem`, `disk`, `net`; default `cpu`), `limit` (1 to 200; default 50) | `hosts`: `id`, `name`, `tags?`, `online`, `last_seen?`, `cpu_pct?`, `mem_ratio?`, `disk_ratio?`, `load1?`, `net_rx_bps?`, `net_tx_bps?`. An offline host has no values. |
| `host_metrics` | One of `host`, `hosts` (at most 50) or `tag`. `metrics` (any history metric of §5 and the alias `load`; at most 16 for one host and 8 for many; default `cpu`), `range` (default `1h`; at most `30d` for many hosts) or `from` and `to`, `resolution` (`auto`, `raw`, `1m`, `5m`, `1h`; default `auto`), `compare` (bool) | `from`, `to`, `resolution`, `clamped?`, `previous?` (`from`, `to`), `hosts` (name order): `host`, `host_id`, `metrics`: for each metric `metric`, `unit`, `resolution`, `count`, `min`, `avg`, `max`, `last`, `points` (`ts`, `value`, `max?`), `previous?` (`avg`, `max`, `min`, `count`) and `change_avg?`. With `compare`, the biggest `change_avg` comes first. |
| `top_processes` | `host` (empty = fleet), `by` (`cpu`, `mem`, `io`; default `cpu`), `range` (default `1h`) or `from` and `to`, `at` (RFC 3339, `now` or `now-<span>`; needs `host`; not with `range`, `from` or `to`), `limit` (1 to 50; default 20) | `from`, `to` (range) or `at`, `tier` (`at`), `by`, `clamped?`, `processes`: `host`, `host_id`, `key`, `name`, `command`, `repo?`, `cpu_pct_avg`, `cpu_pct_max`, `rss_max_bytes`, `io_bps_avg?`, `pid?`, `user?`, `state?` (`pid`, `user` and `state` only for a time in the last hour), `started_at?`, `first_seen?`, `run_seconds?`, `start_exact`. |
| `process_history` | `host`, `process` (a `key` from `top_processes`, or a name), `range` (default `1h`) or `from` and `to` | `host`, `key`, `name`, `command`, `repo?`, `from`, `to`, `resolution`, `clamped?`, `stats` (`cpu_pct_avg`, `cpu_pct_max`, `rss_max_bytes`, `io_bps_avg`), `points` (300 at most: `ts`, `cpu_pct`, `rss_bytes`, `io_bps`), `facts?` (`pid`, `ppid?`, `user?`, `cwd?`, `state?`, `threads?`, `listen_tcp?`, `listen_udp?`), `started_at?`, `first_seen?`, `run_seconds?`, `start_exact`. A name with more than one command gives an error that lists the keys. A process with no row in the range gives the error `process not found`. |
| `alerts` | `state` (`open`, `action`, `resolved`, `all`; default `open`), `range` (default `7d`, at most `90d`) or `from` and `to`, `host` (empty = fleet), `kind` (list of `cpu`, `mem`, `disk`, `health`, `long`, `nodata`), `tint` (list of `critical`, `warn`: the worst level), `q` (text of the host name, metric, process), `sort` (`sev`, `start`, `peak`, `duration`, `title`, `host`, `kind`, `state`; default `sev`), `limit` (1 to 100; default 50) | `from`, `to`, `clamped?`, `truncated?`, `total`, `counts` (`open`, `action`, `resolved`, `all`), `alerts`: `id`, `host`, `host_id`, `kind`, `metric`, `severity`, `state` (`open`, `acked`, `snoozed`, `resolved`), `value`, `peak?`, `start`, `end?`, `acked_by?`, `snoozed_until?`, `proc?` (`key`, `pid`, `name`, `command`, `repo?`, `started_at?`, `high_since?`, `cpu_pct`, `cwd?`, `parent?`). The number sorts give the highest first and the text sorts give A to Z. The old state `recent` is the same as `all`; the old input `days` is gone. |
| `get_alert` | `alert_id` | `alert` (as in `alerts`), `limits?` (`warn`, `critical`, `unit?`: the limits of the alert when it opened), `rule_now?` (the rule of the host now: `kind`, `warn?`, `critical?`, `unit?`, `cpu_pct?` (long process), `sustain_seconds`, `notify`, `muted?`, `source`), `notifications` (the notified lines of the timeline), `timeline`: `ts`, `kind`, `tint?`, `user?`, `until?`, `text?`. An unknown ID gives `alert not found`. |
| `host_rules` | `host` (empty = org only) | `org`: `thresholds` (`warn`, `critical` for each of the 10 metrics), `alerts` (`cpu`, `mem`, `disk`, `health`: `sustain_seconds`, `notify`; `tolerance`), `muted`, `long_proc` (`enabled`, `cpu_pct`, `sustain_seconds`, `notify`, `ignore`), `cooldown_seconds`, `debounce_seconds`, `notify_flags` (`enabled`, `warn`, `critical`). With a host also `host`: `overrides` (`thresholds`, `alerts?`, `muted`, `long_proc?`, `ignored_procs`), `notify_warn`, `notify_critical`, `snoozed_until?`, and `effective`: the same fields as `org` and `ignore`, with `sources` (`thresholds`, `alerts`, `muted`, `long_proc`: `org` or `host`). `units` gives the unit of the limits of each metric (for example cpu is `ratio 0 to 1`). |
| `disks` | `host`, `range` (default `24h`) | `host`, `host_id`, `from`, `to`, `clamped?`, `mounts`: `mount`, `fs`, `used_bytes`, `total_bytes`, `ratio`, `change_bytes` (used now minus used at the start). |
| `disk_scans` | `host`, `job_id?` | `host`, `host_id`, `jobs` (the newest 5: `id`, `state`, `root`, `created`, `finished?`, `error?`, `result_bytes`), and `result?` for `job_id`, or else the newest `done` job: `job_id`, `root`, `fs?` (`mount`, `total`, `used`, `free`), `scanned_bytes`, `top` (the 20 largest entries with no children in the pruned tree: `path`, `bytes`), `recs` (`group`, `label`, `path`, `bytes`, `elevated?`). A job of another org or host gives `disk scan not found`. A server with no scan manager gives `disk scans are off`. |
| `whoami` | none | `org_id`, `org_name?`, `plan`, `history_days`, `max_hosts?` (absent when the plan has no host limit), `user_id?`, `token_kind`, `role`, `server_version`. A self-hosted server gives the plan `self-hosted`. |
| `reference` | `topic` (`metrics`, `thresholds`, `alerts`, `long_process`, `history`, `plan`) | `topic`, `facts`: `name`, `value?`, `unit?`, `text`. The facts come from the server code and the org plan. |
| `fleet_summary` | none | `hosts`, `online`, `offline`, `open_alerts` (`critical`, `warn`), `over` (hosts at or over a limit, the critical first: `host`, `host_id`, `metric`, `value`, `tint`), `top_cpu`, `top_mem`, `top_disk` (at most 5 hosts each, same fields as `list_hosts`), `alerts_24h`, `notifications_24h`, `tags` (tag to host count). |
| `get_host` | `host` | The fields of `list_hosts` for one host, and `kind`, `enabled`, `facts?` (`os`, `arch`, `kernel`, `cores`, `mem_total_bytes`, `uptime_s`, `sampler`), `snoozed_until?`, `open_alerts` (as `alerts`), `effective_limits` (for each metric `warn`, `critical`, `unit`, `source`). |

`host_metrics` gives at most 300 points for each metric. For many metrics or hosts, the budget is 6,000 points for each call: each series gets clamp(6000 / (hosts × metrics), 20, 300) points. When the range has more, the server raises the step and then merges the points: `value` is the average, `max` is the highest. `min`, `avg`, `max` and `last` come from all the points before the merge. A `count` of 0 means no data. With `compare`, `previous` is the window of the same length that ends at `from`. The plan limit moves its start too. A previous window fully outside the plan gives no `previous` and sets `clamped`. The tool `compare` is removed: use `host_metrics` with `compare: true`.

Start time: `started_at` is the start time that the sampler read, and `start_exact` is `true`. It comes from raw rows (1 h). Without it, `first_seen` is the oldest stored time of the process, so the process is older than that. `run_seconds` is the time from `started_at` to the end of the range, or to `at`. The tool sets it only when `start_exact` is `true`. `facts` of `process_history` exist only when `to` is less than 1 h ago.

All tools are read-only. Tools that change a host (for example `kill_process`) are not part of this version.
