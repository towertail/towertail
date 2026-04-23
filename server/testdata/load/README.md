# Load generators

Not wired into CI. Run against a dedicated test server.

## Ingest throughput

```bash
go run -tags=loadtest ./testdata/load \
  --url=http://localhost:8080/v1/ingest/samples \
  --token=tt_xxx \
  --nodes=100 --rps=1000 --duration=60s
```

Watch `/metrics` for the batcher flush counter and ClickHouse insert
latency. A healthy single-node server should hold 2–5k rps with the
defaults. Above that, raise `clickhouse.batch_flush_rows` and the CH
connection pool.
