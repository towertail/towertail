<#
.SYNOPSIS
  Build the Windows Towertail client (WinUI 3, Windows App SDK).

.DESCRIPTION
  Mirrors scripts/build.app.sh for the Mac side. Regenerates nothing
  automatically (no XcodeGen equivalent); builds Towertail.sln via dotnet.
  The publish output (the shippable app) lands at
  dist/app/windows-<arch>/ (e.g. dist/app/windows-x64/). Intermediate
  MSBuild output still lives in app/windows/Towertail.WinUI/bin|obj/ —
  IDEs expect it there — but those are gitignored.

.PARAMETER Configuration
  Debug or Release. Default: Debug.

.PARAMETER Platform
  x64 or ARM64. Default: x64.

.PARAMETER Publish
  Also publish the WinUI app to a folder-output layout (useful for MSIX
  packaging and portable-zip distribution). Produces a folder under
  app/windows/Towertail.WinUI/bin/<Configuration>/net9.0-windows*/<rid>/publish/.

.PARAMETER Zip
  After -Publish, produce a portable ZIP at dist/windows/Towertail-<ver>-<rid>.zip.
  Implies -Publish. The ZIP is self-contained (bundled Windows App Runtime),
  no installation required.

.PARAMETER Msix
  After -Publish, produce an MSIX at dist/windows/Towertail-<ver>-<rid>.msix.
  Requires the x64/ARM64 platform matching the target; signing is left to
  the caller (the CI job signs with a Store cert, dev builds stay unsigned).

.PARAMETER Open
  After building, launch Towertail.exe so the tray icon appears.

.EXAMPLE
  scripts/build.windows.ps1
  scripts/build.windows.ps1 -Configuration Release -Platform ARM64
  scripts/build.windows.ps1 -Configuration Release -Publish -Zip
  scripts/build.windows.ps1 -Configuration Release -Publish -Msix
#>
[CmdletBinding()]
param(
  [ValidateSet('Debug','Release')] [string]$Configuration = 'Debug',
  [ValidateSet('x64','ARM64')]     [string]$Platform = 'x64',
  [switch]$Publish,
  [switch]$Zip,
  [switch]$Msix,
  [switch]$Open
)

$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..')
Set-Location $repoRoot

$sln     = Join-Path $repoRoot 'app/windows/Towertail.sln'
$winProj = Join-Path $repoRoot 'app/windows/Towertail.WinUI/Towertail.WinUI.csproj'

Write-Host ">>> building sampler binaries (prereq for Towertail.WinUI CopySamplers)"
& bash (Join-Path $repoRoot 'scripts/build.sampler.sh')
if ($LASTEXITCODE -ne 0) { throw 'sampler build failed' }

Write-Host ">>> restoring Windows solution"
& dotnet restore $sln
if ($LASTEXITCODE -ne 0) { throw 'dotnet restore failed' }

Write-Host ">>> building Towertail.WinUI ($Configuration / $Platform)"
& dotnet build $winProj `
  -c $Configuration `
  -p:Platform=$Platform `
  --no-restore
if ($LASTEXITCODE -ne 0) { throw 'dotnet build failed' }

if ($Zip -or $Msix) { $Publish = $true }

$rid  = if ($Platform -eq 'ARM64') { 'win-arm64' } else { 'win-x64' }
$arch = if ($Platform -eq 'ARM64') { 'arm64' }    else { 'x64' }
$publishDir = Join-Path $repoRoot "dist/app/windows-$arch"

if ($Publish) {
  Write-Host ">>> publishing Towertail.WinUI ($Configuration / $rid) -> $publishDir"
  if (Test-Path $publishDir) { Remove-Item $publishDir -Recurse -Force }
  New-Item -ItemType Directory -Path $publishDir -Force | Out-Null
  & dotnet publish $winProj `
    -c $Configuration `
    -r $rid `
    -p:Platform=$Platform `
    -p:WindowsAppSDKSelfContained=true `
    -p:WindowsPackageType=None `
    -o $publishDir `
    --no-restore
  if ($LASTEXITCODE -ne 0) { throw 'dotnet publish failed' }
  Write-Host ">>> publish output: $publishDir"
}

# Read the product version from the csproj so artifacts are traceable.
$version = (Select-Xml -Path $winProj -XPath '//PropertyGroup/Version').Node.InnerText
if (-not $version) { $version = '0.0.0' }

$distDir = Join-Path $repoRoot 'dist/windows'
if (($Zip -or $Msix) -and -not (Test-Path $distDir)) {
  New-Item -ItemType Directory -Path $distDir -Force | Out-Null
}

if ($Zip) {
  $zipPath = Join-Path $distDir "Towertail-$version-$rid.zip"
  if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
  Write-Host ">>> zipping portable build to $zipPath"
  Compress-Archive -Path (Join-Path $publishDir '*') -DestinationPath $zipPath -Force
  Write-Host ">>> portable ZIP ready: $zipPath"
}

if ($Msix) {
  $msixPath = Join-Path $distDir "Towertail-$version-$rid.msix"
  if (Test-Path $msixPath) { Remove-Item $msixPath -Force }
  Write-Host ">>> packaging MSIX to $msixPath"
  $makeAppx = Get-Command makeappx.exe -ErrorAction SilentlyContinue
  if (-not $makeAppx) {
    $hits = Get-ChildItem -Path 'C:\Program Files (x86)\Windows Kits\10\bin' -Recurse -Filter 'makeappx.exe' -ErrorAction SilentlyContinue |
      Sort-Object FullName -Descending | Select-Object -First 1
    if ($hits) { $makeAppx = @{ Path = $hits.FullName } }
  }
  if (-not $makeAppx) {
    Write-Warning 'makeappx.exe not found on PATH. Install the Windows 10/11 SDK to enable MSIX packaging.'
  } else {
    & $makeAppx.Path pack /d $publishDir /p $msixPath /o
    if ($LASTEXITCODE -ne 0) { throw "makeappx failed ($LASTEXITCODE)" }
    Write-Host ">>> MSIX ready: $msixPath"
    Write-Host ">>> note: unsigned. Sign with signtool.exe before distributing to end users."
  }
}

if ($Open) {
  $exePath = $null
  if ($Publish) {
    $candidate = Join-Path $publishDir 'Towertail.exe'
    if (Test-Path $candidate) { $exePath = $candidate }
  }
  if (-not $exePath) {
    $exe = Get-ChildItem -Path (Join-Path $repoRoot 'app/windows/Towertail.WinUI/bin') `
      -Recurse -Filter 'Towertail.exe' -ErrorAction SilentlyContinue |
      Sort-Object LastWriteTime -Descending |
      Select-Object -First 1
    if ($exe) { $exePath = $exe.FullName }
  }
  if (-not $exePath) { throw 'Towertail.exe not found after build' }
  Write-Host ">>> launching $exePath"
  Start-Process -FilePath $exePath
}

Write-Host ">>> done"
