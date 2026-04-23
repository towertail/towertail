#!/usr/bin/env bash
# Build the towertail-sampler Go binary for all v1 target triples.
#
# Usage:
#   scripts/build.sampler.sh              # build all five targets
#   scripts/build.sampler.sh linux-arm64  # build a single target (fast iteration)
#
# Output: dist/samplers/<triple>/towertail-sampler (+ manifest.json with sha256s).
set -euo pipefail

cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"
SAMPLER_DIR="$REPO_ROOT/sampler"
OUT_DIR="$REPO_ROOT/dist/samplers"

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

# triple          goos    goarch  goarm
TARGETS=(
  "linux-amd64    linux   amd64"
  "linux-arm64    linux   arm64"
  "linux-armv7    linux   arm     7"
  "darwin-arm64   darwin  arm64"
  "darwin-amd64   darwin  amd64"
  "windows-amd64  windows amd64"
  "windows-arm64  windows arm64"
)

SINGLE="${1:-}"

build_one() {
  local triple="$1" goos="$2" goarch="$3" goarm="${4:-}"
  local dest_dir="$OUT_DIR/$triple"
  local ext=""
  if [[ "$goos" == "windows" ]]; then
    ext=".exe"
  fi
  local dest="$dest_dir/towertail-sampler${ext}"
  mkdir -p "$dest_dir"

  echo "→ $triple  (GOOS=$goos GOARCH=$goarch${goarm:+ GOARM=$goarm})"
  (
    cd "$SAMPLER_DIR"
    env -i \
      PATH="$PATH" HOME="$HOME" \
      ${TEMP:+TEMP="$TEMP"} ${TMP:+TMP="$TMP"} \
      ${LOCALAPPDATA:+LOCALAPPDATA="$LOCALAPPDATA"} \
      ${USERPROFILE:+USERPROFILE="$USERPROFILE"} \
      ${GOCACHE:+GOCACHE="$GOCACHE"} ${GOMODCACHE:+GOMODCACHE="$GOMODCACHE"} \
      ${GOPATH:+GOPATH="$GOPATH"} \
      CGO_ENABLED=0 \
      GOOS="$goos" GOARCH="$goarch" ${goarm:+GOARM="$goarm"} \
      go build -trimpath \
        -ldflags="-s -w -X github.com/towertail/sampler/internal/version.Version=$VERSION -X github.com/towertail/sampler/internal/version.SHA=$SHA" \
        -o "$dest" ./cmd/sampler
  )

  local size
  size="$(wc -c < "$dest" | tr -d ' ')"
  echo "  built $dest ($((size / 1024 / 1024)) MB)"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

BUILT=()
for row in "${TARGETS[@]}"; do
  # shellcheck disable=SC2086
  set -- $row
  triple="$1" goos="$2" goarch="$3" goarm="${4:-}"
  if [[ -n "$SINGLE" && "$SINGLE" != "$triple" ]]; then
    continue
  fi
  build_one "$triple" "$goos" "$goarch" "$goarm"
  BUILT+=("$triple")
done

if [[ ${#BUILT[@]} -eq 0 ]]; then
  echo "error: no target matched '$SINGLE'. valid triples:" >&2
  for row in "${TARGETS[@]}"; do
    set -- $row
    echo "  $1" >&2
  done
  exit 1
fi

# Write manifest.json: {"version": "...", "sha": "...", "binaries": {triple: sha256}}.
MANIFEST="$OUT_DIR/manifest.json"
{
  printf '{\n'
  printf '  "version": "%s",\n' "$VERSION"
  printf '  "sha": "%s",\n' "$SHA"
  printf '  "binaries": {\n'
  first=1
  for triple in "${BUILT[@]}"; do
    bin="$OUT_DIR/$triple/towertail-sampler"
    if [[ "$triple" == windows-* ]]; then
      bin="${bin}.exe"
    fi
    hash="$(sha256_of "$bin")"
    if [[ $first -eq 0 ]]; then printf ',\n'; fi
    printf '    "%s": "%s"' "$triple" "$hash"
    first=0
  done
  printf '\n  }\n}\n'
} > "$MANIFEST"

echo "manifest: $MANIFEST"
