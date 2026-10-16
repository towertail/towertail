# towertail

Towertail monitors a small fleet of remote servers from your menu bar (macOS) or system tray (Windows). It usually connects over a Tailscale network. One panel shows one card for each host, with live CPU, memory, disk, and network values, sparklines, and threshold colors. It is like Stats.app, but for your servers instead of your laptop.

Inspired by [exelban/Stats](https://github.com/exelban/stats) and [mac-stats.com](https://mac-stats.com/).

## How it works

The app bundles a small Go collector, `towertail-sampler`, for 7 target triples. On the first connection to a host, the app uploads the correct binary to `~/.towertail/towertail-sampler`. Then it runs the sampler over SSH at a configurable interval (default 30 s) and reads one JSON sample for each poll. The app keeps the samples in a local SQLite database. The popover and the Full View of each host read from it.

The app finds hosts with `tailscale status --json`, or you add them manually. It uses your `~/.ssh/config` and runs `/usr/bin/ssh` with `ControlMaster=auto`, so Tailscale MagicDNS and your usual SSH setup work. The host needs no daemon, no agent, and no open port.

The optional self-hosted server (`server/`) receives samples from the hosts, keeps the history in ClickHouse, and checks alerts. The apps can show its data instead of their local data.

## Status

| Component | Folder | Status |
|---|---|---|
| Mac app | [`apps/mac`](apps/mac/) | Released. Signed, notarized, and updates automatically. |
| Windows app | [`apps/windows`](apps/windows/) | Build from source. No release yet. |
| Server | [`server`](server/) | Self-host with Docker. Work in progress (refer to the [status](docs/server-plan.md#status)). |
| Sampler | [towertail/sampler](https://github.com/towertail/sampler) | Released. The apps bundle the release that [`samplers.lock`](samplers.lock) pins. |

## Install

### Mac

Requires macOS 14 Sonoma or later (Apple silicon or Intel).

1. Download `Towertail.zip` from the latest release on [GitHub Releases](https://github.com/towertail/towertail/releases/latest).
2. Unzip it and move `Towertail.app` to `/Applications`.
3. Open it. Look for the `server.rack` icon in the menu bar.

The app installs new releases automatically. A Homebrew cask (`brew install --cask towertail/tap/towertail`) comes with the first public release.

### Windows

There is no release yet. Build the app from source. Refer to [`apps/windows/README.md`](apps/windows/README.md).

### Server

Refer to [`server/README.md`](server/README.md). The image is `ghcr.io/towertail/server`.

## Repository layout

```
towertail/
├── apps/
│   ├── mac/      # Phase 1 — SwiftUI macOS menu-bar app
│   └── windows/  # Phase 2 — WinUI 3 / .NET 9 tray app
├── server/       # Phases 3 and 4 — Go server, Docker deployment
├── scripts/      # build, fetch-samplers, test, and winvm scripts
├── samplers.lock # the towertail/sampler release that the apps bundle
└── docs/         # PLAN.md, server-plan.md, wire.md, screenshots/, archive/
```

See [PLAN.md § Roadmap: phases](docs/PLAN.md#roadmap-phases) for the phases.

## Build the Mac app

Requirements:

- macOS 14 or later, Xcode 15 or later with the command-line tools
- [XcodeGen](https://github.com/yonaskolb/XcodeGen): `brew install xcodegen`
- Go 1.25 or later, only for the server or for a sampler build from a local clone

From the repository root:

```bash
scripts/build.sh                # get the samplers, build the Debug app, install it in /Applications, restart it
scripts/build.sh Release        # the same, Release build
```

Individual steps:

```bash
scripts/fetch-samplers.sh               # all 7 triples → dist/samplers/<triple>/
scripts/fetch-samplers.sh darwin-arm64  # one triple
SAMPLER_DIR=../sampler scripts/fetch-samplers.sh darwin-arm64  # build from a local sampler clone
scripts/build.app.sh                    # the app only; runs fetch-samplers.sh first
scripts/build.app.sh Debug --open       # build and open the app
```

The sampler binaries come from the [towertail/sampler](https://github.com/towertail/sampler) release that `samplers.lock` pins. The script checks each sha256.

The app is at `apps/mac/build/Build/Products/<Configuration>/Towertail.app`. A build signs with the Developer ID certificate when it is in the keychain. Otherwise, it uses ad-hoc signing. `scripts/build.sh` also installs the app in `/Applications`.

The app runs in the menu bar only (`LSUIElement`). Click the icon to open the popover. Press ⌘, to open Preferences, where you add servers, set thresholds, select the sample interval, and set launch at login.

## Test

```bash
scripts/tests.sh                    # Swift unit tests and Go server unit tests
scripts/tests.sh --help             # all flags (integration tests, Docker stack, sampler)

# The same, one at a time
xcodebuild -project apps/mac/Towertail.xcodeproj -scheme Towertail test
cd server && go test ./...
```

The Windows tests run with `scripts/test.ps1` (refer to [`apps/windows/README.md`](apps/windows/README.md#tests)).

## Sampler

The sampler (the Go collector on each host) is in its own repository: [towertail/sampler](https://github.com/towertail/sampler). Its README shows how to run it alone. The JSON schema and the flags are in [`wire-format.md`](https://github.com/towertail/sampler/blob/main/docs/wire-format.md).

## Docs

- [`docs/PLAN.md`](docs/PLAN.md) — product decisions, visual design, architecture, milestones.
- [`docs/server-plan.md`](docs/server-plan.md) — server design and status. [`docs/wire.md`](docs/wire.md) — server API contract. [`docs/server-runbook.md`](docs/server-runbook.md) — server operation.
- [`wire-format.md`](https://github.com/towertail/sampler/blob/main/docs/wire-format.md) (towertail/sampler) — sampler spec: JSON schema, build matrix, bootstrap handshake.
- [`docs/archive/`](docs/archive/) — historical research and plans. Not kept up to date.
- [`docs/screenshots/`](docs/screenshots/) — Pencil design exports.

## Contributing

Refer to [CONTRIBUTING.md](https://github.com/towertail/.github/blob/main/CONTRIBUTING.md). Before we can merge your first pull request, you must sign the [CLA](https://github.com/towertail/.github/blob/main/CLA.md). A bot asks you on the pull request.

## License

- The apps (`apps/`) and the server (`server/`) use the [GNU Affero General Public License v3.0](LICENSE) (AGPL-3.0).
- The sampler uses the Apache License 2.0. It is in its own repository: [towertail/sampler](https://github.com/towertail/sampler).
- The name "Towertail" and the logo are trademarks. A fork must use a different name and logo. Refer to [TRADEMARK.md](TRADEMARK.md).
