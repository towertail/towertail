# towertail

A compact macOS menu-bar app for keeping an eye on a handful of remote servers — typically over a Tailscale network. One panel, one card per host, live CPU / memory / disk / network with sparklines and threshold-tinted numbers. Stats.app vibes, but for your fleet instead of your laptop.

Inspired by [exelban/Stats](https://github.com/exelban/stats) and [mac-stats.com](https://mac-stats.com/).

## How it works

The Mac app bundles a small Go collector (`towertail-sampler`) for five target triples. On first connect to a server it uploads the right binary to `~/.towertail/towertail-sampler`, then polls it over SSH on a configurable cadence (default 30s) and reads back one JSON sample per poll. Samples land in a local SQLite store; the popover and per-host Full View render directly from there.

Hosts are discovered from `tailscale status --json` and/or added manually. Credentials come from your existing `~/.ssh/config`; the app shells out to `/usr/bin/ssh` with `ControlMaster=auto`, so Tailscale MagicDNS and your normal SSH setup "just work". No daemon, no agent, no listening socket on the host.

## Repository layout

```
towertail/
├── app/
│   ├── mac/      # Phase 1 — SwiftUI macOS menu-bar app (v1 shipping target)
│   └── windows/  # Phase 2 — stub, not started
├── sampler/      # Go collector binary, 5 target triples, bundled into app/mac
├── server/       # Phase 3 — stub; self-hostable Go control-plane service
├── proto/        # Phase 3 — stub; wire contract shared across components
├── scripts/      # build.sh, build.app.sh, build.sampler.sh
└── docs/         # PLAN.md (source of truth), sampler.md, research notes, design.pen, screenshots/
```

Today only `app/mac/` and `sampler/` have real content. `app/windows/`, `server/`, and `proto/` are README-only stubs reflecting the phased roadmap — see [PLAN.md § Roadmap: phases](docs/PLAN.md#roadmap-phases).

## Requirements

- macOS 14 Sonoma or later (Apple silicon or Intel)
- Xcode 15+ with command-line tools
- [XcodeGen](https://github.com/yonaskolb/XcodeGen) — `brew install xcodegen`
- Go 1.24+ (for building the sampler)
- Remote hosts reachable over SSH; Tailscale recommended but not required

## Build

From the repo root:

```bash
scripts/build.sh                # build sampler (all triples) + Debug app
scripts/build.sh Release        # Release app build
scripts/build.sh Debug --open   # build and launch
```

Individual steps:

```bash
scripts/build.sampler.sh              # all 5 triples → dist/samplers/<triple>/
scripts/build.sampler.sh darwin-arm64 # single triple (fast iteration)
scripts/build.app.sh                  # app only; needs dist/samplers/ populated
```

The app bundle ends up at `app/mac/build/<Configuration>/Towertail.app`.

## Run

```bash
open app/mac/build/Debug/Towertail.app
```

It runs as a menu-bar only app (`LSUIElement`) — look for the `server.rack` glyph in the status bar. Click to open the popover; ⌘, opens Preferences to add servers, tune thresholds, pick a sampling cadence, and manage launch-at-login.

## Test

```bash
# Swift tests
xcodebuild -project app/mac/Towertail.xcodeproj -scheme Towertail test

# Go sampler tests
cd sampler && go test ./...
```

## Sampler — standalone

The sampler runs fine on its own if you want to see what a sample looks like:

```bash
cd sampler
go run ./cmd/sampler --once          # one JSON sample
go run ./cmd/sampler --interval 1s   # NDJSON stream
go run ./cmd/sampler --version
go run ./cmd/sampler --self-check
```

Schema and flags are documented in [`docs/sampler.md`](docs/sampler.md).

## Docs

- [`docs/PLAN.md`](docs/PLAN.md) — product decisions, visual design, architecture, milestones.
- [`docs/sampler.md`](docs/sampler.md) — Go sampler spec: JSON schema, build matrix, bootstrap handshake.
- [`docs/research-stats.md`](docs/research-stats.md), [`research-backend.md`](docs/research-backend.md), [`research-frontend.md`](docs/research-frontend.md) — background research.
- [`docs/screenshots/`](docs/screenshots/) — current Pencil design exports.
