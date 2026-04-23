<#
.SYNOPSIS
  Unified test runner for Towertail on Windows. Mirrors the UX of scripts/tests.sh
  (Bash) but operates against the Windows client solution at app/windows/ and the
  Go server/sampler on the same repo.

.DESCRIPTION
  Flags are composable. When invoked with no flags, runs the fast tier-1 path
  (--unit) so `scripts/test.ps1` with no args behaves like `scripts/tests.sh`
  for Mac's default.

.PARAMETER Unit
  Tier 1. Runs `dotnet test app/windows/Towertail.sln` with
  `--filter "Category!=FlaUI&Category!=Integration"`. No Docker required.

.PARAMETER RemoteIntegration
  Tier 2. Boots server/docker/docker-compose.test.yaml, sets the
  TOWERTAIL_INTEGRATION=1 env flag + flag file, runs the xUnit tests tagged
  `Category=Integration`. Tears the stack down on exit (unless -KeepDocker).

.PARAMETER Flaui
  Tier 3. Runs `dotnet test app/windows/Towertail.UITests` with
  `--filter Category=FlaUI`. Requires an interactive Windows desktop session.

.PARAMETER GoUnit
  Delegates to `go test ./...` on server/. Useful so a Windows dev can validate
  the server before hitting the Tier 2 path.

.PARAMETER GoIntegration
  Delegates to `go test -tags=integration -timeout=180s ./internal/clickhouse/...`
  on server/ (testcontainers-clickhouse).

.PARAMETER Sampler
  Delegates to `go test ./...` on sampler/.

.PARAMETER DockerUp
  Boot server/docker/docker-compose.test.yaml.

.PARAMETER DockerDown
  Tear down server/docker/docker-compose.test.yaml (removes volumes).

.PARAMETER DockerLogs
  Tail `docker compose logs -f` on the test stack.

.PARAMETER All
  --unit + --remote-integration + --go-unit + --go-integration + --sampler.
  Manages docker up/down automatically.

.PARAMETER KeepDocker
  With -All, don't tear the docker stack down after tests.

.PARAMETER Verbose
  Trace every external command that runs.

.EXAMPLE
  scripts/test.ps1 --unit
  scripts/test.ps1 -RemoteIntegration
  scripts/test.ps1 -All -KeepDocker

.NOTES
  The flag file at $env:TEMP\towertail-integration-test.env mirrors Mac's
  /tmp/towertail-integration-test.env convention — the xUnit test harness reads
  it at startup so env-var forwarding from dotnet to the test host isn't
  required.
#>
[CmdletBinding()]
param(
  [switch]$Unit,
  [switch]$RemoteIntegration,
  [switch]$Flaui,
  [switch]$GoUnit,
  [switch]$GoIntegration,
  [switch]$Sampler,
  [switch]$DockerUp,
  [switch]$DockerDown,
  [switch]$DockerLogs,
  [switch]$All,
  [switch]$KeepDocker,
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$Rest = @()
)

$ErrorActionPreference = 'Stop'

# -- Locate repo root ------------------------------------------------------
$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
Set-Location $repoRoot

$composeFile = Join-Path $repoRoot 'server/docker/docker-compose.test.yaml'
$composeProject = 'towertail-win-test'
$winSln = Join-Path $repoRoot 'app/windows/Towertail.sln'
$winTestsProj = Join-Path $repoRoot 'app/windows/Towertail.Tests/Towertail.Tests.csproj'
$winUiTestsProj = Join-Path $repoRoot 'app/windows/Towertail.UITests/Towertail.UITests.csproj'
$flagFile = Join-Path $env:TEMP 'towertail-integration-test.env'

# Parse long-form bash-style flags the Mac suite accepts, so muscle-memory
# works on Windows too (--unit, --remote-integration, --all, etc.).
foreach ($arg in $Rest) {
  switch -Exact ($arg) {
    '--unit'                { $Unit = $true }
    '--remote-integration'  { $RemoteIntegration = $true }
    '--flaui'               { $Flaui = $true }
    '--go-unit'             { $GoUnit = $true }
    '--go-integration'      { $GoIntegration = $true }
    '--sampler'             { $Sampler = $true }
    '--docker-up'           { $DockerUp = $true }
    '--docker-down'         { $DockerDown = $true }
    '--docker-logs'         { $DockerLogs = $true }
    '--all'                 { $All = $true }
    '--keep-docker'         { $KeepDocker = $true }
    '--verbose'             { $VerbosePreference = 'Continue' }
    '-v'                    { $VerbosePreference = 'Continue' }
    '--help'                { Get-Help $PSCommandPath -Detailed; exit 0 }
    '-h'                    { Get-Help $PSCommandPath -Detailed; exit 0 }
    default                 { Write-Error "unknown flag: $arg"; exit 2 }
  }
}

