#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATION_DIR="${ROOT_DIR}/migrations/postgres"
COMPOSE_FILE="${ROOT_DIR}/examples/docker-compose.base.yml"
SCHEMA_ROOT="${ROOT_DIR}/schema"
CATALOG_DIR="${SCHEMA_ROOT}/postgres"
MANIFEST_FILE="${CATALOG_DIR}/manifest.tsv"
RESTRICT_KEY='DenseMemSchemaCatalogV1'

container_name=''
candidate_dir=''
backup_dir=''
work_dir=''

fail() {
	printf 'PostgreSQL schema catalog failed: %s\n' "$1" >&2
	exit 1
}

safe_remove_tree() {
	local path="$1"
	case "${path}" in
		"${SCHEMA_ROOT}"/.postgres-schema-catalog.* | */dense-mem-schema-catalog.*)
			rm -rf -- "${path}"
			;;
		*)
			fail "refusing to remove unexpected temporary path: ${path}"
			;;
	esac
}

cleanup() {
	local status=$?
	if [[ -n "${container_name}" ]]; then
		docker rm -f "${container_name}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${candidate_dir}" && -d "${candidate_dir}" ]]; then
		safe_remove_tree "${candidate_dir}"
	fi
	if [[ -n "${backup_dir}" && -d "${backup_dir}" ]]; then
		if [[ ! -e "${CATALOG_DIR}" ]]; then
			mv -- "${backup_dir}" "${CATALOG_DIR}" || true
		else
			safe_remove_tree "${backup_dir}"
		fi
	fi
	if [[ -n "${work_dir}" && -d "${work_dir}" ]]; then
		safe_remove_tree "${work_dir}"
	fi
	return "${status}"
}
trap cleanup EXIT

