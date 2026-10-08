#!/usr/bin/env bash
# Build everything (Go sampler, all triples + signed macOS app), install the
# app, and restart it.
#
# Usage:
#   scripts/build.sh                # Debug build, install, restart
#   scripts/build.sh Release        # Release build, install, restart
#
# Env:
#   INSTALL_DIR     install location (default: /Applications)
#   SIGN_IDENTITY   codesign identity (see scripts/build.app.sh)
set -euo pipefail

cd "$(dirname "$0")/.."
CONFIG="${1:-Debug}"
INSTALL_DIR="${INSTALL_DIR:-/Applications}"
BUILT_APP="app/mac/build/Build/Products/$CONFIG/Towertail.app"
INSTALLED_APP="$INSTALL_DIR/Towertail.app"

./scripts/build.sampler.sh
./scripts/build.app.sh "$CONFIG"

echo "→ stopping running Towertail"
pkill -x Towertail 2>/dev/null || true
for _ in {1..50}; do
  pgrep -x Towertail >/dev/null || break
  sleep 0.1
done
pkill -9 -x Towertail 2>/dev/null || true

echo "→ installing to $INSTALLED_APP"
rm -rf "$INSTALLED_APP"
ditto "$BUILT_APP" "$INSTALLED_APP"
codesign --verify --deep --strict "$INSTALLED_APP"

echo "→ launching"
open "$INSTALLED_APP"
