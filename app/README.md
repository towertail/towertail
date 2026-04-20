# app/

Desktop clients. One subfolder per platform so each can have its own toolchain, build system, and packaging pipeline without leaking into the others.

- [`mac/`](mac/) — SwiftUI menu-bar app (Phase 1, shipping first).
- [`windows/`](windows/) — Windows client (Phase 2, not started).

If a `shared/` folder shows up here later, it holds truly cross-platform assets (icons, translations, protocol types if we don't put them in [`../proto/`](../proto/)). Don't create it speculatively.

See [PLAN.md — Roadmap: phases](../docs/PLAN.md#roadmap-phases) for the rollout order.
