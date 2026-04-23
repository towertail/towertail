Towertail Windows Port — Implementation Plan

1. Context

Why now. Phase 1 (app/mac, ~74 Swift files, ~12k SLOC) is feature-complete on macOS: menu-bar popover with live CPU/MEM/DISK/NET sparklines, full-view charts
with zoom/pan/hover, per-host process tables hydrated from a 2h SQLite ring, Tailscale bulk-import, per-host threshold overrides, toast notifications, SSH
sampler bootstrap via /usr/bin/ssh. docs/PLAN.md Phase 2 reads: "Ship the same product on Windows. Still local-only. Shared wire format with the sampler; UI is
 a from-scratch native build." app/windows/README.md is the only thing currently in that folder.

What we're building. A native Windows tray app at /Users/fritz/Projects/towertail/app/windows/ that is functionally equivalent to the Mac client: same sampler
binaries, same JSON wire format (schema v=1 from docs/sampler.md §4), same SQLite history schema (4 tables from HistoryStore.swift), and a settings.json format
 that round-trips with the Mac client so a user can copy the file between OSes. WinUI 3 + .NET 9 + C# 13, packaged both as unpackaged portable ZIP and MSIX
(Store + sideload). x64 primary, ARM64 secondary (Surface Pro X / Copilot+ PCs).

User-chosen scope (this session). WinUI 3 toolkit; the Windows tray app must include a popover-like flyout with live charts (parity with Mac). A windows-amd64
sampler triple is added so Windows hosts can also be monitored locally (expansion from the current Unix-only sampler).

What carries over, verbatim. The sampler JSON schema; the SQLite table layout and 2h retention; the SettingsExport envelope JSON shape; the 2s local / 10s SSH
cadences; threshold defaults; the <goos>-<goarch> triple convention.

---
2. Tech Stack

┌────────────────┬─────────────────────────────────────────────────────┬────────────┬────────────────────────────────────────────────────────────────────────┐
│    Concern     │                       Choice                        │  Version   │                                  Why                                   │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ UI toolkit     │ WinUI 3 (Windows App SDK)                           │ 1.6+       │ User choice; native Fluent UI; modern XAML                             │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Runtime        │ .NET 9                                              │ 9.0        │ Current LTS-adjacent; AOT-ready; C# 13 language                        │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Tray icon      │ H.NotifyIcon.WinUI                                  │ 2.4.x      │ De-facto standard; TaskbarIcon with TrayPopup, ContextFlyout, balloon  │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Charts         │ LiveCharts2 (LiveChartsCore.SkiaSharpView.WinUI)    │ 2.0.0-rc5+ │ Skia-backed; real-time; already proven on WinUI 3                      │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Tiny           │ SkiaSharp.Views.WinUI (SKXamlCanvas)                │ 2.88+      │ Bespoke paint for 60×20 sparklines — lower per-cell cost than          │
│ sparklines     │                                                     │            │ LiveCharts                                                             │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ SQLite         │ Microsoft.Data.Sqlite.Core +                        │ 9.0+       │ ADO.NET provider; already in-process for packaged apps                 │
│                │ SQLitePCLRaw.bundle_e_sqlite3                       │            │                                                                        │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Toasts         │ Microsoft.Windows.AppNotifications                  │ (in App    │ AppNotificationBuilder — Win11 action center integration               │
│                │                                                     │ SDK)       │                                                                        │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│                │ Bundled ssh.exe (Windows OpenSSH;                   │            │                                                                        │
│ SSH            │ C:\Windows\System32\OpenSSH\ssh.exe on Win10 1809+) │ system     │ Parity with Mac approach; uses ~/.ssh/config, ssh-agent, known_hosts   │
│                │  via System.Diagnostics.Process                     │            │                                                                        │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ SCP/SFTP       │                                                     │            │                                                                        │
│ (sampler       │ Bundled scp.exe (same OpenSSH)                      │ system     │ Same user creds as SSH                                                 │
│ bootstrap)     │                                                     │            │                                                                        │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ JSON           │ System.Text.Json                                    │ 9.0        │ Source-gen; matches Swift Codable semantics                            │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ MVVM           │ CommunityToolkit.Mvvm                               │ 8.3+       │ [ObservableProperty], [RelayCommand] — cleanest parity with            │
│                │                                                     │            │ @Observable                                                            │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Messaging      │ CommunityToolkit.Mvvm.Messaging                     │ 8.3+       │ Replaces NotificationCenter.default broadcast of sample events         │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Launch at      │ Windows.ApplicationModel.StartupTask (packaged) or  │ system     │ Follows MS guidance                                                    │
│ login          │ HKCU\...\Run (unpackaged)                           │            │                                                                        │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Packaging      │ MSIX + unpackaged portable                          │ —          │ Store + sideload, unsigned portable ZIP for power users                │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Logging        │ Microsoft.Extensions.Logging + file sink            │ 9.x / 6.x  │ Structured logs to %LOCALAPPDATA%\Towertail\logs                       │
│                │ (Serilog.Sinks.File)                                │            │                                                                        │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ HTTP           │ HttpClient + NamedPipeClientStream                  │ BCL        │ Tailscale on Windows exposes LocalAPI over named pipe                  │
│ (Tailscale)    │                                                     │            │ \\.\pipe\ProtectedPrefix\Administrators\Tailscale\tailscaled           │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Tests          │ xUnit v3 + AwesomeAssertions + NSubstitute          │ 3.x / 8.x  │ xUnit v3 is async-first; AwesomeAssertions is the Apache-2.0 drop-in   │
│                │                                                     │ / 5.x      │ replacement for FluentAssertions (v8 relicensed to paid Xceed in Jan   │
│                │                                                     │            │ 2025); NSubstitute is the clean post-SponsorLink mock library          │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ In-process     │ System.Net.HttpListener + custom WS upgrade         │ BCL        │ Mirrors Mac's LocalTestServer.swift — real HTTP/WS over 127.0.0.1:0    │
│ test server    │                                                     │            │ for RemoteBackend unit tests, no Docker                                │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ E2E stack      │ docker-compose (server/docker/docker-compose.test   │ —          │ Boots real towertail-server + ClickHouse; shared with Mac integration  │
│                │ .yaml, shared with Mac)                             │            │ tests via fixed loopback ports (18080 server, 18123/19000 CH)          │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ UI tests       │ FlaUI (UIA3, in-process)                            │ 5.0+       │ WinAppDriver is effectively abandoned (last release 2020); Appium      │
│                │                                                     │            │ Windows Driver is maintained but heavier. FlaUI has no external server │
│                │                                                     │            │ and handles WinUI 3 out of the box — optional smoke tests only         │
├────────────────┼─────────────────────────────────────────────────────┼────────────┼────────────────────────────────────────────────────────────────────────┤
│ Project gen    │ Plain .sln + .csproj (no XcodeGen equivalent        │ —          │ —                                                                      │
│                │ needed)                                             │            │                                                                        │
└────────────────┴─────────────────────────────────────────────────────┴────────────┴────────────────────────────────────────────────────────────────────────┘

---
3. Directory Layout

