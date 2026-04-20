# Towertail — Frontend Architecture (Swift/SwiftUI)

A focused UI blueprint for the macOS 14+ menu bar app. The product is a single popover panel that stacks compact **server cards**, each showing CPU / memory / disk / network with sparklines and threshold-tinted numbers. It must feel as tight and native as exelban/Stats or iStat Menus.

---

## 1. Menu bar app scaffolding (macOS 14+)

### `MenuBarExtra` vs `NSStatusItem` + `NSPopover`

SwiftUI's `MenuBarExtra` (macOS 13+) is the idiomatic choice, and **we should use it** given our 14+ target. It gives us:

- `.menuBarExtraStyle(.window)` — a borderless popover-style panel hosting arbitrary SwiftUI, exactly what we need for a card list. Not a menu.
- Scene lifecycle integrated with `@main App`, so preferences (`Settings { }`) and launch-at-login compose cleanly.
- Automatic light/dark/tinted status icon handling when we pass an `Image` or `Label`.

Historically you'd reach for `NSStatusItem` + `NSPopover` because `MenuBarExtra` had bugs on early 13.x (flicker on open, poor focus behavior, no easy "always on top" option). Those are mostly fixed in 14+. The remaining trade-off:

| Concern | `MenuBarExtra` | `NSStatusItem` + `NSPopover` |
|---|---|---|
| Boilerplate | Minimal | ~80 lines of `AppDelegate` glue |
| Custom popover size/detach | `.menuBarExtraStyle(.window)` + `.frame()` | Full control (`NSPopover.behavior`) |
| Right-click menu | Awkward (need secondary style) | Native `NSMenu` |
| Animating status icon | Static-ish; re-renders on state change | Full `NSImage` control |

**Recommendation:** `MenuBarExtra` with `.window` style. Drop into `NSStatusItem` via an `NSApplicationDelegateAdaptor` **only if** we need right-click context menus or tear-off panels (v2 feature).

```swift
@main
struct TowertailApp: App {
    @State private var store = ServerStore()

    var body: some Scene {
        MenuBarExtra {
            PopoverRoot()
                .environment(store)
                .frame(width: 300, height: 560)
        } label: {
            MenuBarIcon(state: store.aggregateState)
        }
        .menuBarExtraStyle(.window)

        Settings {
            PreferencesWindow()
                .environment(store)
        }
    }
}
```

### Status bar icon

A tiny custom view (`MenuBarIcon`) renders an SF Symbol (`server.rack`) plus, when any server crosses the critical threshold, a **tinted dot badge**. Use `Image(systemName:)` with `.symbolRenderingMode(.palette)` so the badge can be `.red` while the rack stays default. For tinting the whole icon on critical, apply `.foregroundStyle(state.tint)`. The menu bar will respect the user's menu bar appearance (dark/tinted/transparent) as long as we stick to template-compatible symbols.

```swift
struct MenuBarIcon: View {
    let state: AggregateState   // .nominal | .warn | .critical
    var body: some View {
        Image(systemName: state == .critical ? "server.rack.badge.exclamationmark" : "server.rack")
            .symbolRenderingMode(.palette)
            .foregroundStyle(.primary, state.tint)
    }
}
```

### Lifecycle / Info.plist

- `LSUIElement = true` — hides the Dock icon and the app switcher entry. Essential.
- `NSAppTransportSecurity` — we own outbound SSH, not HTTPS, so defaults are fine.
- `LSMinimumSystemVersion = 14.0`.
- Do **not** set `NSMainNibFile`; SwiftUI app loader handles it.

---

## 2. SwiftUI Charts sparklines

Charts (iOS 16 / macOS 13+) is perfect for dense, inline sparklines. Strip all axes and legends, keep just a hair-thin line plus an `AreaMark` gradient for body. 7 days of 1-minute samples = 10,080 points; decimate server-side or resample to ~120 visible points per sparkline to stay 60fps.

```swift
struct Sparkline: View {
    let samples: [MetricSample]           // timestamp + value 0...1
    let tint: Color

    var body: some View {
        Chart(samples) { s in
            AreaMark(x: .value("t", s.t), y: .value("v", s.v))
                .foregroundStyle(LinearGradient(
                    colors: [tint.opacity(0.35), tint.opacity(0.02)],
                    startPoint: .top, endPoint: .bottom))
                .interpolationMethod(.monotone)
            LineMark(x: .value("t", s.t), y: .value("v", s.v))
                .foregroundStyle(tint)
                .lineStyle(.init(lineWidth: 1.2))
                .interpolationMethod(.monotone)
        }
        .chartXAxis(.hidden)
        .chartYAxis(.hidden)
        .chartPlotStyle { $0.background(.clear) }
        .chartYScale(domain: 0...1)
        .frame(height: 22)
        .drawingGroup()                 // Metal rasterize, cheap scroll
    }
}
```

