# Stats (exelban) — Research for Towertail Design

Source code observed in `github.com/exelban/stats` (Swift/Cocoa, AppKit-based, ~38k stars). Primary references: the README screenshots `menus?v2.3.2.png` and `popups?v2.3.2.png` hosted on the author's S3, the marketing page at `mac-stats.com`, and the actual implementation in `Kit/extensions.swift`, `Kit/types.swift`, `Kit/constants.swift`, `Kit/Widgets/*.swift`, and `Modules/*/popup.swift`.

## 1. Visual language

Stats is **AppKit-native to a fault** — every surface is `NSStackView` / `NSView`, no SwiftUI, no custom typography. It inherits the system font (SF Pro), system control colors, and System Accent. That is its dominant aesthetic choice: it does not try to look "designed", it tries to look like a first-party Apple utility.

**Widget primitives** (one file each in `Kit/Widgets/`): `Mini` (text only — `12pt 12 12` style numerics), `Label`, `BarChart`, `LineChart`, `PieChart` (rings), `NetworkChart` (bidirectional fill), `Speed` (up/down arrows + KB/MB), `Tachometer`, `Battery`, `Dot`, `Stack`, `Memory`, `Text`. These are mixed and matched per module — a single CPU readout in the menu bar is typically a `LineChart` widget (32px wide) plus an optional `Mini` percentage.

**Sparklines / line charts**: rendered into a fixed-height area = `22 - 2*margin` ≈ 18 px. `LineChart.swift` defaults to 60 samples wide, configurable to 30/60/90/120, mapping to widget widths of 24/32/42/52 px. Optional 1-px frame (`boxState=true` by default) gives the chart a subtle bordered "well" look. In the popover, the same chart is enlarged to 70 px tall and given a `lightGray @ 0.1 alpha` background (`Modules/CPU/popup.swift:238`) — that pale wash is the signature "container" look.

**Ring/pie charts**: `PieChartView` is used both filled (CPU usage) and as `openCircle: true` (a thin ring for temperature & frequency). In the CPU popover dashboard, rings are stacked horizontally: a large center usage ring with two small (50×50) satellite open-rings flanking it. Numeric value sits in the middle of the filled ring. Very legible at small sizes because they use the value-color routine (see §5).

**Popover structure** (`Kit/constants.swift`):

```
Popup width:  264 px
Popup height: 300 px (default; modules grow as needed)
Margins:       8 px
Settings:    540 × 480 px
Widget:        32 px wide, 22 px tall, margin (1, 0)
```

The popover is a single vertical `NSStackView` with `spacing = 0`. Sections are separated by a `separatorView(localizedString("Usage history"), …)` — a thin labeled divider, no boxes around sections. Density is high but not crammed: each module's popover typically fits ~3 zones (dashboard rings, history chart, process list / details) within ~300–400 px.

**Typography**: System font everywhere, weight `.regular` for labels, `.medium` for values, `.monospacedDigit` design for numerics so columns don't shimmy. No custom display fonts. Section labels are 10–11 pt, value text 10–14 pt.

**Dark mode**: handled implicitly via dynamic system colors (`NSColor.textColor`, `NSColor.controlAccentColor`, `NSColor.systemGray`). The `lightGray @ 0.1` chart wash works in both modes because it's alpha over the popover material. There is no custom color asset catalog — Stats relies entirely on `NSColor.system*` semantic colors.

**mac-stats.com** confirms the marketing emphasis: "lightweight menu bar app", 9 modules (CPU/GPU/RAM/Disk/Sensors/Network/Battery/Bluetooth/Clock), 100M+ downloads, 39 languages. Hero shot is `/img/Stats.webp` showing the popover; module pages show ring + sparkline composition.

## 2. Menu bar integration

- One menu-bar item **per module** (CPU, RAM, etc., each independently toggleable). They can be reordered with cmd-drag (macOS 10.14+).
- Each item is one of the widget primitives above. Common combinations: `Mini` ("87%"), `LineChart` (32-px sparkline), `BarChart` (vertical bars per core), `Speed` (↑1.2 MB ↓340 KB), `BatteryWidget` (icon + percentage inside).
- Refresh cadence is per-module via `ReaderUpdateIntervals`: **1 / 2 / 3 / 5 / 10 / 15 / 30 / 60 sec** (default typically 1s for CPU/RAM, 3s for disk/net). Each module owns a timer; the menu-bar widget redraws on each tick.
- Click on the menu-bar item opens the **popover** (`NSPopover` anchored to the status item view). It animates in with the standard system arrow. Right-click opens that module's settings sheet directly.
- Menu-bar items can colorize (the value or the chart line) using the `usageColor` zones, or stay monochrome — user preference.

