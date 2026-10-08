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
scripts/build.sh                # build sampler + signed Debug app, install to /Applications, restart
scripts/build.sh Release        # same, Release build
```

Individual steps:

```bash
scripts/build.sampler.sh              # all 5 triples → dist/samplers/<triple>/
scripts/build.sampler.sh darwin-arm64 # single triple (fast iteration)
scripts/build.app.sh                  # app only; needs dist/samplers/ populated
```

The app bundle ends up at `app/mac/build/Build/Products/<Configuration>/Towertail.app`. Builds sign with the Developer ID cert when it is in the keychain, else ad-hoc.

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

### Sample output

One JSON object per sample. Newline-delimited in streaming mode. Fields are stable and versioned via the top-level `v` — bump on breaking changes.

```json
{
  "v": 1,
  "ts": "2026-04-20T19:42:07.103Z",
  "host": {
    "name": "db-primary",
    "os": "linux",
    "arch": "arm64",
    "kernel": "6.6.22-1-arm64",
    "uptime_s": 1048273,
    "sampler": "0.1.0+abc1234",
    "machine_id": "24c63e84-ed40-5734-bf08-72572b9bea7d"
  },
  "cpu": {
    "pct": 42.3,
    "load_1": 1.24,
    "load_5": 0.98,
    "load_15": 0.81,
    "cores": 8
  },
  "mem": {
    "used": 5814763520,
    "total": 16777216000
  },
  "swap": {
    "used": 0,
    "total": 0
  },
  "disks": [
    { "mount": "/",     "fs": "ext4", "used": 42949672960, "total": 107374182400 },
    { "mount": "/data", "fs": "xfs",  "used": 17179869184, "total": 53687091200 }
  ],
  "disk_io": {
    "read_bps":   4194304,
    "write_bps":  1048576,
    "read_cum":   2800479338496,
    "write_cum":  1525421113344
  },
  "net": {
    "rx_bps": 3355443,
    "tx_bps": 838860,
    "rx_cum": 198723849203,
    "tx_cum":  48239874321
  },
  "procs": {
    "root": false,
    "top_n": 20,
    "total": 312,
    "visible": 311,
    "items": [
      { "pid": 812, "ppid": 1, "name": "postgres", "cmd": "postgres: main", "user": "postgres", "cpu_pct": 42.7, "rss": 536870912, "threads": 6, "state": "S", "read_bytes": 58720256, "write_bytes": 12582912 },
      { "pid": 914, "ppid": 1, "name": "node",     "cmd": "node /app/server.js", "user": "app", "cpu_pct": 18.3, "rss": 430080000, "threads": 12, "state": "S" }
    ]
  },
  "errors": []
}
```

### Schema

| Field | Type | Notes |
|---|---|---|
| `v` | int | Schema version. App refuses samples with `v > appKnownV`. |
| `ts` | string | RFC 3339, millisecond precision, UTC (`Z`). Set by the host. |
| `host.name` | string | Hostname. |
| `host.os` | string | `linux`, `darwin`, etc. |
| `host.arch` | string | `amd64`, `arm64`, `arm`. |
| `host.kernel` | string | `uname -r`. |
| `host.uptime_s` | int | Seconds since boot. |
| `host.sampler` | string | `<semver>+<short-sha>`. |
| `host.machine_id` | string? | `/etc/machine-id` (Linux) or `IOPlatformUUID` (Darwin). Omitted when unavailable. |
| `cpu.pct` | float | Aggregate across cores, 0–100. |
| `cpu.load_1` / `load_5` / `load_15` | float | Load averages. |
| `cpu.cores` | int | Logical core count. |
| `mem.used` / `mem.total` | int | Bytes. `used` excludes cached/inactive. |
| `swap.used` / `swap.total` | int | Bytes. Both `0` when no swap. |
| `disks[]` | array? | Per-mount `{mount, fs, used, total}` in bytes. Omitted entirely when `--no-disk`. |
| `disk_io.read_bps` / `write_bps` | int | System-wide, short-window rate. |
| `disk_io.read_cum` / `write_cum` | int | Lifetime byte counters. |
| `net.rx_bps` / `tx_bps` | int | Sum across non-loopback interfaces, short-window delta. |
| `net.rx_cum` / `tx_cum` | int | Lifetime counters; decrement signals a reboot. |
| `procs.root` | bool | `true` when sampler ran as euid 0 (full cross-user visibility). |
| `procs.top_n` | int | Requested cap. `0` = unlimited. |
| `procs.total` | int | Full process count on the host. |
| `procs.visible` | int | Processes the sampler could inspect. |
| `procs.items[]` | array | Union of top-N by `cpu_pct` and top-N by `rss`, deduped, CPU-desc. |
| `procs.items[].pid` / `ppid` | int | |
| `procs.items[].name` / `cmd` / `user` / `state` | string | Best-effort; omitted if kernel denies. |
| `procs.items[].cpu_pct` | float | `top(1)` convention — 0..100×cores. |
| `procs.items[].rss` | int | Resident set size in bytes. |
| `procs.items[].threads` | int | |
| `procs.items[].read_bytes` / `write_bytes` | int? | Lifetime per-proc disk I/O. Linux only, SSH user's own procs without `CAP_SYS_PTRACE`; always omitted on macOS. |
| `errors[]` | string[] | Non-fatal collector errors. |

See [`docs/sampler.md`](docs/sampler.md) §4 for full field semantics, permission notes, and degradation rules.

## Docs

- [`docs/PLAN.md`](docs/PLAN.md) — product decisions, visual design, architecture, milestones.
- [`docs/sampler.md`](docs/sampler.md) — Go sampler spec: JSON schema, build matrix, bootstrap handshake.
- [`docs/research-stats.md`](docs/research-stats.md), [`research-backend.md`](docs/research-backend.md), [`research-frontend.md`](docs/research-frontend.md) — background research.
- [`docs/screenshots/`](docs/screenshots/) — current Pencil design exports.
