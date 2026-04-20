# proto/

**Not started.** The wire contract shared between [`../agent/`](../agent/), [`../server/`](../server/) (Phase 3), and the desktop clients in [`../app/`](../app/).

Today the agent emits JSON per [`../docs/agent.md`](../docs/agent.md) §4 and the Mac app decodes it directly — no formal schema file. This folder exists for when the schema outgrows that: protobuf `.proto` files, versioned JSON Schema, or whatever the third consumer (Windows app, Go server) demands.

See [PLAN.md — Roadmap: phases](../docs/PLAN.md#roadmap-phases).
