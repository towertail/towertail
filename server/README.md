# server/

The Towertail server (`towertail-server`), a Go binary. It receives the samples that `towertail-sampler` pushes, keeps the time series in ClickHouse, checks the alert thresholds, and serves a REST and WebSocket API to the Mac and Windows apps. You run it in your own network (self-hosted). The managed cloud uses the same code.

## Quick start (self-hosted)

```bash
cd server/docker
openssl rand -hex 24 > secrets/ch_password
docker compose up -d
```

Refer to [`docker/README.md`](docker/README.md) for the user token, Caddy, and the cloud profile.

## Run with Docker

Each `server/vX.Y.Z` tag publishes a multi-arch image (`linux/amd64`, `linux/arm64`) to `ghcr.io/towertail/server`, and a GitHub release with Linux binaries and `checksums.txt`. The server release is never the "latest" GitHub release, because that is the Mac app release.

```bash
docker run --rm ghcr.io/towertail/server:<version> --version
```

The versions are on the [releases page](https://github.com/towertail/towertail/releases) (tags `server/v*`).

Image tags: `X.Y.Z`, `X.Y`, and `latest`, for final versions only. A pre-release gets only `X.Y.Z-rc.N`. In `docker/docker-compose.yaml`, set `TOWERTAIL_VERSION` to pin a version:

```bash
cd server/docker
TOWERTAIL_VERSION=<version> docker compose up -d
```

Without `TOWERTAIL_VERSION`, compose uses `latest`. `docker compose build` builds the image from source.

## Connect the Mac app

The Mac app uses the server when it starts with these two environment variables:

```bash
TOWERTAIL_REMOTE_URL=http://localhost:8080 \
TOWERTAIL_REMOTE_TOKEN=tt_... \
  /Applications/Towertail.app/Contents/MacOS/Towertail
```

Without them, the app uses SSH to each host (local mode). There is no setting for this in Preferences yet.

## Layout

```
cmd/towertail-server/    urfave/cli entry point: serve, migrate, token
internal/
├── api/                 Echo server, routes, handlers
├── auth/                tokens and password hashes
├── config/              koanf: YAML and env
├── store/               ConfigStore interface, Bolt, Postgres stub
├── clickhouse/          pool, batcher, queries
│   └── migrations/      embedded SQL, applied on the first connection
├── postgres/            client for cloud-managed mode (stub)
│   └── migrations/      embedded goose migrations
├── alerter/             threshold state machine
├── hub/                 WebSocket fan-out
├── control/             control queue for each node (for example, kill process)
├── runtime/             component lifecycle
├── wire/                JSON types between the services
└── version/             set by ldflags
docker/                  Dockerfile, compose, Caddy
testdata/load/           load generator
```

Not started: the relay (`cmd/towertail-relay`) and alert delivery (`internal/notify`). Refer to the status table in [`docs/server-plan.md`](../docs/server-plan.md#status).

Refer to [`docs/server-plan.md`](../docs/server-plan.md) for the design, [`docs/wire.md`](../docs/wire.md) for the wire contract, and [`docs/server-runbook.md`](../docs/server-runbook.md) for operation (first start, failure modes, upgrades, audit log).

## Config

The defaults are in the code. To change them, use a YAML file (`TT_CONFIG_FILE=/path/to/towertail-server.yaml`) or `TT_` environment variables. In a variable name, a double underscore (`__`) separates the levels. For example, `TT_HTTP__ADDR` sets `http.addr`, and `TT_CLICKHOUSE__PASSWORD_FILE` sets `clickhouse.password_file`. A single underscore stays part of the key name. Other examples: `TT_CLICKHOUSE__ADDR`, `TT_CLOUD__MANAGED`, `TT_POSTGRES__DSN`, `TT_AUTH__BOOTSTRAP_TOKEN`. Refer to [`config/config.go`](config/config.go) for all the keys.

## Build

The module path is `github.com/towertail/towertail/server`. Release tags use the form `server/vX.Y.Z`. The module imports `pkg/schema` from `github.com/towertail/sampler` (the version is in `go.mod`).

```bash
go build -o bin/towertail-server ./cmd/towertail-server
../scripts/build.server.sh          # dist/server/<triple>/towertail-server, version from the server/v* tag
```

## Test

```bash
go test ./...                                          # unit tests
go test -tags=integration ./internal/clickhouse/...    # ClickHouse in testcontainers (needs Docker)
../scripts/tests.sh --go-unit --go-integration         # the same, from the repository root
```

## Cloud-managed mode

Set `TT_CLOUD__MANAGED=true` and `TT_POSTGRES__DSN` to start the Postgres component. In self-hosted mode, Postgres stays off and the config is in a BoltDB file in `storage.path`.
