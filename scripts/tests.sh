#!/usr/bin/env bash
# Unified test runner for Towertail. Covers Swift (unit + integration),
# Go server (unit + ClickHouse integration), Go sampler, and the Docker
# test stack used by end-to-end tests.
#
# Cross-platform note:
#   This script is the macOS / Linux side. The Windows client has its own
#   PowerShell runner at scripts/test.ps1 with symmetric flags (--unit,
#   --remote-integration, --flaui, --go-unit, --go-integration).
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
#                         (requires --docker-up or --server-local, or pair with --all)
#   --server-local        build and start the Go server on the host with
#                         ClickHouse disabled (no Docker); stop it on exit
#   --go-unit             go test ./... on server (no build tags)
#   --go-integration      go test -tags=integration on server (uses testcontainers)
#   --sampler             go test ./... on $SAMPLER_DIR (a towertail/sampler clone;
#                         skipped when SAMPLER_DIR is not set)
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
SERVER_LOCAL=0
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
    --server-local)      SERVER_LOCAL=1 ;;
    --docker-down)       DOCKER_DOWN=1 ;;
    --docker-logs)       DOCKER_LOGS=1 ;;
    --keep-docker)       KEEP_DOCKER=1 ;;
    --all)               ALL=1 ;;
    -v|--verbose)        VERBOSE=1 ;;
    -h|--help)
      sed -n '2,35p' "$0" | sed 's/^# \{0,1\}//'
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

# -- Local server (no Docker) ---------------------------------------------

SERVER_LOCAL_DIR=""
SERVER_LOCAL_PID=""

server_local_up() {
  section "starting local server (ClickHouse disabled)"
  local port="${TT_TEST_PORT_SERVER:-18080}"
  SERVER_LOCAL_DIR="$(mktemp -d)"
  (cd "$REPO_ROOT/server" && go build -o "$SERVER_LOCAL_DIR/towertail-server" ./cmd/towertail-server)
  TT_HTTP__ADDR="127.0.0.1:$port" \
  TT_CLICKHOUSE__DISABLED=true \
  TT_STORAGE__PATH="$SERVER_LOCAL_DIR/data" \
  TT_AUTH__BOOTSTRAP_TOKEN="tt-test-admin-token-0000000000000000" \
  TT_CLOUD__MANAGED=false \
  TT_LOG__FORMAT=text \
    "$SERVER_LOCAL_DIR/towertail-server" serve >"$SERVER_LOCAL_DIR/server.log" 2>&1 &
  SERVER_LOCAL_PID=$!
  for _ in $(seq 1 60); do
    if curl -fsS "http://127.0.0.1:$port/readyz" >/dev/null 2>&1; then
      pass "local server is ready on :$port"
      return 0
    fi
    if ! kill -0 "$SERVER_LOCAL_PID" 2>/dev/null; then break; fi
    sleep 1
  done
  fail "local server did not become ready"
  cat "$SERVER_LOCAL_DIR/server.log" >&2
  return 1
}

server_local_down() {
  [[ -n "$SERVER_LOCAL_PID" ]] || return 0
  kill "$SERVER_LOCAL_PID" 2>/dev/null || true
  wait "$SERVER_LOCAL_PID" 2>/dev/null || true
  rm -rf "$SERVER_LOCAL_DIR"
  SERVER_LOCAL_PID=""
}

# -- Swift tests ----------------------------------------------------------

PROJECT="$REPO_ROOT/apps/mac/Towertail.xcodeproj"
SCHEME="Towertail"
DESTINATION="platform=macOS"

regenerate_xcode_if_needed() {
  if [[ ! -d "$PROJECT" ]] || [[ "$REPO_ROOT/apps/mac/project.yml" -nt "$PROJECT" ]]; then
    section "regenerating Xcode project"
    (cd "$REPO_ROOT/apps/mac" && xcodegen generate)
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
  # The app bundle pre-build copies dist/samplers/.
  "$REPO_ROOT/scripts/fetch-samplers.sh"
  xcodebuild -project "$PROJECT" -scheme "$SCHEME" -destination "$DESTINATION" \
    test 2>&1 | xcpretty_or_grep
  pass "swift unit tests"
}

swift_integration() {
  section "swift integration tests (real server + clickhouse)"
  quit_running_towertail
  regenerate_xcode_if_needed
  # The push-mode smoke test runs the sampler binary.
  "$REPO_ROOT/scripts/fetch-samplers.sh"

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

# Tests the towertail/sampler clone in $SAMPLER_DIR.
sampler_tests() {
  local dir="${SAMPLER_DIR:-}"
  section "go unit tests (sampler)"
  if [[ -z "$dir" || ! -d "$dir" ]]; then
    echo "skip: set SAMPLER_DIR to a towertail/sampler clone"
    return
  fi
  (cd "$dir" && go test ./...)
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

trap 'rc=$?; server_local_down; if [[ $ALL -eq 1 ]] && [[ $KEEP_DOCKER -eq 0 ]] && [[ $NEEDS_DOCKER -eq 1 ]]; then docker_down; fi; exit $rc' EXIT

if [[ $SERVER_LOCAL -eq 1 ]]; then server_local_up; fi

[[ $GO_UNIT -eq 1 ]] && go_unit
[[ $SAMPLER -eq 1 ]] && sampler_tests
[[ $GO_INT -eq 1 ]] && go_integration
[[ $SWIFT_UNIT -eq 1 ]] && swift_unit
[[ $SWIFT_INT -eq 1 ]] && swift_integration

echo
pass "all requested suites passed"
