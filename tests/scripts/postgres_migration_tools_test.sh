#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMP_DIR="$(mktemp -d)"
trap 'rm -rf -- "${TEMP_DIR}"' EXIT

fail() {
	printf 'FAIL: %s\n' "$1" >&2
	exit 1
}

expect_failure() {
	local label="$1"
	shift
	if "$@" >/dev/null 2>&1; then
		fail "${label}: command unexpectedly succeeded"
	fi
}

for ci_path in \
	"${ROOT_DIR}/.github/workflows/ci-shared.yml" \
	"${ROOT_DIR}/.github/workflows/ci-pr.yml" \
	"${ROOT_DIR}/.github/workflows/ci-push.yml" \
	"${ROOT_DIR}/scripts/ci-check.sh"; do
	if grep -Eq -- '(-tags=integration|postgres-schema-catalog\.sh (write|check)|docker (run|compose))' "${ci_path}"; then
		fail "CI must not provision PostgreSQL: ${ci_path#${ROOT_DIR}/}"
	fi
done

write_migration() {
	local path="$1"
	local body="${2:-SELECT 1;}"
	printf '%s\n' \
		'-- +goose Up' \
		"${body}" \
		'' \
		'-- +goose Down' \
		'SELECT 1;' >"${path}"
}

reset_history_fixture() {
	git -C "${HISTORY_REPO}" restore --source="${HISTORY_BASE}" --staged --worktree migrations/postgres
	find "${HISTORY_REPO}/migrations/postgres" -maxdepth 1 -type f \
		! -name '2026080803_legacy.sql' \
		! -name '20260808030000_current.sql' -delete
}

HISTORY_REPO="${TEMP_DIR}/history"
FAKE_BIN="${TEMP_DIR}/bin"
mkdir -p "${HISTORY_REPO}/migrations/postgres" "${HISTORY_REPO}/scripts" "${FAKE_BIN}"
cp "${ROOT_DIR}/scripts/check-postgres-migrations.sh" "${HISTORY_REPO}/scripts/"
write_migration "${HISTORY_REPO}/migrations/postgres/2026080803_legacy.sql"
write_migration "${HISTORY_REPO}/migrations/postgres/20260808030000_current.sql"

cat >"${HISTORY_REPO}/go.mod" <<'EOF'
module example.com/migration-history-fixture

go 1.26

require github.com/pressly/goose/v3 v3.24.0
EOF

cat >"${FAKE_BIN}/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
	list)
		printf 'v3.24.0\n'
		;;
	run)
		if grep -Rqs 'INVALID_GOOSE' migrations/postgres; then
			exit 1
		fi
		;;
	*)
		exit 2
		;;
esac
EOF
chmod 0755 "${FAKE_BIN}/go" "${HISTORY_REPO}/scripts/check-postgres-migrations.sh"

git -C "${HISTORY_REPO}" init --quiet --initial-branch=main
git -C "${HISTORY_REPO}" config user.name 'Dense-Mem Test'
git -C "${HISTORY_REPO}" config user.email 'dense-mem-test@example.invalid'
git -C "${HISTORY_REPO}" add go.mod migrations/postgres
git -C "${HISTORY_REPO}" commit --quiet -m 'fixture base'
HISTORY_BASE="$(git -C "${HISTORY_REPO}" rev-parse HEAD)"

(
	cd "${HISTORY_REPO}"
	PATH="${FAKE_BIN}:${PATH}" ./scripts/check-postgres-migrations.sh "${HISTORY_BASE}"
)

printf '\n-- edited\n' >>"${HISTORY_REPO}/migrations/postgres/2026080803_legacy.sql"
expect_failure 'historical edit' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

printf '\n-- staged edit\n' >>"${HISTORY_REPO}/migrations/postgres/2026080803_legacy.sql"
git -C "${HISTORY_REPO}" add migrations/postgres/2026080803_legacy.sql
git -C "${HISTORY_REPO}" restore --source="${HISTORY_BASE}" --worktree \
	migrations/postgres/2026080803_legacy.sql
expect_failure 'staged-only historical edit' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

git -C "${HISTORY_REPO}" rm --cached --quiet migrations/postgres/2026080803_legacy.sql
expect_failure 'staged-only historical deletion' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

rm "${HISTORY_REPO}/migrations/postgres/2026080803_legacy.sql"
expect_failure 'historical deletion' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

mv "${HISTORY_REPO}/migrations/postgres/2026080803_legacy.sql" \
	"${HISTORY_REPO}/migrations/postgres/2026080804_renamed.sql"
expect_failure 'historical rename' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

chmod 0755 "${HISTORY_REPO}/migrations/postgres/2026080803_legacy.sql"
expect_failure 'historical mode change' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

write_migration "${HISTORY_REPO}/migrations/postgres/not_a_version.sql"
expect_failure 'malformed filename' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

write_migration "${HISTORY_REPO}/migrations/postgres/20260808030000_duplicate.sql"
expect_failure 'duplicate version' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

