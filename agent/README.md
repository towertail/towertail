# agent/

Single-binary Go collector that runs on each monitored server and emits one NDJSON sample per invocation (or per tick in streaming mode).

Shipped inside the Mac app bundle (five target triples) and pushed to `~/.towertail/agent` on the remote host on first connect. See [`../docs/agent.md`](../docs/agent.md) for the spec, JSON schema, build matrix, and bootstrap handshake.

## Build

```
# from repo root
scripts/build.agent.sh              # build all 5 targets → dist/agents/<triple>/
scripts/build.agent.sh darwin-arm64 # single target (fast iteration)
```

## Test

```
cd agent && go test ./...
```

## Run locally

```
cd agent
go run ./cmd/agent --once          # emit one JSON sample
go run ./cmd/agent --version       # print version
go run ./cmd/agent --self-check    # print "ok" if the binary runs
go run ./cmd/agent --interval 1s   # stream NDJSON once per second
```
