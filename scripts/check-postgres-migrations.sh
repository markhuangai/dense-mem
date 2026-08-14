#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATIONS_DIR="${ROOT_DIR}/migrations/postgres"
BASE_REF="${1:-origin/main}"
cd "${ROOT_DIR}"

git rev-parse --verify "${BASE_REF}^{commit}" >/dev/null 2>&1 || {
  echo "base ref ${BASE_REF} is unavailable; fetch the current upstream base" >&2
  exit 1
}

merge_base="$(git merge-base HEAD "${BASE_REF}")"
base_max="$(git ls-tree -r --name-only "${merge_base}" -- migrations/postgres | sed -nE 's#^.*/([0-9]{10,14})_.*\.sql$#\1#p' | sort -n | tail -1)"
[[ -n "${base_max}" ]] || { echo "base migration history is empty" >&2; exit 1; }

declare -A seen=()
previous=0
while IFS= read -r path; do
  filename="${path##*/}"
  if [[ ! "${filename}" =~ ^([0-9]{10}|[0-9]{14})_[a-z0-9][a-z0-9_-]*\.sql$ ]]; then
    echo "malformed migration filename: ${filename}" >&2
    exit 1
  fi
  version="${BASH_REMATCH[1]}"
  if [[ -n "${seen[$version]:-}" ]]; then
    echo "duplicate migration version ${version}" >&2
    exit 1
  fi
  seen[$version]="${filename}"
  if (( version <= previous )); then
    echo "migration versions are not strictly increasing at ${filename}" >&2
    exit 1
  fi
  previous="${version}"
  grep -q -- '-- +goose Up' "${MIGRATIONS_DIR}/${filename}" || { echo "${filename} is missing Goose Up" >&2; exit 1; }
  grep -q -- '-- +goose Down' "${MIGRATIONS_DIR}/${filename}" || { echo "${filename} is missing Goose Down" >&2; exit 1; }
done < <(find "${MIGRATIONS_DIR}" -maxdepth 1 -type f -name '*.sql' -printf '%f\n' | sort -n)

while IFS=$'\t' read -r status old new; do
  case "${status}" in
    M|D|R*)
      echo "deployed migration history changed: ${old}${new:+ -> ${new}}" >&2
      exit 1
      ;;
    A)
      added="${new:-$old}"
      version="${added%%_*}"
      if (( version <= base_max )); then
        echo "new migration ${added} does not advance beyond base version ${base_max}" >&2
        exit 1
      fi
      for marker in 'Lock/rewrite' 'RLS impact' 'Backfill' 'Rollback'; do
        grep -qi -- "${marker}" "${ROOT_DIR}/${added}" || { echo "new migration ${added} is missing ${marker} review metadata" >&2; exit 1; }
      done
      ;;
  esac
done < <(git diff --find-renames --name-status "${merge_base}" -- migrations/postgres)

if grep -RIn --exclude='*.md' -E 'TODO[[:space:]]+migration|FIXME[[:space:]]+migration' "${MIGRATIONS_DIR}" >/dev/null; then
  echo "unfinished migration review placeholder found" >&2
  exit 1
fi
echo "postgres migration history is immutable and ordered"
