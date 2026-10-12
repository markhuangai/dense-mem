#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
OUTPUT="${1:?Provide a new output directory}"
[[ ! -e "$OUTPUT" ]] || { printf 'Output already exists\n' >&2; exit 1; }
mkdir -p "$OUTPUT"
CONTAINER=""
cleanup() {
  if [[ -n "$CONTAINER" ]]; then docker rm -f "$CONTAINER" >/dev/null; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
EPOCH="$(git -C "$ROOT" show -s --format=%ct HEAD)"

run_case() {
  local version="$1" sdk="$2" destination="$3" status
  mkdir -p "$destination"
  CONTAINER="$(docker create --workdir /package \
    -e "SOURCE_DATE_EPOCH=$EPOCH" "python:$version-slim" \
    bash /package/scripts/check.sh --output /results --sdk "$sdk")"
  docker cp "$ROOT/packages/python/." "$CONTAINER:/package"
  if docker start --attach "$CONTAINER"; then
    status="$(docker inspect --format '{{.State.ExitCode}}' "$CONTAINER")"
  else
    status=$?
  fi
  docker cp "$CONTAINER:/results/." "$destination"
  docker inspect --format '{{json .Image}}' "$CONTAINER" > "$destination/image.json"
  docker rm "$CONTAINER" >/dev/null
  CONTAINER=""
  return "$status"
}
for version in 3.11 3.12 3.13 3.14; do
  run_case "$version" latest "$OUTPUT/$version"
done
run_case 3.12 minimum "$OUTPUT/minimum"
