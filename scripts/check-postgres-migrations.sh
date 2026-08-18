#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATIONS_ROOT="${ROOT_DIR}/migrations/postgres"
CURRENT_RELEASE="v2_5"
BASE_REF="${1:-origin/main}"
RC_REPAIR_VERSION="2026081701"
RC_REPAIR_BASE_SHA="c2647b9169d4bbc820caa4333ebbbef6f309942fe74cffdd647e37d5b8fc5757"
RC_REPAIR_CURRENT_SHA="4aef7058a8ec18f0af140eb6da3dcfa2bffd39f93fb9c587aa11eb449ff98af8"

cd "${ROOT_DIR}"
git rev-parse --verify "${BASE_REF}^{commit}" >/dev/null 2>&1 || {
  echo "base ref ${BASE_REF} is unavailable; fetch the current upstream base" >&2
  exit 1
}

merge_base="$(git merge-base HEAD "${BASE_REF}")"
declare -A base_sha_by_version=()
declare -A base_filename_by_version=()
declare -A base_path_by_version=()
declare -A current_sha_by_version=()
declare -A current_filename_by_version=()
declare -A current_path_by_version=()
declare -A current_release_by_version=()
base_max=0

validate_filename() {
  local filename="${1##*/}"
  [[ "${filename}" =~ ^([0-9]{10}|[0-9]{14})_[a-z0-9][a-z0-9_-]*\.sql$ ]] || {
    echo "malformed migration filename: $1" >&2
    exit 1
  }
  printf '%s\n' "${BASH_REMATCH[1]}"
}

has_goose_directive() {
  local directive="$1"
  local filename="$2"
  grep -Eiq -- "^--[[:space:]]+\\+goose[[:space:]]+${directive}[[:space:]]*$" "${filename}"
}

is_approved_rc_repair() {
  local version="$1"
  local base_sha="$2"
  local current_sha="$3"
  [[ "${version}" == "${RC_REPAIR_VERSION}" &&
     "${base_sha}" == "${RC_REPAIR_BASE_SHA}" &&
     "${current_sha}" == "${RC_REPAIR_CURRENT_SHA}" ]]
}

mapfile -t base_paths < <(
  git ls-tree -r --name-only "${merge_base}" -- migrations/postgres |
    grep -E '\.sql$' |
    awk -F/ '{print $NF "\t" $0}' |
    sort -n -k1,1 |
    cut -f2-
)
for repository_path in "${base_paths[@]}"; do
  version="$(validate_filename "${repository_path}")"
  if [[ -n "${base_sha_by_version[${version}]:-}" ]]; then
    echo "duplicate base migration version ${version}" >&2
    exit 1
  fi
  base_sha_by_version["${version}"]="$(git show "${merge_base}:${repository_path}" | sha256sum | awk '{print $1}')"
  base_filename_by_version["${version}"]="${repository_path##*/}"
  base_path_by_version["${version}"]="${repository_path}"
  if (( version > base_max )); then
    base_max="${version}"
  fi
done
(( base_max > 0 )) || { echo "base migration history is empty" >&2; exit 1; }

mapfile -t current_paths < <(
  find "${MIGRATIONS_ROOT}" -type f -name '*.sql' -printf '%f\t%p\n' |
    sort -n -k1,1 |
    cut -f2-
)
previous=0
for absolute_path in "${current_paths[@]}"; do
  repository_path="${absolute_path#"${ROOT_DIR}/"}"
  relative_path="${absolute_path#"${MIGRATIONS_ROOT}/"}"
  if [[ ! "${relative_path}" =~ ^(v[0-9]+_[0-9]+)/([^/]+)$ ]]; then
    echo "migration must be inside one versioned release directory: ${repository_path}" >&2
    exit 1
  fi
  release="${BASH_REMATCH[1]}"
  version="$(validate_filename "${repository_path}")"
  if [[ -n "${current_sha_by_version[${version}]:-}" ]]; then
    echo "duplicate migration version ${version}" >&2
    exit 1
  fi
  if (( version <= previous )); then
    echo "migration versions are not strictly increasing at ${repository_path}" >&2
    exit 1
  fi
  previous="${version}"
  current_sha_by_version["${version}"]="$(sha256sum "${absolute_path}" | awk '{print $1}')"
  current_filename_by_version["${version}"]="${absolute_path##*/}"
  current_path_by_version["${version}"]="${repository_path}"
  current_release_by_version["${version}"]="${release}"
  has_goose_directive Up "${absolute_path}" || { echo "${repository_path} is missing Goose Up" >&2; exit 1; }
  has_goose_directive Down "${absolute_path}" || { echo "${repository_path} is missing Goose Down" >&2; exit 1; }
done
(( ${#current_paths[@]} > 0 )) || { echo "migration history is empty" >&2; exit 1; }

for version in "${!base_sha_by_version[@]}"; do
  if [[ -z "${current_sha_by_version[${version}]:-}" ]]; then
    echo "deployed migration version ${version} was deleted or renamed" >&2
    exit 1
  fi
  if [[ "${base_sha_by_version[${version}]}" != "${current_sha_by_version[${version}]}" ]]; then
    # v2.5.1-rc.5 failed before applying on its only persistent deployment.
    # Exact hashes make this authorized repair one-use without weakening later checks.
    if ! is_approved_rc_repair "${version}" "${base_sha_by_version[${version}]}" "${current_sha_by_version[${version}]}"; then
      echo "deployed migration version ${version} was modified" >&2
      exit 1
    fi
    echo "approved release-candidate migration repair accepted for version ${version}"
  fi
  if [[ "${base_filename_by_version[${version}]}" != "${current_filename_by_version[${version}]}" ]]; then
    echo "deployed migration version ${version} was renamed" >&2
    exit 1
  fi
  if [[ "${base_path_by_version[${version}]}" != "${current_path_by_version[${version}]}" ]]; then
    initial_path="migrations/postgres/${base_filename_by_version[${version}]}"
    organized_path="migrations/postgres/v2_4/${base_filename_by_version[${version}]}"
    if [[ "${base_path_by_version[${version}]}" != "${initial_path}" || "${current_path_by_version[${version}]}" != "${organized_path}" ]]; then
      echo "deployed migration version ${version} was moved" >&2
      exit 1
    fi
  fi
done

for version in "${!current_sha_by_version[@]}"; do
  if [[ -n "${base_sha_by_version[${version}]:-}" ]]; then
    continue
  fi
  if (( version <= base_max )); then
    echo "new migration ${current_path_by_version[${version}]} does not advance beyond base version ${base_max}" >&2
    exit 1
  fi
  if [[ "${current_release_by_version[${version}]}" != "${CURRENT_RELEASE}" ]]; then
    echo "new migration ${current_path_by_version[${version}]} must be created under ${CURRENT_RELEASE}" >&2
    exit 1
  fi
  for marker in 'Lock/rewrite impact' 'RLS impact' 'Backfill' 'Backward compatibility' 'Rollback'; do
    grep -qi -- "${marker}" "${ROOT_DIR}/${current_path_by_version[${version}]}" || {
      echo "new migration ${current_path_by_version[${version}]} is missing ${marker} review metadata" >&2
      exit 1
    }
  done
done

if grep -RIn --include='*.sql' -E 'TODO:[[:space:]]*implement migration|FIXME:[[:space:]]*migration' "${MIGRATIONS_ROOT}" >/dev/null; then
  echo "unfinished migration review placeholder found" >&2
  exit 1
fi

echo "postgres migration history is immutable, ordered, and release-organized"
