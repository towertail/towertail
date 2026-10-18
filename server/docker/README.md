# server/docker

Self-hosted deployment for the Towertail server. One command starts ClickHouse, the server, and Caddy (a TLS reverse proxy).

## Quick start

Run these commands in `server/docker`:

```bash
openssl rand -hex 24 > secrets/ch_password
docker compose up -d
```

Compose pulls `ghcr.io/towertail/server:${TOWERTAIL_VERSION:-latest}`. To build the image from source, add `--build`.

The server is at `http://localhost:8080` (direct). Caddy also serves it on port 80 and at `https://towertail.local`. For `towertail.local`, add `127.0.0.1 towertail.local` to `/etc/hosts`. Caddy signs that certificate with its own local CA.

Make the first user token. The server keeps its config database open, so stop it first:

```bash
docker compose stop server
docker compose run --rm server token issue --kind=user --label=me
docker compose start server
```

The command writes the raw token to stdout. Give it to the desktop app (refer to [`../README.md`](../README.md#connect-the-mac-app)).

## Cloud-managed profile

```bash
openssl rand -hex 24 > secrets/pg_password
docker compose --profile cloud up -d
```

This profile also starts Postgres. It does not enable cloud-managed mode in the server. To enable it, set `TT_CLOUD__MANAGED=true` and `TT_POSTGRES__DSN` on the `server` service (for example in a `docker-compose.override.yaml`).

## Directory layout

- `Dockerfile`: builds the distroless `towertail-server` image. The build context is `server/`.
- `docker-compose.yaml`: the stack. Postgres is in the `cloud` profile.
- `docker-compose.test.yaml`: the stack for the integration tests (`scripts/tests.sh --docker-up`).
- `clickhouse/`: ClickHouse config files.
- `postgres/init.sql`: runs one time, when Postgres starts on an empty volume (cloud-managed).
- `caddy/Caddyfile`: the TLS reverse proxy.
- `secrets/`: password files. Git ignores them. Only the `.example` files are in the repository.

## Troubleshooting

- Server logs: `docker compose logs -f server`.
- Check ClickHouse: `docker compose exec clickhouse sh -c 'clickhouse-client --password "$(cat /run/secrets/ch_password)" -q "SELECT 1"'`.
- Check the server: `curl http://localhost:8080/readyz` returns `ready` when ClickHouse is connected.
- Ingest returns 503: the batcher queue is full. The samplers send again after `Retry-After`. Look at `towertail_batcher_queue_depth` on `/metrics`.
- ClickHouse was down: the server keeps the failed batches in `/var/lib/towertail/spill` (in the server volume) and replays them when ClickHouse is back. `towertail_batcher_spill_bytes` shows the size.
- Build again after a code change: `docker compose up -d --build server`.
