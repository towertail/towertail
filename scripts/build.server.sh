#!/usr/bin/env bash
# Build the towertail-server Go binary.
#
# Usage:
#   scripts/build.server.sh                  # host platform (fast iteration)
#   scripts/build.server.sh linux-amd64      # cross-compile for one target
#
# Output: dist/server/<triple>/towertail-server
#
# Docker builds (scripts/build.server.sh not required there) use
# server/docker/Dockerfile which runs its own `go build` inside the
# golang image.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"
SERVER_DIR="$REPO_ROOT/server"
OUT_DIR="$REPO_ROOT/dist/server"

if ! command -v go >/dev/null 2>&1; then
  echo "error: go toolchain not found in PATH" >&2
  exit 1
fi

# Prefer an annotated tag; fall back to 0.0.0-dev when there are no tags
# (git describe --always would otherwise return the bare commit hash and
# cause version==sha in --version output).
if VERSION="$(git -C "$REPO_ROOT" describe --tags --dirty 2>/dev/null)"; then
  :
else
  VERSION="0.0.0-dev"
fi
SHA="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo dev)"

# triple          goos    goarch
TARGETS=(
  "linux-amd64    linux   amd64"
  "linux-arm64    linux   arm64"
  "darwin-arm64   darwin  arm64"
  "darwin-amd64   darwin  amd64"
)

SINGLE="${1:-}"

host_triple() {
  local os arch
  case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux)  os="linux"  ;;
    *)      echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
  esac
  case "$(uname -m)" in
    arm64|aarch64) arch="arm64" ;;
    x86_64|amd64)  arch="amd64" ;;
    *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
  esac
  echo "$os-$arch"
}

build_one() {
  local triple="$1" goos="$2" goarch="$3"
  local dest_dir="$OUT_DIR/$triple"
  local dest="$dest_dir/towertail-server"
  mkdir -p "$dest_dir"

  echo "→ $triple  (GOOS=$goos GOARCH=$goarch)"
  (
    cd "$SERVER_DIR"
    env -i \
      PATH="$PATH" HOME="$HOME" \
      CGO_ENABLED=0 \
      GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath \
        -ldflags="-s -w -X github.com/towertail/server/internal/version.Version=$VERSION -X github.com/towertail/server/internal/version.SHA=$SHA" \
        -o "$dest" ./cmd/towertail-server
  )

  local size
  size="$(wc -c < "$dest" | tr -d ' ')"
  echo "  built $dest ($((size / 1024 / 1024)) MB)"
}

# Default to host platform only — the server is usually run on the box
# that built it or in Docker; cross-compile only when a triple is asked.
if [[ -z "$SINGLE" ]]; then
  SINGLE="$(host_triple)"
fi

matched=0
for row in "${TARGETS[@]}"; do
  # shellcheck disable=SC2086
  set -- $row
  triple="$1" goos="$2" goarch="$3"
  if [[ "$SINGLE" != "$triple" ]]; then
    continue
  fi
  build_one "$triple" "$goos" "$goarch"
  matched=1
  break
done

if [[ $matched -eq 0 ]]; then
  echo "error: no target matched '$SINGLE'. valid triples:" >&2
  for row in "${TARGETS[@]}"; do
    # shellcheck disable=SC2086
    set -- $row
    echo "  $1" >&2
  done
  exit 1
fi