Mirrors the Mac Sources/ tree file-for-file where sensible:

app/windows/
├── Towertail.sln
├── Towertail.WinUI/                       # main app project
│   ├── Towertail.WinUI.csproj
│   ├── Package.appxmanifest               # MSIX manifest
│   ├── app.manifest                       # DPI / UAC
│   ├── App.xaml / App.xaml.cs             # entry, single-instance, DI root
│   ├── Assets/
│   │   ├── AppIcon.ico, Square*.png, *.svg
│   │   └── tint/ (nominal, warn, critical)
│   ├── App/
│   │   ├── TowertailApp.cs                # ~ TowertailApp.swift
│   │   └── AppEnvironment.cs              # DI facade over Backend
│   ├── Backend/
│   │   ├── IBackend.cs, LocalBackend.cs, RemoteBackend.cs
│   │   ├── RemoteClient.cs                 # REST + WebSocket client
│   │   └── RemoteSettings.cs               # wire DTOs (RemoteNode, RemoteServerSettings, RemoteClientSettings)
│   ├── MenuBar/                           # "tray" surface
│   │   ├── TrayIconHost.xaml/.cs          # H.NotifyIcon TaskbarIcon
│   │   ├── TrayPopoverWindow.xaml/.cs     # borderless, topmost, blur-closes
│   │   ├── PopoverRoot.xaml/.cs           # header + filter + list
│   │   ├── PopoverHeader.xaml/.cs
│   │   └── ServerFilter.cs
│   ├── Cards/
│   │   ├── ServerCardView.xaml/.cs
│   │   ├── ServerCardViewModel.cs
│   │   ├── MetricCell.xaml/.cs
│   │   ├── SparklineCanvas.cs             # SKXamlCanvas renderer
│   │   ├── DiskBars.xaml/.cs
│   │   └── CardChrome.xaml/.cs
│   ├── FullView/
│   │   ├── FullViewWindow.xaml/.cs
│   │   ├── FullViewModel.cs
│   │   ├── FullViewContext.cs
│   │   ├── MetricChart.xaml/.cs           # LiveCharts2 CartesianChart
│   │   └── ProcessTable.xaml/.cs          # virtualized DataGrid
│   ├── Collectors/
│   │   ├── ISamplerInvoker.cs
│   │   ├── LocalSamplerInvoker.cs         # launches samplers/windows-*
│   │   ├── SshSamplerInvoker.cs           # shells to ssh.exe
│   │   ├── ProcessRunner.cs
│   │   ├── SamplerManifest.cs
│   │   ├── SshBootstrap.cs                # detect triple + scp deploy
│   │   ├── ICollector.cs
│   │   ├── RealCollector.cs
│   │   ├── MockCollector.cs
│   │   └── SamplerUpdateCoordinator.cs
│   ├── State/
│   │   ├── Sample.cs                      # mirrors docs/sampler.md §4
│   │   ├── Node.cs
│   │   ├── HistoryStore.cs                # 4 SQLite tables, 2h ring
│   │   ├── MetricSeries.cs
│   │   ├── TimeSeriesBuffer.cs
│   │   ├── DiskSeries.cs
│   │   ├── ProcSeries.cs
│   │   ├── ServerViewModel.cs
│   │   ├── ServerStore.cs
│   │   ├── NodeStore.cs
│   │   ├── ClientSettings.cs, ServerSettings.cs
│   │   └── SettingsDiff.cs
│   ├── Preferences/
│   │   ├── PreferencesWindow.xaml/.cs
│   │   ├── GeneralPane.xaml/.cs
│   │   ├── ThresholdsPane.xaml/.cs
│   │   ├── NotificationsPane.xaml/.cs
│   │   ├── ServersPane.xaml/.cs
│   │   ├── ServerEditWindow.xaml/.cs
│   │   ├── LogsPane.xaml/.cs
│   │   ├── ImportSettingsSheet.xaml/.cs
│   │   └── BulkImport/
│   │       ├── BulkImportWizard.xaml/.cs
│   │       ├── SourcePickerPage.*
│   │       ├── TailscalePickerPage.*
│   │       ├── PastePage.*
│   │       ├── ReviewGridPage.*
│   │       └── DeployProgressPage.*
│   ├── System/
│   │   ├── SettingsPersistence.cs         # %APPDATA%\Towertail\settings.json
│   │   ├── SettingsTransfer.cs            # SettingsExport envelope
│   │   ├── ThresholdNotifier.cs           # per-(host,metric) FSM + debounce
│   │   ├── AppNotifier.cs                 # AppNotificationBuilder wrapper
│   │   ├── NotificationTapRouter.cs
│   │   ├── TerminalLauncher.cs            # wt/pwsh/cmd/Alacritty/WezTerm/Tabby
│   │   ├── TailscaleLocalApi.cs           # named-pipe HTTP
│   │   ├── SystemReachabilityMonitor.cs   # NetworkInformation + SessionSwitch
│   │   ├── ActivationPolicyCoordinator.cs # show-in-taskbar toggle for windows
│   │   ├── LaunchAtLogin.cs               # StartupTask or Run key
│   │   └── Logger.cs
│   └── Design/
│       ├── Typography.xaml (ResourceDictionary)
│       ├── ThresholdTint.cs
│       ├── ColorConverters.cs
│       ├── StringFormatters.cs
│       └── Theme.xaml
├── Towertail.Tests/                       # xUnit v3 (unit + in-process integration)
│   ├── Towertail.Tests.csproj             # refs Towertail.WinUI, xUnit, AwesomeAssertions, NSubstitute
│   ├── SamplerInvokerTests.cs
│   ├── SampleDecodingTests.cs             # golden sample-v1.json → Sample round-trip
│   ├── MetricSeriesTests.cs
│   ├── ProcSeriesTests.cs
│   ├── HistoryStoreTests.cs               # temp-file SQLite per test, IAsyncLifetime
│   ├── NodeStoreTests.cs
│   ├── ServerViewModelTests.cs
│   ├── SettingsTransferTests.cs           # cross-OS round-trip (loads TestData/settings.mac.json)
│   ├── LocalBackendTests.cs               # real concrete stores, CRUD, no network
│   ├── RemoteClientTests.cs               # REST + WS via LocalTestHttpServer
│   ├── RemoteBackendTests.cs              # initial load, stream ingest, lifecycle
│   ├── ThresholdNotifierTests.cs          # per-(host,metric) FSM + debounce
│   ├── TailscaleLocalApiTests.cs          # named-pipe parsing w/ fake pipe
│   ├── ProcessRunnerTests.cs              # IProcessRunner fake + arg-shape assertions
│   ├── Helpers/
│   │   ├── LocalTestHttpServer.cs         # mirror of Mac's LocalTestServer.swift
│   │   ├── TestFlags.cs                   # DispatcherQueueShim, SqliteTempFile, WaitFor
│   │   └── GoldenFixtures.cs              # loads TestData/ JSON
│   ├── Integration/                       # requires docker-compose.test.yaml up
│   │   ├── IntegrationConfig.cs           # reads %TEMP%\towertail-integration-test.env
│   │   ├── RemoteBackendIntegrationTests.cs   # real server + ClickHouse + real sampler
│   │   └── SamplerPushIntegrationTests.cs
│   └── TestData/
│       ├── settings.mac.json              # golden Mac settings for round-trip
│       ├── sample-v1.json                 # golden sampler JSON (shared with Go/Swift)
│       └── tailscale-status.json          # golden LocalAPI peer list
├── Towertail.UITests/                     # FlaUI smoke (optional, nightly CI)
│   ├── Towertail.UITests.csproj
│   ├── TrayLaunchSmokeTests.cs            # app starts → tray icon → popover opens
│   └── FullViewSmokeTests.cs              # open full view, switch tabs, close
└── README.md

