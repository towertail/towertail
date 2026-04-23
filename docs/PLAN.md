# Towertail — Implementation Plan

> **Elevator pitch.** A beautiful, Stats-style macOS menu bar app — but instead of watching your own Mac's CPU/GPU/SSD, it watches a set of **remote servers** over your Tailscale network via SSH. One icon, one compact panel, a stack of server cards showing CPU / Memory / Disk / Network with live sparklines, threshold-tinted numbers, color-adaptive menu bar glyph, and 7 days of history retained per host. Native SwiftUI, Developer-ID signed, launch-at-login, notifications on threshold breach.

This document is the single source of truth for building v1. It consolidates the research reports in `docs/`:

- [`research-stats.md`](research-stats.md) — visual language analysis of exelban/Stats and mac-stats.com, palette derivation.
- [`research-backend.md`](research-backend.md) — SSH transport, Tailscale, SQLite schema, cadence, notifications. *(The host-side probe design in that doc is superseded by [`sampler.md`](sampler.md) — see §4 below.)*
- [`research-frontend.md`](research-frontend.md) — MenuBarExtra scaffolding, SwiftUI Charts sparklines, `@Observable` state, preferences.
- [`sampler.md`](sampler.md) — Go sampler binary spec: JSON schema, build matrix, bootstrap handshake, permissions.

Reference imagery in [`screenshots/`](screenshots/): the Stats popover strip (`stats-popups.png`), menu-bar widget strip (`stats-menus.png`), and `mac-stats.com` hero.

### Repository layout

Top-level folders, one per build target + shared contract + docs:

```
towertail/
├── app/
│   ├── mac/       # Phase 1 — SwiftUI macOS menu-bar app (Xcode project, v1 shipping target)
│   ├── windows/   # Phase 2 — WinUI 3 / .NET 9 port (see docs/windows-app.md); Tier 1+2 tests green
│   └── (shared/)  # speculative; create only when something is truly shared
├── sampler/         # Go collector binary, 5 target triples, bundled into app/mac
├── server/        # Phase 3 — stub; Go control-plane service (ingest, store, alert), Dockerised, self-hostable
├── proto/         # Phase 3 — stub; wire contract shared by sampler ↔ server ↔ app
└── docs/          # this doc, research notes, sampler spec, design.pen, screenshots/
```

Each folder has its own toolchain and CI pipeline; they never share a build graph. **Today only `app/mac` and `sampler/` have real content** — `app/windows`, `server/`, and `proto/` are README-only stubs so the shape reflects the plan and future folder moves don't break Swift imports or doc links. See [Roadmap: phases](#roadmap-phases) for when each turns on.

The Mac app embeds the sampler binaries (`Contents/Resources/samplers/<triple>/`) and pushes the right one to `~/.towertail/towertail-sampler` on each monitored server on first connect. Sampler and server are siblings (not nested) because they run in different trust zones with different deploy cadences; the only thing they'll share is the wire contract in `proto/`. See [`sampler.md`](sampler.md) for the sampler details.

<a id="roadmap-phases"></a>
### Roadmap: phases

The lines between these phases are deliberately sharp — each is a separately shippable product, and earlier phases don't acquire complexity because later ones exist.

| Phase | Scope | Components |
|---|---|---|
| **Phase 1 — Local Mac client** *(shipping)* | A single user watches their own fleet from a menu bar, all state local. No server, no account, no cloud. | `app/mac`, `sampler/` |
| **Phase 2 — Windows client** *(active; feature-complete, polish + MSIX pending)* | Same product on Windows. Shared wire format with the sampler and the self-hosted server; UI is a WinUI 3 / .NET 9 / C# 13 build. See [`windows-app.md`](windows-app.md). | + `app/windows` (WinUI app, xUnit Tier 1 + Tier 2 integration tests against the real Go server with `TT_CLICKHOUSE__DISABLED=true` for the no-Docker dev loop) |
| **Phase 3 — Self-hosted server** | A company runs a Go service in their own network. Samplers ship samples to the server (not to a desktop). Desktop clients become read-only consoles talking to the server. Alert rules live on the server. | + `server/`, `proto/` formalised |
| **Phase 4 — Managed cloud** | We run the server. Multi-tenant, auth, billing. Same desktop client, a URL+key away from either a customer's self-hosted server or ours. | infrastructure repo (separate), no new top-level folder |

**Guardrails.**

- Don't leak later-phase concerns into earlier code. Phase 1 has no "account" concept, no "server URL" field, no "org." When Phase 3 lands, those get added in one well-scoped migration, not drip-fed through v1.
- The desktop app's data source is an abstraction behind one protocol (local-samplers vs remote-server). Introducing the server in Phase 3 replaces the implementation, not the call sites.
- `proto/` stays empty until Phase 3 starts. Before then, the JSON in [`sampler.md`](sampler.md) §4 *is* the contract.

---

## 1. Product decisions (locked for v1)

