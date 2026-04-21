#!/usr/bin/env bash
# Build everything: Go agent (all triples) + macOS app.
#
# Usage:
#   scripts/build.sh                # Debug app build, all agent triples
#   scripts/build.sh Release        # Release app build
#   scripts/build.sh Debug --open   # build and launch the app
set -euo pipefail

cd "$(dirname "$0")/.."
CONFIG="${1:-Debug}"
OPEN_FLAG="${2:-}"

./scripts/build.agent.sh
./scripts/build.app.sh "$CONFIG" "$OPEN_FLAG"
