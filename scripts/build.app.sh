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
# Signing: uses $SIGN_IDENTITY (default: the Developer ID cert, if it is in
# the keychain). Falls back to ad-hoc ("-") when the cert is not found.
#
# Version: $VERSION sets CFBundleShortVersionString and $BUILD_NUMBER sets
# CFBundleVersion. Without $VERSION the app is a "-dev" build (see project.yml).
#
# Output: app/mac/build/Build/Products/<Configuration>/Towertail.app
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

DEFAULT_IDENTITY="Developer ID Application: Fritz Larco (Q56WK6TB88)"
TEAM_ID="Q56WK6TB88"
if [[ -z "${SIGN_IDENTITY:-}" ]]; then
  if security find-identity -v -p codesigning | grep -qF "$DEFAULT_IDENTITY"; then
    SIGN_IDENTITY="$DEFAULT_IDENTITY"
  else
    echo "warning: '$DEFAULT_IDENTITY' not in keychain — signing ad-hoc" >&2
    SIGN_IDENTITY="-"
  fi
fi

SIGN_ARGS=(CODE_SIGN_IDENTITY="$SIGN_IDENTITY")
if [[ "$SIGN_IDENTITY" != "-" ]]; then
  SIGN_ARGS+=(CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM="$TEAM_ID")
fi

BUILD_ARGS=()
[[ -n "${VERSION:-}" ]] && BUILD_ARGS+=(MARKETING_VERSION="$VERSION")
[[ -n "${BUILD_NUMBER:-}" ]] && BUILD_ARGS+=(CURRENT_PROJECT_VERSION="$BUILD_NUMBER")
if [[ "$CONFIG" == "Release" ]]; then
  # Universal binary. Notarization needs a secure timestamp.
  BUILD_ARGS+=(ONLY_ACTIVE_ARCH=NO)
  [[ "$SIGN_IDENTITY" != "-" ]] && BUILD_ARGS+=(OTHER_CODE_SIGN_FLAGS=--timestamp)
fi

echo "→ building Towertail ($CONFIG), signing with: $SIGN_IDENTITY"
DERIVED="$APP_DIR/build"
xcodebuild \
  -project "$APP_DIR/Towertail.xcodeproj" \
  -scheme Towertail \
  -configuration "$CONFIG" \
  -derivedDataPath "$DERIVED" \
  "${SIGN_ARGS[@]}" \
  ${BUILD_ARGS[@]+"${BUILD_ARGS[@]}"} \
  build

APP_PATH="$DERIVED/Build/Products/$CONFIG/Towertail.app"
echo "built: $APP_PATH"

if [[ "$OPEN_FLAG" == "--open" ]]; then
  pkill -x Towertail 2>/dev/null || true
  open "$APP_PATH"
fi