| Dimension                | Decision                                                                                     | Why                                                                                   |
| ------------------------ | -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| Target                   | **macOS 14 Sonoma+**, Apple silicon + Intel                                                  | `MenuBarExtra(.window)` + Observation framework; covers ~85% of active Macs in 2026  |
| Language / UI            | **Swift 5.9+, SwiftUI** for popover & prefs                                                  | Native, fast, tight binary, minimal deps                                              |
| Distribution             | **Developer ID + notarized .dmg**, Sparkle 2 auto-update                                     | Shelling out to `ssh` rules out App Sandbox → no App Store                            |
| Data collection          | **Shell out to `/usr/bin/ssh`** with `ControlMaster=auto, ControlPersist=10m`                | Respects ssh-sampler, `~/.ssh/config`, MagicDNS; Tailscale "just works"                 |
| Host discovery           | **`tailscale status --json`** + a manual "Add server" path                                   | Tailnet is the target network; manual option for non-Tailscale hosts                  |
| Metrics source on host   | **`towertail-sampler`** — static Go binary pushed to `~/.towertail/towertail-sampler` on first connect, `gopsutil` under the hood | Uniform JSON schema across Linux/macOS/BSD, single exec per poll, runs fine as non-root. See [`sampler.md`](sampler.md). |
| Storage                  | **SQLite via GRDB**                                                                          | ~50 MB for 20 hosts × 7 d × 30 s; WAL; idiomatic Swift                                |
| Sampling cadence         | **30 s default** (15/30/60 s preference) with ±0–5 s per-host jitter                         | Balance between signal freshness and laptop battery                                   |
| History retention        | **7 days** rolling (hourly delete job)                                                       | User spec; plenty for "how was last week"                                             |
| Notifications            | **`UNUserNotificationCenter`**, persisted `alert_state`, edge-triggered + long re-notify     | No double-notify across restart, snoozable                                            |
| Popover size             | **Width 300pt**, height min 180 / max 640 (inner scroll)                                     | Wide enough for 4 metrics + sparkline; narrower than Stats 264 is too cramped         |
| Thresholds (default)     | CPU / Mem: warn **0.75**, critical **0.90**. Disk: warn **0.85**, critical **0.95**          | Remote-server ops conventions; per-server override                                    |
| Menu bar glyph           | `server.rack` SF Symbol; paints `systemRed` if any host is critical                          | Surfaces problems without opening popover                                             |

Non-goals for v1: GPU monitoring, per-process breakdown, historic charts beyond a card-level sparkline, team sync / shared config, Windows host targets, root-only metrics (temperature, disk SMART, per-process I/O across users), non-Tailscale public SSH (still works, but no UX polish).

---

## 2. Visual design — one panel, N server cards

### 2.1 Popover composition

Single vertical `ScrollView` inside the `MenuBarExtra(.window)`. Top chrome is a slim header row (3 pieces of state + 1 action). Below, a list of server cards. No tab bar, no sidebar — **one panel, that's the whole promise**.

```
┌───────────────────────────────────────────────┐  300 pt wide
│  Towertail          4 online · 1 warn   ⚙︎ ⟳  │  ← 28 pt header row
├───────────────────────────────────────────────┤
│                                               │
│  ┌─────────────── server card 1 ───────────┐  │
│  │                                         │  │
│  └─────────────────────────────────────────┘  │  card → 112 pt tall
│  ┌─────────────── server card 2 ───────────┐  │
│  │                                         │  │
│  └─────────────────────────────────────────┘  │
│   … scrolls                                   │
│                                               │
└───────────────────────────────────────────────┘
```

Header shows: app name (or nothing, if minimal), aggregate status ("4 online · 1 warn · 1 down"), gear (opens Settings), refresh arrow (force-poll all). No quit button — use the status icon's Option-click menu.

### 2.2 Server card — "A-grid" (primary)

The canonical card. 280 pt content width, 112 pt tall, 12 pt padding.

```
┌──────────────────────────────────────────────────────┐
│ ● db-primary                              refreshed 32s│  ← header row: 18pt
│ db-primary.tailnet.ts.net · linux/arm64               │  ← subtitle: 11pt secondary
│                                                       │
│  ┌───────────┐  ┌───────────┐                         │
│  │ CPU   42% │  │ MEM   71% │                         │  ← metric cells: 60pt tall
│  │ ╱╲_╱╲__╱╲ │  │ _╱╲__╱╲__ │                         │     sparkline = 22pt
│  └───────────┘  └───────────┘                         │     big number = 17pt semibold rounded mono
│  ┌───────────┐  ┌───────────┐                         │
│  │ DISK  88% │  │ NET  4MB/s│                         │
│  │ ▁▂▃▅▆▇█▇▆ │  │ _╱╲__╱╲_╱ │                         │
│  └───────────┘  └───────────┘                         │
└──────────────────────────────────────────────────────┘
```