Threshold fill: flip `tint` at the ViewModel level based on the **latest** sample, not the peak, to avoid flapping. Add a faint `RuleMark` at the warn level for context.

### Hover interaction — one pixel == one 10s sample

The card's big metric number is **live** by default (latest sample) and becomes **point-in-time** while the cursor hovers over its sparkline.

- Sparkline width ≈ 116pt. Default visible window for the card sparkline is the **last 2 hours** (720 samples) so each pixel maps ≈ 1 sample — the user sees recent behavior clearly without the line becoming a noisy smear of 24h at 1:75 compression. The full 24h lives in the ring and is queryable for the expanded chart view. For hover we reverse the mapping: each pixel resolves to the nearest-timestamp sample in the ring buffer (see backend §5.6).
- `ChartProxy.value(atX:)` converts cursor X to a `Date`; we pick the ring entry with the smallest `|sample.t − hoverDate|`.
- ViewModel holds `hoverSample: Sample?`. `hoverSample == nil` → display latest; non-nil → display the hovered point's value + timestamp.
- No debouncing, no SQLite query — the ring is in memory and the binding is synchronous.

```swift
.chartOverlay { proxy in
    GeometryReader { geo in
        Rectangle().fill(.clear).contentShape(.rect)
            .onContinuousHover { phase in
                switch phase {
                case .active(let point):
                    let x = point.x - geo[proxy.plotAreaFrame].origin.x
                    if let t: Date = proxy.value(atX: x) {
                        vm.hover(at: t)     // actor-isolated; nearest-match in ring
                    }
                case .ended:
                    vm.hover(at: nil)
                }
            }
    }
}
```

The ViewModel's `hover(at:)` returns immediately — the ring is a `ContiguousArray<Sample>` sized to ~720 points per metric (2 hours). Binary-search by `ts` or walk backwards; either is sub-microsecond at this size.

---

## 3. Card layout recipes (~280pt wide)

```
+--------------------------------------------------+  280pt
|  ● db-primary                          2m ago    |  hostname row
|  tailnet.mycorp.ts.net · linux/arm64             |  muted subtitle
|                                                  |
|  ┌────────────┐ ┌────────────┐                   |
|  │ CPU   42%  │ │ MEM   71%  │                   |  2x2 metric grid
|  │ ~~~/\_~~/\ │ │ __/~~\__/~ │                   |  sparkline row
|  └────────────┘ └────────────┘                   |
|  ┌────────────┐ ┌────────────┐                   |
|  │ DISK  88%! │ │ NET  4MB/s │                   |
|  │ ▁▁▂▃▄▅▆▇█  │ │ _/\__/\__/ │                   |
|  └────────────┘ └────────────┘                   |
+--------------------------------------------------+
```

Alt: **single-row dense** (when user scales down in prefs)

```
+--------------------------------------------------+
| ● db-primary                             2m ago  |
| CPU 42 ~/\_ │ MEM 71 _/~ │ DSK 88! ▂▅█ │ NET 4M _/\|
+--------------------------------------------------+
```

Spacing: card padding 12pt, cell padding 8pt, cell corner radius 6. Big number uses `.system(size: 17, weight: .semibold, design: .rounded).monospacedDigit()`. Metric label is `.caption2` uppercased `.secondary`. Entire card sits on `RoundedRectangle` with `.fill(.background.secondary)` and a 0.5pt stroke of `.separator`.

---

## 4. Color system — `ThresholdTint`

All tints live in the asset catalog with light + dark variants so they adapt automatically. Three semantic colors: `Accent/Nominal`, `Accent/Warn`, `Accent/Critical`. Keep them close to system (nominal ≈ `secondaryLabel`, warn ≈ `systemOrange`, critical ≈ `systemRed`) for that "belongs on macOS" feel.

