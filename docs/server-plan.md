# Towertail Server — Implementation Plan

> **Elevator pitch.** A self-hosted Go control-plane that ingests metrics pushed by `towertail-sampler` instances running as background services on remote hosts, stores time-series in ClickHouse, evaluates alert rules centrally, and serves a REST+WebSocket API that the Mac/Windows clients (via `RemoteBackend`) consume as thin read/write consoles. A stubbed Postgres sidecar is provisioned for the future managed-cloud tier (users/orgs/billing), but all Phase-3 behaviour lives against a single-tenant self-hosted deployment.

Source of truth companion docs:

- [`PLAN.md`](PLAN.md) — overall phase roadmap; this plan is Phase 3.
- [`backend-split.md`](backend-split.md) — Swift-side `Backend` seam already landed. `RemoteBackend` is the client this server must satisfy.
- [`sampler.md`](sampler.md) — existing sampler JSON schema. This plan extends the sampler with **push mode** and a `service` subcommand without breaking the schema.

**Non-goals for this plan.** Full multi-tenant SaaS, billing, SSO, managed cloud operations, Windows client — out of scope; the Postgres "cloud-managed" schema is stubbed so the seam is in place.

---

## 0. Top-level decisions (locked)

| Dimension | Decision | Why |
|---|---|---|
| Language / runtime | **Go 1.24**, single static binary `towertail-server` | Same toolchain as sampler; ops friendly; easy Docker image |
| HTTP framework | **Echo v4** (`github.com/labstack/echo/v4`) | Batteries-included router, first-class middleware (JWT, CORS, rate-limit, gzip, recover, request-id), strong ergonomics for binding/validation |
| WebSocket | **nhooyr.io/websocket** (aka `coder/websocket`) | Context-aware, idiomatic, small API surface; plugs into any Echo handler |
| Ingest transport | **HTTPS POST + NDJSON batching** from sampler (push) | Firewall-friendly one-way from host to server; no inbound SSH to hosts |
| Time-series store | **ClickHouse (latest)** via `clickhouse-go/v2` native proto | Columnar; partitioning + TTL built in; handles 10k hosts × 30s easily |
| Metadata store | **Postgres 16** (`pgx/v5`) — **stubbed** for cloud-managed mode | Users/orgs/billing/audit. Unused by self-hosted path in v1 |
| Config store | **BoltDB** (`go.etcd.io/bbolt`) for self-hosted node config | Embedded KV; no external dep for single-tenant; swapped out behind a `ConfigStore` interface when Postgres takes over |
| Auth | **Bearer tokens** (opaque, hashed at rest). `POST /v1/auth/login` issues; no OIDC yet | Simple; one knob to replace later |
| Migrations | **goose** for Postgres; ClickHouse DDL run by server at boot if absent | Standard, reviewable |
| Config | `koanf` + env-var overrides + `towertail-server.yaml` | Flexible without being a kitchen sink |
| Logging | `log/slog` (stdlib, structured) | Zero external dep; json/text switch |
| Metrics (self) | `prometheus/client_golang` on `/metrics` | Ops observability |
| Testing | `stretchr/testify` + `testcontainers-go` for integration | Real CH + PG in CI, no mocks-lying-to-you |
| CLI | `urfave/cli/v3` (same as sampler for symmetry) | Stable v3, ergonomic subcommand tree |

**Lazy loading / efficiency posture.**

- Handlers register routes but backends (CH pool, PG pool, notifier, alerter) are constructed via a small DI container and only **dialed** on first request that needs them. Health probe hits `/healthz` without touching DB.
- ClickHouse writes go through an **async batcher** (flush at 5k rows *or* 1s *or* 1MiB, whichever first). Sync REST reads use a separate connection pool sized smaller.
- WebSocket fan-out uses a per-org `broadcast hub` — clients register, server pushes; no N² ingest loops.
- Alert evaluation runs in a single goroutine fed by an in-memory ring of the last N samples per node; recomputed incrementally on each ingest, not polled.

---

## 1. Target architecture

```
┌──────────── Remote host ──────────┐        HTTPS POST         ┌─────────────── Towertail Server ─────────────┐
│                                   │    /v1/ingest/samples     │                                              │
│  towertail-sampler --push         │───────── NDJSON ─────────▶│  ingest → batcher → ClickHouse (samples)     │
│  (running as systemd/launchd)     │                           │                         │                    │
│  service install/start/stop/log   │                           │  alerter (ring buffer) ─┘── events → WS hub  │
│                                   │                           │                                              │
└───────────────────────────────────┘                           │  REST API (chi)                              │
                                                                │    /v1/nodes   /v1/settings  /v1/events     │
┌──────────── Mac / Windows client ──┐     HTTPS + WSS          │    /v1/alerts  /v1/history   /v1/auth       │
│                                    │◀─────────────────────────│                                              │
│  RemoteBackend (Swift)             │                           │  WS hub (/v1/stream)                         │
│   - REST for CRUD                  │                           │                                              │
│   - WS for samples + events        │                           │  BoltDB (self-hosted config)                 │
│                                    │                           │  Postgres (stub, cloud-managed)              │
└────────────────────────────────────┘                           └──────────────────────────────────────────────┘
```

### Components

1. **Ingest API** — `POST /v1/ingest/samples` (NDJSON, one `Sample` per line, gzip accepted). Auth: sampler-token header (separate token class from user bearers). Validates schema `v`; drops or 400s on unknown.
2. **Config store (BoltDB)** — node registry, per-node settings, global `ServerSettings`. Lives at `/var/lib/towertail/config.db`. `ConfigStore` interface; `BoltStore` implementation today, `PostgresStore` later for cloud-managed.
3. **Time-series store (ClickHouse)** — partitioned tables for `samples_raw`, `samples_1m`, `samples_5m`, `disks`, `disk_io_devices`, `net`, `procs_topn`, `events`. Materialised views roll up.
4. **Alerter** — goroutine consuming a bounded channel from ingest, maintains per-node thresholds state (warn / critical / ok with edge detection + debounce + snooze), emits `BackendEvent` payloads to the WS hub and writes to `events` table.
5. **REST API** — satisfies `RemoteBackend` contract in one pass (nodes CRUD, settings, thresholds, snooze/favourite, history range queries, alert ack/snooze, kill-process fan-out).
6. **WS hub** — `wss://…/v1/stream` multiplexed channel; on connect, server replays last known sample per node then streams new samples + events.
7. **Auth** — bearer tokens for user sessions; sampler tokens for ingest. Simple table in BoltDB (or Postgres later).
8. **Admin CLI** (`towertail-server` subcommands) — `migrate`, `token issue`, `user create` (stub), `config dump`, `serve`.