- **Status dot** (12 pt): green = connected, yellow = stale (reader ≥ 2× interval behind), red = connection failed, gray = never connected.
- **Subtitle**: `<dns_name> · <os>/<arch>`. Muted. Truncates head-first so tailnet suffix is trimmed before hostname.
- **Metric cell**: `<LABEL>   <VALUE>` row on top (11 pt uppercased `.caption2` label, 17 pt rounded monospaced value), 22 pt sparkline below with `AreaMark` gradient + 1.2 pt line, corner radius 6, fill `.background.secondary`, 0.5 pt `.separator` stroke.
- **Threshold tinting**: both the big number AND the sparkline stroke take the tint from `ThresholdTint` (nominal/warn/critical). Card gains a 2-pt left border in the worst-metric color when any cell ≥ warn.
- **CPU / MEM** are percentages (0–100). **DISK** shows the busiest mount's used%. **NET** is total throughput (rx + tx) as `KB/s` or `MB/s`.

### 2.3 Server card — "B-dense" (user-toggleable in prefs)

Single-row variant for users with many hosts. 44 pt tall. Loses sparkline colors per cell, gains density.

```
┌──────────────────────────────────────────────────────┐
│ ● db-primary                              refreshed 32s│
│ CPU 42 ╱╲_ │ MEM 71 _╱╲ │ DSK 88! ▂▅█ │ NET 4M _╱╲_ │
└──────────────────────────────────────────────────────┘
```

Implemented as the same `ServerCardView` with a `@Environment(\.cardDensity)` switch. Ship **A-grid as default**.

### 2.4 Color palette (semantic, auto light/dark)

Mapped to `NSColor.system*` for free HIG conformance (see research-stats.md §5 for provenance).

| Token             | Light     | Dark       | Used for                                     |
| ----------------- | --------- | ---------- | -------------------------------------------- |
| `Tint/Nominal`    | `#34C759` | `#30D158`  | 0 – warn%, healthy                           |
| `Tint/Elevated`   | `#FFCC00` | `#FFD60A`  | reserved (v1.1 three-stop option)            |
| `Tint/Warn`       | `#FF9500` | `#FF9F0A`  | warn ≤ v < critical                          |
| `Tint/Critical`   | `#FF3B30` | `#FF453A`  | ≥ critical                                   |
| `Tint/Stale`      | `#8E8E93` | `#8E8E93`  | reader lagging, connection pending           |
| Text primary      | dynamic   | dynamic    | `.primary` (system text color)               |
| Text secondary    | dynamic   | dynamic    | `.secondary` for labels, timestamps          |
| Chart fill wash   | 10 % α    | 10 % α     | Sparkline background (`.background.secondary` + alpha) |

v1 is **two-stop** (nominal → warn → critical). v1.1 can introduce the Elevated middle tier (preserves assets but shifts thresholds).

### 2.5 Menu bar icon

SF Symbol `server.rack` in template mode. When any host is critical, switch to `server.rack.badge.exclamationmark` and tint with `Tint/Critical`. When any host is warn (and none critical), tint the base symbol with `Tint/Warn` only if user opted-in ("Color menu bar icon on warnings"). Default: only critical tints.

```
┌────────────────────────────────────────────┐
│          󰒋   (nominal, all green)          │
│          󰒋   (warn, tinted orange — opt-in)│
│          󰒑   (critical, badge + red)       │
└────────────────────────────────────────────┘
```

### 2.6 Aggregate header badge

Right-aligned in the popover header row. Stats-style counted summary:

```
4 online · 1 warn · 1 down
```

- "online" = polled successfully within last 2 × interval.
- "warn" = any metric ≥ warn & < critical.
- "critical" / "down" only shown when non-zero.
- Tinted with the worst-tier color in play.

### 2.7 Typography

- Big number:    `.system(size: 17, weight: .semibold, design: .rounded).monospacedDigit()`
- Hostname:      `.system(size: 13, weight: .medium)`
- Subtitle:      `.system(size: 11)` `.secondary`
- Metric label:  `.caption2` uppercased tracking 0.4 `.secondary`
- Timestamp:     `.system(size: 10)` `.tertiary`

All numbers use `.monospacedDigit()` so columns don't jitter across ticks.

---

## 3. Architecture

### 3.1 Process view

