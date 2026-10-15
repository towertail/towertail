#!/usr/bin/env bash
# Get the towertail-sampler binaries of the release that samplers.lock pins.
#
# Usage: scripts/fetch-samplers.sh [triple ...]
#   No triple gets all triples in the release manifest.
#
# Output: dist/samplers/<triple>/towertail-sampler[.exe], dist/samplers/manifest.json,
# dist/samplers/sample-v1.json.
#
# The script checks manifest.json and sample-v1.json against the sha256 values in
# samplers.lock, and each binary against the manifest. It does not download a
# file again when its sha256 is correct. On a mismatch after a download, it
# deletes the file and exits with code 1.
#
# Env:
#   SAMPLER_DIR  path to a towertail/sampler clone. When set, the script builds
#                the binaries from that clone with its scripts/build.sh and
#                downloads nothing.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="$REPO_ROOT/dist/samplers"

# shellcheck source=SCRIPTDIR/../samplers.lock
. "$REPO_ROOT/samplers.lock"
: "${SAMPLER_VERSION:?missing in samplers.lock}"
: "${SAMPLER_REPO:?missing in samplers.lock}"
: "${SAMPLER_MANIFEST_SHA256:?missing in samplers.lock}"
: "${SAMPLER_FIXTURE_SHA256:?missing in samplers.lock}"

mkdir -p "$OUT_DIR"

if [[ -n "${SAMPLER_DIR:-}" ]]; then
  echo "→ building samplers from $SAMPLER_DIR"
  exec "$SAMPLER_DIR/scripts/build.sh" --out "$OUT_DIR" "$@"
fi

BASE_URL="https://github.com/$SAMPLER_REPO/releases/download/$SAMPLER_VERSION"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# fetch NAME DEST SHA256: download NAME to DEST unless DEST already has SHA256.
fetch() {
  local name="$1" dest="$2" want="$3" got
  if [[ -f "$dest" && "$(sha256_of "$dest")" == "$want" ]]; then
    echo "  ok      $name"
    return
  fi
  mkdir -p "$(dirname "$dest")"
  curl -fsSL --retry 3 -o "$dest.part" "$BASE_URL/$name"
  got="$(sha256_of "$dest.part")"
  if [[ "$got" != "$want" ]]; then
    rm -f "$dest.part" "$dest"
    echo "error: sha256 mismatch for $name: want $want, got $got" >&2
    exit 1
  fi
  mv -f "$dest.part" "$dest"
  echo "  fetched $name"
}

echo "→ $SAMPLER_REPO $SAMPLER_VERSION"
MANIFEST="$OUT_DIR/manifest.json"
fetch manifest.json "$MANIFEST" "$SAMPLER_MANIFEST_SHA256"

# The manifest has one '"<triple>": "<sha256>"' line for each binary.
manifest_sha() {
  sed -n "s/^ *\"$1\": *\"\([0-9a-f]\{64\}\)\".*/\1/p" "$MANIFEST"
}

TRIPLES=("$@")
if [[ ${#TRIPLES[@]} -eq 0 ]]; then
  while read -r t; do TRIPLES+=("$t"); done < <(
    sed -n 's/^ *"\([a-z0-9]*-[a-z0-9]*\)": *"[0-9a-f]\{64\}".*/\1/p' "$MANIFEST")
fi

for triple in "${TRIPLES[@]}"; do
  want="$(manifest_sha "$triple")"
  if [[ -z "$want" ]]; then
    echo "error: $triple is not in the $SAMPLER_VERSION manifest" >&2
    exit 1
  fi
  ext=""
  [[ "$triple" == windows-* ]] && ext=".exe"
  dest="$OUT_DIR/$triple/towertail-sampler$ext"
  fetch "towertail-sampler-$triple$ext" "$dest" "$want"
  chmod +x "$dest"
done

fetch sample-v1.json "$OUT_DIR/sample-v1.json" "$SAMPLER_FIXTURE_SHA256"
