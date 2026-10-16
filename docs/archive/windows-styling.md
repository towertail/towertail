# Towertail WinUI — Styling & Polish Plan

Status: proposal. Captures the research done 2026-04-23 on bringing the WinUI 3 client closer to the Mac SwiftUI version's visual quality. Nothing in here is committed yet; pick what's worth doing and in what order.

Target stack (unchanged): WinUI 3 + .NET 9 + Windows App SDK 1.6 on Windows 10 1809+ / Windows 11.

## Why the current look falls short

What's wrong today, concretely:
- `MetricChart` draws raw Unix-tick numbers (`639125636500…`) on the X axis because `DateTimePoint` values stream straight into the default numeric axis with no `Labeler`. Lines are hardcoded `SteelBlue` with no theme awareness, no gradient fill, no axis limits. On a percent chart we should force 0–100.
- `PreferencesWindow` is plain: no Mica backdrop, default system titlebar, navigation pane at 40% default density, settings panes built from raw `TextBox`/`ComboBox`/`CheckBox` stacks with no grouping or iconography. Compare to the Mac "Servers" screenshot (native table, toolbar, rounded row chrome).
- `ServersPane` uses plain `<Button>Add</Button>` text buttons instead of a `CommandBar`, and the server list is a minimal `ListView` with hand-drawn header row — no column resize, no sort, no selection chrome. The Mac version is a real table.
- `FullViewWindow` opens without Mica, without a styled titlebar, and the `NavigationView` content host has no padding.
- `Design/Theme.xaml` hardcodes semantic colors (`#FF34C759` etc.) instead of deriving from system theme brushes — breaks in dark mode and doesn't respect the user's accent color.
- Typography is inconsistent: custom `CardTitleFontSize`/`MetricValueFontSize` doubles exist in parallel with WinUI's built-in type ramp (`BodyStrongTextBlockStyle`, `SubtitleTextBlockStyle`, etc.).

The ceiling: we're not trying to make WinUI *look* like SwiftUI — that's a losing game. We're trying to make the app feel native-Windows-11-premium, which is what the Mac side feels like on macOS. The WinUI 3 design system can get there; we just haven't plugged it in.

## Research summary — what WinUI gives us for free

Six areas, in rough order of bang-for-buck.

### 1. Chart cleanup — `LiveChartsCore.SkiaSharpView.WinUI` 2.0.0-rc5

Already installed (we just upgraded from rc2 to fix the missing `LiveChartsCore.Behaviours.dll` bug).

Leverage points:
- `Axis.Labeler = v => new DateTime((long)v).ToLocalTime().ToString("HH:mm")` — formats `DateTimePoint.Ticks` as a time string, no more `639125636500`.
- `Axis.LabelsPaint = new SolidColorPaint(themeAwareColor)` — axis text color.
- `Axis.SeparatorsPaint = null` — hides gridlines (sparkline look). `SeparatorsPaint = new SolidColorPaint(faintColor)` for subtle horizontal rules.
- `Axis.MinLimit / MaxLimit` — pin percent axes to 0–100. Omit on bytes/sec axes so they autoscale.
- `Axis.MinStep` — don't let the auto-ticker produce a label per second on dense data (`TimeSpan.FromMinutes(1).Ticks` for time, `25` for percent).
- `CartesianChart.DrawMarginFrame = new DrawMarginFrame { Stroke = null, Fill = null }` — kills the outer rectangle border.
- `LineSeries.Fill = new LinearGradientPaint(...)` — the soft translucent fill under the line that mimics `.areaStyle` on macOS Charts. Two stops: 33% alpha of line color at top → 0% at bottom.
- `LineSeries.LineSmoothness = 0.4` — gentle Catmull-Rom-ish curve.
- `LineSeries.GeometrySize = 0` — no point dots; just the line.

Theme-aware colors: subscribe to `UserControl.ActualThemeChanged`, recompute `SKColor` values, reassign axes + series. Pull the accent from `Application.Current.Resources["SystemAccentColor"]` and lighten for dark mode.