```
┌────────────────────────────────────────────────────────────────┐
│ Towertail.app (single process, LSUIElement=YES)                │
│                                                                │
│ ┌────────────── UI (main actor) ────────────────┐              │
│ │ MenuBarExtra ← PopoverRoot ← ServerCardView…  │              │
│ │                    ▲                          │              │
│ │                    │ @Observable              │              │
│ └────────────────────┼──────────────────────────┘              │
│                      │                                          │
│ ┌──────── ServerStore actor ─────────────┐                      │
│ │ map<ServerID, ServerViewModel>         │                      │
│ │ ingest(Sample)                         │                      │
│ │ vend(sparkline ranges)                 │                      │
│ └──────────┬─────────────────┬───────────┘                      │
│            │                 │                                  │
│   ┌────────▼────────┐ ┌──────▼────────┐                         │
│   │ PerHost actor   │ │ SQLiteStore   │ (GRDB)                  │
│   │ (one per server)│ │ WAL, 7-d ring │                         │
│   │ poll() loop     │ └──────┬────────┘                         │
│   │ + SamplerBootstrap│        │                                  │
│   └────────┬────────┘        ▼                                  │
│            │          ~/Library/Application Support/            │
│            │           Towertail/history.sqlite                 │
│            ▼                                                    │
│   ┌──────────────────┐                                          │
│   │ /usr/bin/ssh     │   bundled samplers:                        │
│   │ (ControlMaster)  │   Towertail.app/Contents/Resources/      │
│   └────────┬─────────┘     samplers/<triple>/towertail-sampler      │
└────────────┼────────────────────────────────────────────────────┘
             │   scp once  ── or ── ssh exec per poll
             ▼
   ┌─────────────────────────────────────┐
   │ Remote host (Linux / macOS)         │
   │  ~/.towertail/towertail-sampler --once          │
   │    └─ reads /proc, sysctl,          │
   │       gopsutil collectors           │
   │    └─ prints one JSON line, exits   │
   └─────────────────────────────────────┘
```

### 3.2 Module layout

Two sibling trees at the repo root. The Swift app consumes the Go sampler as a build artifact, never as source.

```
towertail/
├── app/mac/                                 # Swift — the Mac menu-bar app (Phase 1)
│   ├── Towertail.xcodeproj
│   ├── Sources/
│   │   ├── App/
│   │   │   ├── TowertailApp.swift           # @main
│   │   │   ├── AppEnvironment.swift         # DI
│   │   │   └── Info.plist                   # LSUIElement, SUFeedURL
│   │   ├── MenuBar/
│   │   │   ├── MenuBarIcon.swift
│   │   │   └── PopoverRoot.swift
│   │   ├── Cards/
│   │   │   ├── ServerCardView.swift
│   │   │   ├── MetricCell.swift
│   │   │   ├── Sparkline.swift
│   │   │   └── CardChrome.swift
│   │   ├── Preferences/
│   │   │   ├── PreferencesWindow.swift
│   │   │   ├── ServersPane.swift
│   │   │   ├── ThresholdsPane.swift
│   │   │   ├── NotificationsPane.swift
│   │   │   └── GeneralPane.swift
│   │   ├── State/
│   │   │   ├── ServerStore.swift            # actor
│   │   │   ├── ServerViewModel.swift        # @Observable, @MainActor
│   │   │   ├── MetricSeries.swift           # ring buffer
│   │   │   └── AppSettings.swift
│   │   ├── Design/
│   │   │   ├── ThresholdTint.swift
│   │   │   ├── Color+Threshold.swift
│   │   │   ├── Typography.swift
│   │   │   └── Assets.xcassets              # Tint/* colors
│   │   ├── Collectors/
│   │   │   ├── SSHCollector.swift           # per-host actor, wraps /usr/bin/ssh
│   │   │   ├── SamplerBootstrap.swift         # detect arch, scp, chmod, self-check
│   │   │   ├── SampleDecoder.swift          # JSONDecoder → Sample
│   │   │   └── TailscaleDiscovery.swift
│   │   ├── Storage/
│   │   │   ├── SQLiteStore.swift            # GRDB facade
│   │   │   ├── Migrations.swift
│   │   │   └── Schema.swift
│   │   ├── System/
│   │   │   ├── LaunchAtLogin.swift          # SMAppService
│   │   │   ├── Notifier.swift               # UNUserNotificationCenter
│   │   │   └── KeychainStore.swift
│   │   └── Updates/
│   │       └── SparkleUpdater.swift
│   ├── Tests/
│   │   ├── SampleDecoderTests/              # JSON fixtures from ../../sampler/test/fixtures/
│   │   ├── StoreTests/
│   │   └── ThresholdTests/
│   ├── Resources/
│   │   └── samplers/                          # populated by Packaging/build-samplers.sh
│   │       ├── manifest.json                # sha256 per triple, sampler semver
│   │       ├── linux-amd64/towertail-sampler
│   │       ├── linux-arm64/towertail-sampler
│   │       ├── linux-armv7/towertail-sampler
│   │       ├── darwin-arm64/towertail-sampler
│   │       └── darwin-amd64/towertail-sampler
│   └── Packaging/
│       ├── build-samplers.sh                  # cross-compiles from ../../sampler/, writes Resources/samplers/
│       ├── create-dmg.sh
│       ├── sparkle-sign.sh
│       └── entitlements.plist
│
└── sampler/                                   # Go — the remote collector
    ├── go.mod
    ├── cmd/sampler/main.go                    # see docs/sampler.md §9 for the full tree
    ├── internal/collect/ …
    ├── internal/schema/sample.go            # canonical JSON schema
    ├── Makefile                             # build-all, build-<triple>
    └── test/fixtures/*.json                 # shared with app/Tests/SampleDecoderTests
```