### Kill-process in remote mode

Because samplers push (no inbound shell), a kill-process needs a back-channel. **Decision for v1:** the server enqueues a "control message" on a per-node queue; the sampler polls `GET /v1/control/next?node_id=…` on its heartbeat tick (every 15s) and acts. Keeps the one-way-outbound firewall story. WebSocket back to sampler is a future optimisation.

### Lazy-loading details

- Backends implement a `Component` interface: `Name() string`, `Start(ctx) error`, `Stop(ctx) error`, `Ready() bool`. The server's `Runtime` starts only the components reachable by the current config (e.g. Postgres stays dormant unless `cloud_managed: true`).
- HTTP handlers go through a `components.Require("clickhouse")` guard that triggers lazy dial on first hit and returns 503 until `Ready()` is true.
- ClickHouse native client uses `MaxOpenConns` sized to CPU count; writer pool is separate from reader pool so a slow query can't starve ingest.

---

## 2. ClickHouse schema

Designed for: high-cardinality `node_id` (UUID), 30s default cadence, 7-day rolling retention default (configurable to 90d), fast "last 1h for node X" queries, fast aggregate "top warning nodes now" dashboards.

**Database:** `towertail`.

### 2.1 `samples_raw` — fact table, one row per sample

```sql
CREATE TABLE towertail.samples_raw
(
    ts                DateTime64(3, 'UTC')  CODEC(Delta, ZSTD),
    org_id            UUID,                  -- stub: all-zero UUID in self-hosted
    node_id           UUID,
    schema_v          UInt8,
    host_name         LowCardinality(String),
    host_os           LowCardinality(String),
    host_arch         LowCardinality(String),
    host_kernel       LowCardinality(String),
    host_uptime_s     UInt64,
    host_sampler      LowCardinality(String),
    host_machine_id   String,

    cpu_pct           Float32,
    cpu_load_1        Float32,
    cpu_load_5        Float32,
    cpu_load_15       Float32,
    cpu_cores         UInt16,

    mem_used          UInt64,
    mem_total         UInt64,
    swap_used         UInt64,
    swap_total        UInt64,

    disk_read_bps     UInt64,
    disk_write_bps    UInt64,
    disk_read_cum     UInt64,
    disk_write_cum    UInt64,

    net_rx_bps        UInt64,
    net_tx_bps        UInt64,
    net_rx_cum        UInt64,
    net_tx_cum        UInt64,

    procs_total       UInt32,
    procs_visible     UInt32,
    procs_root        UInt8,

    errors            Array(String),
    ingested_at       DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, node_id, ts)
TTL toDateTime(ts) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192;
```

**Partition by day** → dropping a partition is instant at TTL time, and scans of a recent range touch one or two partitions. **Order by `(org_id, node_id, ts)`** → "last 1h for node X" is one primary-key range seek. `LowCardinality(String)` slashes storage for repeated fields. `CODEC(Delta, ZSTD)` on timestamps compresses exceptionally.

### 2.2 Disks (one row per mount per sample)

```sql
CREATE TABLE towertail.disks
(
    ts         DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id     UUID,
    node_id    UUID,
    mount      LowCardinality(String),
    fs         LowCardinality(String),
    used       UInt64,
    total      UInt64
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, node_id, mount, ts)
TTL toDateTime(ts) + INTERVAL 7 DAY;
```

### 2.3 Per-device disk I/O (optional, one row per device per sample)

```sql
CREATE TABLE towertail.disk_io_devices
(
    ts          DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id      UUID,
    node_id     UUID,
    device      LowCardinality(String),
    read_bps    UInt64,
    write_bps   UInt64,
    read_cum    UInt64,
    write_cum   UInt64
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, node_id, device, ts)
TTL toDateTime(ts) + INTERVAL 7 DAY;
```

### 2.4 Process top-N (compressed heavy column)

```sql
CREATE TABLE towertail.procs_topn
(
    ts           DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id       UUID,
    node_id      UUID,
    pid          Int32,
    ppid         Int32,
    name         LowCardinality(String),
    cmd          String         CODEC(ZSTD(3)),
    user_name    LowCardinality(String),
    cpu_pct      Float32,
    rss          UInt64,
    threads      Int32,
    state        LowCardinality(String),
    start_ts     DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    read_bytes   Nullable(UInt64),
    write_bytes  Nullable(UInt64)
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (org_id, node_id, ts, pid)
TTL toDateTime(ts) + INTERVAL 3 DAY;  -- heavier; shorter retention
```

### 2.5 Events (alerts)

```sql
CREATE TABLE towertail.events
(
    ts           DateTime64(3, 'UTC') CODEC(Delta, ZSTD),
    org_id       UUID,
    node_id      UUID,
    event_id     UUID,
    kind         Enum8('threshold_crossed' = 1, 'reachability_changed' = 2, 'sampler_version' = 3),
    metric       LowCardinality(String),     -- 'cpu' | 'mem' | 'disk' | ''
    tint         Enum8('ok' = 0, 'warn' = 1, 'critical' = 2),
    payload      String CODEC(ZSTD(3)),      -- JSON with kind-specific fields
    acked_by     Nullable(UUID),
    acked_at     Nullable(DateTime64(3, 'UTC')),
    snoozed_until Nullable(DateTime64(3, 'UTC'))
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org_id, node_id, ts)
TTL toDateTime(ts) + INTERVAL 90 DAY;
```

Month-partitioned because events are sparse.

### 2.6 Rollups (materialised views)

For the "last 24h sparkline" card queries we don't want to scan raw:

