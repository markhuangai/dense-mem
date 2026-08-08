#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATION_DIR="${ROOT_DIR}/migrations/postgres"

usage() {
	printf 'Usage: %s <snake_case_name>\n' "${0##*/}" >&2
}

if [[ "$#" -ne 1 ]]; then
	usage
	exit 2
fi

name="$1"
if [[ ! "${name}" =~ ^[a-z][a-z0-9]*(_[a-z0-9]+)*$ ]]; then
	printf 'Migration name must be lower snake case: %s\n' "${name}" >&2
	exit 2
fi

if [[ ! -d "${MIGRATION_DIR}" ]]; then
	printf 'Migration directory does not exist: %s\n' "${MIGRATION_DIR}" >&2
	exit 1
fi

latest_version=0
shopt -s nullglob
for migration_path in "${MIGRATION_DIR}"/*.sql; do
	filename="${migration_path##*/}"
	if [[ ! "${filename}" =~ ^([0-9]+)_ ]]; then
		printf 'Cannot derive version from existing migration: %s\n' "${filename}" >&2
		exit 1
	fi
	version="${BASH_REMATCH[1]}"
	if ((10#${version} > 10#${latest_version})); then
		latest_version="${version}"
	fi
done

version="$(date -u +%Y%m%d%H%M%S)"
if ((10#${version} <= 10#${latest_version})); then
	printf 'UTC version %s is not greater than repository maximum %s; check the clock or rebase.\n' \
		"${version}" "${latest_version}" >&2
	exit 1
fi

migration_path="${MIGRATION_DIR}/${version}_${name}.sql"
if [[ -e "${migration_path}" ]]; then
	printf 'Migration already exists: %s\n' "${migration_path}" >&2
	exit 1
fi

created=false
cleanup() {
	if [[ "${created}" == true ]]; then
		rm -f -- "${migration_path}"
	fi
}
trap cleanup EXIT

set -o noclobber
: >"${migration_path}"
set +o noclobber
created=true

cat >"${migration_path}" <<'EOF'
-- +goose Up

-- MIGRATION_REVIEW_REQUIRED: Lock/rewrite impact and representative-data analysis.
-- MIGRATION_REVIEW_REQUIRED: RLS, role, and tenant-isolation impact.
-- MIGRATION_REVIEW_REQUIRED: Backfill plan, including bounds and retry behavior.
-- MIGRATION_REVIEW_REQUIRED: Compatibility with the previous application release.
-- MIGRATION_REVIEW_REQUIRED: Up migration SQL.

-- +goose Down

-- MIGRATION_REVIEW_REQUIRED: Controlled rollback SQL or explicit irreversible boundary.
EOF
chmod 0644 "${migration_path}"
created=false

printf '%s\n' "${migration_path#${ROOT_DIR}/}"
