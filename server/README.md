# server/

Towertail control-plane (Phase 3 per [`docs/PLAN.md`](../docs/PLAN.md)). Go binary (`towertail-server`) that ingests samples pushed by `towertail-sampler`, stores time-series in ClickHouse, evaluates thresholds, and serves a REST + WebSocket API the Mac/Windows clients consume.

## Quick start (self-hosted)

```bash
cd server/docker
cp secrets/ch_password.example secrets/ch_password
docker compose up -d
```

Point the Mac app at `https://localhost` (via Caddy) after issuing a user token:

```bash
docker compose exec server towertail-server token issue --kind=user --label=fritz
```

## Layout

```
cmd/towertail-server/    # urfave/cli entrypoint (serve, migrate, token)
internal/
├── api/                 # Echo server, routes, handlers
├── auth/                # token + password hashing
├── config/              # koanf YAML + env
├── store/               # ConfigStore interface + Bolt + PG stub
├── clickhouse/          # pool, batcher, queries
│   └── migrations/      # embedded SQL, applied on first dial
├── postgres/            # stub client for cloud-managed mode
│   └── migrations/      # embedded goose migrations
├── alerter/             # threshold state machine
├── hub/                 # WebSocket fan-out
├── control/             # per-node control queue (kill process etc.)
├── runtime/             # Component lifecycle / lazy-require
├── wire/                # cross-service JSON types
└── version/             # ldflags-injected
docker/                  # Dockerfile, docker-compose, reverse proxy
testdata/                # fixtures, load, e2e
```

See [`docs/server-plan.md`](../docs/server-plan.md) for the full design, [`docs/wire.md`](../docs/wire.md) for the authoritative wire contract, and [`docs/server-runbook.md`](../docs/server-runbook.md) for operating notes (first boot, failure modes, upgrades, audit log).

## Config

Defaults ship in code; override via `TT_CONFIG_FILE=/path/to/towertail-server.yaml` or env vars like `TT_HTTP_ADDR`, `TT_CLICKHOUSE_ADDR`, `TT_CLOUD_MANAGED`. See [`internal/config/config.go`](internal/config/config.go) for the full list.

## Build

```bash
go build -o bin/towertail-server ./cmd/towertail-server
```

## Test

```bash
go test ./...                       # unit
go test -tags=integration ./...     # spins ClickHouse + Postgres via testcontainers
make e2e                            # compose + smoke script in testdata/e2e
```

## Cloud-managed mode

Flip `TT_CLOUD_MANAGED=true` (plus `TT_POSTGRES_DSN`) to activate the Postgres component. In self-hosted mode Postgres stays dormant and the config lives in a BoltDB file under `storage.path`.