A **`TowertailCore`** SPM package wrapping `State/`, `Collectors/`, `Storage/`, `System/Notifier`, `System/KeychainStore` is worth extracting once APIs settle — enables pure unit tests and keeps UI ignorant of SSH.

The sampler's JSON fixtures in `sampler/test/fixtures/` are the **shared contract** between the two trees: Go tests emit them, Swift tests decode them. Any schema drift fails both CI jobs.

### 3.3 Key data types

```swift
struct Server: Identifiable, Codable, Hashable {
    let id: UUID                 // stable across hostname changes
    var displayName: String?
    var dnsName: String          // foo.tailnet.ts.net (no trailing dot)
    var sshUser: String
    var tags: [String]
    var thresholds: Thresholds?  // nil = use global defaults
    var enabled: Bool
}

struct Thresholds: Codable, Equatable {
    var cpuWarn, cpuCritical: Double      // 0..1
    var memWarn, memCritical: Double
    var diskWarn, diskCritical: Double
    var netWarnBps, netCritBps: UInt64?   // optional per-host
}

struct Sample: Hashable, Codable {
    let ts: Date
    let cpuPct: Double                   // 0..100
    let memUsed, memTotal: UInt64        // bytes
    let netRxBps, netTxBps: UInt64
    let disks: [DiskSample]              // one per mount
}

enum ServerConnState { case unknown, connecting, online, stale, offline(reason: String) }

enum ThresholdTint { case nominal, warn, critical, stale }
```

### 3.4 Concurrency model

- **One actor per host.** Owns the poll loop, enforces at-most-one-in-flight probe. Any tick that fires while the previous is still running is *skipped*, never queued. Exponential backoff 30 s → 60 s → 2 m → 5 m → 10 m on consecutive failures; reset on first success.
- **`ServerStore` actor.** Single writer for the in-memory `ServerViewModel` map. Receives `Sample`s from host actors, appends to ring buffer, persists to SQLite, mutates `@Observable` properties on `@MainActor`.
- **`SQLiteStore`.** Wraps a GRDB `DatabasePool`. Writes are `try await db.write { … }`; reads for sparklines are `try await db.read { … }`. Never on main.
- **Global concurrency cap** of 8 concurrent probes via a `TaskLimiter` helper (semaphore-ish) so the first poll on 30 hosts doesn't saturate the NIC.

---

## 4. Data collection

Host-side metrics come from a small static Go binary (`towertail-sampler`) that the app bundles, pushes, and invokes over SSH. The full spec — JSON schema, flags, build matrix, bootstrap handshake, permissions — lives in [`sampler.md`](sampler.md). What follows is the view from the app's side.

### 4.1 Sampler invocation

On every poll, the per-host actor runs:

```
/usr/bin/ssh -F <cfg> user@dns '~/.towertail/towertail-sampler --once'
```

- **One JSON object on stdout** per invocation. The app decodes it into a `Sample` via `SampleDecoder`. Parse failures are logged and the sample is dropped — no partial ingestion.
- **No shell indirection.** The sampler does all OS branching internally via `gopsutil`, so the app doesn't care whether the host is Linux, macOS, or BSD.
- **One-shot in v1.** Streaming mode (`--interval 1s` over a persistent SSH channel) is wired only if a user opts into sub-30s cadence in M4+.

### 4.2 Bootstrap (first connect & upgrade)

`SamplerBootstrap.swift` runs before the first poll for a host, and again whenever the bundled sampler's SHA differs from the on-host `sampler.version` file. Five steps:

1. **Detect arch:** `ssh host 'uname -sm'` → one of `{linux-amd64, linux-arm64, linux-armv7, darwin-arm64, darwin-amd64}`.
2. **Probe existing:** `ssh host '~/.towertail/towertail-sampler --version'`; if missing or version mismatch, proceed to step 3.
3. **Upload:** `scp Resources/samplers/<triple>/towertail-sampler host:~/.towertail/towertail-sampler.new`, then `ssh host 'mkdir -p ~/.towertail && chmod +x ~/.towertail/towertail-sampler.new && mv -f ~/.towertail/towertail-sampler.new ~/.towertail/towertail-sampler'`. Atomic replace avoids `ETXTBSY` with any concurrent streaming invocation.
4. **Self-check:** `ssh host '~/.towertail/towertail-sampler --self-check'` must print `ok`. On failure (e.g., musl-only distro where a gopsutil path needs cgo), mark host as `offline(reason: "sampler incompatible")` and stop retrying until user clicks refresh.
5. **Integrity:** `ssh host 'shasum -a 256 ~/.towertail/towertail-sampler'` must match the entry in `Resources/samplers/manifest.json`. Mismatch → abort bootstrap, flag as offline with "sampler integrity check failed."

See [`sampler.md` §6](sampler.md) for the full handshake diagram and failure modes.

### 4.3 SSH config

