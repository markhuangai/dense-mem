#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATION_DIR_REL='migrations/postgres'
MIGRATION_DIR="${ROOT_DIR}/${MIGRATION_DIR_REL}"

fail() {
	printf 'PostgreSQL migration check failed: %s\n' "$1" >&2
	exit 1
}

if [[ "$#" -gt 1 ]]; then
	printf 'Usage: %s [base-ref]\n' "${0##*/}" >&2
	exit 2
fi

cd "${ROOT_DIR}"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail 'repository Git metadata is unavailable'
[[ -d "${MIGRATION_DIR}" ]] || fail "missing ${MIGRATION_DIR_REL}"

base_ref="${1:-${DENSE_MEM_MIGRATION_BASE_REF:-origin/main}}"
[[ -n "${base_ref}" ]] || fail 'base ref is empty'
if [[ "${base_ref}" =~ ^0+$ ]]; then
	fail "base ref is an all-zero sentinel: ${base_ref}"
fi
base_commit="$(git rev-parse --verify --quiet "${base_ref}^{commit}")" || \
	fail "base ref is not a commit: ${base_ref}"
base_mode="${DENSE_MEM_MIGRATION_BASE_MODE:-merge-base}"
case "${base_mode}" in
	merge-base)
		comparison_base="$(git merge-base HEAD "${base_commit}")" || \
			fail "cannot compute merge base with ${base_ref}"
		[[ -n "${comparison_base}" ]] || fail "merge base with ${base_ref} is empty"
		;;
	exact)
		comparison_base="${base_commit}"
		;;
	*)
		fail "unsupported base mode: ${base_mode}"
		;;
esac

declare -A base_paths=()
base_max=0
while IFS= read -r -d '' entry; do
	metadata="${entry%%$'\t'*}"
	path="${entry#*$'\t'}"
	read -r mode object_type object_id <<<"${metadata}"
	[[ "${object_type}" == 'blob' ]] || fail "base migration is not a blob: ${path}"
	[[ "${mode}" == '100644' ]] || fail "base migration has unsupported mode ${mode}: ${path}"
	mapfile -t index_entries < <(git ls-files --stage -- "${path}")
	[[ "${#index_entries[@]}" -eq 1 ]] || fail "base migration index entry changed: ${path}"
	index_metadata="${index_entries[0]%%$'\t'*}"
	read -r index_mode index_id index_stage <<<"${index_metadata}"
	[[ "${index_stage}" == '0' && "${index_mode}" == "${mode}" && "${index_id}" == "${object_id}" ]] || \
		fail "base migration index content or mode changed: ${path}"
	[[ -f "${path}" && ! -L "${path}" ]] || fail "base migration was deleted or renamed: ${path}"
	[[ ! -x "${path}" ]] || fail "base migration mode changed: ${path}"
	current_id="$(git hash-object -- "${path}")" || fail "cannot hash ${path}"
	[[ "${current_id}" == "${object_id}" ]] || fail "base migration content changed: ${path}"
	base_paths["${path}"]=1

	filename="${path##*/}"
	if [[ ! "${filename}" =~ ^([0-9]{10}|[0-9]{14})_[a-z0-9]+(_[a-z0-9]+)*\.sql$ ]]; then
		fail "base migration filename is malformed: ${filename}"
	fi
	version="${BASH_REMATCH[1]}"
	if ((10#${version} > 10#${base_max})); then
		base_max="${version}"
	fi
done < <(git ls-tree -rz "${comparison_base}" -- "${MIGRATION_DIR_REL}")

mapfile -d '' migration_paths < <(
	find "${MIGRATION_DIR}" -maxdepth 1 -type f -name '*.sql' -print0 | LC_ALL=C sort -z
)
(("${#migration_paths[@]}" > 0)) || fail 'no PostgreSQL migrations found'

mapfile -d '' migration_symlinks < <(
	find "${MIGRATION_DIR}" -maxdepth 1 -type l -name '*.sql' -print0 | LC_ALL=C sort -z
)
(("${#migration_symlinks[@]}" == 0)) || fail 'migration symlinks are not allowed'

declare -A seen_versions=()
new_count=0
for absolute_path in "${migration_paths[@]}"; do
	path="${absolute_path#${ROOT_DIR}/}"
	filename="${absolute_path##*/}"
	if [[ ! "${filename}" =~ ^([0-9]{10}|[0-9]{14})_[a-z0-9]+(_[a-z0-9]+)*\.sql$ ]]; then
		fail "migration filename is malformed: ${filename}"
	fi
	version="${BASH_REMATCH[1]}"
	if [[ -n "${seen_versions[${version}]+present}" ]]; then
		fail "duplicate migration version: ${version}"
	fi
	seen_versions["${version}"]="${path}"
	[[ ! -x "${absolute_path}" ]] || fail "migration must use mode 0644: ${path}"

	if grep -q 'MIGRATION_REVIEW_REQUIRED' "${absolute_path}"; then
		fail "unfinished migration review marker: ${path}"
	fi

	if [[ -z "${base_paths[${path}]+present}" ]]; then
		new_count=$((new_count + 1))
		[[ "${#version}" -eq 14 ]] || fail "new migration must use a 14-digit UTC version: ${path}"
		if ((10#${version} <= 10#${base_max})); then
			fail "new migration ${version} is not greater than base maximum ${base_max}: ${path}"
		fi
	fi
done

goose_version="$(go list -m -f '{{.Version}}' github.com/pressly/goose/v3)" || \
	fail 'cannot resolve Goose version from go.mod'
[[ "${goose_version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || \
	fail "Goose module version is not pinned: ${goose_version}"

go run "github.com/pressly/goose/v3/cmd/goose@${goose_version}" \
	-dir "${MIGRATION_DIR}" validate || fail 'Goose validation failed'

printf 'PostgreSQL migration history is valid at %s (%d new migration(s)).\n' \
	"${comparison_base}" "${new_count}"