---
4. Cross-OS Settings Contract

Critical user requirement: settings.json must be importable between Mac and Windows.

On-disk paths

- macOS: ~/Library/Application Support/Towertail/settings.json
- Windows: %APPDATA%\Towertail\settings.json (i.e. C:\Users\<u>\AppData\Roaming\Towertail\settings.json)

Shared schema (PersistedSettings)

All fields use camelCase JSON keys matching the existing Mac Codable output. Node IDs are UUIDs (platform-neutral). Polling intervals, thresholds, and debounce
 seconds are platform-neutral floats/ints.

{
  "schemaVersion": 1,
  "nodes": [ { "id": "<uuid>", "displayName": "db-primary", "kind": "ssh",
               "sshUser": "fritz", "sshHost": "db.tail1234.ts.net",
               "tags": ["prod"], "enabled": true,
               "iconOnWarn": true, "iconOnCritical": true,
               "notifyOnWarn": true, "notifyOnCritical": true,
               "customThresholds": null, "snoozedUntil": null,
               "favorite": false } ],
  "thresholds": { "cpuWarn": 0.75, "cpuCritical": 0.9,
                  "memWarn": 0.75, "memCritical": 0.9,
                  "diskWarn": 0.75, "diskCritical": 0.9 },
  "localPollingIntervalSeconds": 2,
  "sshPollingIntervalSeconds": 10,
  "cardDensity": "a",
  "notificationsEnabled": false,
  "notifyWarn": true, "notifyCritical": true,
  "notifyDebounceSeconds": 60,
  "autoUpdateSamplersEnabled": false,
  "postWakeGraceSeconds": 15,

  "platform": {
    "darwin": { "launchAtLogin": false, "defaultTerminalApp": "Terminal" },
    "windows": { "launchAtStartup": false, "defaultTerminalApp": "WindowsTerminal" }
  }
}

Platform-specific fields

- Put all platform-only fields under a top-level "platform" object, keyed by OS (darwin / windows / linux). Each OS reads only its own sub-object and preserves
 the others on save. This is the one schema change required on the Mac side to achieve clean round-tripping — a trivial edit in SettingsPersistence.swift +
SettingsTransfer.swift. Ships in the Mac app v1.1 alongside the Windows port.
- Loader rules: missing "platform" (legacy Mac file) → migrate known Mac flat fields (launchAtLogin, defaultTerminalApp) into platform.darwin; set defaults for
 current OS. Save always writes the normalized form.

SettingsExport envelope (import/export)

Identical JSON shape to Mac's SettingsTransfer.swift:
{ "version": 1, "exportedAt": "<ISO8601>", "appVersion": "<semver>",
  "sourcePlatform": "darwin",
  "general": { … polling / density / grace … },
  "globalThresholds": { … },
  "notifications": { … },
  "nodes": [ … ] }
Import UI shows a pre-selection matrix (general / thresholds / notifications / servers × merge|overwrite) and a warning banner if sourcePlatform differs.

Round-trip test (required)

Towertail.Tests/SettingsTransferTests.cs includes a golden settings.mac.json copied from the Mac app, round-trips through the Windows loader → saver, then
asserts byte-for-byte equivalence for all non-platform fields and preservation of the platform.darwin sub-object.

---
5. Sampler Changes — windows-amd64 Triple

Scope expansion: current sampler targets Unix only. Adding Windows.

Code changes (sampler/)

- internal/collect/cpu.go — gopsutil/v4/cpu already supports Windows; verify Times() delta math works (it does on Windows via NTDLL).
- internal/collect/mem.go — gopsutil/v4/mem.VirtualMemory() is cross-platform.
- internal/collect/disk.go — disk.Partitions(false) on Windows returns drive letters; disk.IOCounters() works via PDH. Filter pseudo drives (A:, empty CD/DVD).
- internal/collect/net.go — net.IOCounters(false) returns aggregate across adapters on Windows. OK.
- internal/collect/proc.go — process.Processes() works; process.IOCounters() requires PROCESS_QUERY_LIMITED_INFORMATION which the invoker has by default.
Populate read_bytes/write_bytes on Windows too (update docs/sampler.md §4 note that currently says "Linux only").
- internal/collect/host.go — host.Info() cross-platform; add Windows version detection.
- cmd/sampler/main.go — no changes; flags are portable.

Build changes (scripts/build.sampler.sh)

TARGETS=(
  linux-amd64 linux-arm64 linux-armv7
  darwin-amd64 darwin-arm64
  windows-amd64 windows-arm64       # NEW
)
# Per-target extension:
ext=""
[[ "$target" == windows-* ]] && ext=".exe"
# …GOOS=windows GOARCH=amd64 go build -o dist/samplers/$target/towertail-sampler${ext}

Manifest (dist/samplers/manifest.json)

Add two keys: windows-amd64, windows-arm64, same SHA256 format.

Docs (docs/sampler.md)

- Remove "windows-amd64 is not a goal" sentence; replace with a Windows-specific notes paragraph: executable name has .exe extension; default deploy path on
remote Windows is %USERPROFILE%\.towertail\towertail-sampler.exe; bootstrap uses PowerShell via ssh to run $env:USERPROFILE and Test-Path.
- Clarify that procs.items[].read_bytes/write_bytes are now Linux+Windows (macOS still null).

Windows bootstrap (Collectors/SshBootstrap.cs)

- Detection: run ssh <host> "uname -sm || ver" — Unix hosts return uname output; Windows returns the ver fallback. Parse both. Map windows + detected arch →
windows-amd64 / windows-arm64.
- Deploy: use bundled scp.exe, push to %USERPROFILE%/.towertail/towertail-sampler.exe. Use forward slashes (OpenSSH accepts them).
- Verify: ssh host "%USERPROFILE%\.towertail\towertail-sampler.exe --self-check".

---
6. Build & Packaging

Windows build pipeline

scripts/build.windows.ps1           # NEW — on Windows
scripts/build.windows.sh            # NEW — documents the steps; actual build runs on Win/CI