Written to `~/Library/Application Support/Towertail/ssh/config` on first launch:

```
Host *.ts.net
    ControlMaster  auto
    ControlPath    ~/Library/Application Support/Towertail/ssh/cm-%r@%h:%p
    ControlPersist 10m
    ServerAliveInterval 30
    ServerAliveCountMax 3
    ConnectTimeout 5
    StrictHostKeyChecking accept-new
    UserKnownHostsFile ~/Library/Application Support/Towertail/ssh/known_hosts
    BatchMode yes
```

`ControlMaster auto` + `ControlPersist 10m` means bootstrap and subsequent polls share one TCP connection for ten minutes. Poll round-trips are typically <50ms on Tailscale.

### 4.4 Tailscale discovery

- Locate CLI in order: `/Applications/Tailscale.app/Contents/MacOS/Tailscale`, `/usr/local/bin/tailscale`, `/opt/homebrew/bin/tailscale`, `PATH`.
- Run `tailscale status --json` every 30 s (no network cost, talks to local `tailscaled`).
- Parse peers; filter `Online == true`, optionally `tag:server`.
- Reconcile with user's explicit server list: additions surface a "New host on tailnet — monitor?" pill in the popover; deletions mark as `offline(reason: "left tailnet")`.

### 4.5 Storage schema

```sql
CREATE TABLE host (
    id TEXT PRIMARY KEY, hostname TEXT, dns_name TEXT NOT NULL,
    os TEXT, ssh_user TEXT, added_at INTEGER, display_name TEXT
);

CREATE TABLE sample (
    host_id TEXT, ts INTEGER, cpu_pct REAL,
    mem_used INTEGER, mem_total INTEGER,
    net_rx_bps INTEGER, net_tx_bps INTEGER,
    rx_cum INTEGER, tx_cum INTEGER,
    PRIMARY KEY (host_id, ts)
) WITHOUT ROWID;

CREATE TABLE disk_sample (
    host_id TEXT, ts INTEGER, mount TEXT,
    used INTEGER, total INTEGER,
    PRIMARY KEY (host_id, ts, mount)
) WITHOUT ROWID;

CREATE TABLE alert_state (
    host_id TEXT, rule TEXT,
    fired_at INTEGER, last_notified INTEGER,
    PRIMARY KEY (host_id, rule)
);

CREATE INDEX idx_sample_ts ON sample(ts);
```

`PRAGMA journal_mode=WAL; synchronous=NORMAL; auto_vacuum=INCREMENTAL`.
Hourly housekeeping: `DELETE FROM sample WHERE ts < strftime('%s','now') - 7*86400;` then `PRAGMA incremental_vacuum;`.

### 4.6 In-memory ring buffer

Per-metric per-host: 10 080 slots (7 days × 1-minute decimation). Backing store is the SQLite table; the in-memory ring is a fast path for the sparkline render. Decimation: we collect at 30 s, keep raw in SQLite, but render from 1-minute averages (2-sample mean) to halve the point count without losing fidelity.

---

## 5. Notifications

### 5.1 Permission

Requested on **first** enable of any notification toggle, not on first launch, so the permission dialog lands in context:

```swift
try await UNUserNotificationCenter.current().requestAuthorization(
    options: [.alert, .sound]   // badge is pointless for a menu-bar-only app
)
```

### 5.2 Rule evaluation

One evaluator task, ticks every 30 s in parallel with sampling. For each host × rule:

```sql
-- "CPU > 90% for 5 min"
SELECT MIN(cpu_pct) AS low FROM sample
 WHERE host_id = ? AND ts >= strftime('%s','now') - 300;
```

If `low > 90` → rule is *firing*. State machine drives notification:

```swift
enum AlertPhase { case clear, firing(since: Date), snoozed(until: Date) }

switch (isFiring, state.phase) {
case (true,  .clear):            state.phase = .firing(since: .now); post(.raised)
case (true,  .firing) where lastNotified < .now - 30.minutes:        post(.reminder)
case (false, .firing):           state.phase = .clear;               post(.resolved)
default: break
}
```

`UNNotificationRequest.identifier = "\(hostId).\(rule.id)"` so the system replaces rather than stacks. Snooze action in the notification writes `.snoozed(until:)` to `alert_state`. Quiet hours (configurable in General pane) short-circuit evaluation.

### 5.3 Built-in rules (v1)

| Rule ID              | Default threshold       | Default on?  |
| -------------------- | ----------------------- | ------------ |
| `cpu_high_5m`        | > 90 % for 5 min        | Yes          |
| `mem_high_5m`        | > 90 % for 5 min        | Yes          |
| `disk_nearly_full`   | any mount ≥ 95 %        | Yes          |
| `host_unreachable`   | 3 consecutive poll fail | Yes          |
| `host_reachable`     | resolved (edge)         | Yes          |
| `net_saturated`      | ≥ user-set bps for 2 m  | No           |

Per-server overrides exist on every rule.

---

## 6. Preferences