require_command() {
	command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

configured_image() {
	awk '
		$1 == "postgres:" { in_postgres = 1; next }
		in_postgres && $1 == "image:" { print $2; exit }
	' "${COMPOSE_FILE}"
}

manifest_value() {
	local key="$1"
	local -a values=()
	mapfile -t values < <(awk -F '\t' -v key="${key}" '$1 == "meta" && $2 == key { print $3 }' "${MANIFEST_FILE}")
	[[ "${#values[@]}" -eq 1 && -n "${values[0]}" ]] || fail "manifest must contain one ${key} value"
	printf '%s\n' "${values[0]}"
}

resolve_goose_version() {
	local version
	version="$(cd "${ROOT_DIR}" && go list -m -f '{{.Version}}' github.com/pressly/goose/v3)" || \
		fail 'cannot resolve Goose version from go.mod'
	[[ "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || \
		fail "Goose module version is not pinned: ${version}"
	printf '%s\n' "${version}"
}

image_repository() {
	local image="${1%%@*}"
	local final_component="${image##*/}"
	if [[ "${final_component}" == *:* ]]; then
		image="${image%:*}"
	fi
	printf '%s\n' "${image}"
}

resolve_pulled_digest() {
	local image="$1"
	local expected_repository
	local -a digests=()
	mapfile -t digests < <(
		docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "${image}" |
			sed '/^[[:space:]]*$/d' |
			LC_ALL=C sort -u
	)
	[[ "${#digests[@]}" -eq 1 ]] || \
		fail "configured image must resolve to exactly one repository digest: ${image}"
	[[ "${digests[0]}" =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || \
		fail "resolved image digest is malformed: ${digests[0]}"
	expected_repository="$(image_repository "${image}")"
	[[ "${digests[0]%%@*}" == "${expected_repository}" ]] || \
		fail "resolved digest repository does not match ${expected_repository}: ${digests[0]}"
	printf '%s\n' "${digests[0]}"
}

latest_migration() {
	local latest=''
	local latest_version=0
	local path filename version
	shopt -s nullglob
	for path in "${MIGRATION_DIR}"/*.sql; do
		filename="${path##*/}"
		[[ "${filename}" =~ ^([0-9]+)_ ]] || fail "cannot derive migration version: ${filename}"
		version="${BASH_REMATCH[1]}"
		if ((10#${version} > 10#${latest_version})); then
			latest_version="${version}"
			latest="${filename}"
		fi
	done
	[[ -n "${latest}" ]] || fail 'no PostgreSQL migrations found'
	printf '%s\n' "${latest}"
}

tcp_ready() {
	local host="$1"
	local port="$2"
	timeout 2 bash -c 'exec 3<>"/dev/tcp/$1/$2"' bash "${host}" "${port}" >/dev/null 2>&1
}

discover_database_host() {
	local port="$1"
	local gateway=''
	local candidate attempt
	local -a candidates=()

	if [[ -n "${TESTCONTAINERS_HOST_OVERRIDE:-}" ]]; then
		candidates+=("${TESTCONTAINERS_HOST_OVERRIDE}")
	fi
	candidates+=('127.0.0.1' 'host.docker.internal')
	if command -v ip >/dev/null 2>&1; then
		gateway="$(ip route show default 2>/dev/null | awk 'NR == 1 { print $3 }')"
		if [[ -n "${gateway}" ]]; then
			candidates+=("${gateway}")
		fi
	fi

	for attempt in $(seq 1 30); do
		for candidate in "${candidates[@]}"; do
			if tcp_ready "${candidate}" "${port}"; then
				printf '%s\n' "${candidate}"
				return 0
			fi
		done
		sleep 1
	done
	return 1
}

wait_for_postgres() {
	local attempt
	for attempt in $(seq 1 60); do
		if docker exec "${container_name}" pg_isready --username postgres --dbname postgres >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

create_database() {
	docker exec "${container_name}" createdb --username postgres "$1"
}

restore_dump() {
	local database="$1"
	local input_file="$2"
	docker exec -i "${container_name}" psql -X --username postgres --dbname "${database}" \
		--set ON_ERROR_STOP=1 <"${input_file}" >/dev/null
}

dump_database() {
	local output_file="$1"
	local database="$2"
	shift 2
	docker exec "${container_name}" pg_dump \
		--dbname "${database}" \
		--username postgres \
		--schema-only \
		--no-owner \
		--no-privileges \
		--restrict-key="${RESTRICT_KEY}" \
		--exclude-table='public.goose_db_version' \
		"$@" |
		awk '
			/^[[:space:]]*$/ { pending = pending $0 ORS; next }
			{ printf "%s", pending; pending = ""; print }
		' >"${output_file}"
}

write_manifest() {
	local output_dir="$1"
	local configured="$2"
	local resolved="$3"
	local goose_version="$4"
	local latest="$5"
	local relative_path checksum

	{
		printf 'meta\tconfigured_image\t%s\n' "${configured}"
		printf 'meta\tresolved_image\t%s\n' "${resolved}"
		printf 'meta\tgoose_version\t%s\n' "${goose_version}"
		printf 'meta\tlatest_migration\t%s\n' "${latest}"
		while IFS= read -r relative_path; do
			checksum="$(sha256sum "${output_dir}/${relative_path}" | awk '{ print $1 }')"
			printf 'sha256\t%s\t%s\n' "${relative_path}" "${checksum}"
		done < <(
			cd "${output_dir}"
			find . -type f -name '*.sql' -printf '%P\n' | LC_ALL=C sort
		)
	} >"${output_dir}/manifest.tsv"
}

generate_catalog() {
	local configured="$1"
	local resolved="$2"
	local goose_version="$3"
	local latest="$4"
	local password port database_host dsn_host source_dsn
	local raw_dump parity_dump relation relation_file
	local -a relations=()
	local -a exclude_relations=()

	work_dir="$(mktemp -d "${TMPDIR:-/tmp}/dense-mem-schema-catalog.XXXXXX")"
	candidate_dir="$(mktemp -d "${SCHEMA_ROOT}/.postgres-schema-catalog.XXXXXX")"
	chmod 0755 "${candidate_dir}"
	mkdir -p "${candidate_dir}/relations"
	chmod 0755 "${candidate_dir}/relations"
	cp "${CATALOG_DIR}/README.md" "${candidate_dir}/README.md"

	password="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
	container_name="dense-mem-schema-catalog-$$-${RANDOM}"
	docker run --detach --rm \
		--name "${container_name}" \
		--env POSTGRES_PASSWORD="${password}" \
		--publish '127.0.0.1::5432/tcp' \
		"${resolved}" >/dev/null

	wait_for_postgres || fail 'disposable PostgreSQL did not become internally ready'
	port="$(docker port "${container_name}" 5432/tcp | awk -F ':' 'NR == 1 { print $NF }')"
	[[ "${port}" =~ ^[0-9]+$ ]] || fail "cannot resolve published PostgreSQL port: ${port}"
	database_host="$(discover_database_host "${port}")" || \
		fail "published PostgreSQL port ${port} is not reachable from the host"
	if [[ "${database_host}" == *:* ]]; then
		dsn_host="[${database_host}]"
	else
		dsn_host="${database_host}"
	fi

	create_database dense_mem_source
	create_database dense_mem_canonical
	create_database dense_mem_parity
	source_dsn="postgres://postgres:${password}@${dsn_host}:${port}/dense_mem_source?sslmode=disable"
	(
		cd "${ROOT_DIR}"
		GOOSE_DRIVER=postgres GOOSE_DBSTRING="${source_dsn}" \
			go run "github.com/pressly/goose/v3/cmd/goose@${goose_version}" \
			-dir "${MIGRATION_DIR}" up
	)

	raw_dump="${work_dir}/source.sql"
	parity_dump="${work_dir}/parity.sql"
	dump_database "${raw_dump}" dense_mem_source
	restore_dump dense_mem_canonical "${raw_dump}"
	dump_database "${candidate_dir}/current.sql" dense_mem_canonical
	restore_dump dense_mem_parity "${candidate_dir}/current.sql"
	dump_database "${parity_dump}" dense_mem_parity
	if ! cmp -s "${candidate_dir}/current.sql" "${parity_dump}"; then
		diff -u "${candidate_dir}/current.sql" "${parity_dump}" | sed -n '1,200p' >&2 || true
		fail 'current.sql is not stable after restore and redump'
	fi

	mapfile -t relations < <(
		docker exec "${container_name}" psql -X --username postgres --dbname dense_mem_canonical \
			--tuples-only --no-align --set ON_ERROR_STOP=1 --command "
				SELECT class.relname
				FROM pg_class AS class
				JOIN pg_namespace AS namespace ON namespace.oid = class.relnamespace
				WHERE namespace.nspname = 'public'
				  AND class.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
				  AND class.relname <> 'goose_db_version'
				ORDER BY class.relname::text COLLATE \"C\";
			"
	)
	(("${#relations[@]}" > 0)) || fail 'canonical database has no public relations'
	for relation in "${relations[@]}"; do
		[[ "${relation}" =~ ^[a-z_][a-z0-9_]*$ ]] || \
			fail "public relation name is not catalog-file safe: ${relation}"
		exclude_relations+=("--exclude-table=public.${relation}")
		relation_file="${candidate_dir}/relations/public.${relation}.sql"
		dump_database "${relation_file}" dense_mem_canonical "--table=public.${relation}"
	done
	dump_database "${candidate_dir}/global.sql" dense_mem_canonical "${exclude_relations[@]}"

	write_manifest "${candidate_dir}" "${configured}" "${resolved}" "${goose_version}" "${latest}"
}

if [[ "$#" -ne 1 || ( "$1" != 'write' && "$1" != 'check' ) ]]; then
	printf 'Usage: %s write|check\n' "${0##*/}" >&2
	exit 2
fi
mode="$1"

require_command awk
require_command cmp
require_command diff
require_command docker
require_command go
require_command od
require_command sha256sum
require_command timeout
[[ -f "${COMPOSE_FILE}" ]] || fail "missing ${COMPOSE_FILE#${ROOT_DIR}/}"
[[ -d "${MIGRATION_DIR}" ]] || fail 'missing migrations/postgres'
[[ -f "${CATALOG_DIR}/README.md" ]] || fail 'schema/postgres/README.md is required'
mkdir -p "${SCHEMA_ROOT}"

configured="$(configured_image)"
[[ -n "${configured}" ]] || fail 'cannot read the PostgreSQL image from examples/docker-compose.base.yml'
goose_version="$(resolve_goose_version)"
latest="$(latest_migration)"

if [[ "${mode}" == 'write' ]]; then
	docker pull "${configured}" >/dev/null
	resolved="$(resolve_pulled_digest "${configured}")"
else
	[[ -f "${MANIFEST_FILE}" ]] || fail 'schema catalog manifest is missing; run write first'
	recorded_configured="$(manifest_value configured_image)"
	[[ "${recorded_configured}" == "${configured}" ]] || \
		fail "configured image changed from ${recorded_configured} to ${configured}; run write"
	resolved="$(manifest_value resolved_image)"
	[[ "${resolved}" =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || \
		fail "manifest image digest is malformed: ${resolved}"
	[[ "${resolved%%@*}" == "$(image_repository "${configured}")" ]] || \
		fail "manifest image digest does not belong to configured image: ${resolved}"
	docker pull "${resolved}" >/dev/null
fi

generate_catalog "${configured}" "${resolved}" "${goose_version}" "${latest}"

if [[ "${mode}" == 'check' ]]; then
	if ! diff -ruN "${CATALOG_DIR}" "${candidate_dir}"; then
		fail 'committed schema catalog is stale; run postgres-schema-catalog.sh write'
	fi
	printf 'PostgreSQL schema catalog is current.\n'
	exit 0
fi

backup_dir="${SCHEMA_ROOT}/.postgres-schema-catalog.backup.$$"
[[ ! -e "${backup_dir}" ]] || fail "catalog backup path already exists: ${backup_dir}"
mv -- "${CATALOG_DIR}" "${backup_dir}"
if ! mv -- "${candidate_dir}" "${CATALOG_DIR}"; then
	mv -- "${backup_dir}" "${CATALOG_DIR}" || true
	backup_dir=''
	fail 'cannot install generated schema catalog'
fi
candidate_dir=''
safe_remove_tree "${backup_dir}"
backup_dir=''
printf 'Updated schema/postgres using %s.\n' "${resolved}"
