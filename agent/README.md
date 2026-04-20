# agent/

Single-binary Go collector that runs on each monitored server and emits one NDJSON sample per invocation (or per tick in streaming mode).

Shipped inside the Mac app bundle (five target triples) and pushed to `~/.towertail/agent` on the remote host on first connect. See [`../docs/agent.md`](../docs/agent.md) for the spec, JSON schema, build matrix, and bootstrap handshake.
