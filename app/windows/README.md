# app/windows

Windows WinUI 3 client, functional parity with the Mac client.

## Prerequisites

- Windows 11 (Windows 10 1809+ supported)
- .NET 9 SDK (`winget install Microsoft.DotNet.SDK.9`)
- Windows App SDK runtime (bundled self-contained in Release builds)
- PowerShell 7 (`winget install Microsoft.PowerShell`)
- For Tier 2 integration tests: Docker Desktop with WSL 2 backend

## Quick start

```powershell
# unit tests only (< 60s)
.\scripts\test.ps1 --unit

# build the app (copies dist/samplers into Assets\samplers first)
.\scripts\build.windows.ps1

# run the app (Debug)
dotnet run --project app\windows\Towertail.WinUI -c Debug
```

## Project layout

See `docs/windows-app.md` §3 — directory structure mirrors `app/mac/Sources/` file-for-file.

## Where things live

- Settings JSON: `%APPDATA%\Towertail\settings.json`
- History SQLite: `%APPDATA%\Towertail\history.sqlite`
- Log files: `%LOCALAPPDATA%\Towertail\logs\towertail-YYYY-MM-DD.log`
- Bundled samplers: `Assets\samplers\<triple>\towertail-sampler.exe` (copied from `dist/samplers/` by the csproj pre-build target)

## Tests

```powershell
# Tier 1 — pure unit tests (< 60s)
.\scripts\test.ps1 --unit

# Tier 2 — real server + ClickHouse via docker-compose
.\scripts\test.ps1 --remote-integration

# Tier 3 — FlaUI smoke tests (interactive Windows session required)
.\scripts\test.ps1 --flaui

# all tiers
.\scripts\test.ps1 --all
```

Contract-test fixture shared with Swift + Go: `sampler/testdata/sample-v1.json`.

## Settings interoperability

The schema carries a top-level `platform.{darwin,windows,...}` envelope so a `settings.json`
copied between Mac and Windows round-trips cleanly. Mac v1.1 reads/writes the same form.
See `docs/windows-app.md` §4.