```sql
CREATE MATERIALIZED VIEW towertail.samples_1m
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMMDD(ts_minute)
ORDER BY (org_id, node_id, ts_minute)
TTL toDateTime(ts_minute) + INTERVAL 30 DAY
AS SELECT
    toStartOfMinute(ts)                      AS ts_minute,
    org_id, node_id,
    avgState(cpu_pct)                        AS cpu_pct_avg,
    maxState(cpu_pct)                        AS cpu_pct_max,
    avgState(mem_used / mem_total)           AS mem_ratio_avg,
    avgState(net_rx_bps)                     AS net_rx_avg,
    avgState(net_tx_bps)                     AS net_tx_avg,
    avgState(disk_read_bps)                  AS disk_read_avg,
    avgState(disk_write_bps)                 AS disk_write_avg
FROM towertail.samples_raw
GROUP BY ts_minute, org_id, node_id;
```

A second MV rolls `samples_1m` into `samples_5m` with TTL 90 days. Card views pick the right resolution for the time range requested.

### 2.7 Retention tuning

Retention is a server config knob (`CH_RETENTION_RAW_DAYS` etc.); boot-time `ALTER TABLE … MODIFY TTL` is a standard ClickHouse op.

---

## 3. Postgres stub (cloud-managed)

**All cloud-managed tables live in Postgres.** Migrations ship in `server/migrations/postgres/` and run via goose. The self-hosted binary does not dial Postgres unless `cloud_managed: true`.

### 3.1 Schema (stubbed)

```sql
CREATE TABLE orgs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    plan         TEXT NOT NULL DEFAULT 'free',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    email        TEXT NOT NULL UNIQUE,
    password_hash TEXT,                    -- argon2id
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE api_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    kind         TEXT NOT NULL,            -- 'user' | 'sampler' | 'admin'
    token_hash   BYTEA NOT NULL UNIQUE,    -- sha256 of opaque token
    label        TEXT,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id           BIGSERIAL PRIMARY KEY,
    org_id       UUID NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    actor_id     UUID,
    action       TEXT NOT NULL,
    target       TEXT,
    payload      JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ON users (org_id);
CREATE INDEX ON api_tokens (org_id);
CREATE INDEX ON audit_log (org_id, created_at DESC);
```

In self-hosted v1 the server synthesises a single zero-UUID org + a "local" user at boot and stores the hash of the bearer token in BoltDB; when `cloud_managed: true` flips, migration mirrors those into Postgres.

---

## 4. REST API (satisfies `RemoteBackend`)

Base path: `/v1`. All requests authenticated via `Authorization: Bearer <token>` except `/healthz`, `/v1/auth/login`. Routes registered on an Echo `*echo.Echo` instance; global middleware chain is `RequestID → Recover → Logger(slog) → Gzip → CORS → RateLimit`. `/v1/*` group adds the bearer-auth middleware; `/v1/ingest/*` uses a separate sampler-token middleware.

| Method | Path | Purpose | Maps to `RemoteBackend` |
|---|---|---|---|
| GET  | `/healthz` | Liveness (no deps) | — |
| GET  | `/readyz` | Readiness (CH ping + config store) | — |
| POST | `/v1/auth/login` | Email+password → user bearer token | Initial client login |
| POST | `/v1/auth/logout` | Revoke token | — |
| GET  | `/v1/nodes` | List nodes for org | `nodes` store hydration |
| POST | `/v1/nodes` | Create node | `addNode` / `addNodes` |
| GET  | `/v1/nodes/{id}` | Fetch one | — |
| PUT  | `/v1/nodes/{id}` | Update node | `updateNode`, `setNodeEnabled`, `setNodeSnooze`, `setNodeFavorite` |
| DELETE | `/v1/nodes/{id}` | Remove node | `removeNode` |
| POST | `/v1/nodes/{id}/kill-process?pid=…` | Queue control msg | Phase-follow-up kill |
| GET  | `/v1/nodes/{id}/history?metric=cpu&from=…&to=…&step=1m` | Range query, uses rollup MVs | Full-view charts |
| GET  | `/v1/settings` | Fetch `ServerSettings` | `serverSettings` hydration |
| PUT  | `/v1/settings` | Update `ServerSettings` | `updateServerSettings` |
| GET  | `/v1/alerts?state=active` | Active alerts | Snooze/ack UI |
| POST | `/v1/alerts/{id}/ack` | Ack | Shared account state |
| POST | `/v1/alerts/{id}/snooze?until=…` | Snooze | Shared account state |
| GET  | `/v1/sampler/versions` | Current expected sampler sha per triple | `SamplerUpdateCoordinator` display |
| POST | `/v1/sampler/enroll` | Sampler → server: register host, get sampler-token + server URL pin | Used by `sampler service install` |
| POST | `/v1/ingest/samples` | NDJSON push | From sampler push mode |
| GET  | `/v1/control/next?node_id=…` | Sampler long-poll for control messages | Kill-process back-channel |
| GET  | `/v1/stream` | WebSocket — samples + events | `BackendEvent` firehose |
| GET  | `/metrics` | Prometheus | Ops |

**Error envelope** (all JSON endpoints):

```json
{ "error": { "code": "not_found", "message": "node does not exist", "request_id": "…" } }
```

### WebSocket message shapes

```jsonc
// client → server
{ "type": "subscribe", "node_ids": ["…"] }          // optional filter
{ "type": "ping" }

// server → client
{ "type": "sample",   "node_id": "…", "sample": { /* same as sampler.md §4 */ } }
{ "type": "event",    "event": { "kind": "threshold_crossed", … } }
{ "type": "node_updated", "node": { … } }
{ "type": "settings_updated", "settings": { … } }
{ "type": "pong" }
```

On connect the server replays **last known sample per node** + **active unresolved events** so the UI paints immediately.

---

## 5. Sampler push mode + `service` subcommand

Changes to `sampler/` land alongside the server; the binary stays backward-compatible (all existing `--once` / `--interval` / `--self-check` / `--version` / flags keep working).

### 5.1 CLI migration to `urfave/cli/v3`

Replace the handcrafted `flag.NewFlagSet` in `sampler/cmd/sampler/main.go` with a `cli.Command` tree:

```
towertail-sampler                      # default: prints help
towertail-sampler once                 # one-shot (alias for legacy --once)
towertail-sampler stream --interval=1s # NDJSON stream
towertail-sampler push   --endpoint=https://… --token=…  [--interval=30s] [--batch=10]
towertail-sampler service install    --endpoint=… --token=… [--user=…]
towertail-sampler service uninstall
towertail-sampler service start
towertail-sampler service stop
towertail-sampler service status
towertail-sampler service log        [--follow]
towertail-sampler version
towertail-sampler self-check
```