## 3. Settings / preferences UX

- **Settings window**: 540 × 480, `NSSplitView` style — left rail of modules + global pages (Dashboard, Update, Support), right pane with the selected page.
- Per-module settings are tabbed: **Widgets** (toggle each widget primitive on/off, configure each one), **Popup** (history length: `60 / 120 / 180 / 300 / 600` sec → 1–10 min in popover; chart scale: none/linear/square/cube/log/fixed), **Notifications**, **Module-specific**.
- **Threshold inputs** are dropdowns (not free-text), values from `notificationLevels` in `Kit/types.swift`: `Disabled, 3%, 5%, 10%, 15% … 100%`. Each metric has its own threshold (CPU total/system/user/eCores/pCores; RAM/Swap; Disk free; Net throughput; Battery level/temperature).
- **Notifications** route through `UNUserNotificationCenter`. Each notification has a stable ID (e.g. `totalUsage`) so it dedupes — Stats re-fires only when the value crosses back below and above the threshold again.
- **Update intervals** for the *app itself*: `Silent / At start / Once per day / Once per week / Once per month / Never`.
- **Color picker per widget**: the widget can be tinted from `SColor.allColors` (~25 named choices including `systemAccent`, monochrome, all `system*` colors plus extras like `msamplera`, `cyan`, `indigo`). Two semantic options on top: `utilization` (auto-color by value), `pressure` (RAM-pressure-aware), `cluster` (per-CPU-cluster coloring). This is the elegant part: you pick *how* a widget gets colored, not just one color.

## 4. What to borrow vs. what to drop for Towertail

**Borrow:**