if ($All) {
  $Unit = $true
  $RemoteIntegration = $true
  $GoUnit = $true
  $GoIntegration = $true
  $Sampler = $true
}

# Default to tier-1 when no flags are given.
if (-not ($Unit -or $RemoteIntegration -or $Flaui -or $GoUnit -or $GoIntegration -or
          $Sampler -or $DockerUp -or $DockerDown -or $DockerLogs)) {
  $Unit = $true
}

# -- helpers ---------------------------------------------------------------

function Write-Section($msg) { Write-Host "`e[1m>>> $msg`e[0m" }
function Write-Pass($msg)    { Write-Host "`e[32m[PASS]`e[0m $msg" }
function Write-Fail($msg)    { Write-Host "`e[31m[FAIL]`e[0m $msg" }

function Compose([string[]]$rest) {
  $all = @('compose', '-f', $composeFile, '-p', $composeProject) + $rest
  & docker @all
  if ($LASTEXITCODE -ne 0) { throw "docker compose $($rest -join ' ') failed (exit $LASTEXITCODE)" }
}

function Require-Docker {
  $null = Get-Command docker -ErrorAction SilentlyContinue
  if (-not $?) {
    throw "docker not found on PATH. Install Docker Desktop: https://www.docker.com/products/docker-desktop"
  }
  & docker compose version | Out-Null
  if ($LASTEXITCODE -ne 0) {
    throw "docker compose subcommand unavailable. Update Docker Desktop to a recent version."
  }
}

function Docker-Up {
  Require-Docker
  Write-Section 'bringing up docker stack (clickhouse + server)'
  Compose @('up', '-d', '--build', '--wait')
  Write-Pass 'clickhouse + server are healthy'
}

function Docker-Down {
  Require-Docker
  Write-Section 'tearing down docker stack'
  Compose @('down', '-v', '--remove-orphans')
  Write-Pass 'docker stack down'
}

function Docker-Follow-Logs {
  Require-Docker
  Compose @('logs', '-f', '--tail=50')
}

function Write-Flag-File {
  $serverPort = if ($env:TT_TEST_PORT_SERVER) { $env:TT_TEST_PORT_SERVER } else { '18080' }
  $endpoint = "http://127.0.0.1:$serverPort"
  @(
    "TOWERTAIL_INTEGRATION=1",
    "TOWERTAIL_ENDPOINT=$endpoint",
    "TOWERTAIL_ADMIN_TOKEN=tt-test-admin-token-0000000000000000",
    "TOWERTAIL_REPO_ROOT=$repoRoot"
  ) | Set-Content -Path $flagFile -Encoding utf8
  # Also export to env so direct `dotnet test` invocations work without the file.
  $env:TOWERTAIL_INTEGRATION = '1'
  $env:TOWERTAIL_ENDPOINT = $endpoint
  $env:TOWERTAIL_ADMIN_TOKEN = 'tt-test-admin-token-0000000000000000'
  $env:TOWERTAIL_REPO_ROOT = $repoRoot
}

function Clear-Flag-File {
  Remove-Item -Path $flagFile -ErrorAction SilentlyContinue
  Remove-Item -Path Env:TOWERTAIL_INTEGRATION -ErrorAction SilentlyContinue
  Remove-Item -Path Env:TOWERTAIL_ENDPOINT -ErrorAction SilentlyContinue
  Remove-Item -Path Env:TOWERTAIL_ADMIN_TOKEN -ErrorAction SilentlyContinue
  # Leave TOWERTAIL_REPO_ROOT — it's also useful outside integration mode.
}

# -- dotnet test suites ----------------------------------------------------

