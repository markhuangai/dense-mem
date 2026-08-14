#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATIONS_DIR="${ROOT_DIR}/migrations/postgres"
CATALOG_DIR="${ROOT_DIR}/schema/postgres"
IMAGE="${POSTGRES_SCHEMA_IMAGE:-pgvector/pgvector:0.8.2-pg18-trixie}"
MODE="${1:-check}"

usage() {
  echo "usage: $0 write|check" >&2
  exit 2
}

[[ "${MODE}" == "write" || "${MODE}" == "check" ]] || usage
command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }

TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/dense-mem-schema.XXXXXX")"
CONTAINER=""
cleanup() {
  if [[ -n "${CONTAINER}" ]]; then
    docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
  fi
  rm -rf "${TMP_ROOT}"
}
trap cleanup EXIT

generate_manifest() {
  local out="$1"
  {
    printf '# image=%s\n' "${IMAGE}"
    printf '# goose=%s\n' "$(go list -m -f '{{.Version}}' github.com/pressly/goose/v3)"
    printf '# latest=%s\n' "$(find "${MIGRATIONS_DIR}" -maxdepth 1 -type f -name '*.sql' -printf '%f\n' | sed -nE 's/^([0-9]{10,14})_.*/\1/p' | sort -n | tail -1)"
    printf '# version\tfilename\tsha256\n'
    while IFS= read -r filename; do
      version="${filename%%_*}"
      checksum="$(sha256sum "${MIGRATIONS_DIR}/${filename}" | awk '{print $1}')"
      printf '%s\t%s\t%s\n' "${version}" "${filename}" "${checksum}"
    done < <(find "${MIGRATIONS_DIR}" -maxdepth 1 -type f -name '*.sql' -printf '%f\n' | sort -n)
  } > "${out}"
}

start_database() {
  CONTAINER="$(docker run -d --rm \
    --name "dense-mem-schema-${RANDOM}" \
    -e POSTGRES_USER=densemem \
    -e POSTGRES_PASSWORD=dense-mem-schema-password \
    -e POSTGRES_DB=densemem \
    "${IMAGE}")"
  for _ in $(seq 1 90); do
    if docker exec "${CONTAINER}" pg_isready -h 127.0.0.1 -U densemem -d densemem >/dev/null 2>&1; then
      export CATALOG_DSN="host=127.0.0.1 port=5432 user=densemem password=dense-mem-schema-password dbname=densemem sslmode=disable"
      return
    fi
    sleep 1
  done
  echo "timed out waiting for disposable PostgreSQL" >&2
  exit 1
}

generate_catalog() {
  local out="$1"
  mkdir -p "${out}/relations"
  start_database
  if [[ ! -x "${TMP_ROOT}/schema-catalog" ]]; then
    (cd "${ROOT_DIR}" && go build -o "${TMP_ROOT}/schema-catalog" ./cmd/schema-catalog)
  fi
  docker cp "${TMP_ROOT}/schema-catalog" "${CONTAINER}:/schema-catalog"
  docker cp "${MIGRATIONS_DIR}" "${CONTAINER}:/migrations"
  docker exec "${CONTAINER}" /schema-catalog -dsn "${CATALOG_DSN}" -migrations /migrations >/dev/null

  docker exec "${CONTAINER}" pg_dump "${CATALOG_DSN}" --schema-only --no-owner --no-privileges --no-comments \
    --exclude-table=public.goose_db_version --exclude-table=public.goose_db_version_id_seq \
    | sed -E '/^-- Dumped (from|by)/d; /^-- Started on /d; /^-- Completed on /d; /^\\(un)?restrict /d' \
    | perl -0777 -pe 's/\n+\z/\n/' > "${out}/current.sql"
  docker exec "${CONTAINER}" pg_dump "${CATALOG_DSN}" --schema-only --section=pre-data --no-owner --no-privileges --no-comments \
    --exclude-table=public.goose_db_version --exclude-table=public.goose_db_version_id_seq \
    | sed -E '/^-- Dumped (from|by)/d; /^-- Started on /d; /^-- Completed on /d; /^\\(un)?restrict /d' \
    | perl -0777 -pe 's/\n+\z/\n/' > "${out}/global.sql"

  while IFS=$'\t' read -r schema relation; do
    [[ "${schema}" == "public" && -n "${relation}" ]] || continue
    docker exec "${CONTAINER}" pg_dump "${CATALOG_DSN}" --schema-only --no-owner --no-privileges --no-comments \
      --table="${schema}.${relation}" \
      | sed -E '/^-- Dumped (from|by)/d; /^-- Started on /d; /^-- Completed on /d; /^\\(un)?restrict /d' \
      | perl -0777 -pe 's/\n+\z/\n/' > "${out}/relations/public.${relation}.sql"
  done < <(docker exec "${CONTAINER}" psql "${CATALOG_DSN}" -AtF $'\t' -c "SELECT schemaname, tablename FROM pg_catalog.pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version' ORDER BY tablename")
  generate_manifest "${out}/manifest.tsv"
}

if [[ "${MODE}" == "write" ]]; then
  generated="${TMP_ROOT}/catalog"
  generate_catalog "${generated}"
  mkdir -p "${CATALOG_DIR}"
  rm -f "${CATALOG_DIR}/current.sql" "${CATALOG_DIR}/global.sql" "${CATALOG_DIR}/manifest.tsv"
  rm -rf "${CATALOG_DIR}/relations"
  cp "${generated}/current.sql" "${CATALOG_DIR}/current.sql"
  cp "${generated}/global.sql" "${CATALOG_DIR}/global.sql"
  cp "${generated}/manifest.tsv" "${CATALOG_DIR}/manifest.tsv"
  cp -R "${generated}/relations" "${CATALOG_DIR}/relations"
  echo "schema catalog written to ${CATALOG_DIR}"
  exit 0
fi

expected="${TMP_ROOT}/expected"
generate_catalog "${expected}"
diff -ru --exclude=README.md "${CATALOG_DIR}" "${expected}"
echo "schema catalog is current"