`Settings { TabView { … } }` with four panes, 560×380 min:

### Servers
`Table` with columns: status dot, Display name, DNS/Host, SSH user, Tags, Last seen. `+` / `−` buttons. Add flow:
1. **"Discover on Tailscale"** — lists peers with a checkbox each (defaults checked).
2. **"Add manually"** — sheet: Display name, Host, User, optional Port (22), Key (picker reading `~/.ssh/config` + `~/.ssh/*.pub`).

Per-row edit opens inline sheet with: display name, ssh user, per-server thresholds (override), tags, enable toggle.

Secrets: we don't store passwords; key material comes from the user's sampler. If a server requires a passphrase-protected key, we rely on the sampler prompt on first connect.

### Thresholds
Global defaults: three sliders per metric (warn, critical). Preview swatch changes color as you drag. "Reset to defaults" per metric.

### Notifications
Master toggle + per-rule toggles + "Quiet hours" time range + "Reminder interval" stepper (default 30 min). Test notification button.

### General
Launch at login (`SMAppService.mainApp`), sampling cadence (15/30/60 s), sparkline retention (1 d / 3 d / 7 d), card density (A-grid / B-dense), appearance (auto / light / dark), menu-bar icon color behavior (critical-only / warn-and-critical / never). Check-for-updates button (Sparkle).

Storage: scalars → `@AppStorage`. Structured data (`servers.json`, `thresholds.json`) → `~/Library/Application Support/Towertail/`, atomic writes.

---

## 7. Testing strategy

- **Unit tests**
  - `SampleDecoder`: decode every `sampler/test/fixtures/*.json` (Linux, Darwin, no-swap, counter-reset, iface-rename) → expected `Sample`. Unknown-field policy is strict — protocol drift fails CI on both sides.
  - `MetricSeries`: ring buffer correctness, decimation invariants.
  - `ThresholdTint`: boundary behavior, hysteresis if we add it.
  - `AlertEvaluator`: state machine transitions using an injected clock.
  - **Go side** (`sampler/internal/collect/*_test.go`): deterministic fakes for procfs/sysctl, emit fixture JSON that the Swift tests consume.
- **Integration tests**
  - `SQLiteStore` with in-memory DB: write/read/delete round-trip, 7-day rollover.
  - `SamplerBootstrap` against a local Docker container running `sshd`: uploads the binary, runs `--self-check`, verifies sha256 — exercised in CI for `linux-amd64` and `linux-arm64` (via emulation).
  - `SSHCollector` end-to-end: ssh into the same container, run `sampler --once`, decode the sample.
- **Snapshot tests**
  - `ServerCardView` in nominal, warn, critical, offline, stale states → PNGs checked in as fixtures.
- **Manual QA checklist** (lives in `docs/qa-checklist.md`):
  - Cold launch with 0 / 1 / 10 / 30 servers on tailnet.
  - Put laptop to sleep for 1 h, wake, verify sampler poll reconnects via `ControlMaster`.
  - Kill `tailscaled`, confirm all hosts flip to `offline(reason:)`.
  - Disk fill → notification → resolve → resolution notification.
  - Dark mode, reduced transparency, increased contrast.

---

## 8. Packaging & distribution

- **Signing**: Developer ID Application cert, hardened runtime, no sandbox. Entitlements: `com.apple.security.network.client`.
- **Notarization**: `xcrun notarytool submit Towertail.dmg --wait` → `xcrun stapler staple`.
- **DMG**: `create-dmg` with a background image showing drag-to-Applications.
- **Auto-update**: Sparkle 2 via SPM. `SUFeedURL` pointing to a GitHub Pages–hosted `appcast.xml`. EdDSA-sign every release.
- **Launch at login**: `SMAppService.mainApp.register()`.
- **Crash reports**: opt-in in General pane, ships minidumps to a Cloudflare Workers endpoint or just `os_log` to Console — decide in Milestone 5.

---

## 9. Milestones

A rough, ~6–8 week arc for a single engineer working part time. Each milestone is independently useful and demoable.

### M1 — Walking skeleton (week 1)
**Goal:** one hard-coded server, one hard-coded card, menu bar icon opens popover.

- Xcode project, `MenuBarExtra(.window)` scaffolding.
- Stub `ServerViewModel` with random data on a 1 s `Timer`.
- `ServerCardView` A-grid layout; sparkline renders `[0,1]` random values.
- `ThresholdTint` enum + three asset-catalog colors.
- Settings window with an empty Servers pane.

**Exit criteria:** Click menu bar → popover shows one card with moving numbers. Numbers change color above 0.75 / 0.90.

### M2 — Real SSH + real data (week 2)
**Goal:** real remote polling to one manually-configured host.