function Run-Unit {
  Write-Section 'windows client: tier 1 (unit + in-process)'
  # Tests live in Towertail.Tests.csproj (net9.0-windows, plain class lib +
  # Towertail.Core). FlaUI lives in Towertail.UITests.csproj; integration
  # tests inside Towertail.Tests carry [Trait("Category","Integration")].
  & dotnet test $winTestsProj --no-restore:$false `
    --filter 'Category!=FlaUI&Category!=Integration'
  if ($LASTEXITCODE -ne 0) { throw 'unit tests failed' }
  Write-Pass 'windows unit tests'
}

function Run-RemoteIntegration {
  Write-Section 'windows client: tier 2 (RemoteBackend against real server)'

  # Prefer the Docker path when Docker is installed. Otherwise drop to the
  # local-server mode (no-Docker): the fixture boots a real go binary with
  # TT_CLICKHOUSE__DISABLED=true. Either way, no mocks; real sampler push
  # → real hub → real WS fan-out.
  $dockerAvailable = $false
  try {
    $null = Get-Command docker -ErrorAction Stop
    & docker compose version | Out-Null
    if ($LASTEXITCODE -eq 0) { $dockerAvailable = $true }
  } catch { }

  Write-Flag-File
  try {
    if ($dockerAvailable) {
      Write-Host '    mode: docker compose (full ClickHouse persistence)'
    } else {
      Write-Host '    mode: local go server with TT_CLICKHOUSE__DISABLED=true (no docker)'
      $env:TOWERTAIL_LOCAL_SERVER = '1'
    }
    & dotnet test $winTestsProj --filter 'Category=Integration'
    if ($LASTEXITCODE -ne 0) { throw 'integration tests failed' }
  } finally {
    Clear-Flag-File
    Remove-Item -Path Env:TOWERTAIL_LOCAL_SERVER -ErrorAction SilentlyContinue
  }
  Write-Pass 'windows integration tests'
}

function Run-Flaui {
  Write-Section 'windows client: tier 3 (FlaUI smoke)'
  & dotnet test $winUiTestsProj --filter 'Category=FlaUI'
  if ($LASTEXITCODE -ne 0) { throw 'FlaUI tests failed' }
  Write-Pass 'windows FlaUI tests'
}

# -- Go test suites --------------------------------------------------------

function Run-GoUnit {
  Write-Section 'go unit tests (server)'
  Push-Location (Join-Path $repoRoot 'server')
  try {
    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'server go test failed' }
  } finally { Pop-Location }
  Write-Pass 'server unit tests'
}

function Run-GoIntegration {
  Write-Section 'go integration tests (server → clickhouse via testcontainers)'
  Push-Location (Join-Path $repoRoot 'server')
  try {
    & go test '-tags=integration' -timeout=180s ./internal/clickhouse/...
    if ($LASTEXITCODE -ne 0) { throw 'server go integration test failed' }
  } finally { Pop-Location }
  Write-Pass 'server integration tests'
}

function Run-Sampler {
  Write-Section 'go unit tests (sampler)'
  Push-Location (Join-Path $repoRoot 'sampler')
  try {
    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'sampler go test failed' }
  } finally { Pop-Location }
  Write-Pass 'sampler tests'
}

# -- main ------------------------------------------------------------------

if ($DockerDown) { Docker-Down }
if ($DockerUp)   { Docker-Up }
if ($DockerLogs) { Docker-Follow-Logs; exit 0 }

$broughtUp = $false
$failures = 0

try {
  if ($GoUnit)            { try { Run-GoUnit }            catch { $failures++; Write-Fail $_.Exception.Message } }
  if ($Sampler)           { try { Run-Sampler }           catch { $failures++; Write-Fail $_.Exception.Message } }
  if ($GoIntegration)     { try { Run-GoIntegration }     catch { $failures++; Write-Fail $_.Exception.Message } }
  if ($Unit)              { try { Run-Unit }              catch { $failures++; Write-Fail $_.Exception.Message } }
  if ($RemoteIntegration) {
    # Run-RemoteIntegration decides between docker compose and the
    # no-Docker local-server path itself; we only bring docker up here
    # when it's actually available.
    $dockerOk = $false
    try {
      $null = Get-Command docker -ErrorAction Stop
      & docker compose version | Out-Null
      if ($LASTEXITCODE -eq 0) { $dockerOk = $true }
    } catch { }
    if ($dockerOk -and -not $broughtUp) { Docker-Up; $broughtUp = $true }
    try { Run-RemoteIntegration } catch { $failures++; Write-Fail $_.Exception.Message }
  }
  if ($Flaui)             { try { Run-Flaui }             catch { $failures++; Write-Fail $_.Exception.Message } }
}
finally {
  if ($broughtUp -and -not $KeepDocker) {
    Docker-Down
  }
}

if ($failures -gt 0) {
  Write-Fail "$failures suite(s) failed"
  exit $failures
}
Write-Pass 'all requested suites passed'
exit 0
