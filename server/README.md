# server/

**Not started.** Phase 3 — the control-plane service. Go codebase, runs in Docker, self-hostable by customers on closed networks and later offered as managed cloud.

Responsibilities (future): continuously ingest samples from agents on a fleet, persist time-series history, evaluate alert rules centrally, expose a gRPC/REST API for the desktop clients to read from.

Kept as a sibling of [`../agent/`](../agent/) because they run in different trust zones with different deploy cadences; the only thing they share is the wire contract in [`../proto/`](../proto/).

See [PLAN.md — Roadmap: phases](../docs/PLAN.md#roadmap-phases). Do not scaffold Go code here until Phase 3 starts.