For the **compact card sparkline**, different rules: no axes at all (`LabelsPaint = null` and `SeparatorsPaint = null` on both axes), `TooltipPosition = Hidden`, no `DrawMarginFrame`. We already render these with `SKXamlCanvas` directly — don't switch; LiveCharts is overkill for 60-point sparks.

Source pointers: [LiveCharts2 axes.style](https://livecharts.dev/docs/winui/2.0.0-rc4/samples.axes.style), [Axis API](https://livecharts.dev/api/2.0.0-rc2/LiveChartsCore.SkiaSharpView.Axis).

### 2. Settings shell — `CommunityToolkit.WinUI.Controls.SettingsControls`

NuGet: `CommunityToolkit.WinUI.Controls.SettingsControls 8.2.251219`.

This is the package Microsoft themselves use for the Windows 11 Settings app aesthetic. Two key controls:

- `SettingsCard` — one row: icon + header + description on the left, a single interactive control (ToggleSwitch, ComboBox, NumberBox, Button…) on the right. Rounded, hairline-bordered, hover-highlighted.
- `SettingsExpander` — same header row, but expands to reveal child `SettingsCard`s underneath. Good for grouped advanced options.

Register in `App.xaml` merged dictionaries:

```xml
<ResourceDictionary Source="ms-appx:///CommunityToolkit.WinUI.Controls.SettingsControls/SettingsCard/SettingsCard.xaml" />
<ResourceDictionary Source="ms-appx:///CommunityToolkit.WinUI.Controls.SettingsControls/SettingsExpander/SettingsExpander.xaml" />
```

Panes to rewrite with this pattern:
- **GeneralPane** — cards for "Launch at login" (ToggleSwitch), "Theme" (ComboBox System/Light/Dark), "Menu-bar icon style" (RadioButtons).
- **ThresholdsPane** — one SettingsExpander per metric (CPU / MEM / DISK / NET) with warn + critical NumberBoxes inside.
- **NotificationsPane** — cards for "Enable notifications" (ToggleSwitch), "Sound" (ComboBox), "Grouping window" (NumberBox).
- **LogsPane** — card "Log level" (ComboBox), card "Open log folder" (Button), SettingsExpander "Retention" with size/age NumberBoxes.

Section headers between groups — reuse the official recipe:

```xml
<Style x:Key="SettingsSectionHeaderTextBlockStyle"
       BasedOn="{StaticResource BodyStrongTextBlockStyle}"
       TargetType="TextBlock">
    <Setter Property="Margin" Value="1,30,0,6"/>
</Style>
```

Padding around the whole scroll content: `MaxWidth="1000"`, left/right 24, top 12, bottom 24. Matches the Windows 11 Settings app.

Source: [SettingsExpander docs](https://learn.microsoft.com/en-us/dotnet/communitytoolkit/windows/settingscontrols/settingsexpander).

### 3. Proper table — `WinUI.TableView` 1.3.4

Today's `ServersPane.xaml` hand-builds a table with a header `Grid` over a `ListView`. Works, but no sort, no resize, no filter, no selection chrome.

Options evaluated:
- **`CommunityToolkit.WinUI.UI.Controls.DataGrid`** — legacy, only ships for UWP / Uno, effectively unmaintained on WinUI 3. **Skip.**
- **`CommunityToolkit.Labs.WinUI.DataTable`** — experimental, no NuGet, repo-only. **Skip.**
- **Raw `ItemsRepeater` + styled rows** — what we have, plus more boilerplate. Means re-implementing virtualization, selection, sort headers. **Skip.**
- **`WinUI.TableView`** (by w-ahmad) — actively maintained, derives from `ListView` (virtualization + selection inherited), sort + Excel-style filter out of the box, typed column types (`TableViewTextColumn`, `TableViewCheckBoxColumn`, `TableViewComboBoxColumn`, etc.). **Recommend.**

Dependencies to understand: it's a single NuGet, no transitive WinUI version collisions, .NET 9 compatible, ~50KB dll. BSD-ish MIT license.

Apply to:
- `ServersPane` — columns: Name / Kind / User@Host / Status / Version / Enabled (two-way CheckBox). Get sort free, get resize free.
- `ProcessTable` — columns: PID / Name / CPU% / RSS. Get "click CPU% to sort descending" free.

Optional: `tv:TableView.AlternateRowForeground` / `AlternateRowBackground` for zebra stripes. The default ListView chrome is already fine, so this is a nice-to-have.

Source: [WinUI.TableView repo](https://github.com/w-ahmad/WinUI.TableView), [NuGet](https://www.nuget.org/packages/WinUI.TableView/).

### 4. Mica + custom TitleBar on the main windows

Two changes, both one-liners:

**Mica backdrop** — makes the window chrome pick up the desktop wallpaper in a blurred, tinted way. This is the single biggest "feels Windows 11" lever.

```xml
<Window.SystemBackdrop>
    <MicaBackdrop Kind="Base"/>
</Window.SystemBackdrop>
```

`Kind="Base"` for most windows; `Kind="BaseAlt"` for side-pane panels within the same window. Guard the assignment with `if (MicaController.IsSupported())` and fall back to `DesktopAcrylicBackdrop` on older Win10.

**Custom `TitleBar` control** — new in Windows App SDK 1.6 (`Microsoft.UI.Xaml.Controls.TitleBar`). Replaces the default OS titlebar and lets us show an app icon + subtitle.

```xml
<TitleBar x:Name="AppTitleBar" Title="Towertail" Subtitle="Preferences">
    <TitleBar.IconSource>
        <FontIconSource Glyph="&#xEEA1;" FontFamily="{ThemeResource SymbolThemeFontFamily}"/>
    </TitleBar.IconSource>
</TitleBar>
```

```csharp
ExtendsContentIntoTitleBar = true;
SetTitleBar(AppTitleBar);
```

Apply to:
- `PreferencesWindow` — Mica Base, TitleBar with "Preferences" subtitle.
- `FullViewWindow` — Mica Base, TitleBar with the host display name as subtitle.
- `TrayPopoverWindow` — **special case**. It's borderless by design. Mica on a borderless floating rectangle looks off. Two choices:
  - Stay with the current opaque background; don't apply any backdrop.
  - Switch to `DesktopAcrylicBackdrop` for the popover specifically, plus a 8px rounded `Border` with a hairline `SurfaceStrokeColorDefaultBrush` stroke. This is the Mac popover's shape.

Caption buttons: default is usually fine once Mica is on. If we want transparent buttons:

```csharp
AppWindow.TitleBar.ButtonBackgroundColor = Colors.Transparent;
AppWindow.TitleBar.ButtonInactiveBackgroundColor = Colors.Transparent;
```

Source: [System backdrops](https://learn.microsoft.com/en-us/windows/apps/develop/ui/system-backdrops), [Title bar control](https://learn.microsoft.com/en-us/windows/apps/develop/ui/controls/title-bar).

### 5. Theme brushes instead of hardcoded colors

`Design/Theme.xaml` currently:

```xml
<SolidColorBrush x:Key="NominalBrush"  Color="#FF34C759"/>
<SolidColorBrush x:Key="WarnBrush"     Color="#FFFF9500"/>
<SolidColorBrush x:Key="CriticalBrush" Color="#FFFF3B30"/>
```

These are Apple's SF colors verbatim — they look wrong against Windows' accent and don't dark-mode-adapt. Replace with the Fluent system fills:

```xml
<SolidColorBrush x:Key="NominalBrush"  Color="{ThemeResource SystemFillColorSuccess}"/>
<SolidColorBrush x:Key="WarnBrush"     Color="{ThemeResource SystemFillColorCaution}"/>
<SolidColorBrush x:Key="CriticalBrush" Color="{ThemeResource SystemFillColorCritical}"/>
```

Catalog of theme brushes worth standardizing on (never hardcode):

| Purpose | Brush key |
|---|---|
| Primary text | `TextFillColorPrimaryBrush` |
| Secondary text ("last seen", subtitles) | `TextFillColorSecondaryBrush` |
| Tertiary / disabled | `TextFillColorTertiaryBrush`, `TextFillColorDisabledBrush` |
| Card fill | `CardBackgroundFillColorDefaultBrush` |
| Card border | `CardStrokeColorDefaultBrush` |
| Layered surface (already used) | `LayerFillColorDefaultBrush` |
| Hairline divider | `DividerStrokeColorDefaultBrush` |
| Accent | `AccentFillColorDefaultBrush`, `SystemAccentColorLight2Brush` |
| Status: ok/warn/crit | `SystemFillColorSuccessBrush` / `Caution` / `Critical` |

Source: [XAML theme resources](https://learn.microsoft.com/en-us/windows/apps/develop/platform/xaml/xaml-theme-resources).

### 6. Typography ramp

Delete custom sizes in `Design/Typography.xaml`. Use the built-in ramp:

| Style key | Weight | Size | Use for |
|---|---|---|---|
| `CaptionTextBlockStyle` | Regular | 12 | metric units, "last seen" |
| `BodyTextBlockStyle` | Regular | 14 | default body copy |
| `BodyStrongTextBlockStyle` | Semibold | 14 | card titles, settings row headers |
| `BodyLargeTextBlockStyle` | Regular | 18 | — |
| `SubtitleTextBlockStyle` | Semibold | 20 | full-view tab titles, section headers |
| `TitleTextBlockStyle` | Semibold | 28 | window titles if we build our own |
| `TitleLargeTextBlockStyle` | Semibold | 40 | — |

`Segoe UI Variable` is applied automatically by these styles on Win11. Don't set `FontFamily` by hand.

Source: [Typography in Windows apps](https://learn.microsoft.com/en-us/windows/apps/design/signature-experiences/typography).

## Smaller polish items

### Segoe Fluent Icons — relevant codepoints

Use `FontFamily="{ThemeResource SymbolThemeFontFamily}"` (falls back to `Segoe MDL2 Assets` on Win10).

| Concept | Glyph |
|---|---|
| CPU | `&#xEEA1;` |
| Memory | `&#xEEA0;` |
| Disk | `&#xEDA2;` |
| Network / tower | `&#xEC05;` |
| Ethernet | `&#xE839;` |
| Download (RX) | `&#xE896;` |
| Upload (TX) | `&#xE898;` |
| Server / devices | `&#xE772;` |
| Laptop | `&#xE7F7;` |
| Globe (remote) | `&#xE774;` |
| Dashboard | `&#xE9D9;` |
| Terminal | `&#xE756;` |
| Settings | `&#xE713;` |
| Refresh | `&#xE72C;` |
| History | `&#xE81C;` |
| Power / online | `&#xE7E8;` |
| OK / check | `&#xE73E;` |
| Warning | `&#xE7BA;` |
| Error / critical | `&#xE783;` |
| Info | `&#xE946;` |
| Status circle | `&#xEA81;` |
| Add / Edit / Delete / Save | `&#xE710;` / `&#xE70F;` / `&#xE74D;` / `&#xE74E;` |
| Close (X) | `&#xE8BB;` |

Source: [Segoe Fluent Icons font](https://learn.microsoft.com/en-us/windows/apps/design/style/segoe-fluent-icons-font).

### `CommandBar` for pane toolbars

Replace plain-button toolbars (Add / Edit / Remove / Bulk import in ServersPane) with:

```xml
<CommandBar Background="Transparent" IsOpen="False" DefaultLabelPosition="Right">
    <AppBarButton Icon="Add" Label="Add" Click="OnAddClick"/>
    <AppBarButton Icon="Edit" Label="Edit" Click="OnEditClick"/>
    <AppBarButton Icon="Delete" Label="Remove" Click="OnRemoveClick"/>
    <AppBarSeparator/>
    <AppBarButton Label="Bulk import…" Click="OnBulkClick">
        <AppBarButton.Icon><FontIcon Glyph="&#xE8E5;"/></AppBarButton.Icon>
    </AppBarButton>
</CommandBar>
```

Gets keyboard navigation, proper hit targets, reveal highlight, and overflow menu for free.

### Server-card chrome

The compact card in the popover is already decent but can tighten up:
- Use `CardBackgroundFillColorDefaultBrush` instead of `LayerFillColorAlt`.
- Add hairline border `CardStrokeColorDefaultBrush`.
- Tap ripple: wrap the root `Grid` in a `Button` with `Background="Transparent"` and `BorderThickness=0`, or add a pointer-hover state that shifts the background to `CardBackgroundFillColorSecondaryBrush`.

### FullView content padding

`<Grid x:Name="Host" Margin="0"/>` → `Margin="24,12,24,24"`. Fixes the chart hugging the window edges.

### NavigationView density

Default `NavigationView` in vertical mode is wide. For the Preferences sidebar use:

```xml
<NavigationView IsBackButtonVisible="Collapsed"
                IsPaneToggleButtonVisible="False"
                IsSettingsVisible="False"
                OpenPaneLength="200"
                ExpandedModeThresholdWidth="0"/>
```

And give each `NavigationViewItem` an `Icon`.

### Subtle shadow on elevated surfaces

```xml
<Border Translation="0,0,32">
    <Border.Shadow><ThemeShadow/></Border.Shadow>
    ...
</Border>
```

Good for the tray popover, not needed on regular windows.

## What we're explicitly not doing

- **Reskinning to look literally like macOS.** No traffic-light buttons, no SF Symbols, no Helvetica substitutes. Goal is "Windows 11 premium", not "Mac port".
- **WPF or MAUI.** Stack stays WinUI 3. Not migrating.
- **Custom-drawn controls.** Every upgrade here is a built-in control or a well-maintained NuGet. If we need a custom renderer for something specific (e.g., threshold bars), we can — but not as part of this pass.
- **Animation polish.** WinUI's default connected-animations are fine for now. Revisit after the static look is right.
- **Replacing the compact-card sparkline with LiveCharts.** Current `SKXamlCanvas` path is 40 lines and renders at 60fps; LiveCharts adds overhead for no visible gain at that size.

## Suggested implementation order

Ranked by visual-impact-per-hour.

1. **Chart rewrite** (Section 1). 30 min. Kills the worst-looking screen we have (Unix timestamps).
2. **Theme brushes + typography cleanup** (Sections 5, 6). 15 min. One-shot find-and-replace in `Design/*.xaml`. Unblocks dark mode.
3. **Mica + custom TitleBar on Preferences + FullView** (Section 4). 30 min.
4. **`SettingsCard` / `SettingsExpander` in all preference panes** (Section 2). 60 min. Biggest win on the Preferences side.
5. **`WinUI.TableView` for `ServersPane` + `ProcessTable`** (Section 3). 45 min.
6. **CommandBar toolbar + Fluent icons everywhere** (smaller polish). 30 min.

Total: ~3h30m of work for an entirely different-feeling app. None of these touch backend code, `RealCollector`, `MetricSeries`, or any wire contract — it's all UI layer.

## Packages to add

```xml
<PackageReference Include="CommunityToolkit.WinUI.Controls.SettingsControls" Version="8.2.251219" />
<PackageReference Include="WinUI.TableView" Version="1.3.4" />
```

Everything else (LiveCharts rc5, H.NotifyIcon, CommunityToolkit.Mvvm, Serilog) is already in the csproj. No version collisions expected — the SettingsControls package is from the same monorepo as `CommunityToolkit.Mvvm 8.3.2` we already consume.

## Reference links

- [WinUI Gallery (copy-paste patterns for every built-in control)](https://github.com/microsoft/WinUI-Gallery)
- [Fluent 2 design guidelines — Mica](https://learn.microsoft.com/en-us/windows/apps/design/style/mica)
- [Fluent 2 design guidelines — Typography](https://learn.microsoft.com/en-us/windows/apps/design/signature-experiences/typography)
- [LiveCharts2 WinUI docs](https://livecharts.dev/docs/winui/2.0.0-rc4/gallery)
- [Windows Community Toolkit — SettingsControls](https://learn.microsoft.com/en-us/dotnet/communitytoolkit/windows/settingscontrols/)
- [WinUI.TableView GitHub](https://github.com/w-ahmad/WinUI.TableView)
- [Segoe Fluent Icons full catalog](https://learn.microsoft.com/en-us/windows/apps/design/style/segoe-fluent-icons-font)
