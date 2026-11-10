#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
OUTPUT="${1:?Provide a new output directory}"
if [[ $# != 1 && $# != 3 ]]; then
  printf 'Usage: matrix.sh OUTPUT [PYTHON SDK]\n' >&2
  exit 2
fi
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
  printf 'Checking Python %s (%s SDK)\n' "$version" "$sdk"
  mkdir -p "$destination"
  CONTAINER="$(docker create --workdir /workspace/packages/python \
    -e "SOURCE_DATE_EPOCH=$EPOCH" "python:$version-slim" \
    bash -c 'set -euo pipefail
      apt-get update -qq
      apt-get install -y --no-install-recommends git
      exec bash /workspace/packages/python/scripts/check.sh --output /results --sdk "$1"
    ' -- "$sdk")"
  docker cp "$ROOT/packages/python/." "$CONTAINER:/workspace/packages/python"
  docker cp "$ROOT/dependency-exceptions.json" "$CONTAINER:/workspace/dependency-exceptions.json"
  if docker start --attach "$CONTAINER"; then
    status="$(docker inspect --format '{{.State.ExitCode}}' "$CONTAINER")"
  else
    status=$?
  fi
  docker cp "$CONTAINER:/results/." "$destination"
  docker inspect --format '{{json .Image}}' "$CONTAINER" > "$destination/image.json"
  docker rm "$CONTAINER" >/dev/null
  CONTAINER=""
  printf 'Python %s (%s SDK) completed with status %s\n' "$version" "$sdk" "$status"
  return "$status"
}
if [[ $# == 3 ]]; then
  run_case "$2" "$3" "$OUTPUT/$2-$3"
  exit
fi
for version in 3.11 3.12 3.13 3.14; do
  run_case "$version" latest "$OUTPUT/$version"
done
run_case 3.12 minimum "$OUTPUT/minimum"
