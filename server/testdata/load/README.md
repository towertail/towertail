# Load generators

Not wired into CI (CI only runs `go vet -tags=loadtest ./testdata/load`). Run against a dedicated test server.

## Ingest

The generator acts as many hosts. Each host has its own node and sampler token, because the server takes the node from the token. Each host sends one gzip POST each interval with a real Linux sample (`sample-linux.json`: 40 processes, 4 disks, 2 disk I/O devices). The values change a little on each send, so the compression is real.

1. Make the tokens. The command opens the Bolt file of the server, so stop the server first. It makes N nodes and writes `node_id,token` lines:

   ```bash
   towertail-server token issue-bulk --count=10000 --out=tokens.csv   # --org=<uuid> for another org
   ```

   Each node and token is one Bolt write. 10,000 take about 5 minutes.

2. Start the server. All the load comes from one IP, so raise `TT_HTTP__INGEST_RATE_LIMIT_RPS` (default 1000 for each IP). 10,000 hosts at 10 s send 1000 requests each second. Above the limit, the server returns 429.

3. Run the generator in `server/`:

   ```bash
   go run -tags=loadtest ./testdata/load \
     --url=http://localhost:8080/v1/ingest/samples \
     --tokens=tokens.csv --interval=10s --duration=30m
   ```

   Other flags: `--payload` (default `testdata/load/sample-linux.json`), `--jitter=2s` (each wait changes by up to half of it), `--concurrency=512` (requests in flight at most).

   Each 10 s and at the end, it writes: requests, 2xx, 503, other status codes, errors, p50 and p99 latency. 503 means that the batcher queue is full (refer to `towertail_batcher_queue_depth` on `/metrics`).

4. Read the report:

   ```bash
   clickhouse-client --database=towertail --multiquery < testdata/load/report.sql
   ```

   It gives the rows, the compressed and uncompressed size and the bytes for each row of each table, the bytes for each host for each day, the hosts in the detail tables, the insert times from `system.query_log`, and the CPU and memory of ClickHouse.

## Smoke test (100 hosts, 2 minutes)

```bash
cd server/docker && docker compose -f docker-compose.test.yaml -p tt-smoke up -d --wait clickhouse && cd ..
export TT_STORAGE__PATH=$(mktemp -d) TT_CLICKHOUSE__ADDR=127.0.0.1:19000 \
  TT_CLICKHOUSE__USER=towertail TT_CLICKHOUSE__PASSWORD=test \
  TT_HTTP__ADDR=127.0.0.1:18081 TT_HTTP__INGEST_RATE_LIMIT_RPS=10000
go run ./cmd/towertail-server token issue-bulk --count=100 --out=$TT_STORAGE__PATH/tokens.csv
go run ./cmd/towertail-server serve &
go run -tags=loadtest ./testdata/load --url=http://127.0.0.1:18081/v1/ingest/samples \
  --tokens=$TT_STORAGE__PATH/tokens.csv --duration=2m
docker exec -i tt-smoke-clickhouse-1 clickhouse-client --user=towertail --password=test \
  --database=towertail --multiquery < testdata/load/report.sql
```

The report shows 100 hosts in `samples_raw`, `procs_topn`, `disks` and `disk_io_devices`.

If the run gives 503 or high latency, raise `clickhouse.batch_flush_rows` and the ClickHouse connection pool.