- `towertail-sampler` v0.1: `--once`, `--version`, `--self-check`. Linux amd64/arm64 and darwin-arm64 first; other triples by end of milestone. Fixtures checked in.
- `Packaging/build-samplers.sh` cross-compiles all five targets and writes `Resources/samplers/` + `manifest.json`.
- `SamplerBootstrap` handles detect → scp → chmod → self-check → sha256 verify.
- `SSHCollector` shells out via `Process` → runs `sampler --once` → decodes JSON → emits `Sample`.
- Per-host actor with 30 s loop, `ControlMaster` config.
- `Server` record persisted in `servers.json`; Servers pane can add/remove manually.
- Menu-bar icon switches to critical state when host metric ≥ critical.

**Exit criteria:** Add a server via Settings, first connect uploads the sampler within 5 s, subsequent polls show real CPU/mem/disk/net on the card. Pull ethernet, watch it go stale then offline. Reconnect, recover.

### M3 — Storage + sparklines over time (week 3)
**Goal:** 7-day history, real sparklines from SQLite.

- GRDB dependency, schema + migrations.
- `SQLiteStore.ingest(_:)` and `.samples(for:range:)`.
- `Sparkline` renders last hour (in-popover default); preference to show 6 h / 24 h / 7 d.
- Hourly housekeeping delete + incremental vacuum.

**Exit criteria:** Relaunch app after a day of running — sparkline shows yesterday's shape, not a blank reset.

### M4 — Tailscale discovery + multi-host (week 4)
**Goal:** scan tailnet, add servers with one click, handle 10+ cards.

- `TailscaleDiscovery` runs `tailscale status --json` on 30 s loop.
- "Discover on Tailscale" sheet in Servers pane.
- Global concurrency cap (8), per-host jitter, exponential backoff.
- Dense B variant behind prefs toggle.
- Aggregate header badge ("4 online · 1 warn").

**Exit criteria:** On a tailnet with 10 machines, add all with one click, popover scrolls smoothly, nothing saturates the NIC on first sync.

### M5 — Notifications + polish (week 5–6)
**Goal:** production-quality notifications, accessibility, dark mode pass.

- `UNUserNotificationCenter` permission flow.
- `AlertEvaluator` state machine with SQLite-backed state.
- Snooze action on notifications.
- VoiceOver labels on cards; Dynamic Type capped test.
- Reduced-motion, increased-contrast passes.
- Snapshot tests for all card states.

**Exit criteria:** Saturate a host's CPU for 5 min → system notification fires. Snooze for 1 h. No duplicate fires after app restart.

### M6 — Packaging + launch (week 7–8)
**Goal:** signed, notarized, auto-updating `.dmg`.

- Developer ID signing pipeline.
- Sparkle 2 integration, first appcast published to GitHub Pages.
- `SMAppService` launch-at-login.
- Landing page (optional) — separate repo.
- v1.0 DMG released.

**Exit criteria:** Fresh Mac downloads DMG, drags to Applications, launches, no Gatekeeper scare, adds a tailnet server within 60 s of opening, stays running across reboot.

---

## 10. Open questions & pre-M1 decisions

1. **SSH user defaulting.** Do we default to the local user's `$USER`? Yes — but prompt on first add with a "looks wrong?" hint if the Tailscale hostname OS reports `linux` and our username has no shell match.
2. **Key picking.** We read `~/.ssh/config` and offer its `Host` entries as a picker. Do we add a "use ssh-sampler only" mode? **Yes, default.** Explicit key path is an advanced field.
3. **Multiple mounts on disk card.** Show worst by used %. Hovering the disk cell (popover is a window, hover works) reveals a compact list of all mounts for 1.5 s. Alternative: expand-to-full-detail by clicking card → skip for v1.
4. **What if Tailscale isn't installed?** Show an empty state in the Servers pane — "No Tailscale found. Add servers manually." Don't block the app.
5. **Telemetry.** None in v1. No phone-home except Sparkle's appcast fetch.

---

## 11. Success criteria for v1

- Cold-launch to first useful card: **< 10 seconds** on a tailnet with 5 hosts.
- Idle CPU on main thread: **< 1 %** with 10 hosts at 30 s cadence.
- Popover open-animation: indistinguishable from stock system popovers.
- Memory footprint: **< 80 MB** resident with 10 hosts × 7 d history.
- Zero crashes in 7 days of continuous background run across 3 beta testers.
- Tailscale peer added / removed → UI reflects within 60 s.
- Matches the Stats visual "feel" bar — someone who uses Stats opens Towertail and immediately understands the cards, the colors, the density.

---

## 12. Stretch ideas (out of scope for v1)

- **Per-process top 5** (click a card to expand, fetch via `ps` on remote).
- **Temperature / fan sensors** where available (`sensors` on Linux, `powermetrics` on Darwin root).
- **Load average** as a secondary CPU metric.
- **Windowed detail view** — detach a card into a free-floating window for always-on display.
- **Scriptable custom probes** — let users drop a `probe-<hostid>.sh` that overrides the default.
- **iOS companion** via CloudKit sync of server list + latest samples (read-only).
- **Menu bar "mini gauge"** — opt-in single-metric sparkline in the bar for a "primary" host.