- dotnet build app/windows/Towertail.sln -c Release -p:Platform=x64
- dotnet publish app/windows/Towertail.WinUI -c Release -r win-x64 --self-contained
- MSIX via msbuild /p:GenerateAppxPackageOnBuild=true /p:AppxPackageSigningEnabled=true
- Pre-build target in Towertail.WinUI.csproj copies dist/samplers/** into Assets/samplers/ so the sampler binaries ship inside the package (parity with Mac's
preBuildScript):
<Target Name="CopySamplers" BeforeTargets="Build">
  <ItemGroup><SamplerFiles Include="..\..\dist\samplers\**\*" /></ItemGroup>
  <Copy SourceFiles="@(SamplerFiles)" DestinationFiles="Assets\samplers\%(RecursiveDir)%(Filename)%(Extension)" SkipUnchangedFiles="true" />
</Target>

scripts/build.sh (updated)

Detects uname; on Windows (Git Bash / WSL) delegates to build.windows.ps1. On macOS keeps the existing path. Neither forces the other platform's build.

CI

- GitHub Actions matrix: macos-14 (existing) + windows-latest (new).
- Windows job: install .NET 9 SDK + Windows App SDK, run dotnet test, build MSIX, upload as artifact.
- Sampler builds on Linux (already cross-compiles all triples including Windows).

---
7. Phases

Each phase is scoped to roughly a 1–2 week engineering chunk. Deliverables are verifiable end-to-end before the next phase starts.

Phase A — Scaffold + Windows Sampler + Local Invoker (days 1–4)

Goal. Prove the Windows app can launch a bundled sampler and decode a Sample.
- Create solution, csproj, minimal App.xaml blank window.
- Create Towertail.Tests project (xUnit v3 + AwesomeAssertions + NSubstitute). CI runs `dotnet test` on every push.
- Add windows-amd64 + windows-arm64 to scripts/build.sampler.sh; rebuild manifest.
- Implement Sample.cs (System.Text.Json source-gen) to exactly match docs/sampler.md §4.
- Implement LocalSamplerInvoker.cs — probe Assets/samplers/windows-<arch>/towertail-sampler.exe.
- Implement IProcessRunner seam (ProcessRunner.cs) so invokers are testable without spawning real binaries.
- Commit TestData/sample-v1.json shared with sampler/testdata and Mac's test suite — prevents schema drift.
- Tests: SampleDecodingTests (golden sample-v1.json round-trip, fractional-second ISO8601, v=1 enforcement, unknown-field tolerance), SamplerInvokerTests (IProcessRunner fake, arg-shape assertions for --once / --interval / --self-check), LocalSamplerInvokerTests (triple resolution on x64 + ARM64).
- Exit: `scripts/test.ps1 --unit` green on Windows dev box and on windows-latest CI runner. Manual: launch app, click a button, see decoded Sample printed to debug output.

Phase B — SQLite HistoryStore + Settings + LocalBackend + Cross-OS Transfer (days 5–10)

Goal. Local-only persistence parity with Mac; cross-OS settings round-trip works; LocalBackend fully exercised without network.
- HistoryStore.cs — 4 tables matching Mac schema; writer queue on dedicated single-thread TaskScheduler (no DispatcherQueue dep); 2h + hard-cap trim on each insert. Temp-file DB path (not `:memory:`) so connection pooling works.
- NodeStore.cs, ServerStore.cs, MetricSeries.cs, DiskSeries.cs, ProcSeries.cs — plain observable state, no UI dep.
- SettingsPersistence.cs — records with System.Text.Json source-gen; reads legacy flat Mac files AND new platform.<os> envelope; always saves normalized.
- SettingsTransfer.cs — SettingsExport encode/decode; merge/overwrite engine; handles foreign `platform.*` blocks by preserving them.
- IBackend.cs + LocalBackend.cs — identical surface to Mac's Backend protocol.
- Mac-side change: update SettingsPersistence.swift + SettingsTransfer.swift to emit/accept the new platform envelope while reading legacy flat files. Ship as Mac app v1.1. Extend app/mac/Tests/SettingsTransferTests.swift with the same golden fixture.
- Tests:
  - HistoryStoreTests (insert 10k samples, verify 2h trim, verify concurrent writer doesn't crash).
  - MetricSeriesTests + ProcSeriesTests (decimation + decay math; ported verbatim from Swift).
  - NodeStoreTests (CRUD, favorite/snooze, persists across reload).
  - SettingsTransferTests (cross-OS): loads TestData/settings.mac.json → Windows loader → saver → byte-compare all non-`platform` keys + preservation of `platform.darwin`.
  - LocalBackendTests (mirrors Mac's LocalBackendTests.swift): instantiate, node CRUD round-trip, bulk add, updateServerSettings persists to disk and round-trips.
- Exit: `scripts/test.ps1 --unit` green. Manual: copy a Mac settings.json into %APPDATA%\Towertail\, start the app, verify nodes list; save; verify Mac can read back with nodes intact.

Phase B2 — RemoteBackend + RemoteClient + Server Integration (days 11–16)

Goal. RemoteBackend parity with Mac, tested against both an in-process HTTP+WS fake AND the real `towertail-server` via docker-compose.
- RemoteClient.cs — REST client (GET/POST/PUT/DELETE /v1/nodes, PUT /v1/settings) + URLSession-equivalent WebSocket task. Bearer token on every request. Mirror `RemoteClient.RemoteError` shape.
- RemoteBackend.cs — wires REST initial-load + WS fan-out into ServerStore, identical ingest path to LocalBackend.
- RemoteSettings.cs — RemoteNode / RemoteServerSettings / RemoteClientSettings wire DTOs (snake_case via JsonPropertyName).
- Helpers/LocalTestHttpServer.cs — port of Mac's LocalTestServer.swift: real HTTP/1.1 + WebSocket-upgrade server on 127.0.0.1:0, handlers keyed by "METHOD PATH" with prefix match, request capture, `sendToAllWebSockets` for scripted frames. Uses System.Net.HttpListener + a 50-line RFC 6455 upgrade (or `WebSocketHandler` from `System.Net.WebSockets`).
- Tests — in-process (no Docker, runs in `--unit`):
  - RemoteClientTests: list/create/update/delete nodes, settings PUT/GET round-trip, 4xx → RemoteError.http surfacing, wrong-token → 401, bearer-header assertion.
  - RemoteBackendTests: initial load populates NodeStore; WS frame with `{type:"sample", nodeId, sample:…}` lands in ServerViewModel (lastSeen populated, samplerVersion surfaces); stop() tears down WS cleanly; reconnect on WS close.
- Tests — integration (`--remote-integration`, requires docker-compose.test.yaml up):
  - IntegrationConfig.cs reads `%TEMP%\towertail-integration-test.env` (parity with Mac's `/tmp/towertail-integration-test.env`). Skips with xUnit `Skip` when `TOWERTAIL_INTEGRATION != "1"`.
  - RemoteBackendIntegrationTests.cs: waits on `/readyz`; lists nodes with admin token; round-trips `/v1/settings`; enrolls sampler via `POST /v1/sampler/enroll`; spawns the real `towertail-sampler.exe` in push mode and asserts a sample frame arrives on the WS for the enrolled node within 30s (mirrors Mac's RemoteBackendIntegrationTests.swift).
- Exit: `scripts/test.ps1 --unit` green (no Docker needed). `scripts/test.ps1 --remote-integration` green against the shared `server/docker/docker-compose.test.yaml` stack. Manual: point the Windows app at `http://127.0.0.1:18080` with the test bootstrap token; see nodes streaming live.

Phase C — Tray Icon + Popover Window + Static Cards (days 17–21)

Goal. The "living in tray with a popover" experience.
- TrayIconHost using H.NotifyIcon.WinUI TaskbarIcon. Dynamic 16×16 PNG rendered from tint state.
- TrayPopoverWindow — borderless, topmost, WS_EX_TOOLWINDOW, WS_EX_NOACTIVATE. 360×620 fixed. Positioned via Shell_NotifyIconGetRect + GetDpiForWindow.
- Hide on deactivate (WM_ACTIVATE LOWORD=0). Show on single click of tray icon.
- PopoverRoot with filter tabs (all|online|warn|down), 400ms debounced search TextBox, virtualized ItemsRepeater in a ScrollViewer binding to a mock
ObservableCollection<ServerCardViewModel> of 10 dummy hosts.
- ServerCardView renders name + 4 static metric cells.
- Exit: Run app → taskbar icon appears → click → popover opens under icon → shows 10 dummy cards → click elsewhere → popover hides. No memory leak after 100
open/close cycles.

Phase D — Live Metrics + Sparklines (days 22–27)

Goal. Real live data end-to-end.
- ServerViewModel — CPU% from cumulative ms delta; net Mbps from cumulative byte delta; per-mount disk %; per-device disk I/O.
- ServerStore — ingest → fan-out to VMs → notify ThresholdNotifier → enqueue HistoryStore write.
- RealCollector — per-node pacer Task, respects SystemReachabilityMonitor.ShouldPoll.
- SystemReachabilityMonitor — NetworkInformation.NetworkStatusChanged + SystemEvents.PowerModeChanged (sleep/wake) + SystemEvents.SessionSwitch. Post-wake
grace window suppresses notifications for postWakeGraceSeconds.
- SparklineCanvas — SKXamlCanvas, paints only on MetricSeries change notification (not on a frame timer). Threshold tint overlays.
- DiskBars — stacked per-mount bars.
- Exit: Add a localhost node → see live CPU/MEM/DISK/NET sparklines updating every 2s. CPU < 1% idle, memory < 80 MB with 10 hosts.

Phase E — Full Detail Window (days 28–34)

Goal. Full-view parity: charts, zoom, hover, process table.
- FullViewWindow — separate Window per host (multi-instance). Tabs: CPU / MEM / DISK / NET / PROCS.
- MetricChart — LiveCharts2 CartesianChart with LineSeries. Multi-series (per-core CPU, load1/5/15, rx+tx). Threshold bands via RectangularSection. Hover
cross-hair via PointerMoved. Drag-to-zoom via SectionsPaint.
- Lazy proc hydration. On first open of full view for a host, ServerStore.EnsureProcsHydratedAsync(nodeId) queries HistoryStore.proc_snapshots into ProcSeries.
 Before that, the PROCS tab shows a spinner.
- ProcessTable — virtualized ListView with ItemsStackPanel, sortable columns. Root vs user badge.
- FullViewModel — zoom stack, pause mode, hover timestamp.
- Exit: Open full view, switch tabs, drag-select to zoom, hover to see process table at that timestamp.

Phase F — Preferences + Toast Notifications (days 35–40)

Goal. Full preferences window and threshold alerts.
- PreferencesWindow with NavigationView: General, Thresholds, Notifications, Servers, Logs, About.
- ServersPane — DataGrid of nodes, add/edit/remove/bulk-import buttons.
- ServerEditWindow — per-node config (all fields from Node).
- ThresholdNotifier — per-(hostId, metric) FSM (nominal↔warn↔critical) identical to Mac; debounce 60s; snooze / reachability gates.
- AppNotifier — wraps AppNotificationBuilder with title/body + Launch argument (host=<id>&metric=cpu).
- NotificationTapRouter — handles App.OnActivated with notification args → opens full-view window for that host.
- Per-metric icon/notify toggles per node; global notifications enable.
- Exit: Trigger a CPU spike on a test host → toast appears → click → full-view opens on CPU tab.

Phase G — Integrations (days 41–46)

Goal. Tailscale bulk import, terminal launcher, launch-at-startup, sampler auto-update.
- TailscaleLocalApi — connect to named pipe \\.\pipe\ProtectedPrefix\Administrators\Tailscale\tailscaled, GET /localapi/v0/status, parse peers. Fallback: read
%ProgramData%\Tailscale\tailscaled.state.
- BulkImport/ wizard — identical flow to Mac: SourcePicker → TailscalePicker/Paste → ReviewGrid → DeployProgress.
- TerminalLauncher — detect installed terminals in order: Windows Terminal (wt.exe), PowerShell 7 (pwsh), cmd, Alacritty, WezTerm, Tabby. Launch wt.exe ssh
user@host (WT supports direct command forwarding) or equivalent.
- LaunchAtLogin — packaged: StartupTask.GetAsync("TowertailAutoStart").RequestEnableAsync(); unpackaged: HKCU\Software\Microsoft\Windows\CurrentVersion\Run.
- SamplerUpdateCoordinator — SHA compare + scp push for stale remote binaries. Handles Windows remote hosts via %USERPROFILE%\.towertail\...exe.
- Exit: Bulk-add 5 nodes from Tailscale; right-click card → Open Terminal launches Windows Terminal into SSH session; enable launch-at-startup; restart
Windows; app tray appears.

Phase H — UI Smoke + Packaging + Polish (days 47–53)

Goal. Shippable 0.1.
- Confirm every Mac test file has a Windows equivalent (see §10 coverage matrix). Any gaps are blocking.
- FlaUI smoke suite (Towertail.UITests/) — optional but recommended:
  - TrayLaunchSmokeTests: launch unpackaged build → wait for tray icon (UIA query on notification area) → invoke via keyboard/UIA → assert popover window appears → close.
  - FullViewSmokeTests: open popover → click a card → FullViewWindow appears → switch all 5 tabs → close.
  - Uses a `--test-pin-popover` command-line flag baked into Debug builds so the borderless popover doesn't dismiss during UIA inspection.
- MSIX packaging: signing with self-signed cert for dev; document Store submission path.
- Portable ZIP build artifact (unpackaged; easier for dev + CI).
- Installer icons, tile assets, file associations (optional: .towertail-export for settings files).
- Performance sweep: confirm < 3s cold start, < 80 MB with 10 hosts × 7 days.
- Accessibility pass: tab order, screen-reader labels on cards.
- Exit: Unsigned MSIX installs on a clean Windows 11 VM; `scripts/test.ps1 --all` green on Windows dev box; CI matrix (unit + remote-integration) green on windows-latest. FlaUI smoke green on the dev box (CI runs nightly only, see §10).

---
8. Lazy Loading & Efficiency Strategy

- Virtualized card list. ItemsRepeater inside ScrollViewer with StackLayout and ElementFactory recycling — only visible cards are realized, critical when the
user has 50+ hosts.
- Lazy proc hydration. ProcSeries is populated only when full view opens (EnsureProcsHydratedAsync(nodeId)), backed by HistoryStore.proc_snapshots query.
Compact popover never loads procs. Matches Mac's ServerStore.ensureProcsHydrated.
- Off-UI-thread writes. HistoryStore uses a single-thread TaskScheduler (equivalent to Mac's dispatch queue). Channel<HistoryWrite> for backpressure-safe
enqueue from any thread.
- Sparkline invalidation only on change. SKXamlCanvas.Invalidate() is called from the MetricSeries CollectionChanged handler, not on a timer. Idle cost ≈ 0.
Paint cost ~100 µs per 60×20 sparkline.
- LiveCharts2 in full view only. LiveCharts has higher per-chart overhead; using Skia directly for sparklines keeps the popover cheap.
- Warm-start popover. TrayPopoverWindow is created once at app start and AppWindow.Hide()/.Show() toggled — avoids per-open reflow cost.
- Single DispatcherQueue. All @MainActor-equivalent state mutations marshaled via DispatcherQueue.GetForCurrentThread(). Strict-concurrency parity.
- Source-gen JSON. System.Text.Json source-gen JsonSerializerContext eliminates reflection cost on every sample decode.
- Per-node pacer backoff. On SSH failure, exponential backoff 10s → 60s to avoid hammering dead hosts.
- Gated rendering during sleep. SystemReachabilityMonitor suspends polling + notifications during PowerModeChanged(Suspend).
- AOT-ready. .csproj compatible with PublishAot=true; fall back to ReadyToRun if AOT is too painful with LiveCharts2.
- Cold-start target: < 3s to first interactive popover; measured via ETW start event.

---
9. Cross-Platform Parity Matrix

┌────────────────────┬──────────────────────────────────┬────────────────────────────────────────────────────────────────┬──────────────────────────────────┐
│      Feature       │          Mac (app/mac)           │                     Windows (app/windows)                      │              Notes               │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Menu-bar/tray icon │ MenuBarExtra + custom Canvas     │ H.NotifyIcon.WinUI + 16×16 rendered PNG                        │ —                                │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Popover            │ MenuBarExtra's native popover    │ Custom borderless topmost Window, Shell_NotifyIconGetRect      │ WinUI has no native popover      │
│                    │                                  │ positioning                                                    │                                  │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Sparklines         │ Canvas (SwiftUI)                 │ SKXamlCanvas (SkiaSharp)                                       │ Parity at paint-cost level       │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Full charts        │ SwiftUI Canvas bespoke           │ LiveCharts2 (Skia)                                             │ Accept slightly different        │
│                    │                                  │                                                                │ visuals                          │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Process table      │ SwiftUI Table                    │ DataGrid (CommunityToolkit)                                    │ Both virtualize                  │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ SQLite             │ Raw C API                        │ Microsoft.Data.Sqlite                                          │ Same schema                      │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Settings JSON      │ Codable at ~/Library/...         │ System.Text.Json at %APPDATA%\...                              │ Same schema w/ platform.<os>     │
│                    │                                  │                                                                │ envelope                         │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Toast              │ UNUserNotification               │ AppNotificationBuilder                                         │ Both tap-routable                │
│ notifications      │                                  │                                                                │                                  │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ SSH                │ shell to /usr/bin/ssh            │ shell to C:\Windows\System32\OpenSSH\ssh.exe                   │ Bundled since Win10 1809         │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ SCP                │ /usr/bin/scp                     │ bundled scp.exe                                                │ —                                │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Tailscale LocalAPI │ sameuserproof-<port>-<token>     │ named pipe \\.\pipe\...\tailscaled                             │ Different transport, same JSON   │
│                    │ file                             │                                                                │                                  │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Terminal launcher  │ Terminal/iTerm/Ghostty/…         │ Windows Terminal / pwsh / Alacritty / WezTerm / Tabby          │ —                                │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Launch at login    │ SMAppService                     │ StartupTask or Run key                                         │ —                                │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Sleep/wake gate    │ NSWorkspace notifications        │ SystemEvents.PowerModeChanged                                  │ Post-wake grace identical        │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Network            │ NWPathMonitor                    │ NetworkInformation                                             │ —                                │
│ reachability       │                                  │                                                                │                                  │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Local sampler      │ darwin-arm64/amd64 bundle        │ windows-amd64/arm64 bundle (NEW)                               │ This PR adds the triples         │
├────────────────────┼──────────────────────────────────┼────────────────────────────────────────────────────────────────┼──────────────────────────────────┤
│ Dock/taskbar       │ ActivationPolicyCoordinator      │ show-in-taskbar toggle on secondary windows                    │ —                                │
│ toggle             │                                  │                                                                │                                  │
└────────────────────┴──────────────────────────────────┴────────────────────────────────────────────────────────────────┴──────────────────────────────────┘

---
10. Verification / Test Plan

Strategy. A 3-tier pyramid, mirroring the Mac side (scripts/tests.sh, app/mac/Tests/) and sharing the same Docker stack (server/docker/docker-compose.test.yaml) so the clients cannot drift from the server contract.

┌─────┬───────────────────────────┬────────────────────────────────────┬────────────────────────────┬─────────────────────────────────────┐
│ Tier│          Runs              │           What it covers           │         Deps on host       │       CI invocation                  │
├─────┼───────────────────────────┼────────────────────────────────────┼────────────────────────────┼─────────────────────────────────────┤
│ 1   │ every PR, < 60s           │ pure unit + LocalBackend + in-     │ .NET 9 SDK only            │ scripts/test.ps1 --unit             │
│     │                           │ process RemoteClient/RemoteBackend │                            │                                     │
│     │                           │ against LocalTestHttpServer        │                            │                                     │
├─────┼───────────────────────────┼────────────────────────────────────┼────────────────────────────┼─────────────────────────────────────┤
│ 2   │ every PR, < 4min          │ RemoteBackend against REAL server  │ Docker Desktop + Windows   │ scripts/test.ps1 --remote-integration│
│     │                           │ + ClickHouse + real sampler.exe    │ App Runtime                │ (wraps compose up/down)             │
│     │                           │ push mode over WS                  │                            │                                     │
├─────┼───────────────────────────┼────────────────────────────────────┼────────────────────────────┼─────────────────────────────────────┤
│ 3   │ nightly / pre-release     │ FlaUI smoke: tray launch, popover, │ interactive Win11 session  │ scripts/test.ps1 --flaui            │
│     │                           │ full view tab switching            │ (self-hosted runner)       │                                     │
└─────┴───────────────────────────┴────────────────────────────────────┴────────────────────────────┴─────────────────────────────────────┘

Tier 1 — unit + in-process (no Docker, runs on every commit)

Each listed test class is required before the phase that introduces it can be marked complete.

LocalBackend coverage (mirrors app/mac/Tests/LocalBackendTests.swift):
- `LocalBackend_Instantiates_WiresObservableStores` — assert b.Servers, b.Nodes, b.ClientSettings, b.ServerSettings are distinct concrete objects loaded from disk.
- `LocalBackend_NodeCRUD_RoundTrips` — AddNode, UpdateNode, SetNodeEnabled, SetNodeFavorite, SetNodeSnooze, RemoveNode. Uses a scoped %APPDATA% override so tests don't stomp on the dev install.
- `LocalBackend_BulkAddNodes` — 5 nodes added atomically; all removed cleanly.
- `LocalBackend_UpdateServerSettings_Persists` — mutate → UpdateServerSettingsAsync → re-load from disk → assert value round-trips.

RemoteBackend + RemoteClient coverage (mirrors app/mac/Tests/RemoteBackendTests.swift, uses LocalTestHttpServer):
- `RemoteClient_ListNodes_ReturnsSeededFleet`
- `RemoteClient_SendsBearerTokenHeader` — assert exact Authorization header shape on every request.
- `RemoteClient_CreateNode_RoundTrips` — handler echoes request body, verify decode.
- `RemoteClient_PutSettings_EncodesAndDecodes` — catches silent snake_case drift.
- `RemoteClient_DeleteNode_HitsCorrectPath` — prefix-match handler asserts UUID in path.
- `RemoteClient_4xx_SurfacesAsHttpError` — code + body available on thrown `RemoteException`.
- `RemoteClient_WrongToken_Rejected` — LocalTestHttpServer's bearer check returns 401.
- `RemoteBackend_InitialLoad_PopulatesNodes` — GET /v1/nodes + GET /v1/settings → NodeStore populated.
- `RemoteBackend_WebSocketFrame_DeliversSampleToServerViewModel` — send `{type:"sample",nodeId,sample:…}` frame → assert vm.LastSeen populated, samplerVersion surfaced.
- `RemoteBackend_StopTearsDownWebSocket` — no leaked tasks after Stop().
- `RemoteBackend_WebSocketClose_Reconnects` — server closes, client re-hits /v1/stream within backoff window.

Other Tier 1:
- `SampleDecodingTests` — golden TestData/sample-v1.json round-trip + fractional-second ISO8601 + v=1 enforcement + unknown-field tolerance.
- `SettingsTransferTests` (cross-OS contract) — TestData/settings.mac.json → Windows loader → saver; byte-compare all non-`platform` keys; `platform.darwin` preserved verbatim; `platform.windows` injected with defaults on first save.
- `HistoryStoreTests` — 4-table insert + 2h trim, concurrent writer, temp-file DB (not `:memory:` — pooling breaks that).
- `MetricSeriesTests`, `ProcSeriesTests` — decimation + decay; ported from Swift.
- `NodeStoreTests` — CRUD + persist across reload.
- `ServerViewModelTests` — CPU% from cumulative-ms delta, net Mbps from byte delta, disk %.
- `SamplerInvokerTests` (IProcessRunner fake) + `LocalSamplerInvokerTests` (triple resolution).
- `ThresholdNotifierTests` — per-(host,metric) FSM; 60s debounce; snooze gates.
- `TailscaleLocalApiTests` — named-pipe fake via `NamedPipeServerStream`; parse TestData/tailscale-status.json.
- `ProcessRunnerTests` — assert the exact argv shape passed to ssh.exe / scp.exe.

Tier 2 — real server integration (Docker required, runs every PR on windows-latest with `services: docker`)

- `IntegrationConfig.cs` reads %TEMP%\towertail-integration-test.env, parity with Mac's /tmp flag file. Tests are `Skip`'d when `TOWERTAIL_INTEGRATION != "1"` so the unit-only `dotnet test` path remains green.
- `RemoteBackendIntegrationTests` (mirrors app/mac/Tests/Integration/RemoteBackendIntegrationTests.swift):
  - `AdminToken_CanListNodes` — smoke.
  - `Settings_RoundTripThroughRealServer` — mutate notifyDebounceSeconds → PUT → GET → assert persistence through the real Go server, through ClickHouse-less config path.
  - `SamplerPush_DeliversSampleOverWebSocket` — `POST /v1/sampler/enroll` → spawn the real `dist/samplers/windows-<arch>/towertail-sampler.exe` in push mode → open WS on `/v1/stream` → assert a `type:"sample"` frame arrives for our enrolled node within 30s.
- Same fixed loopback ports as Mac (18080 server, 18123/19000 CH), controlled by `TT_TEST_PORT_SERVER` / `TT_TEST_PORT_CH_HTTP` / `TT_TEST_PORT_CH_NATIVE`.
- Admin token is the literal string `tt-test-admin-token-0000000000000000` set via `TT_AUTH__BOOTSTRAP_TOKEN` (parity with Mac).

Tier 3 — UI smoke (FlaUI, nightly or pre-release)

- Hosted `windows-latest` runners are too flaky for UI automation (session 0 behavior + slow VM). Run FlaUI on a self-hosted runner or locally on the dev box; CI-gate with a `[Trait("Category","FlaUI")]` filter.
- `TrayLaunchSmokeTests`: launch unpackaged build → wait for tray icon via UIA query → click via UIA invoke → assert TrayPopoverWindow present → close.
- `FullViewSmokeTests`: from popover → click first card → FullViewWindow appears → switch all 5 tabs (CPU/MEM/DISK/NET/PROCS) → close without crash.
- Debug builds expose `--test-pin-popover` so UIA inspection doesn't dismiss the borderless topmost popover on focus loss (documented known UIA pain point).

Contract tests (cross-language)

- Sampler schema contract: one golden `testdata/sample-v1.json` (committed to the repo root or `sampler/testdata/`) is decoded by:
  - Go: `sampler/internal/schema/schema_test.go`
  - Swift: `app/mac/Tests/SampleDecodingTests.swift`
  - C#: `Towertail.Tests/SampleDecodingTests.cs`
  Any schema change requires bumping `v` AND updating all three decoders in the same PR. This is enforced by CI: all three jobs must pass.
- Wire contract for server REST/WS shares `server/internal/wire/*.go` types; Mac's Backend/RemoteClient.swift and Windows's Backend/RemoteClient.cs each duplicate those types as DTOs. `SettingsTransferTests` + `RemoteClient_PutSettings_EncodesAndDecodes` catch drift on the two most-trafficked payloads.

CI matrix (GitHub Actions)

```yaml
jobs:
  sampler-tests:        # ubuntu-latest; scripts/tests.sh --sampler
  mac-unit:             # macos-14;     scripts/tests.sh --swift-unit
  mac-integration:      # macos-14;     scripts/tests.sh --all (brings Docker up/down)
  windows-unit:         # windows-latest; scripts/test.ps1 --unit
  windows-integration:  # windows-latest; scripts/test.ps1 --remote-integration
  windows-flaui:        # self-hosted Windows; scripts/test.ps1 --flaui  (nightly cron)
```

- `windows-latest` has Docker Desktop available via `docker/setup-docker-action@v4`. Windows App Runtime must be installed in-step (`Add-AppxPackage WindowsAppRuntimeInstall-x64.msix`) OR the project publishes self-contained (`<WindowsAppSDKSelfContained>true</WindowsAppSDKSelfContained>`). We use self-contained for CI — simpler.
- Tier 2 uses Linux containers; they run on windows-latest via WSL 2 backend (pre-installed). Fixed loopback ports avoid runner-to-runner contamination.

scripts/test.ps1 — Windows test runner

PowerShell 7 script mirroring the UX of scripts/tests.sh. Lives at the repo root, runs under `pwsh` or `powershell` (prefer pwsh for `&&`).

Flags (composable; default: `--unit`):
- `--unit` — `dotnet test app/windows/Towertail.sln --filter "Category!=FlaUI&Category!=Integration"`. Tier 1 only.
- `--remote-integration` — writes `$env:TEMP\towertail-integration-test.env` with `TOWERTAIL_INTEGRATION=1`, endpoint, and admin token. Brings up docker-compose.test.yaml via `docker compose -f server/docker/docker-compose.test.yaml -p towertail-win-test up -d --wait`. Runs `dotnet test --filter Category=Integration`. Tears down on exit.
- `--flaui` — runs `dotnet test Towertail.UITests --filter Category=FlaUI`. Requires interactive session.
- `--go-unit` / `--go-integration` / `--sampler` — delegates to the existing Go test paths (cd into server/ or sampler/ and run `go test`). Kept for parity with tests.sh so a Windows dev can validate the server before hitting the Tier 2 path.
- `--docker-up` / `--docker-down` / `--docker-logs` — same semantics as tests.sh.
- `--all` — --unit + --remote-integration + --go-unit + --go-integration + --sampler; manages docker up/down.
- `--keep-docker` — don't tear down after --all.
- `--verbose` / `-v` — `Set-PSDebug -Trace 1`.
- `--help` / `-h` — prints usage.

Implementation notes:
- Uses `$PSScriptRoot/..` to locate repo root; mirrors tests.sh's `cd "$(dirname "$0")/.."`.
- Exit code = sum of failures across selected suites; prints summary at end.
- Probes `docker compose version` up-front and fails fast with install guidance if absent.
- Passes `TT_TEST_PORT_*` env vars through verbatim so the port-override convention works on both OSes.
- On CI, set `$env:TOWERTAIL_REPO_ROOT` so RemoteBackendIntegrationTests can locate `dist/samplers/windows-<arch>/towertail-sampler.exe` regardless of xUnit's CWD.

Manual end-to-end (run at end of each phase — both LocalBackend and RemoteBackend code paths)

LocalBackend mode:
1. Fresh install → tray icon appears
2. Add localhost node → sparklines populate within 4 s (local polling cadence 2s)
3. Add SSH node (Tailscale peer) → bootstrap deploys sampler → metrics flow within 15 s
4. Full view: open, switch 5 tabs, zoom, pause, hover at past timestamp → PROCS table shows that moment's processes
5. Trigger threshold: toast fires, click → full view opens on correct host+metric
6. Settings: export from Mac, copy JSON to Windows, import → all nodes appear, thresholds match
7. Close popover repeatedly (50×) → memory stable
8. Sleep Windows for 1 min, wake → collector resumes after postWakeGraceSeconds
9. Enable launch-at-startup, restart → tray icon appears automatically

RemoteBackend mode (backend = real `towertail-server` via docker-compose.test.yaml):
10. Configure Windows app to point at `http://127.0.0.1:18080` with the bootstrap admin token.
11. From a second machine (or WSL), enroll a sampler + run it in push mode → node appears in the Windows UI within 10s.
12. Kill the server container → Windows UI surfaces connection-lost state; restart → reconnects and resumes without reload.
13. Use a second Mac client pointed at the same server simultaneously; add a node on the Mac → the Windows UI shows the same node within one WS heartbeat.

---
11. Critical Files to Create/Modify

New (Windows app) — ~70 files in app/windows/Towertail.WinUI/ + ~20 in Towertail.Tests/ + ~2 in Towertail.UITests/

Listed exhaustively in §3.

Modified (existing repo) — cross-cutting

Sampler:
- sampler/internal/collect/disk.go — verify Windows drive enumeration; filter pseudo drives
- sampler/internal/collect/proc.go — populate read_bytes/write_bytes on Windows
- sampler/testdata/sample-v1.json — NEW canonical golden fixture shared across Go/Swift/C# decoders

Build/CI scripts:
- scripts/build.sampler.sh — add windows-amd64, windows-arm64 targets with .exe extension
- scripts/build.sh — OS detection, delegate to build.windows.ps1 on Windows
- scripts/build.windows.ps1 — NEW
- scripts/test.ps1 — NEW Windows test runner (see §10 for spec)
- scripts/tests.sh — add `--cross-platform` note and ensure `--go-unit`/`--go-integration`/`--sampler` paths are invokable from a Windows Git Bash shell too (they are, modulo path separators — verify)
- .github/workflows/ci.yaml — add windows-unit, windows-integration jobs; add windows-flaui cron job targeting self-hosted runner
- dist/samplers/manifest.json — (auto-regenerated) two new entries

Docs:
- docs/sampler.md — Windows notes; update §4 note on per-process I/O (Linux + Windows, not Linux-only)
- docs/PLAN.md — tick Phase 2 status; link to this plan

Mac (required for cross-OS round-trip):
- app/mac/Sources/System/SettingsPersistence.swift — emit platform.darwin envelope; read legacy flat form
- app/mac/Sources/System/SettingsTransfer.swift — include sourcePlatform; handle foreign platform.* blocks
- app/mac/Tests/SettingsTransferTests.swift — add cross-OS fixtures (shared TestData/settings.mac.json with Windows)

Server (minor — already supports what clients need):
- server/docker/docker-compose.test.yaml — no changes expected; the Windows tests reuse this file verbatim. If CI needs a different project name (`-p towertail-win-test` vs `-p towertail-test`), that's configured in test.ps1, not the compose file.

Windows app stubs:
- app/windows/README.md — replace stub with real setup instructions (.NET 9 SDK install, `scripts/test.ps1 --unit` quickstart, docker requirements for Tier 2)

---
12. Known Risks & Deferred Decisions

- H.NotifyIcon.WinUI packaged-app quirks. In MSIX, the taskbar icon can duplicate after Explorer restart. Mitigation: subscribe to TaskbarCreated Win32 message
 (the library supports this).
- Popover positioning on multi-monitor/DPI. Shell_NotifyIconGetRect returns screen coords but the tray may be on a secondary monitor. Test on 125%/150%/200%
scales.
- LiveCharts2 GPU pressure. Real-time updates with multi-series can spike CPU. Fallback plan: swap to bespoke SkiaSharp for charts too if we miss the 1% idle
CPU target.
- Windows OpenSSH availability. Not installed on Windows Server SKUs by default. Detection + friendly prompt to install via Settings → Optional features.
- Tailscale pipe permissions. Non-admin users may lack access; fallback to querying tailscale.exe status --json.
- Windows sampler per-process I/O reliability. Verify IOCounters() returns usable data under UAC-limited user tokens; may degrade to null on locked-down
systems.
- Deferred: auto-update mechanism for the app itself (not the sampler) — post-0.1. Candidates: MSIX Store updates, Velopack, or Squirrel.
- Deferred: Windows-on-ARM SSH host monitoring (sampler ARM64 builds, but rare host target).
- Deferred: dark/light theme switching animation polish.