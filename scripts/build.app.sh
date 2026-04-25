#!/usr/bin/env bash
# Build the Towertail macOS app.
#
# Usage:
#   scripts/build.app.sh                # Debug build (default)
#   scripts/build.app.sh Release        # Release build
#   scripts/build.app.sh Debug --open   # build and launch the app
#
# Prerequisites:
#   - Xcode command line tools
#   - xcodegen (brew install xcodegen)
#   - dist/samplers/ populated (run scripts/build.sampler.sh first)
#
# Output: app/mac/build/<Configuration>/Towertail.app
set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"
APP_DIR="$REPO_ROOT/app/mac"
CONFIG="${1:-Debug}"
OPEN_FLAG="${2:-}"

if ! command -v xcodegen >/dev/null 2>&1; then
  echo "error: xcodegen not found. Install with: brew install xcodegen" >&2
  exit 1
fi

if [[ ! -d "$REPO_ROOT/dist/samplers/darwin-arm64" ]]; then
  echo "warning: dist/samplers/ missing — running scripts/build.sampler.sh first" >&2
  "$REPO_ROOT/scripts/build.sampler.sh"
fi

echo "→ regenerating Xcode project"
(cd "$APP_DIR" && xcodegen generate)

echo "→ building Towertail ($CONFIG)"
DERIVED="$APP_DIR/build"
xcodebuild \
  -project "$APP_DIR/Towertail.xcodeproj" \
  -scheme Towertail \
  -configuration "$CONFIG" \
  -derivedDataPath "$DERIVED" \
  build

APP_PATH="$DERIVED/Build/Products/$CONFIG/Towertail.app"
echo "built: $APP_PATH"

if [[ "$OPEN_FLAG" == "--open" ]]; then
  pkill -x Towertail 2>/dev/null || true
  open "$APP_PATH"
fi