```swift
enum ThresholdTint {
    case nominal, warn, critical
    var color: Color {
        switch self {
        case .nominal:  Color("Tint/Nominal")
        case .warn:     Color("Tint/Warn")
        case .critical: Color("Tint/Critical")
        }
    }
}

extension Color {
    /// value 0...1; thresholds user-configurable per metric.
    static func threshold(_ value: Double, warn: Double, critical: Double) -> Color {
        switch value {
        case ..<warn:       ThresholdTint.nominal.color
        case ..<critical:   ThresholdTint.warn.color
        default:            ThresholdTint.critical.color
        }
    }
}
```

Usage: `Text("\(pct)%").foregroundStyle(.threshold(pct, warn: 0.75, critical: 0.9))` — wrap in a small `View` extension so we don't call the helper inline twice. Hysteresis lives in the ViewModel, not the color helper.

---

## 5. State management — `@Observable` + `ServerStore` actor

macOS 14's Observation framework lets us drop `ObservableObject`/`@Published` in favor of the `@Observable` macro. Per-property tracking means a metric tick on server A does **not** re-render server B's card.

```swift
@Observable
final class ServerViewModel: Identifiable {
    let id: Server.ID
    var hostname: String
    var state: ServerConnState = .unknown
    var cpu: MetricSeries = .empty
    var memory: MetricSeries = .empty
    var disk: MetricSeries = .empty
    var network: MetricSeries = .empty
    var lastSeen: Date?
}

actor ServerStore {
    private(set) var servers: [ServerViewModel] = []

    func ingest(_ sample: Sample, for id: Server.ID) { /* append, decimate */ }
    func start() async { /* spin one Task per server, polling collector */ }
}
```

Views take the store from `@Environment(ServerStore.self)` (SwiftUI 14 environment-by-type). Each `ServerCardView` receives **its own** `ServerViewModel` via initializer, not the whole store, so Observation's fine-grained tracking kicks in:

```swift
ForEach(store.serverVMs) { vm in
    ServerCardView(vm: vm)
}
```

The actor guarantees serialized mutation; UI reads are off `ServerViewModel` on the main actor (mark it `@MainActor` to be explicit). Data flow: `SSHCollector` (background Task, concurrency) → `ServerStore.ingest(...)` → mutates `ServerViewModel` on main → only the affected card redraws.

For the menu bar icon, compute `aggregateState` as a derived `@Observable` property that reads each VM's current bucket; changing one VM only flips the icon when the overall max tier changes.

---

## 6. Preferences window — `Settings { }` scene

```swift
Settings {
    TabView {
        ServersPane()       .tabItem { Label("Servers", systemImage: "server.rack") }
        ThresholdsPane()    .tabItem { Label("Thresholds", systemImage: "gauge") }
        NotificationsPane() .tabItem { Label("Notifications", systemImage: "bell") }
        GeneralPane()       .tabItem { Label("General", systemImage: "gear") }
    }
    .frame(minWidth: 560, minHeight: 380)
}
```

- **Servers**: `Table` with columns Hostname, SSH User, Host/IP, Tags, Status. `+` / `-` buttons under the table. Editing opens an inline sheet with SSH key picker (reads from `~/.ssh/config`). Secrets ride in Keychain (`kSecClassGenericPassword`, per-server account).
- **Disks**: per-host detected-mounts list with a checkbox column; unchecking excludes a mount from cards/alerts. See §9.
- **Network**: per-host detected-interfaces list with a checkbox column; real NICs + `tailscale0`/`wg*` checked by default, Docker/bridge/veth unchecked. See §10.
- **Thresholds**: per-metric warn/critical sliders (global defaults + per-server overrides).
- **Notifications**: `UNUserNotificationCenter` toggles for `.warn` / `.critical`, debounce interval, quiet hours.
- **General**: launch at login, polling interval, appearance (auto/dense). Retention is fixed at 24h (see backend §4.1).

Settings state lives in a small `@Observable AppSettings` saved via `@AppStorage` for scalars + `~/Library/Application Support/Towertail/settings.json` for structured data (per-host thresholds, disk exclusions, quiet hours). Schema in backend §6.1. Atomic writes via `rename(2)`; file-system observer reloads on external edits. No YAML — `JSONEncoder`/`JSONDecoder` for free.

---

## 7. Keyboard & accessibility

