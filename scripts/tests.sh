#!/usr/bin/env bash
# Unified test runner for Towertail. Covers Swift (unit + integration),
# Go server (unit + ClickHouse integration), Go sampler, and the Docker
# test stack used by end-to-end tests.
#
# Cross-platform note:
#   This script is the macOS / Linux side. The Windows client has its own
#   PowerShell runner at scripts/test.ps1 with symmetric flags (--unit,
#   --remote-integration, --flaui, --go-unit, --go-integration, --sampler).
#   Both drive the same server/docker/docker-compose.test.yaml stack and
#   share the TOWERTAIL_INTEGRATION flag-file contract so CI matrices stay
#   in lockstep. --go-unit / --go-integration / --sampler work from a
#   Windows Git Bash shell too if you need to run them there.
#
# Usage:
#   scripts/tests.sh [flags]
#
# Flags (composable; default is --swift-unit --go-unit --sampler):
#   --swift-unit          xcodebuild unit tests (fast, no Docker)
#   --swift-integration   e2e tests against docker-compose.test.yaml
#                         (requires --docker-up beforehand, or pair with --all)
#   --go-unit             go test ./... on server+sampler (no build tags)
#   --go-integration      go test -tags=integration on server (uses testcontainers)
#   --sampler             go test ./... on sampler module
#   --docker-up           bring up docker-compose.test.yaml
#   --docker-down         tear down docker-compose.test.yaml (volumes too)
#   --docker-logs         follow server logs (useful when a test fails)
#   --all                 swift-unit + swift-integration + go-unit +
#                         go-integration + sampler (brings Docker up/down
#                         around the integration phases)
#   --keep-docker         don't tear Docker down after --all
#   --verbose / -v        echo every command as it runs
#   --help / -h           this message
set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"

# -- arg parsing ----------------------------------------------------------

SWIFT_UNIT=0
SWIFT_INT=0
GO_UNIT=0
GO_INT=0
SAMPLER=0
DOCKER_UP=0
DOCKER_DOWN=0
DOCKER_LOGS=0
KEEP_DOCKER=0
VERBOSE=0
ALL=0

if [[ $# -eq 0 ]]; then
  SWIFT_UNIT=1
  GO_UNIT=1
  SAMPLER=1
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --swift-unit)        SWIFT_UNIT=1 ;;
    --swift-integration) SWIFT_INT=1 ;;
    --go-unit)           GO_UNIT=1 ;;
    --go-integration)    GO_INT=1 ;;
    --sampler)           SAMPLER=1 ;;
    --docker-up)         DOCKER_UP=1 ;;
    --docker-down)       DOCKER_DOWN=1 ;;
    --docker-logs)       DOCKER_LOGS=1 ;;
    --keep-docker)       KEEP_DOCKER=1 ;;
    --all)               ALL=1 ;;
    -v|--verbose)        VERBOSE=1 ;;
    -h|--help)
      sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "error: unknown flag: $1" >&2
      exit 2
      ;;
  esac
  shift
done

if [[ $VERBOSE -eq 1 ]]; then set -x; fi

if [[ $ALL -eq 1 ]]; then
  SWIFT_UNIT=1
  SWIFT_INT=1
  GO_UNIT=1
  GO_INT=1
  SAMPLER=1
fi

# -- colour output --------------------------------------------------------

if [[ -t 1 ]]; then
  BOLD=$'\033[1m'; RED=$'\033[31m'; GRN=$'\033[32m'; DIM=$'\033[2m'; RST=$'\033[0m'
else
  BOLD=""; RED=""; GRN=""; DIM=""; RST=""
fi

section() { echo "${BOLD}▶ $*${RST}"; }
pass()    { echo "${GRN}✓${RST} $*"; }
fail()    { echo "${RED}✗${RST} $*"; }

# -- Docker helpers -------------------------------------------------------

COMPOSE_FILE="$REPO_ROOT/server/docker/docker-compose.test.yaml"
COMPOSE_PROJECT="towertail-test"

compose() {
  docker compose -f "$COMPOSE_FILE" -p "$COMPOSE_PROJECT" "$@"
}

docker_up() {
  section "bringing up docker stack"
  compose up -d --build --wait
  pass "clickhouse + server are healthy"
}

docker_down() {
  section "tearing down docker stack"
  compose down -v --remove-orphans >/dev/null
  pass "docker stack down"
}

docker_follow_logs() {
  compose logs -f --tail=50
}

# -- Swift tests ----------------------------------------------------------

PROJECT="$REPO_ROOT/app/mac/Towertail.xcodeproj"
SCHEME="Towertail"
DESTINATION="platform=macOS"

regenerate_xcode_if_needed() {
  if [[ ! -d "$PROJECT" ]] || [[ "$REPO_ROOT/app/mac/project.yml" -nt "$PROJECT" ]]; then
    section "regenerating Xcode project"
    (cd "$REPO_ROOT/app/mac" && xcodegen generate)
  fi
}