write_migration "${HISTORY_REPO}/migrations/postgres/20260808020000_out_of_order.sql"
expect_failure 'out-of-order addition' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

write_migration "${HISTORY_REPO}/migrations/postgres/20990101000001_unfinished.sql" \
	'MIGRATION_REVIEW_REQUIRED'
expect_failure 'unfinished review marker' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

write_migration "${HISTORY_REPO}/migrations/postgres/20990101000002_invalid_goose.sql" \
	'INVALID_GOOSE'
expect_failure 'Goose validation failure' env PATH="${FAKE_BIN}:${PATH}" \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_BASE}"
reset_history_fixture

write_migration "${HISTORY_REPO}/migrations/postgres/20990101000003_valid.sql"
(
	cd "${HISTORY_REPO}"
	PATH="${FAKE_BIN}:${PATH}" DENSE_MEM_MIGRATION_BASE_REF="${HISTORY_BASE}" \
		./scripts/check-postgres-migrations.sh
)

reset_history_fixture
write_migration "${HISTORY_REPO}/migrations/postgres/20260808040000_old_tip.sql"
git -C "${HISTORY_REPO}" add migrations/postgres/20260808040000_old_tip.sql
git -C "${HISTORY_REPO}" commit --quiet -m 'fixture old main tip'
HISTORY_OLD_TIP="$(git -C "${HISTORY_REPO}" rev-parse HEAD)"
git -C "${HISTORY_REPO}" switch --quiet --detach "${HISTORY_BASE}"
(
	cd "${HISTORY_REPO}"
	PATH="${FAKE_BIN}:${PATH}" ./scripts/check-postgres-migrations.sh "${HISTORY_OLD_TIP}"
)
expect_failure 'exact prior tip deletion' env PATH="${FAKE_BIN}:${PATH}" \
	DENSE_MEM_MIGRATION_BASE_MODE=exact \
	"${HISTORY_REPO}/scripts/check-postgres-migrations.sh" "${HISTORY_OLD_TIP}"

SCAFFOLD_REPO="${TEMP_DIR}/scaffold"
mkdir -p "${SCAFFOLD_REPO}/migrations/postgres" "${SCAFFOLD_REPO}/scripts"
cp "${ROOT_DIR}/scripts/new-postgres-migration.sh" "${SCAFFOLD_REPO}/scripts/"
write_migration "${SCAFFOLD_REPO}/migrations/postgres/2026080803_base.sql"
(
	cd "${SCAFFOLD_REPO}"
	./scripts/new-postgres-migration.sh fresh_change >/dev/null
)

mapfile -t scaffolded < <(find "${SCAFFOLD_REPO}/migrations/postgres" -maxdepth 1 \
	-type f -name '??????????????_fresh_change.sql' -print)
[[ "${#scaffolded[@]}" -eq 1 ]] || fail 'scaffold did not create exactly one 14-digit migration'
grep -q '^-- +goose Up$' "${scaffolded[0]}" || fail 'scaffold is missing Goose Up annotation'
grep -q '^-- +goose Down$' "${scaffolded[0]}" || fail 'scaffold is missing Goose Down annotation'
grep -q 'MIGRATION_REVIEW_REQUIRED' "${scaffolded[0]}" || fail 'scaffold is missing review markers'
[[ "$(stat -c '%a' "${scaffolded[0]}")" == '644' ]] || fail 'scaffold mode is not 0644'

expect_failure 'invalid scaffold name' \
	"${SCAFFOLD_REPO}/scripts/new-postgres-migration.sh" 'Invalid-Name'
write_migration "${SCAFFOLD_REPO}/migrations/postgres/99999999999999_future.sql"
expect_failure 'clock behind repository maximum' \
	"${SCAFFOLD_REPO}/scripts/new-postgres-migration.sh" 'clock_guard'

CATALOG_DIR="${ROOT_DIR}/schema/postgres"
[[ -f "${CATALOG_DIR}/current.sql" ]] || fail 'catalog current.sql is missing'
[[ -f "${CATALOG_DIR}/global.sql" ]] || fail 'catalog global.sql is missing'
[[ -f "${CATALOG_DIR}/manifest.tsv" ]] || fail 'catalog manifest.tsv is missing'
[[ -d "${CATALOG_DIR}/relations" ]] || fail 'catalog relations directory is missing'

catalog_meta() {
	local key="$1"
	local -a values=()
	mapfile -t values < <(
		awk -F '\t' -v key="${key}" '$1 == "meta" && $2 == key { print $3 }' \
			"${CATALOG_DIR}/manifest.tsv"
	)
	[[ "${#values[@]}" -eq 1 && -n "${values[0]}" ]] || fail "manifest metadata differs: ${key}"
	printf '%s\n' "${values[0]}"
}

migration_tree_sha256() {
	local directory="$1"
	(
		cd "${directory}"
		while IFS= read -r -d '' filename; do
			printf '%s\0' "${filename}"
			sha256sum -- "${filename}" | awk '{ print $1 }'
		done < <(find . -maxdepth 1 -type f -name '*.sql' -printf '%P\0' | LC_ALL=C sort -z)
	) | sha256sum | awk '{ print $1 }'
}

