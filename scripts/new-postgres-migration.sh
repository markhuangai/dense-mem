#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MIGRATIONS_ROOT="${ROOT_DIR}/migrations/postgres"
MIGRATIONS_DIR="${MIGRATIONS_ROOT}/v2_5"
name="${1:-}"
if [[ ! "${name}" =~ ^[a-z][a-z0-9_]*$ ]]; then
  echo "usage: $0 <snake_case_name>" >&2
  exit 2
fi

latest="$(find "${MIGRATIONS_ROOT}" -type f -name '*.sql' -printf '%f\n' | sed -nE 's/^([0-9]{10,14})_.*/\1/p' | sort -n | tail -1)"
version="$(date -u +%Y%m%d%H%M%S)"
if [[ "${version}" -le "${latest}" ]]; then
  version="$((latest + 1))"
fi
path="${MIGRATIONS_DIR}/${version}_${name}.sql"
if [[ -e "${path}" ]]; then
  echo "migration already exists: ${path}" >&2
  exit 1
fi

cat > "${path}" <<EOF
-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: describe locks, table rewrites, and expected duration.
-- RLS impact: describe policies, FORCE RLS, and transaction-local context.
-- Backfill: describe bounded, resumable backfill and reconciliation behavior.
-- Backward compatibility: describe the previous-release read/write contract.
-- Rollback: describe the forward repair or irreversible boundary.

-- TODO: implement one logical, additive schema change.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Local rollback only; production recovery uses roll-forward or PITR.

-- +goose StatementEnd
EOF
echo "created ${path}"
