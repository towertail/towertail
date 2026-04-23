# Towertail server runbook

Operational notes for `towertail-server`. Design rationale is in
[`server-plan.md`](server-plan.md); the wire contract is in
[`wire.md`](wire.md). This file is the *operator* view — what to do when
something is on fire.

---

## 1. Service layout

```
┌──────────────┐    POST /v1/ingest/samples (gzip NDJSON)
│   samplers   │──────────────────────────────────────┐
└──────────────┘                                      ▼
                                              ┌────────────────┐
┌──────────────┐    REST + WS                 │ towertail-srv  │
│   Mac app    │◀────────────────────────────▶│  (Echo/Go)     │
└──────────────┘                              └───────┬────────┘
                                                      │
                                   ┌──────────────────┼────────────────┐
                                   ▼                  ▼                ▼
                         ClickHouse (TSDB)   BoltDB (config)   Postgres (cloud-only)
```

Components inside the server process (all implement `runtime.Component`):

| Name         | Started by                | What it does                              |
|--------------|---------------------------|-------------------------------------------|
| `clickhouse` | first ingest / history    | Dials CH, applies embedded migrations     |
| `alerter`    | server start              | Threshold state machine, emits events     |
| `hub`        | server start              | WebSocket fan-out (per-org)               |
| `control`    | kill-process request      | Per-node control queue                    |
| `postgres`   | `cloud.managed=true` only | User/org store for cloud-managed mode     |

Lazy start means ClickHouse can be down at boot and the server still
comes up, then heals when CH returns.

---

## 2. First-boot bootstrap

```bash
docker compose -f server/docker/docker-compose.yaml up -d
docker compose exec server towertail-server migrate     # Postgres only
docker compose exec server towertail-server token issue --kind=admin --label=ops
```

Save the printed token — it's shown **once**, not stored in plaintext.
Subsequent Mac clients get `kind=user` tokens via the same command.

Sampler tokens are bound to a node; mint them with:

```bash
towertail-server token issue --kind=sampler --node-id=<uuid> --label=host-xyz
```

---

## 3. Health and observability

- `GET /healthz` — process is up, Echo is serving. No dependencies.
- `GET /readyz` — lazy-started components that *have been required* must
  be `Ready()`. Returns 503 while CH is reconnecting, for example.
- `GET /metrics` — Prometheus text format (bundled `promhttp.Handler`).

Structured logs go to stdout in JSON by default (`log.format=json`).
Three categories worth filtering on:

- `msg=audit` — one line per mutation (create/update/delete/kill). Keep
  these for compliance/forensics; see §7.
- `msg="api: request"` — per-request access log (method, path, status,
  duration, request_id).
- `msg="api: error"` — any 5xx — page on a sustained rate here.

---

## 4. Common failure modes

### ClickHouse dial failures

Symptom: `readyz` returns 503 once Mac clients hit `/v1/nodes/:id/history`,
logs show `clickhouse: dial: …`. The rest of the API keeps working since
CH is lazy — only history + ingest degrade.

Fix: check `docker compose ps clickhouse`, then `logs clickhouse`. Most
common cause is the `ch_password` secret file mismatching the
`users.d/towertail.xml` hash.

### Ingest 413 or 400 spam

Symptom: sampler push logs flood with HTTP 413 / 400 from the server.

Cause 1 — body over `TT_INGEST_MAX_BODY_BYTES` (default 16 MiB). Lower
the sampler `--batch` size or raise the server limit.

Cause 2 — sampler version mismatch broke schema. Check `wire.v` in the
rejected payload; if it's != 1, the sampler predates this server.

### WebSocket "slow consumer"

Symptom: `hub: dropped slow client` in logs. The per-client send buffer
overflowed and we cut the connection rather than backpressure the hub.

Mitigation: the client will reconnect. Persistent drops on the same
token usually mean the client is paused or stuck in a sheet — check Mac
app `Logger` output.

### Rate-limit 429s

The generic limiter is per-IP at `TT_HTTP_RATE_LIMIT_RPS` (default 100).
The ingest limiter is separate at `TT_HTTP_INGEST_RATE_LIMIT_RPS`
(default 1000). If the Mac app gets 429s, raise the generic; if samplers
get them, raise ingest.

### Alerter stuck on a stale threshold crossing

Symptom: a node keeps paging after it recovered.

The engine debounces return-to-OK by
`ServerSettings.NotifyDebounceSeconds`. If the sample stream is flapping
*under* that window, the alerter won't emit OK. Raise the debounce, or
check the sampler for jitter (CPU busy-loop on the host itself).

---

## 5. Backup and retention

**ClickHouse** — `samples_raw` retained 7 days, `procs_topn` 3 days,
rollups 30 days, `events` 90 days. All TTL-enforced per table; no
external cron needed. Back up with
`clickhouse-backup create && clickhouse-backup upload` if you need
longer history than retention.

**BoltDB** — single file under `storage.path`. Copy it while the server
is stopped, or use `bbolt` file-level snapshot; there is no online
backup tool. Losing it loses node/user/settings config but does *not*
lose metrics.

**Postgres** (cloud only) — goose migrations in
`server/migrations/postgres`. Standard `pg_dump` for backup.

---

## 6. Upgrading

1. `docker compose pull server`
2. `docker compose exec server towertail-server migrate` (Postgres only —
   ClickHouse migrations auto-apply on dial).
3. `docker compose up -d server`

Mac clients and samplers reconnect automatically. The wire version
(`wire.v = 1`) is pinned; a server rolling to `v = 2` without sampler
upgrades is a breaking change — use `docs/wire.md` to gate it.

---

## 7. Audit log

Every mutation emits a structured `audit` log line with:

- `action` — `create` / `update` / `delete` / `kill_process`
- `resource` — `node` / `settings`
- `id` — resource UUID
- `org_id`, `actor_token`, `actor_kind`
- `request_id` — matches the request-log line 1:1

Pipe stdout to Loki/CloudWatch/whatever and keep at least 90 days. This
is the only record of *who killed PID 1337 on prod-db-3*.

---

## 8. Security notes

- Tokens are stored only as SHA-256 hashes; the raw `tt_<base64>` is
  shown once at issue and never persisted. Rotate by re-issuing; the
  old hash is left in place with `revoked_at` set.
- Passwords use argon2id via `auth.HashPassword` — no cost-of-living
  fallback, bump `argon2Time` if you need to harden further.
- CORS defaults to `*`. In production set
  `TT_HTTP_CORS_ALLOW_ORIGINS='["https://app.towertail.com"]'` so
  browsers can't call the API from anywhere.
- Bearer tokens are required on *every* route except `/v1/auth/login`,
  `/healthz`, `/readyz`, `/metrics`.
- TLS is terminated by Caddy (in the compose file); the server itself
  is plain HTTP on `:8080`. Don't expose `:8080` to the internet.