- Popover root is a `ScrollView` with `focusable()` + `FocusState` per card; Up/Down move the selection ring, Return opens a detail sheet, Cmd-, opens Preferences (wired via `.keyboardShortcut`).
- Cmd-R force-refresh, Cmd-W dismiss popover (call `NSApp.keyWindow?.close()` or toggle via `MenuBarExtra`).
- Every metric cell: `.accessibilityElement(children: .combine)` with `.accessibilityLabel("CPU 42 percent, trending down, nominal")` — compose from the numeric value plus the tint enum plus a coarse trend ("rising", "falling", "steady") computed from the last 10 samples.
- Sparkline itself is `.accessibilityHidden(true)`; its data is already verbalized in the parent cell's label.
- Respect `accessibilityReduceMotion` — skip the area-fill animation on metric updates.
- Dynamic Type: cap at `.xxLarge` in cards (the popover is fixed width); Preferences is fully flexible.

---

## 8. Packaging

- **Signing**: Developer ID Application cert, hardened runtime enabled, entitlements limited to `com.apple.security.network.client` (for SSH to tailnet hosts) and `com.apple.security.files.user-selected.read-write` (SSH key import). No sandbox in v1 — we shell out to `ssh` and need access to `~/.ssh`.
- **Notarization**: `xcrun notarytool submit … --wait` in CI, then `xcrun stapler staple`. Ship a `.dmg` with a background image and `/Applications` symlink (use `create-dmg`).
- **Auto-updates**: [Sparkle 2](https://sparkle-project.org) — add via SPM, `SUFeedURL` in Info.plist, EdDSA-sign the appcast. Sparkle 2 supports sandbox/XPC if we tighten later.
- **Launch at login**: `SMAppService.mainApp` (macOS 13+). Toggle from General pane:

  ```swift
  try SMAppService.mainApp.register()   // or .unregister()
  ```

  No helper app or LaunchAgent plist needed. Surface the current `.status` so we can show "Blocked by user" if they denied it in System Settings.

---

## 9. Multi-disk UX

Real servers mount `/`, `/boot`, `/data`, `/var/lib/docker`, etc. We collect all real mounts (backend §1.6) but have to show something useful in a single compact "DISK" cell.

### 9.1 Compact card — worst mount wins

The DISK cell's big number is `max(used/total)` across **included** mounts (user can exclude — see §9.3). Threshold color fires on that same max value. Average would hide an outage: if `/` is 95% and `/data` is 20%, mean 57% reads nominal while the host is minutes from unable-to-write.

Card-level hover tooltip on the disk sparkline reads e.g.:

```
22m ago · 3 disks · worst: / 95%
```

The sparkline itself plots the rolling max over time — each 10s pixel is `max(used/total)` across included mounts at that timestamp.

### 9.2 Maximized disk view — per-mount dropdown

Clicking the minimized disk cell opens the maximized view (see `design.pen` element `FIGxb`). A dropdown sits under the "Disk" title listing every included mount:

```
┌─── Disk ────────────────────────────────┐
│  [ / — 95% ▾ ]           96%            │  ← picker; current % on right of title
│                                         │
│  100 ─────────────── critical rule ─── │
│   90 ─────────────── warn rule     ─── │
│   ...area+line for selected mount...    │
│    0 ───────────────────────────────── │
│     −60m                              now│
└─────────────────────────────────────────┘
```

Picker rows: `<mount> — <current %>`, sorted worst-first. Default selection:

1. If any disk alert is firing for this host → that mount.
2. Else → worst current mount.

No "Combined" / "Average" entry. It isn't actionable and we don't want to encourage reading it.

Switching the dropdown is instant — each mount has its own ring buffer (backend §1.6 last paragraph) so we don't SQLite-query on selection change.

### 9.3 Preferences — Disks pane

A dedicated tab in `Settings { }`:

```
┌─ Disks ──────────────────────────────────────────────────┐
│  Host: [ db-primary ▾ ]                                  │
│                                                          │
│   Include  Mount              Size    Used    Current    │
│   ☑        /                  50 GB   47.5    95%   ⚠    │
│   ☑        /boot              1 GB    0.4     40%        │
│   ☑        /data              2 TB    441 GB  22%        │
│   ☐        /mnt/backups       8 TB    6.2 TB  77%        │
│   ☑        /var/lib/docker    50 GB   12 GB   24%        │
│                                                          │
│   Mounts < 1 GB and virtual filesystems are hidden.      │
└──────────────────────────────────────────────────────────┘
```

- Host picker at the top (or a sidebar of hosts on the left — TBD based on how many hosts typical users have).
- Unchecking writes to `disks.excluded_mounts[host_id]` (backend §6.2).
- Excluded mounts are **still collected** and their history is preserved — toggling back on instantly restores the series in the card and alerts. We don't delete rows on exclusion.
- The "Current" column uses the live `ServerViewModel` reading so the user can make the call with fresh numbers.
- The `⚠` glyph is shown on rows at/above the user's disk warn threshold, so the "should I alert on this?" decision has context.

### 9.4 Empty / single-disk cases


- **One included mount** → the dropdown still renders but is disabled (single-item menu looks odd; consider swapping for a plain label reading the mount name). Card hover tooltip drops the "3 disks · worst:" prefix.
- **Zero included mounts** (user excluded everything) → DISK cell shows `—` and a muted label "no mounts". No alerts fire. Don't collapse the cell; that'd shift layout when they re-enable.
- **Host unreachable** → card-level state; all four cells already grey out (no disk-specific handling).

---

## 10. Multi-interface UX

Every Linux server with Docker reports ~4 virtual interfaces alongside `eth0`. If we summed everything, a container-to-container chat would register as host network traffic. Backend §1.7 is the source of truth for what counts; this section is the UX surface.

### 10.1 Compact card — aggregate of included real interfaces

Unlike disk (worst-wins), network aggregates by **summing** `rx_bps` and `tx_bps` across included interfaces. The split-axis chart (element `u5fGv` in `design.pen`) plots the aggregate, with download below the zero line in blue and upload above in red.

Default included set:

- **Real NICs:** anything not under `/sys/devices/virtual/` on Linux, `networksetup -listallhardwareports` on Darwin.
- **Tailscale / WireGuard TUN:** `tailscale0`, `wg*` on Linux; user opts in per-host on Darwin.
- **Explicitly excluded, always:** `lo`, `docker*`, `br-*`, `veth*`, `cni*`, `flannel*`, `cali*`, `virbr*`, `vnet*`, `vmnet*`.

Card hover tooltip reads e.g.:

```
22m ago · 2 nics · ↓ 11.6 MB/s · ↑ 1.1 MB/s
```

### 10.2 Maximized network view — aggregate picker

Clicking the minimized network cell opens the maximized view (`u5fGv`). A dropdown sits under the "Network" title listing an "All" entry first, then each included interface with current throughput:

```
┌─── Network ─────────────────────────────────────┐
│  [ All (aggregate) ▾ ]   ↓ 11.6 MB/s · ↑ 1.1 MB │
│                                                 │
│   1.1M ──── upload (red, above center) ────────│
│      0 ─────────────── axis ────────────────── │
│  11.6M ──── download (blue, below center) ──── │
│     −60m                                     now│
└─────────────────────────────────────────────────┘
```

Picker rows:

```
  All (aggregate)     ↓ 11.6 / ↑ 1.1 MB/s
  eth0                ↓ 11.6 / ↑ 1.0 MB/s
  tailscale0          ↓ 0.02 / ↑ 0.08 MB/s
```

Unlike disk (where "average" is meaningless), the **aggregate is the meaningful default** for network — "how saturated is this host's uplink?" is exactly what you want. Per-interface is the drill-down for when a spike shows up and you want to know which NIC moved.

Aggregate selection renders from a derived ring that re-sums included interfaces on each sample append (backend §1.7). Per-interface selection reads that interface's ring directly. Both are instant — no SQLite roundtrip.

### 10.3 Preferences — Network pane

Mirrors the Disks pane (§9.3):

```
┌─ Network ───────────────────────────────────────────────────┐
│  Host: [ db-primary ▾ ]                                     │
│                                                             │
│   Include  Interface      Type       Current (↓ / ↑)        │
│   ☑        eth0           physical   11.6 / 1.0 MB/s        │
│   ☑        tailscale0     tailscale  0.02 / 0.08 MB/s       │
│   ☐        docker0        bridge     0 / 0 KB/s             │
│   ☐        br-a1b2c3d4    bridge     14 / 12 KB/s           │
│   ☐        veth4f2a       container  1.2 / 1.1 MB/s    ⚠   │
│   ☐        lo             loopback   —                      │
│                                                             │
│  ⚠ High throughput on an excluded interface — include it if │
│    this traffic is real (not container-internal).           │
└─────────────────────────────────────────────────────────────┘
```

- Type column surfaces the classification the probe made (`physical`, `tailscale`, `wireguard`, `bridge`, `container`, `loopback`, `other`) so the user doesn't have to guess from the name.
- Excluded interfaces are **still sampled** — the "Current" column shows live rates so the user can spot the Docker bridge they *do* care about.
- The `⚠` glyph appears when an excluded interface has sustained non-trivial throughput, gently suggesting they may want to include it.
- Checking `docker0` etc. writes to `network.excluded_interfaces[host_id]` being *removed* (default excludes are implicit via §1.7 classification; user overrides live in settings).
- Checking a `utun` on Darwin writes to `network.included_virtual_interfaces[host_id]`.

### 10.4 Alerts — host-scoped, not per-interface

Network threshold rules evaluate on the aggregate. `alert_state.mount` stays `NULL` for network rules (column is reused: disk uses it, network doesn't). Rationale: "eth0 > 500 Mbps" alerts are noisy; "this host's uplink is saturated" is the actionable form, and that's the aggregate.

### 10.5 Empty / single-interface cases

- **One included interface** → dropdown collapses to a disabled label ("eth0" or similar); the "All" entry is hidden since aggregate == that one NIC.
- **Zero included interfaces** → card cell shows `—` with muted "no interfaces"; no alerts; don't collapse layout.
- **Unreachable host** → card-level state; no network-specific handling.

---

## 11. Full view — chart + process table window

Clicking a card's chart area (CPU / memory / disk / network) opens a standalone window (not a popover) with the maximized chart on top and a process table below. See `design.pen` element `Ugzuf`, exported to `docs/screenshots/towertail-fullview.png`.

### 11.1 Window scaffolding

A second SwiftUI `WindowGroup` sibling to `MenuBarExtra`. Invoked with `openWindow(id:"full-view", value: FullViewContext(hostId:…, metric:…))`.

```swift
WindowGroup(for: FullViewContext.self) { $ctx in
    if let ctx { FullViewWindow(context: ctx).environment(store) }
} .windowResizability(.contentSize)
  .defaultSize(width: 1200, height: 900)
```

- **Resizable**, min `900 × 600`.
- **Not sandboxed** (inherits app-level). Standard traffic lights.
- **One window per host**, keyed by `FullViewContext.hostId`. Re-clicking the card focuses the existing window instead of opening a new one.

### 11.2 Layout — 30 / 70 vertical split, draggable

```
┌──────────────────────────────────────────────────────────┐
│ ● ● ●    web-edge-1  ubuntu@…ts.net · linux/arm64       │  TitleBar (38pt)
├──────────────────────────────────────────────────────────┤
│ [ CPU  MEM  DISK  NET ]                 ⏸ ⏮ ⏭           │  Toolbar (44pt)
├──────────────────────────────────────────────────────────┤
│ CPU · last hour                   PIN 22m · 71%  avg 38 │  ChartPane (~30%)
│ ╭────────────────────────────────────────────────╮  x   │
│ │  line+area chart with pin crosshair/dot/chip   │      │
│ ╰────────────────────────────────────────────────╯      │
├──────────────────────────────────────────────────────────┤
│ ━━━━━━ splitter (drag to resize) ━━━━━━                  │  Splitter (6pt)
├──────────────────────────────────────────────────────────┤
│ TOP 20 PROCESSES BY CPU · snapshot at 13:28:40  [🔍][Copy]│  TablePane (~70%)
│ PID   COMMAND               USER     CPU%   RSS  …      │
│ 18421 postgres: autovacuum…  postgres  47.2 1.8G …      │
│ …                                                        │
│ 20 of 347 · snapshot age 22m        press Space to play │
└──────────────────────────────────────────────────────────┘
```

- Use `HSplitView` / `VSplitView` (NSSplitView via SwiftUI's `Group` + geometry) for the draggable split. Persist the ratio to `@AppStorage("fullview.split.ratio")`.
- Splitter grip is a 3pt pill centered on a 6pt-tall drag surface (visible in design).

### 11.3 Metric tab bar + play controls

- **Tabs** switch the chart *and* the table columns together (see §11.5 for column sets). The selected metric is persisted per-host so re-opening the window remembers whether the user was looking at CPU or network last.
- **Play / Pause button** (single button, icon flips) — toggles follow-latest mode. Default is **play** (live). When paused, a `LIVE` → `PAUSED` badge swap happens in the title bar area.
- **Step back / step forward** (⏮ ⏭) — when paused, walk one sample (10s) at a time. Disabled in live mode.
- Keyboard: `Space` toggles play/pause, `←`/`→` step, `⌘W` closes window, `⌘R` forces refresh.

### 11.4 Chart states: live, hover, pinned

Three states for the chart + table pair:

| State | Trigger | Chart shows | Table shows |
|---|---|---|---|
| **Live** | default; play button active | latest sample with no crosshair | most recent snapshot, follows latest |
| **Hover** | cursor over chart area | crosshair + dot + chip at cursor timestamp | snapshot at hovered timestamp |
| **Pinned** | click a chart point; or press Space to pause | crosshair + dot + chip in **orange**, persistent | snapshot at pinned timestamp; header shows `PINNED 22m ago · 13:28:40` |

Semantics:

- Hover is a transient read; leaving the chart returns to live (if playing) or the pinned point (if paused).
- Click-to-pin sets `pausedAt: Date?` on the `FullViewModel`. A small pin glyph appears in the chart header alongside the timestamp.
- Pressing play unpins (`pausedAt = nil`) and resumes live-follow.
- The 'x' circle in the chart header right side is a *chart-level* close for the hover chip — it clears the pinned state without switching metrics. (The window traffic-lights are the only close for the window.)

Implementation: the chart exposes `onHover(at:) -> Date?` via `ChartProxy.value(atX:)` (§2 hover pattern). The `FullViewModel` has:

```swift
@Observable @MainActor
final class FullViewModel {
    var metric: Metric              // .cpu / .mem / .disk / .net
    var mode: Mode                  // .live, .paused(Date), .pinned(Date)
    var hoverAt: Date?              // transient; drives crosshair when non-nil
    var snapshot: [ProcessRow] = [] // refreshed when effective timestamp changes
    var snapshotTs: Date?
    
    // effective timestamp for the table: hover wins, then pinned, then latest
    var effectiveTs: Date? {
        hoverAt ?? mode.pinnedDate ?? store.latestTs(for: hostId)
    }
}
```

A debounced `onChange(of: effectiveTs)` of ~100ms fires the SQLite process-snapshot query (backend §5.7.3) and updates `snapshot`.

### 11.5 Process table

Seven columns, metric-aware:

| Metric | Columns |
|---|---|
| **CPU** | PID, Command, User, **CPU % (↓)**, RSS, Threads, Started |
| **Memory** | PID, Command, User, **RSS (↓)**, CPU %, Threads, Started |
| **Network** | PID, Command, User, **I/O B/s (↓)**, ↓ B/s, ↑ B/s, Threads, Started |
| **Disk** | PID, Command, User, **I/O B/s (↓)**, Read B/s, Write B/s, Threads, Started |

The disk columns come from `/proc/<pid>/io` (Linux) — `read_bytes` / `write_bytes` deltas over a 1s sample window (backend §5.7.1). When the probe reports `procs_disk_available: false` (Darwin or kernel without `/proc/<pid>/io`), the table replaces its rows with the install-hint empty state (same pattern as network when `nethogs` is missing).

The "sort-by" column is always the one the metric ranks on; the server-side ranking already ordered the rows 1..20, so client-side sort is a no-op re-render. Secondary client-side sort (click a column header) is a nice-to-have for v2.

Command column shows an icon hint per process family (zap=database worker, container=container, database=RDBMS, globe=web server, shield=sshd, activity=systemd, hard-drive=kworker/kthread, wifi=tailscaled, box=dockerd, cpu=redis). The probe can't reliably classify, so we pattern-match on `comm` client-side with a small lookup table — a cosmetic touch, not load-bearing.

Row colors zebra-stripe between `#1C1C1E` and `#17171A`. On hover (cursor over a row), fill flips to `#2C2C2E` and the row becomes selectable (Cmd+click a PID copies it).

**Copy button** in the table header: copies the current snapshot as TSV to clipboard — good for pasting into Slack when reporting an incident. Shift+Copy gets markdown-formatted.

**Search box**: filter by command substring; doesn't re-rank, just hides non-matching rows.

**Empty state for network / disk**: when per-process collection isn't available on the host — `nethogs` missing (network), or the kernel doesn't expose `/proc/<pid>/io` / we're on Darwin (disk) — show a card-style empty state in the table area with the specific missing dependency, an install command (`apt install nethogs`, or "upgrade to Linux 2.6.20+ / supported kernel" for disk), and a "Retry" button that re-probes. The chart above still renders normally; only the table is gated by probe capability.

### 11.6 Footer

A 22pt footer under the table:

```
20 of 347 processes · snapshot age 22m · sorted by CPU       ▶ press Space to resume live
```

- "20 of N" — N is total process count reported by the probe (cheap additional field: `ps -e | wc -l`). Makes the table feel representative, not truncated arbitrarily.
- "snapshot age" — friendly duration since the effective timestamp. Live mode shows `snapshot age 0s`; pinned shows `22m` etc.
- Hint on the right flips between "press Space to resume live" (when paused) and "hover chart to inspect · click to pin" (when live).

### 11.7 Opening / closing semantics

- Clicking any of the four cells on the compact card opens this window with that metric pre-selected.
- Re-clicking the same cell (window already open) focuses it and switches to that metric.
- Closing the window leaves the menu-bar popover untouched.
- Window state (metric, paused timestamp, split ratio) persists per-host in `UserDefaults` so the window re-opens where it was.

### 11.8 Why a separate window, not an expanded popover

- **Real estate**: 20-row table + 1-hour chart at card width would be unreadable.
- **Modality**: the popover is meant to be glanceable; the full view is investigative. Making it a window lets the user keep it open next to logs/Terminal while they dig.
- **Multi-host**: the user can open fullviews for two hosts side-by-side. One popover, many windows.

---

## Proposed module structure

```
Towertail/
├── Towertail.xcodeproj
├── Package.swift                    # optional: SPM workspace for core
├── Sources/
│   ├── App/
│   │   ├── TowertailApp.swift       # @main, MenuBarExtra, Settings scene
│   │   ├── AppEnvironment.swift     # DI container
│   │   └── Info.plist               # LSUIElement, SUFeedURL, etc.
│   ├── MenuBar/
│   │   ├── MenuBarIcon.swift
│   │   └── PopoverRoot.swift        # ScrollView + ForEach of cards
│   ├── Cards/
│   │   ├── ServerCardView.swift
│   │   ├── MetricCell.swift
│   │   ├── Sparkline.swift
│   │   └── CardChrome.swift         # background, stroke, header row
│   ├── Preferences/
│   │   ├── PreferencesWindow.swift
│   │   ├── ServersPane.swift
│   │   ├── ThresholdsPane.swift
│   │   ├── NotificationsPane.swift
│   │   └── GeneralPane.swift
│   ├── State/
│   │   ├── ServerStore.swift        # actor
│   │   ├── ServerViewModel.swift    # @Observable, @MainActor
│   │   ├── MetricSeries.swift       # ring buffer + decimation
│   │   └── AppSettings.swift
│   ├── Design/
│   │   ├── ThresholdTint.swift
│   │   ├── Color+Threshold.swift
│   │   ├── Typography.swift
│   │   └── Assets.xcassets          # Tint/Nominal, Tint/Warn, Tint/Critical
│   ├── Collectors/                  # backend-facing, but owned here for DI
│   │   ├── SSHCollector.swift
│   │   ├── PingCollector.swift
│   │   └── SampleParser.swift
│   ├── System/
│   │   ├── LaunchAtLogin.swift      # SMAppService wrapper
│   │   ├── Notifications.swift      # UNUserNotificationCenter
│   │   └── KeychainStore.swift
│   └── Updates/
│       └── SparkleUpdater.swift
├── Tests/
│   ├── StateTests/                  # ServerStore, MetricSeries
│   └── DesignTests/                 # threshold helper, snapshot tests
└── Resources/
    ├── Credits.rtf
    └── appcast.xml                  # published separately
```

A thin **`TowertailCore`** SPM package (wrapping `State/`, `Collectors/`, `System/KeychainStore`) is worth extracting once the API stabilizes — it lets us unit-test without launching the app target and keeps the UI module blissfully ignorant of SSH details.

---

### Opinionated defaults worth locking in early

- Popover width **300pt** fixed, height **min 180 / max 640** with inner scroll.
- Refresh cadence **15s** default, configurable 5s–5m.
- Ring buffer **7 days @ 1-minute** per metric per server (~40KB each, trivial).
- Sparklines **decimated to 120 points** at render time.
- Thresholds default **warn 0.75, critical 0.90** for CPU/MEM/DISK; NET is rate-based per-link (configured per server).
- No color beyond the three semantic tints plus `.primary`/`.secondary` system text. Discipline is how we get "beautiful".
