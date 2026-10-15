#!/usr/bin/env bash
# Sync + build + (optionally) run the WinUI app on winvm.
#
# Usage:
#   scripts/winvm-run.sh build        # sync + dotnet build
#   scripts/winvm-run.sh run          # sync + dotnet run (app launches on the VM)
#   scripts/winvm-run.sh test         # sync + scripts/test.ps1 --unit
#   scripts/winvm-run.sh publish      # sync + scripts/build.windows.ps1 -Publish
#   scripts/winvm-run.sh shell        # interactive PowerShell over SSH
#   scripts/winvm-run.sh tail-log     # tail today's log file
#   scripts/winvm-run.sh kill         # kill Towertail.exe on the VM
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Read WINVM_HOST and WINVM_REMOTE from the environment or scripts/winvm.env.
# shellcheck source=/dev/null
[[ -f "$REPO_ROOT/scripts/winvm.env" ]] && source "$REPO_ROOT/scripts/winvm.env"
: "${WINVM_HOST:?not set. Copy scripts/winvm.env.example to scripts/winvm.env and edit it.}"
: "${WINVM_REMOTE:?not set. Copy scripts/winvm.env.example to scripts/winvm.env and edit it.}"
HOST="$WINVM_HOST"
# C:\Users\me\x -> /c/Users/me/x
if [[ -z "${WINVM_REMOTE_UNIX:-}" ]]; then
  WINVM_REMOTE_UNIX="${WINVM_REMOTE//\\//}"
  drive="${WINVM_REMOTE_UNIX%%:*}"
  WINVM_REMOTE_UNIX="/$(tr '[:upper:]' '[:lower:]' <<<"$drive")${WINVM_REMOTE_UNIX#*:}"
fi
REMOTE_PATH="$WINVM_REMOTE"
REMOTE_PATH_UNIX="$WINVM_REMOTE_UNIX"
export WINVM_HOST WINVM_REMOTE WINVM_REMOTE_UNIX
CMD="${1:-build}"

sync() {
  "$REPO_ROOT/scripts/winvm-sync.sh"
}

ps_run() {
  # Pass a PowerShell command string; we quote on the remote side. Use pwsh
  # (PowerShell 7) — test.ps1 and build.windows.ps1 use unicode chars and
  # syntax the legacy Windows PowerShell 5.1 parser rejects.
  local script="$1"
  ssh "$HOST" "pwsh -NoProfile -Command \"cd '$REMOTE_PATH'; \$ErrorActionPreference='Stop'; $script\""
}

case "$CMD" in
  doctor)
    # One-time setup so `git status` on the VM doesn't show every synced file
    # as modified due to LF↔CRLF conversion.
    ssh "$HOST" "bash -lc 'cd $REMOTE_PATH_UNIX && git config core.autocrlf false && git config core.eol lf && git checkout -- . && echo OK'"
    ;;
  build)
    sync
    # Build for the VM's native arch. WindowsAppSDKSelfContained does not run
    # well under x64-on-ARM emulation, so we match the host.
    ps_run "\$arch = if ([Environment]::Is64BitOperatingSystem -and (\$env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or \$env:PROCESSOR_ARCHITEW6432 -eq 'ARM64')) { 'ARM64' } else { 'x64' }; dotnet build app\\windows\\Towertail.sln -c Debug -p:Platform=\$arch"
    ;;
  run)
    sync
    ssh "$HOST" "pwsh -NoProfile -Command \"Get-Process Towertail -ErrorAction SilentlyContinue | Stop-Process -Force\"" || true
    # Build + launch for the VM's native arch. WindowsAppSDKSelfContained
    # cannot run under x64-on-ARM emulation on an ARM64 host.
    ps_run "\$arch = if (\$env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or \$env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { 'ARM64' } else { 'x64' }; \$archLow = \$arch.ToLower(); dotnet build app\\windows\\Towertail.sln -c Debug -p:Platform=\$arch --nologo -v:m; \$exe = Join-Path '$REMOTE_PATH' \"app\\windows\\Towertail.WinUI\\bin\\\$arch\\Debug\\net9.0-windows10.0.19041.0\\Towertail.exe\"; Start-Process -FilePath \$exe"
    echo ">>> launched Towertail.exe on $HOST (tray icon in system tray)"
    ;;
  test)
    sync
    ps_run ".\\scripts\\test.ps1 --unit"
    ;;
  publish)
    sync
    ps_run ".\\scripts\\build.windows.ps1 -Configuration Release -Platform x64 -Publish"
    ;;
  shell)
    ssh -t "$HOST" "powershell -NoProfile -NoExit -Command \"cd '$REMOTE_PATH'\""
    ;;
  tail-log)
    ssh "$HOST" "powershell -NoProfile -Command \"Get-Content -Wait -Tail 50 (\$env:LOCALAPPDATA + '\\Towertail\\logs\\towertail-' + (Get-Date -Format 'yyyy-MM-dd') + '.log')\""
    ;;
  kill)
    ssh "$HOST" "powershell -NoProfile -Command \"Get-Process Towertail -ErrorAction SilentlyContinue | Stop-Process -Force; 'killed'\""
    ;;
  *)
    echo "usage: $0 {doctor|build|run|test|publish|shell|tail-log|kill}" >&2
    exit 2
    ;;
esac