Legacy flags `--once`, `--interval`, `--version`, `--self-check`, `--no-disk`, `--no-net`, `--no-proc`, `--top-n` continue to work at the root command (aliased for bootstrap compat — the Mac app currently invokes `--version`, `--once`, `--self-check` over SSH).

### 5.2 Push mode internals

- Collect loop identical to stream mode, writes samples into an in-memory ring buffer (size `--buffer` default 1000).
- Flusher goroutine POSTs NDJSON (`Content-Encoding: gzip`) to `{endpoint}/v1/ingest/samples` every `--flush` (default 5s) or when `--batch` rows are ready.
- `Authorization: Bearer <sampler-token>`. Token obtained via `service install` via `/v1/sampler/enroll` or supplied directly.
- On 5xx or network error: exponential backoff (1s → 30s), retain samples in ring (drops oldest on overflow; surfaces in `errors[]` next success).
- Heartbeat tick (15s) also polls `/v1/control/next?node_id=…`; acts on kill-process messages by shelling `/bin/kill -9 <pid>` **only if enabled via `--allow-control` flag on install** (default off — audit-safe).

### 5.3 Service management

New Go package `sampler/internal/svc/`:

```
svc/
├── svc.go              # Manager interface: Install/Uninstall/Start/Stop/Status/Log
├── launchd_darwin.go   # writes ~/Library/LaunchAgents/com.towertail.sampler.plist (user agent);
│                       # system daemon at /Library/LaunchDaemons/ requires sudo
├── systemd_linux.go    # writes /etc/systemd/system/towertail-sampler.service (system) OR
│                       # ~/.config/systemd/user/… (user); invokes systemctl
└── service_manager.go  # platform dispatch; uses runtime.GOOS
```

Library choice: `github.com/kardianos/service` provides the launchd/systemd/Windows abstraction but its install path is opinionated and the status/log commands don't match what users expect. Plan is to **template the unit files ourselves** — smaller dep graph, predictable layout, easy `log` implementation (`journalctl -u towertail-sampler` / `log show --predicate …`).

Example systemd unit (`service install`):

```ini
[Unit]
Description=Towertail Sampler
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/towertail-sampler push --endpoint=${ENDPOINT} --interval=30s
Restart=on-failure
RestartSec=5
Environment=TOWERTAIL_TOKEN=<loaded from EnvFile>
EnvironmentFile=/etc/towertail/sampler.env
User=towertail
Group=towertail
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

Example launchd plist (`~/Library/LaunchAgents/com.towertail.sampler.plist`):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>                 <string>com.towertail.sampler</string>
    <key>ProgramArguments</key>
    <array>
      <string>/usr/local/bin/towertail-sampler</string>
      <string>push</string>
      <string>--endpoint=https://towertail.example.com</string>
      <string>--interval=30s</string>
    </array>
    <key>RunAtLoad</key>             <true/>
    <key>KeepAlive</key>             <true/>
    <key>StandardOutPath</key>       <string>/usr/local/var/log/towertail-sampler.log</string>
    <key>StandardErrorPath</key>     <string>/usr/local/var/log/towertail-sampler.err</string>
    <key>EnvironmentVariables</key>
    <dict><key>TOWERTAIL_TOKEN</key><string>…</string></dict>
  </dict>
</plist>
```

Token persisted to `/etc/towertail/sampler.env` (0600) on Linux / `~/Library/Application Support/Towertail/sampler.env` on macOS.

### 5.4 Sampler Dockerfile + docker-compose

Lives at `sampler/docker/`:

```
sampler/docker/
├── Dockerfile            # multi-stage: builder → scratch with CA certs
├── docker-compose.yaml   # single-service example
└── README.md             # "here is how to run the sampler in docker"
```

Dockerfile (sketch):

```dockerfile
# syntax=docker/dockerfile:1.7
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG SHA=unknown
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w -X github.com/towertail/sampler/internal/version.Version=${VERSION} -X github.com/towertail/sampler/internal/version.SHA=${SHA}" \
      -o /out/towertail-sampler ./cmd/sampler

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/towertail-sampler /usr/local/bin/towertail-sampler
ENTRYPOINT ["/usr/local/bin/towertail-sampler"]
CMD ["push", "--endpoint=$TOWERTAIL_ENDPOINT", "--interval=30s"]
```

docker-compose.yaml (sketch):

```yaml
services:
  sampler:
    image: ghcr.io/towertail/sampler:latest
    restart: unless-stopped
    pid: host            # needed for process visibility
    network_mode: host   # needed for accurate net counters
    volumes:
      - /:/hostfs:ro     # read-only mount root for disk metrics
    environment:
      TOWERTAIL_ENDPOINT: ${TOWERTAIL_ENDPOINT}
      TOWERTAIL_TOKEN:    ${TOWERTAIL_TOKEN}
      HOST_PROC:          /hostfs/proc
      HOST_SYS:           /hostfs/sys
      HOST_ETC:           /hostfs/etc
    command: ["push","--endpoint","${TOWERTAIL_ENDPOINT}","--interval","30s"]
```

