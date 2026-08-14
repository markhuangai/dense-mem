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
declare -A base_sha_by_version=()
declare -A base_filename_by_version=()
declare -A current_sha_by_version=()
declare -A current_filename_by_version=()
declare -A current_path_by_version=()
base_max=0

validate_filename() {
  local path="$1"
  local filename="${path##*/}"
  [[ "${filename}" =~ ^([0-9]{10}|[0-9]{14})_[a-z0-9][a-z0-9_-]*\.sql$ ]] || {
    echo "malformed migration filename: ${path#"${ROOT_DIR}/migrations/postgres/"}" >&2
    exit 1
  }
  printf '%s\n' "${BASH_REMATCH[1]}"
}

mapfile -t base_paths < <(
  git ls-tree -r --name-only "${merge_base}" -- migrations/postgres |
    grep -E '\.sql$' |
    awk -F/ '{print $NF "\t" $0}' |
    sort -n -k1,1 |
    cut -f2-
)
for path in "${base_paths[@]}"; do
  version="$(validate_filename "${path}")"
  if [[ -n "${base_sha_by_version[${version}]:-}" ]]; then
    echo "duplicate base migration version ${version}" >&2
    exit 1
  fi
  base_sha_by_version["${version}"]="$(git show "${merge_base}:${path}" | sha256sum | awk '{print $1}')"
  base_filename_by_version["${version}"]="${path##*/}"
  if (( version > base_max )); then
    base_max="${version}"
  fi
done
[[ "${base_max}" -gt 0 ]] || { echo "base migration history is empty" >&2; exit 1; }

mapfile -t current_paths < <(
  find "${MIGRATIONS_DIR}" -type f -name '*.sql' -printf '%f\t%p\n' |
    sort -n -k1,1 |
    cut -f2-
)
previous=0
for path in "${current_paths[@]}"; do
  version="$(validate_filename "${path}")"
  if [[ -n "${current_sha_by_version[${version}]:-}" ]]; then
    echo "duplicate migration version ${version}" >&2
    exit 1
  fi
  if (( version <= previous )); then
    echo "migration versions are not strictly increasing at ${path#"${ROOT_DIR}/"}" >&2
    exit 1
  fi
  previous="${version}"
  current_sha_by_version["${version}"]="$(sha256sum "${path}" | awk '{print $1}')"
  current_filename_by_version["${version}"]="${path##*/}"
  current_path_by_version["${version}"]="${path}"
  grep -q -- '-- +goose Up' "${path}" || { echo "${path#"${ROOT_DIR}/"} is missing Goose Up" >&2; exit 1; }
  grep -q -- '-- +goose Down' "${path}" || { echo "${path#"${ROOT_DIR}/"} is missing Goose Down" >&2; exit 1; }
done
[[ "${#current_paths[@]}" -gt 0 ]] || { echo "migration history is empty" >&2; exit 1; }

for version in "${!base_sha_by_version[@]}"; do
  if [[ -z "${current_sha_by_version[${version}]:-}" ]]; then
    echo "deployed migration version ${version} was deleted or renamed" >&2
    exit 1
  fi
  if [[ "${base_sha_by_version[${version}]}" != "${current_sha_by_version[${version}]}" ]]; then
    echo "deployed migration version ${version} was modified" >&2
    exit 1
  fi
  if [[ "${base_filename_by_version[${version}]}" != "${current_filename_by_version[${version}]}" ]]; then
    echo "deployed migration version ${version} was renamed" >&2
    exit 1
  fi
done

for version in "${!current_sha_by_version[@]}"; do
  if [[ -n "${base_sha_by_version[${version}]:-}" ]]; then
    continue
  fi
  if (( version <= base_max )); then
    echo "new migration ${current_path_by_version[${version}]#"${ROOT_DIR}/"} does not advance beyond base version ${base_max}" >&2
    exit 1
  fi
  relative="${current_path_by_version[${version}]#"${MIGRATIONS_DIR}/"}"
  [[ "${relative}" == */* ]] || {
    echo "new migration ${current_path_by_version[${version}]#"${ROOT_DIR}/"} must be inside a versioned directory" >&2
    exit 1
  }
  for marker in 'Lock/rewrite' 'RLS impact' 'Backfill' 'Rollback'; do
    grep -qi -- "${marker}" "${current_path_by_version[${version}]}" || {
      echo "new migration ${current_path_by_version[${version}]#"${ROOT_DIR}/"} is missing ${marker} review metadata" >&2
      exit 1
    }
  done
done

if grep -RIn --include='*.sql' -E 'TODO[[:space:]]+migration|FIXME[[:space:]]+migration' "${MIGRATIONS_DIR}" >/dev/null; then
  echo "unfinished migration review placeholder found" >&2
  exit 1
fi
echo "postgres migration history is immutable, ordered, and release-organized"
