# server/docker

Self-hosted deployment for the Towertail server. Brings up ClickHouse + the server + Caddy (TLS reverse proxy) with one command.

## Quick start

```bash
cp secrets/ch_password.example secrets/ch_password
echo 'a-strong-random-string' > secrets/ch_password
docker compose up -d --build
```

Server will be reachable at `http://localhost:8080` (direct) and `https://towertail.local` (Caddy, edit your `/etc/hosts` to point at 127.0.0.1). Issue a first user token:

```bash
docker compose exec server towertail-server token issue --kind=user --label=me
```

The raw token is printed on stdout — hand it to the Mac client.

## Cloud-managed profile

```bash
cp secrets/pg_password.example secrets/pg_password
docker compose --profile cloud up -d
```

This starts Postgres alongside the rest and flips `TT_CLOUD_MANAGED=true` so the server's Postgres component activates.

## Directory layout

- `Dockerfile` — builds the `towertail-server` distroless image.
- `docker-compose.yaml` — stack definition. Postgres is behind the `cloud` profile.
- `clickhouse/` — ClickHouse server-side config snippets.
- `postgres/init.sql` — runs once on Postgres first boot (cloud-managed).
- `caddy/Caddyfile` — TLS reverse proxy.
- `secrets/` — password files. `.gitignore`d; ship only `.example` variants.

## Troubleshooting

- `towertail-server` logs: `docker compose logs -f server`.
- ClickHouse not ready: `docker compose exec clickhouse clickhouse-client -q "SELECT 1"`.
- Rebuild after code change: `docker compose up -d --build server`.