README documents the `pid: host` / `network_mode: host` requirements (gopsutil on Linux needs host procfs to see more than the container's own namespace).

---

## 6. Server Docker setup

Lives at `server/docker/`:

```
server/docker/
├── Dockerfile                  # towertail-server image
├── docker-compose.yaml         # server + clickhouse + postgres (stub) + caddy
├── clickhouse/
│   ├── config.d/01-logging.xml
│   └── users.d/01-default.xml  # disables default-user no-password login
├── postgres/
│   └── init.sql                # creates towertail db + role (seeds cloud-managed schema)
├── caddy/
│   └── Caddyfile               # reverse proxy + automatic HTTPS
└── README.md
```

`docker-compose.yaml` (self-hosted, single-node):

```yaml
services:
  clickhouse:
    image: clickhouse/clickhouse-server:latest
    restart: unless-stopped
    volumes:
      - ch-data:/var/lib/clickhouse
      - ./clickhouse/config.d:/etc/clickhouse-server/config.d:ro
      - ./clickhouse/users.d:/etc/clickhouse-server/users.d:ro
    environment:
      CLICKHOUSE_DB: towertail
      CLICKHOUSE_USER: towertail
      CLICKHOUSE_PASSWORD_FILE: /run/secrets/ch_password
    secrets: [ch_password]
    ulimits:
      nofile: { soft: 262144, hard: 262144 }
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8123/ping"]
      interval: 10s

  postgres:
    # STUB — only started when cloud_managed=true; kept in compose for parity
    image: postgres:16-alpine
    restart: unless-stopped
    profiles: ["cloud"]
    volumes:
      - pg-data:/var/lib/postgresql/data
      - ./postgres/init.sql:/docker-entrypoint-initdb.d/init.sql:ro
    environment:
      POSTGRES_DB: towertail
      POSTGRES_USER: towertail
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_password
    secrets: [pg_password]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U towertail"]

  server:
    image: ghcr.io/towertail/server:latest
    restart: unless-stopped
    depends_on:
      clickhouse: { condition: service_healthy }
    environment:
      TT_CLICKHOUSE_ADDR: clickhouse:9000
      TT_CLICKHOUSE_DB:   towertail
      TT_CLICKHOUSE_USER: towertail
      TT_CLICKHOUSE_PASSWORD_FILE: /run/secrets/ch_password
      TT_HTTP_ADDR:       :8080
      TT_CONFIG_PATH:     /var/lib/towertail
      TT_CLOUD_MANAGED:   "false"
    volumes:
      - server-data:/var/lib/towertail
    secrets: [ch_password]
    ports: ["127.0.0.1:8080:8080"]

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports: ["80:80","443:443"]
    volumes:
      - ./caddy/Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data
      - caddy-config:/config

volumes: { ch-data: {}, pg-data: {}, server-data: {}, caddy-data: {}, caddy-config: {} }

secrets:
  ch_password: { file: ./secrets/ch_password }
  pg_password: { file: ./secrets/pg_password }
```

`profiles: ["cloud"]` keeps Postgres dormant for self-hosted — `docker compose up` skips it; `docker compose --profile cloud up` starts it.

Caddyfile: `towertail.example.com { reverse_proxy server:8080 }` — one line of TLS.

---

## 7. Source layout

```
server/
├── cmd/
│   └── towertail-server/
│       └── main.go                 # urfave/cli root: serve, migrate, token, user, config
├── internal/
│   ├── api/
│   │   ├── server.go               # Echo instance assembly + global middleware
│   │   ├── routes.go               # route registration (groups per auth class)
│   │   ├── auth.go                 # bearer middleware + login handler
│   │   ├── nodes.go                # CRUD handlers
│   │   ├── settings.go
│   │   ├── alerts.go
│   │   ├── history.go              # range queries → CH
│   │   ├── ingest.go               # NDJSON push endpoint
│   │   ├── control.go              # sampler long-poll
│   │   ├── sampler.go              # enroll + versions
│   │   ├── stream.go               # WS handler
│   │   └── errors.go               # error envelope helpers
│   ├── auth/
│   │   ├── tokens.go               # issue/verify/revoke; sha256 at rest
│   │   └── passwords.go            # argon2id wrapper
│   ├── config/
│   │   ├── config.go               # koanf load; env + yaml
│   │   └── defaults.go
│   ├── store/
│   │   ├── config_store.go         # interface
│   │   ├── bolt_store.go           # BoltDB impl (self-hosted)
│   │   ├── postgres_store.go       # stub impl (cloud-managed); compiles, returns ErrNotImplemented for self-hosted
│   │   └── models.go               # Node, ServerSettings, Alert etc. shared DTOs
│   ├── clickhouse/
│   │   ├── client.go               # pool, batcher, readiness
│   │   ├── schema.go               # embed SQL; idempotent boot DDL
│   │   ├── ingest.go               # write path
│   │   └── query.go                # history range queries
│   ├── postgres/
│   │   ├── client.go               # pgx pool; lazy init if cloud_managed
│   │   └── migrations.go           # goose runner
│   ├── alerter/
│   │   ├── engine.go               # ring buffer + threshold eval
│   │   └── state.go                # per-node state machine
│   ├── hub/
│   │   └── hub.go                  # WS broadcast hub
│   ├── control/
│   │   └── queue.go                # per-node control msg queue in Bolt
│   ├── runtime/
│   │   ├── runtime.go              # Component lifecycle / DI
│   │   └── components.go           # Require(name) guard
│   └── version/
│       └── version.go              # ldflags injected
├── migrations/
│   ├── clickhouse/
│   │   ├── 0001_samples_raw.sql
│   │   ├── 0002_disks.sql
│   │   ├── 0003_disk_io_devices.sql
│   │   ├── 0004_procs_topn.sql
│   │   ├── 0005_events.sql
│   │   └── 0006_rollups.sql
│   └── postgres/
│       ├── 00001_orgs.sql
│       ├── 00002_users.sql
│       ├── 00003_api_tokens.sql
│       └── 00004_audit_log.sql
├── docker/
│   ├── Dockerfile
│   ├── docker-compose.yaml
│   ├── clickhouse/…
│   ├── postgres/…
│   ├── caddy/Caddyfile
│   └── README.md
├── testdata/
│   └── fixtures/                   # golden samples for ingest tests
├── go.mod
├── go.sum
└── README.md
```

---

## 8. Dependencies — full list

### Server (`server/go.mod`)

| Module | Purpose |
|---|---|
| `github.com/labstack/echo/v4` | HTTP framework (router, middleware, binding/validation) |
| `github.com/go-playground/validator/v10` | Request validation via Echo's `Validator` hook |
| `nhooyr.io/websocket` | WebSocket (tracks to `github.com/coder/websocket`); used inside Echo handlers |
| `github.com/urfave/cli/v3` | CLI |
| `github.com/ClickHouse/clickhouse-go/v2` | ClickHouse native proto |
| `github.com/jackc/pgx/v5` | Postgres |
| `github.com/pressly/goose/v3` | Postgres migrations |
| `go.etcd.io/bbolt` | BoltDB (self-hosted config) |
| `github.com/knadh/koanf/v2` + providers | Config |
| `github.com/google/uuid` | UUID |
| `github.com/prometheus/client_golang` | `/metrics` |
| `github.com/stretchr/testify` | Tests |
| `github.com/testcontainers/testcontainers-go` | Integration tests |
| `golang.org/x/crypto` | argon2id |
| `github.com/cespare/xxhash/v2` | Token hash keying |

### Sampler additions (`sampler/go.mod`)

| Module | Purpose |
|---|---|
| `github.com/urfave/cli/v3` | CLI (replaces `flag`) |
| `github.com/klauspost/compress/gzip` | Faster gzip for push |

Service management is done with stdlib + `os/exec` (no `kardianos/service` dependency).

---

## 9. Test strategy

Testing follows a **pyramid**: lots of fast unit tests (`go test ./...` in < 10s), a focused ring of integration tests that spin up real ClickHouse / Postgres / server binaries via `testcontainers-go`, and a small number of end-to-end smoke tests exercising the sampler → server → Swift-client path. Coverage target is **≥ 75% on `internal/alerter`, `internal/clickhouse`, `internal/store`, `internal/api`**; UI-adjacent glue is not gated on coverage.

### 9.1 Guiding principles

- **Real deps over mocks for integration paths.** ClickHouse and Postgres behaviour (TTL drops, MV rollups, partition pruning, JSON binding) doesn't survive mocking. Use `testcontainers-go` to boot real images. Shared container fixtures across a package keep wall-clock cost low.
- **Mocks at boundaries only** — HTTP transport, time, filesystem, `os/exec`. Use small hand-written fakes, not a mocking framework.
- **Golden files** for JSON wire contracts (ingest payloads, REST responses, WS frames). Regenerated via `go test -update`; diffs reviewed in PRs. Guards against accidental schema drift between server and `RemoteBackend`.
- **Table-driven tests** for anything branchy (threshold state machine, flag parsing, retention config).
- **Deterministic clocks.** Alerter, rate-limiter, and retention logic take a `clock.Clock` (our own interface, advanced in tests) — never `time.Now()` directly.
- **Fast by default.** Integration tests live behind `//go:build integration` and a `TT_INTEGRATION=1` env check; CI runs both tiers, `go test ./...` without the tag stays unit-only.

### 9.2 Server (`server/`) test matrix

| Layer | Test type | Key scenarios | Tooling |
|---|---|---|---|
| `internal/config` | Unit | YAML + env override precedence, invalid values rejected | `testify` |
| `internal/auth` | Unit | Token issue/verify/revoke, argon2id round-trip, constant-time compare | `testify` |
| `internal/store` (Bolt) | Unit (real bbolt in tmpdir) | Node CRUD, settings round-trip, concurrent writes | `testify` |
| `internal/store` (Postgres stub) | Integration | Migrations apply cleanly, same CRUD surface parity with Bolt | `testcontainers-go` |
| `internal/clickhouse` | Integration | Schema boot idempotent, batched insert, MV populates, TTL drops old partition (simulated by `ALTER TABLE … MODIFY TTL 0 DAY`), range query picks right resolution | `testcontainers-go` |
| `internal/alerter` | Unit | Threshold state machine: ok→warn→critical→ok, debounce, snooze ignores during window, ack clears, edge-triggered (no re-fire on same tint), clock advances drive transitions | deterministic clock |
| `internal/hub` | Unit | Connect replays last sample + active events, per-org isolation, slow consumer doesn't backpressure fast ones (ring drops), reconnect dedupes | `httptest` + nhooyr client |
| `internal/api` | Unit (handler level) | Each endpoint's happy + 4xx paths; validator rejects malformed DTOs; error envelope shape is stable | Echo's `httptest.NewRecorder`, golden-file response bodies |
| `internal/api` (ingest) | Integration | NDJSON push end-to-end: valid batch → rows in CH; mixed-validity batch partial-accepts; gzip payloads; wrong token → 401; oversize body → 413 | `testcontainers-go` (CH) |
| `internal/api` (stream) | Integration | WS auth handshake, sample fanout latency, event fanout, disconnect cleans up | nhooyr client |
| `internal/api` (control) | Integration | Long-poll returns control message; kill-process end-to-end with a fake sampler | `httptest` |
| `internal/runtime` | Unit | `Require(name)` returns 503 until `Ready()` flips; Start/Stop ordering | fake components |
| `cmd/towertail-server` | Smoke | `serve`, `migrate`, `token issue`, `config dump` don't panic; help output stable (golden) | `os/exec` |

**Load test** (Phase L gate): 500 concurrent simulated samplers @ 30s cadence for 30 minutes via `k6` (or a small bespoke Go driver). Pass criteria: CH write latency p95 < 100ms, server RSS < 256 MiB, zero dropped samples under nominal network. Script lives in `server/testdata/load/`.

### 9.3 Sampler (`sampler/`) test matrix

| Layer | Test type | Key scenarios | Tooling |
|---|---|---|---|
| `cmd/sampler` (CLI) | Unit | `urfave/cli/v3` tree: every subcommand parses expected flags, legacy `--once`/`--version`/`--self-check` still work, help output is stable (golden) | `testify`, golden files |
| `internal/collect/*` | Unit | Existing gopsutil-wrapping tests stay; add a `disk/net` interface-filter test | existing fakes |
| `internal/collect` | Integration (Linux) | Debian + Alpine Dockerfiles run `sampler once`, assert schema and plausible ranges; catches musl/cgo regressions | `sampler/test/*.dockerfile` (already planned in §10 of `sampler.md`) |
| `internal/collect` (Darwin) | Integration | `go test -tags=integration` gated on `runtime.GOOS == "darwin"`, runs on dev machine / CI mac runner | build tag |
| `internal/push` (new in Phase E) | Unit | Ring buffer overflow drops oldest, gzip NDJSON body shape, 5xx triggers exponential backoff, 401 surfaces distinctly | `httptest` |
| `internal/push` | Integration | Real server container receives pushes; reconnects after a restart | `testcontainers-go` |
| `internal/svc` (launchd) | Manual playbook | Documented in `sampler/README.md`: fresh macOS VM → `service install/start/status/log/stop/uninstall`; reboot retains active service | manual checklist, captured screenshots |
| `internal/svc` (systemd) | Manual playbook | Same for a Debian + a RHEL-family VM | manual checklist |
| `internal/svc` | Unit | Unit-file rendering is byte-stable (golden); `status` parser tolerates both `systemctl` formats; error paths don't leave half-installed state | golden, fakes around `os/exec` |
| `heartbeat` / control poll | Integration | Fake server queues a kill; sampler with `--allow-control=true` executes it; without the flag, kill is rejected and logged | `httptest` |

### 9.4 Wire-contract cross-check (server ↔ sampler ↔ Swift)

Single shared fixture set under `testdata/wire/`:

- `samples/*.json` — representative `Sample` payloads (Linux root, Linux non-root, Darwin non-root, container w/o machine-id, post-reboot counter reset).
- `events/*.json` — representative `BackendEvent` payloads.
- `settings/*.json` — representative `ServerSettings` payloads.

Each fixture round-trips through three decoders in CI:

1. **Sampler** (`schema.Sample` encode/decode).
2. **Server** (`models.Sample` decode + CH row build).
3. **Swift** (`Sample.swift` `JSONDecoder`; runs as part of the existing Xcode test target — a new "WireFixtures" XCTest case reads the same JSON files).

Any of the three failing to decode a fixture, or a fixture's canonical form changing unexpectedly, fails CI. This is the contract enforcement that replaces a full `proto/` definition for v1.

### 9.5 End-to-end smoke test

Runs in CI on every `main` push, time-budgeted to < 3 minutes:

1. `docker compose -f server/docker/docker-compose.yaml up -d` (server + ClickHouse, no Postgres).
2. Wait for `/readyz` to return 200.
3. `towertail-server token issue --kind=sampler --label=ci` → capture token.
4. Run `towertail-sampler push --endpoint=http://localhost:8080 --token=$T --interval=1s` for 10 seconds in the background.
5. Assert ClickHouse has ≥ 5 rows for that node in `samples_raw`.
6. Issue a user token, hit `GET /v1/nodes` and `GET /v1/nodes/{id}/history?metric=cpu&from=now-1m&to=now&step=1s`; assert shape.
7. Open WS at `/v1/stream`; assert at least one `sample` frame arrives within 3s.
8. Tear down compose.

Lives at `server/testdata/e2e/smoke.sh`, invoked by a `make e2e` target.

### 9.6 Swift side (`app/mac/Tests/`)

New XCTest target `RemoteBackendTests`:

- **Unit**: REST client encodes/decodes every wire fixture from §9.4 into the right Swift types. Stubbed `URLSession` via `URLProtocol`.
- **Unit**: WS handler routes `sample`, `event`, `node_updated`, `settings_updated` messages to the right observable store mutations. `URLSessionWebSocketTask` stubbed.
- **Unit**: reconnect policy (exponential backoff capped, preserves subscribe state across reconnects).
- **Integration (opt-in)**: an Xcode scheme that points at the compose stack — runs locally when the engineer flips a `TT_E2E_ENDPOINT` env var. Not gated in CI.

### 9.7 CI pipeline shape

- **`unit`** — `go test ./... -race` in both modules + `xcodebuild test` on the Mac target. Runs on every PR.
- **`integration`** — `go test -tags=integration ./... -race` with testcontainers. Runs on every PR (cached images). Skipped on docs-only diffs.
- **`e2e`** — the compose smoke from §9.5. Runs on every PR but allowed to be manually re-triggered if flaky (hard-fails after 3 consecutive flakes on `main`).
- **`lint`** — `golangci-lint`, `go vet`, `swiftlint` where configured.
- **`load`** (Phase L only) — nightly on `main`, results posted to a dashboard; regression > 20% on any tracked metric opens an issue automatically.

### 9.8 Test data & secrets

- No real tokens in fixtures. All bearer/sampler tokens in tests are synthetic (`"test-"` prefixed) and rejected by production builds via a boot-time startup check in non-`test` builds.
- Docker secrets files in `server/docker/secrets/` are `.gitignore`d; examples shipped as `*.example`.

---

## 10. Phased delivery

Each phase is an independently reviewable, shippable increment. Later phases do not block on later-phase scope leaking back.

### Phase A — Scaffolding & contracts (≈ 1 day)

- Flesh out `server/README.md`, scaffold directory tree above. Empty-but-compilable packages.
- Add `server/go.mod` with pinned deps.
- Write `docs/wire.md` (new) documenting the REST + WS message shapes (authoritative contract, replaces any ad-hoc growth).
- No functional code.

**DoD:** `cd server && go build ./...` succeeds; CI pipeline lints and tests pass on empty suites.

### Phase B — Sampler CLI migration (≈ 1 day)

- Introduce `urfave/cli/v3`. Preserve all existing flags as root-command shims so Mac-side bootstrap over SSH (`--version`, `--once`, `--self-check`) still works.
- Add subcommands: `once`, `stream`, `push`, `version`, `self-check`. `push` is a stub that prints "not yet wired" in this phase.
- Update `sampler/cmd/sampler/main_test.go` to exercise the new tree. Add golden help output tests.
- Update `app/mac/Sources/Collectors/SamplerInvoker.swift` and `SamplerUpdateCoordinator.swift` if they rely on exact `--` flag form — they should still work; add assertions in a bootstrap test.

**DoD:** existing Mac-app bootstrap works unchanged; `towertail-sampler --help` shows new subcommands; `go test ./...` green.

### Phase C — Server skeleton: config, HTTP, health (≈ 1 day)

- `cmd/towertail-server/main.go` with `serve` subcommand.
- `internal/config` loads `TT_*` env + YAML.
- `internal/runtime` with `Component` + `Require` guard.
- `/healthz`, `/readyz`, `/metrics` handlers.
- `docker/Dockerfile` builds a working server image.

**DoD:** `docker compose up server` serves `/healthz` returning `ok`; `/readyz` reports CH not-yet-configured.

### Phase D — ClickHouse integration & ingest (≈ 2 days)

- `internal/ch/client.go` with lazy dial + pool.
- Embed `migrations/clickhouse/*.sql`; on first successful dial, create schema if tables absent.
- `POST /v1/ingest/samples` — NDJSON → batcher → CH `samples_raw` / `disks` / `disk_io_devices` / `procs_topn`.
- Batch flush at 5k/1s/1MiB configurable.
- Integration tests via `testcontainers-go` booting a throwaway ClickHouse.
- Self-metrics: `towertail_ingest_samples_total`, `towertail_ingest_batch_flush_seconds`.

**DoD:** pushing 10k synthetic samples with `hey` lands them in `samples_raw`; rollup MVs populate; TTL drops old partition in a test.

### Phase E — Sampler push mode (≈ 1.5 days)

- Flesh out `push` subcommand: ring buffer, flusher, gzip NDJSON, backoff, heartbeat.
- Add `--endpoint`, `--token`, `--interval`, `--flush`, `--batch`, `--buffer`, `--allow-control`.
- `sampler/docker/` with Dockerfile + compose + README.
- Integration: `docker compose up` (server + clickhouse + one sampler) produces live rows.

**DoD:** a real sampler container pushes to a real server container, samples observable in ClickHouse via HTTP query.

### Phase F — Sampler `service` subcommand (≈ 1.5 days)

- `internal/svc` with launchd (darwin) + systemd (linux) impls.
- Subcommands `install`, `uninstall`, `start`, `stop`, `status`, `log` (+ `--follow`).
- Token storage layout per §5.3.
- Manual-test playbook in `sampler/README.md`.

**DoD:** fresh macOS + Debian VMs: `sampler service install --endpoint=… --token=…` followed by `status` shows active; `log --follow` tails; reboot keeps service running.

### Phase G — Auth + node CRUD (≈ 2 days)

- `internal/auth`: token issue, verify, revoke; argon2id for passwords.
- `BoltStore` for nodes + `ServerSettings` + tokens.
- `/v1/auth/login`, `/v1/nodes*`, `/v1/settings*`, `/v1/sampler/enroll`.
- Admin CLI: `towertail-server token issue --kind=user|sampler --label=…`.

**DoD:** fresh deployment → `token issue` → Mac client (RemoteBackend wired) adds and lists nodes.

### Phase H — Alerter + events + WS stream (≈ 2 days)

- `internal/alerter` with per-node ring and threshold state machine (mirrors `ThresholdNotifier` semantics: edge-triggered, debounce, snooze, ack).
- `internal/hub` WS hub; `/v1/stream` handler; connect → replay last sample + active events → stream.
- Alert persistence in `towertail.events`; `/v1/alerts*` endpoints.
- Settings-driven thresholds applied live on change.

**DoD:** synthetic sample crossing a threshold fans out as `BackendEvent` to a connected WS client and to Mac notification when the Swift side renders it.

### Phase I — Wire `RemoteBackend` in Swift (≈ 2 days)

- Flesh out `app/mac/Sources/Backend/RemoteBackend.swift`:
  - `URLSession` REST client, `URLSessionWebSocketTask` for stream.
  - Token + endpoint persisted in Keychain / `ClientSettings`.
  - Populate `servers` / `nodes` / `serverSettings` from server responses.
  - CRUD methods hit REST, optimistic-update observable stores.
  - `start()` opens WS, dispatches messages to stores.
- Add a `BackendMode` toggle in preferences (behind an advanced flag for now).
- Integration smoke: run the docker-compose stack locally, point the Mac app at `https://localhost`, see live cards.

**DoD:** `RemoteBackend` fully conforms to `Backend` with no `BackendError.notImplemented`; docker-compose + Mac app is a working end-to-end demo.

### Phase J — History & kill-process control channel (≈ 1.5 days)

- `GET /v1/nodes/{id}/history` over rollup MVs, step-sensitive resolution picker.
- Control queue via BoltDB; sampler long-poll at `/v1/control/next`; kill-process wired end-to-end behind `--allow-control`.
- Swift `RemoteBackend.killProcess` → REST.

**DoD:** full-view charts render history from server; kill-process on a remote sampler terminates the target pid.

### Phase K — Postgres stub (cloud-managed seam) (≈ 0.5 day)

- `postgres_store.go` compiles; goose migrations for Postgres schema in §3.
- `TT_CLOUD_MANAGED=true` boots Postgres component; `token issue` etc. mirror to Postgres (but tests can be skipped behind an integration tag).
- `docker-compose --profile cloud up` brings Postgres online.

**DoD:** schema applies cleanly; self-hosted path unchanged and unaware of Postgres.

### Phase L — Hardening (≈ 2 days, recurring)

- Rate-limit middleware on ingest + login.
- CORS config for future browser UI.
- Structured audit logs on node/settings changes.
- Load test: 500 simulated samplers @ 30s → verify CH write latency < 100ms p95, server memory < 256MiB.
- Docs pass: `server/README.md` quickstart; `docs/server-runbook.md` for ops.

**DoD:** load test green, runbook checked in, Caddy + TLS example deployable.

**Total estimate**: ~16 eng-days for Phase A–K; Phase L ongoing. Reasonable for a single engineer over three weeks with the sampler push work front-loaded.

---

## 11. Open questions / follow-ups

- **Tailscale identity.** Sampler enrollment could leverage the host's Tailscale identity instead of a pre-shared token — nice for fleets already on Tailnet. Deferred; token path ships first.
- **mTLS instead of bearer.** Nicer for closed networks. Add once `enroll` lands — cert material reuses the enroll path.
- **Retention UX.** Today retention is a config knob. A future settings pane may expose it per-org once Postgres is active.
- **Proto repo.** `proto/` stays empty in this plan; the ingest/stream wire stays JSON/NDJSON for simplicity. If we adopt gRPC for ingest later it moves to `proto/` per `PLAN.md`.
- **Sampler → WebSocket back-channel.** Eliminates control long-poll latency (currently up to 15s for a kill). Deferred until we see real demand.

---

## 12. Acceptance checklist for Phase 3 "done"

- [ ] `RemoteBackend` satisfies the full `Backend` protocol with zero `notImplemented`.
- [ ] `docker compose up` starts server + ClickHouse and is reachable over Caddy TLS.
- [ ] `sampler service install` works on macOS and Linux end-to-end.
- [ ] Mac app, pointed at the self-hosted server, shows live cards, receives threshold alerts, and can add/remove nodes.
- [ ] `docs/server-plan.md` (this file) kept in sync with reality — out-of-sync is a review blocker.
