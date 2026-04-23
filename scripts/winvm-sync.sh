#!/usr/bin/env bash
# Sync the working tree from this Mac to winvm.
#
# Uses tar-over-ssh (both sides have tar) so it works out of the box — no
# rsync needed on the Windows host. For this repo size (~tens of MB of source)
# it's fast enough (~3-5s). Set WINVM_USE_RSYNC=1 to force rsync if you've
# installed it on the Windows side (e.g. `scoop install cwrsync`).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOST="${WINVM_HOST:-fritz@winvm}"
REMOTE_UNIX="${WINVM_REMOTE_UNIX:-/c/Users/fritz/Projects/towertail}"

EXCLUDES=(
  --exclude='./.git'
  --exclude='./bin'
  --exclude='./obj'
  --exclude='./dist'
  --exclude='./node_modules'
  --exclude='.DS_Store'
  --exclude='._*'
  --exclude='./app/mac'
  --exclude='*.xcodeproj'
  --exclude='./DerivedData'
  --exclude='**/bin'
  --exclude='**/obj'
)

if [[ "${WINVM_USE_RSYNC:-0}" == "1" ]]; then
  rsync -az --delete \
    --exclude '.git/' --exclude 'bin/' --exclude 'obj/' --exclude 'dist/' \
    --exclude 'node_modules/' --exclude '.DS_Store' --exclude 'app/mac/' \
    --exclude '*.xcodeproj/' --exclude 'DerivedData/' \
    "$REPO_ROOT/" "$HOST:$REMOTE_UNIX/"
  echo ">>> synced (rsync) to $HOST:$REMOTE_UNIX"
  exit 0
fi

# tar pipe. We don't delete on the remote side — build artifacts the Windows
# compiler produces should survive a push. If you want a clean slate, use:
#   scripts/winvm-run.sh shell, then: Remove-Item -Recurse -Force *
#
# NOTE: the VM should have `git config core.autocrlf false` set for this repo.
# Otherwise git diff will report every synced file as modified (LF → CRLF).
# winvm-run.sh doctor will set it for you.
cd "$REPO_ROOT"
# COPYFILE_DISABLE and --no-mac-metadata both suppress macOS AppleDouble (._*)
# forks in the tar stream. Belt and suspenders.
COPYFILE_DISABLE=1 tar --no-mac-metadata -cf - "${EXCLUDES[@]}" . \
  | ssh "$HOST" "bash -lc \"mkdir -p $REMOTE_UNIX && tar -xf - -C $REMOTE_UNIX\""

echo ">>> synced (tar) to $HOST:$REMOTE_UNIX"