- **AppKit-native popover** (264-ish px wide, single vertical stack, no chrome, system materials). Resist building this in SwiftUI — `NSPopover` + `NSStackView` is faster to lay out and renders pixel-perfect at small sizes. SwiftUI can host individual cards inside.
- **Sparkline + ring composition**: each server card = small ring (CPU) + sparkline (CPU history) + numeric labels for Memory / Disk / Net, mirroring Stats' "dashboard + history" two-zone rhythm.
- **`usageColor(zones:)` pattern** (`Kit/extensions.swift:116`). Hard-code a global zones tuple, default `(0.6, 0.8)`, allow override per-server. Use the same `0..<orange / orange..<red / red..` switch.
- **`monospacedDigit` SF Pro for all numerics** — keeps the per-card values aligned across rows.
- **Fixed-cadence reader timers** with selectable interval (1/3/5/10/30 sec). For remote SSH polling, default to **5 sec** (Stats' equivalent for "expensive" modules) — anything tighter wastes Tailscale bandwidth and SSH session reuse.
- **Threshold-as-dropdown** (5 % steps) with a "Disabled" sentinel. Free-text invites typos.
- **Notification dedup IDs** — `cpu_<host>`, `mem_<host>`, etc. Critical for not spamming when a server flaps.
- **History retention 1–10 min in popover, but separate "deep history" buffer for 7-day retention** (Stats does *not* retain 7 days — it discards on relaunch). Persist as ring-buffer SQLite or `Data` file in `Application Support/Towertail/history/<host>.bin`.

**Drop / change:**

- **One menu-bar item per module** does not translate. Towertail = **one** menu-bar item that opens **one** popover containing **N server cards**. Stats' multi-icon model would clutter the bar with N×4 icons.
- Drop the 540×480 split-view settings window — Towertail's settings can be a single ~540×~600 SwiftUI form (server list + global preferences). Tabs aren't needed for ~5 sections.
- Drop the SColor "any color" picker. It's a consumer-app touch that doesn't help server monitoring; offer only nominal/warn/critical theme. Maybe a single accent override per server (for visual identification).
- Drop scale options (square/cube/log) — pure noise for ops monitoring. Linear, fixed-100%-axis only.
- Stats' popover is 264 px wide, which is too narrow for a server card with 4 metrics + name + status. Plan for **~340–380 px wide** cards, popover total ~360 px.
- Stats hides offline/error states inside the icon glyph. Towertail must surface SSH connection state explicitly on every card (a small dot: green=connected, gray=stale >2× interval, red=auth/connection failed).

## 5. Color / threshold convention

**Stats' ground truth** (`Kit/extensions.swift:115-138`):

```swift
func usageColor(zones: colorZones = (0.6, 0.8), ...) -> NSColor {
    let firstColor:  NSColor = NSColor.systemBlue
    let secondColor: NSColor = NSColor.orange
    let thirdColor:  NSColor = NSColor.red
    // 0 … 0.6 → blue, 0.6 … 0.8 → orange, 0.8 … 1.0 → red
}
```

Battery (`Kit/extensions.swift:142`): 0–20 % red, 20–40 % orange (`systemOrange`), 40–100 % green (`systemGreen`); low-power-mode forces orange. Notice the use of *named* colors, not hex — Stats lets the OS pick the exact RGB for light/dark mode.

Resolved RGB on macOS Sonoma+ (light mode):
- `NSColor.systemBlue`   → **#007AFF**
- `NSColor.orange`        → **#FF8000** (the AppKit `orange`, not `systemOrange` which is `#FF9500`)
- `NSColor.red`           → **#FF0000** (the AppKit `red`, not `systemRed` `#FF3B30`)
- `NSColor.systemGreen`   → **#34C759**
- `NSColor.systemOrange`  → **#FF9500**
- `NSColor.systemRed`     → **#FF3B30`
- `NSColor.systemGray`    → **#8E8E93**
- `NSColor.textColor`     → dynamic (~#000 light / ~#FFF dark)

The `usageColor` palette uses the older non-`system*` variants — slightly more saturated, more "warning sign" vibe — while everywhere else Stats uses `system*`. For Towertail we can do better: use `system*` consistently for HIG conformance and free dark-mode adaptation.

**Proposed Towertail palette** (semantic, all dynamic via `NSColor.system*` so dark mode is free):

| State            | Color (named)            | Light hex | Dark hex  | Use                            |
|------------------|--------------------------|-----------|-----------|--------------------------------|
| Nominal          | `NSColor.systemGreen`    | `#34C759` | `#30D158` | 0–60 % utilization, healthy    |
| Elevated         | `NSColor.systemYellow`   | `#FFCC00` | `#FFD60A` | 60–75 %, "watch this"          |
| Warning          | `NSColor.systemOrange`   | `#FF9500` | `#FF9F0A` | 75–90 %, threshold breached    |
| Critical         | `NSColor.systemRed`      | `#FF3B30` | `#FF453A` | ≥ 90 %, paging-worthy          |
| Stale data       | `NSColor.systemGray`     | `#8E8E93` | `#8E8E93` | reader >2× interval behind     |
| Offline / error  | `NSColor.systemGray2`    | `#AEAEB2` | `#636366` | SSH down, auth failed          |
| Accent / brand   | `NSColor.controlAccent`  | dynamic   | dynamic   | selection, primary buttons     |
| Chart fill wash  | `lightGray α=0.10`       | —         | —         | sparkline well background      |
| Sparkline stroke | dynamic = current state  | —         | —         | line color matches state color |

Default zones: `(elevated: 0.60, warning: 0.75, critical: 0.90)`. Stats uses two-stop; we propose **three-stop** because remote-server ops cares about the "concerning but not paging" middle ground that Stats glosses over for desktop use. Per-server override for disks (where 90 % full is normal and 98 % is critical — invert) and for memory (Linux cache inflates usage; threshold 85/92/97 by default).

Apply state color to: (a) the metric numeric, (b) the sparkline stroke, (c) a 2-px left border on the card when any metric is ≥ warning, (d) the menu-bar item glyph itself when any server card is critical (so the menu bar surfaces problems without being opened).

---

**Key files referenced** (all in `github.com/exelban/stats`):

- `Kit/extensions.swift:115-138` — `usageColor(zones:)` and `batteryColor`
- `Kit/types.swift` — `SColor` palette, `notificationLevels`, `ReaderUpdateIntervals`, `LineChartHistory`, `Scale`
- `Kit/constants.swift:14-48` — popup/settings/widget dimensions
- `Kit/Widgets/{LineChart,PieChart,BarChart,Mini,Speed,Battery}.swift` — primitive renderers
- `Modules/CPU/popup.swift:138-242` — canonical popover layout (rings + history chart with `lightGray α=0.1` wash)
- `Modules/CPU/notifications.swift` — per-metric threshold + dedup ID pattern
- `Stats/Views/{Dashboard,Settings,AppSettings}.swift` — settings window structure
