# towertail

A compact macOS menu-bar app for keeping an eye on a handful of remote servers — typically over a Tailscale network. One panel, one card per host, live CPU / memory / disk / network with sparklines and threshold-tinted numbers. Stats.app vibes, but for your fleet instead of your laptop.

Inspired by [exelban/Stats](https://github.com/exelban/stats) and [mac-stats.com](https://mac-stats.com/).

## Repository layout

```
towertail/
├── app/
│   ├── mac/      # Phase 1 — SwiftUI macOS menu-bar app (v1 shipping target)
│   └── windows/  # Phase 2 — stub, not started
├── sampler/        # Go collector binary, 5 target triples, bundled into app/mac
├── server/       # Phase 3 — stub; self-hostable Go control-plane service
├── proto/        # Phase 3 — stub; wire contract shared across components
└── docs/         # PLAN.md (source of truth), sampler.md, research notes, design.pen, screenshots/
```

Today only `app/mac/` and `sampler/` have real content. `app/windows/`, `server/`, and `proto/` are README-only stubs reflecting the phased roadmap — see [PLAN.md § Roadmap: phases](docs/PLAN.md#roadmap-phases).

The Mac app embeds the sampler binaries, pushes the right one to `~/.towertail/sampler` on each monitored server on first connect, then polls it over SSH for JSON samples.

## Docs

- [`docs/PLAN.md`](docs/PLAN.md) — product decisions, visual design, architecture, milestones.
- [`docs/sampler.md`](docs/sampler.md) — Go sampler spec: JSON schema, build matrix, bootstrap handshake.
- [`docs/research-stats.md`](docs/research-stats.md), [`research-backend.md`](docs/research-backend.md), [`research-frontend.md`](docs/research-frontend.md) — background research.
- [`docs/screenshots/`](docs/screenshots/) — current Pencil design exports.

## Status

Pre-M1. No code yet; the plan is the artifact.