expected_image="$(
	awk '
		$1 == "postgres:" { in_postgres = 1; next }
		in_postgres && $1 == "image:" { print $2; exit }
	' "${ROOT_DIR}/examples/docker-compose.base.yml"
)"
expected_goose="$(cd "${ROOT_DIR}" && go list -m -f '{{.Version}}' github.com/pressly/goose/v3)"
expected_latest="$(
	find "${ROOT_DIR}/migrations/postgres" -maxdepth 1 -type f -name '*.sql' -printf '%f\n' |
		LC_ALL=C sort -t_ -k1,1n |
		tail -n 1
)"

[[ "$(catalog_meta configured_image)" == "${expected_image}" ]] || fail 'catalog image metadata is stale'
resolved_image="$(catalog_meta resolved_image)"
[[ "${resolved_image}" =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || \
	fail 'catalog resolved image is not an immutable digest'
expected_repository="${expected_image}"
if [[ "${expected_repository##*/}" == *:* ]]; then
	expected_repository="${expected_repository%:*}"
fi
[[ "${resolved_image%%@*}" == "${expected_repository}" ]] || \
	fail 'catalog digest repository differs from the configured image'
[[ "$(catalog_meta goose_version)" == "${expected_goose}" ]] || fail 'catalog Goose metadata is stale'
[[ "$(catalog_meta latest_migration)" == "${expected_latest}" ]] || \
	fail 'catalog latest-migration metadata is stale'
expected_migration_tree="$(migration_tree_sha256 "${ROOT_DIR}/migrations/postgres")"
[[ "$(catalog_meta migration_tree_sha256)" == "${expected_migration_tree}" ]] || \
	fail 'catalog migration-tree metadata is stale'

checksum_rows=0
declare -A checksum_paths=()
while IFS=$'\t' read -r record key value; do
	case "${record}" in
		meta)
			[[ -n "${key}" && -n "${value}" ]] || fail 'catalog metadata row is incomplete'
			case "${key}" in
				configured_image | resolved_image | goose_version | latest_migration | migration_tree_sha256) ;;
				*) fail "unknown manifest metadata: ${key}" ;;
			esac
			;;
		sha256)
			[[ -z "${checksum_paths[${key}]+present}" ]] || fail "duplicate manifest path: ${key}"
			checksum_paths["${key}"]=1
			[[ -f "${CATALOG_DIR}/${key}" ]] || fail "manifest path is missing: ${key}"
			actual="$(sha256sum "${CATALOG_DIR}/${key}" | awk '{print $1}')"
			[[ "${actual}" == "${value}" ]] || fail "manifest checksum differs: ${key}"
			checksum_rows=$((checksum_rows + 1))
			;;
		*)
			fail "unknown manifest record: ${record}"
			;;
	esac
done <"${CATALOG_DIR}/manifest.tsv"
sql_file_count="$(find "${CATALOG_DIR}" -type f -name '*.sql' | wc -l)"
[[ "${checksum_rows}" -eq "${sql_file_count}" ]] || fail 'manifest does not cover every SQL file exactly once'
mapfile -t unexpected_catalog_files < <(
	find "${CATALOG_DIR}" -type f \
		! -name '*.sql' \
		! -path "${CATALOG_DIR}/README.md" \
		! -path "${CATALOG_DIR}/manifest.tsv" -print
)
(("${#unexpected_catalog_files[@]}" == 0)) || fail 'catalog contains an unexpected non-SQL file'

! grep -q 'goose_db_version' "${CATALOG_DIR}/current.sql" || fail 'catalog includes Goose history'
! grep -Eq '^(ALTER .* OWNER TO|GRANT |REVOKE )' "${CATALOG_DIR}/current.sql" || \
	fail 'catalog includes ownership or privilege DDL'
grep -q '^CREATE EXTENSION' "${CATALOG_DIR}/global.sql" || fail 'global catalog omits extensions'
grep -Eq '^CREATE (OR REPLACE )?FUNCTION' "${CATALOG_DIR}/global.sql" || \
	fail 'global catalog omits function bodies'
grep -Rqs '^ALTER TABLE .* ENABLE ROW LEVEL SECURITY' "${CATALOG_DIR}/relations" || \
	fail 'relation catalog omits RLS enablement'
grep -Rqs '^CREATE POLICY' "${CATALOG_DIR}/relations" || fail 'relation catalog omits policies'
grep -Rqs 'FOREIGN KEY' "${CATALOG_DIR}/relations" || fail 'relation catalog omits foreign keys'
grep -Rqs '^CREATE .*INDEX' "${CATALOG_DIR}/relations" || fail 'relation catalog omits indexes'
grep -Rqs '^CREATE TRIGGER' "${CATALOG_DIR}/relations" || fail 'relation catalog omits triggers'

printf 'PostgreSQL migration tooling tests passed.\n'