quit_running_towertail() {
  # Tests use the same on-disk NodeStore/SettingsPersistence paths as
  # the production app. Leaving a live Towertail running during a test
  # causes it to observe test-written nodes (and vice versa). Kill any
  # instance cleanly before the suite.
  if pgrep -f "Towertail.app/Contents/MacOS/Towertail" >/dev/null; then
    section "closing running Towertail.app"
    osascript -e 'tell application "Towertail" to quit' 2>/dev/null || true
    pkill -f "Towertail.app/Contents/MacOS/Towertail" 2>/dev/null || true
    # Give it a moment to actually exit.
    for _ in 1 2 3 4 5; do
      pgrep -f "Towertail.app/Contents/MacOS/Towertail" >/dev/null || break
      sleep 0.4
    done
  fi
}

swift_unit() {
  section "swift unit tests"
  quit_running_towertail
  regenerate_xcode_if_needed
  # Ensure samplers are bundled so app build succeeds.
  if [[ ! -d "$REPO_ROOT/dist/samplers/darwin-arm64" ]] && \
     [[ ! -d "$REPO_ROOT/dist/samplers/darwin-amd64" ]]; then
    section "building host sampler (needed by app bundle pre-build)"
    "$REPO_ROOT/scripts/build.sampler.sh"
  fi
  xcodebuild -project "$PROJECT" -scheme "$SCHEME" -destination "$DESTINATION" \
    test 2>&1 | xcpretty_or_grep
  pass "swift unit tests"
}

swift_integration() {
  section "swift integration tests (real server + clickhouse)"
  quit_running_towertail
  regenerate_xcode_if_needed
  # Build sampler binary for push-mode smoke test.
  "$REPO_ROOT/scripts/build.sampler.sh"

  # xcodebuild doesn't reliably forward shell env vars to macOS test
  # hosts, so we drop config into a file the tests read on load.
  local flag_file="/tmp/towertail-integration-test.env"
  cat > "$flag_file" <<EOF
TOWERTAIL_INTEGRATION=1
TOWERTAIL_ENDPOINT=http://127.0.0.1:${TT_TEST_PORT_SERVER:-18080}
TOWERTAIL_ADMIN_TOKEN=tt-test-admin-token-0000000000000000
EOF
  trap 'rm -f "$flag_file"' RETURN

  xcodebuild -project "$PROJECT" -scheme "$SCHEME" -destination "$DESTINATION" \
    -only-testing:TowertailTests/RemoteBackendIntegrationTests \
    test 2>&1 | xcpretty_or_grep
  pass "swift integration tests"
}

xcpretty_or_grep() {
  if command -v xcpretty >/dev/null; then
    xcpretty --test --color
    return "${PIPESTATUS[0]}"
  else
    grep -E "(Test Case|passed|failed|error:|XCTAssert|Executed|BUILD|\*\* TEST)" || true
  fi
}

# -- Go tests -------------------------------------------------------------

go_unit() {
  section "go unit tests (server)"
  (cd "$REPO_ROOT/server" && go test ./...)
  pass "server unit tests"
}

go_integration() {
  section "go integration tests (server, testcontainers → clickhouse)"
  (cd "$REPO_ROOT/server" && go test -tags=integration -timeout=180s ./internal/clickhouse/...)
  pass "server integration tests"
}

sampler_tests() {
  section "go unit tests (sampler)"
  (cd "$REPO_ROOT/sampler" && go test ./...)
  pass "sampler tests"
}

# -- main -----------------------------------------------------------------

# Docker-only operations.
if [[ $DOCKER_DOWN -eq 1 ]]; then
  docker_down
fi
if [[ $DOCKER_UP -eq 1 ]]; then
  docker_up
fi
if [[ $DOCKER_LOGS -eq 1 ]]; then
  docker_follow_logs
  exit 0
fi

# Bring Docker up around integration phases (unless caller already did it).
NEEDS_DOCKER=0
if [[ $SWIFT_INT -eq 1 ]]; then NEEDS_DOCKER=1; fi

if [[ $NEEDS_DOCKER -eq 1 ]] && [[ $ALL -eq 1 ]]; then
  docker_up
fi

trap 'rc=$?; if [[ $ALL -eq 1 ]] && [[ $KEEP_DOCKER -eq 0 ]] && [[ $NEEDS_DOCKER -eq 1 ]]; then docker_down; fi; exit $rc' EXIT

[[ $GO_UNIT -eq 1 ]] && go_unit
[[ $SAMPLER -eq 1 ]] && sampler_tests
[[ $GO_INT -eq 1 ]] && go_integration
[[ $SWIFT_UNIT -eq 1 ]] && swift_unit
[[ $SWIFT_INT -eq 1 ]] && swift_integration

echo
pass "all requested suites passed"
