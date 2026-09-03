#!/usr/bin/env bash
# Sourced by e2e-host-controller.sh. PostgreSQL setup is shared by local and CI
# execution; the controller remains the only caller that decides when it runs.

DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER="densemem_e2e_bootstrap"
DENSE_MEM_CI_BOOTSTRAP_POSTGRES_PASSWORD="dense-mem-e2e-bootstrap-password"

compose_server_environment_value() {
  local field="$1"
  ci_compose config --format json |
    node -e '
let input = "";
const field = process.argv[1];
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  try {
    const value = JSON.parse(input).services?.server?.environment?.[field];
    if (typeof value !== "string" || value.length === 0 || /[\r\n]/.test(value)) process.exit(1);
    process.stdout.write(value);
  } catch {
    process.exit(1);
  }
});
' "$field"
}

wait_for_postgres_service() {
  for _ in $(seq 1 90); do
    if ci_compose exec -T postgres sh -ec \
      'pg_isready -U "${POSTGRES_USER}" -d "${POSTGRES_DB}"' \
      >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  fail "timed out waiting for the E2E PostgreSQL service"
}

validate_postgres_identifier() {
  [[ "$1" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] ||
    fail "invalid PostgreSQL identifier in CI environment: $1"
}

provision_postgres_runtime_role() {
  local runtime_user runtime_password runtime_database role_state
  runtime_user="$(compose_server_environment_value POSTGRES_USER)"
  runtime_password="$(compose_server_environment_value POSTGRES_PASSWORD)"
  runtime_database="$(compose_server_environment_value POSTGRES_DB)"
  validate_postgres_identifier "$runtime_user"
  validate_postgres_identifier "$runtime_database"
  [[ -n "$runtime_password" ]] || fail "POSTGRES_PASSWORD is required for runtime role provisioning"
  [[ "$runtime_user" != "$DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER" ]] ||
    fail "POSTGRES_USER conflicts with the E2E bootstrap role"
  [[ "$runtime_user" != "densemem_e2e_database_owner" ]] ||
    fail "POSTGRES_USER conflicts with the E2E database owner role"

  ci_compose exec -T \
    -e "DENSE_MEM_RUNTIME_POSTGRES_USER=$runtime_user" \
    -e "DENSE_MEM_RUNTIME_POSTGRES_PASSWORD=$runtime_password" \
    -e "DENSE_MEM_RUNTIME_POSTGRES_DB=$runtime_database" \
    postgres sh -ec \
      'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
      -v runtime_user="$DENSE_MEM_RUNTIME_POSTGRES_USER" \
      -v runtime_password="$DENSE_MEM_RUNTIME_POSTGRES_PASSWORD" \
      -v runtime_database="$DENSE_MEM_RUNTIME_POSTGRES_DB"' >/dev/null <<'SQL'
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS vector;

SELECT format(
  'CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS',
  :'runtime_user', :'runtime_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'runtime_user')
\gexec

SELECT format(
  'ALTER ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS',
  :'runtime_user', :'runtime_password'
)
\gexec

SELECT 'CREATE ROLE densemem_e2e_database_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS'
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'densemem_e2e_database_owner')
\gexec

ALTER DATABASE :"runtime_database" OWNER TO densemem_e2e_database_owner;
GRANT CONNECT, CREATE, TEMPORARY ON DATABASE :"runtime_database" TO :"runtime_user";
GRANT USAGE, CREATE ON SCHEMA public TO :"runtime_user";
SQL

  role_state="$(
    ci_compose exec -T \
      -e "PGPASSWORD=$runtime_password" \
      postgres psql -X -v ON_ERROR_STOP=1 -At -F '|' \
      -h 127.0.0.1 -U "$runtime_user" -d "$runtime_database" \
      -c "
        SELECT
          role.rolsuper,
          role.rolcreatedb,
          role.rolcreaterole,
          role.rolreplication,
          role.rolbypassrls,
          pg_get_userbyid(database.datdba) = CURRENT_USER,
          has_database_privilege(CURRENT_USER, current_database(), 'CREATE'),
          has_schema_privilege(CURRENT_USER, 'public', 'CREATE')
        FROM pg_roles AS role
        JOIN pg_database AS database ON database.datname = current_database()
        WHERE role.rolname = CURRENT_USER
      "
  )"
  if [[ "$role_state" != "f|f|f|f|f|f|t|t" ]]; then
    fail "unexpected E2E PostgreSQL runtime role state: ${role_state}"
  fi
}

identity_postgres_scalar() {
  local statement="$1"
  ci_compose exec -T \
    -e "PGPASSWORD=${DENSE_MEM_CI_IDENTITY_POSTGRES_PASSWORD}" \
    -e "PGOPTIONS=-c app.tx_mode=system" \
    postgres psql -X -v ON_ERROR_STOP=1 -At \
    -h 127.0.0.1 \
    -U "$DENSE_MEM_CI_IDENTITY_POSTGRES_USER" \
    -d "$DENSE_MEM_CI_IDENTITY_POSTGRES_DATABASE" \
    -c "$statement"
}

load_identity_postgres_config() {
  DENSE_MEM_CI_IDENTITY_POSTGRES_USER="$(compose_server_environment_value POSTGRES_USER)"
  DENSE_MEM_CI_IDENTITY_POSTGRES_PASSWORD="$(compose_server_environment_value POSTGRES_PASSWORD)"
  DENSE_MEM_CI_IDENTITY_POSTGRES_DATABASE="$(compose_server_environment_value POSTGRES_DB)"
  export DENSE_MEM_CI_IDENTITY_POSTGRES_USER DENSE_MEM_CI_IDENTITY_POSTGRES_PASSWORD DENSE_MEM_CI_IDENTITY_POSTGRES_DATABASE
}

reset_identity_cleanup_database() {
  ci_compose down -v --remove-orphans >/dev/null
  ci_compose up -d --wait --wait-timeout 300 postgres >/dev/null
  wait_for_postgres_service
  provision_postgres_runtime_role
  load_identity_postgres_config
}

seed_identity_cleanup_database() {
  local source_dir="$1" project="$2" variant="$3"
  DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID="$(node -e 'process.stdout.write(require("node:crypto").randomUUID())')"
  DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID="$(node -e 'process.stdout.write(require("node:crypto").randomUUID())')"
  DENSE_MEM_CI_IDENTITY_UPGRADE_API_KEY="dm_upgrade_$(node -e 'process.stdout.write(require("node:crypto").randomBytes(32).toString("base64url"))')"
  export DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID DENSE_MEM_CI_IDENTITY_UPGRADE_API_KEY

  local go_image
  go_image="$(env_value DENSE_MEM_CI_GO_TEST_IMAGE 2>/dev/null || printf '%s' golang:1.26.6-bookworm)"
  [[ "$go_image" =~ ^[A-Za-z0-9._/:@-]+$ ]] || fail "invalid identity cleanup Go test image"
  run_go_source_container \
    "$source_dir" "$go_image" "$project" "$DENSE_MEM_CI_RUN_ID" "$DENSE_MEM_CI_RUN_ATTEMPT" \
    "$DENSE_MEM_CI_PHASE" "$DENSE_MEM_CI_SCENARIO" "$DENSE_MEM_CI_IMAGE_DIGEST" "${project}_ci" "" "$ENV_FILE" \
    "$DENSE_MEM_CI_IDENTITY_UPGRADE_API_KEY" "$DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID" "$DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID" -- \
    "DENSE_MEM_E2E_IDENTITY_SEED_VARIANT=${variant}" \
    "DENSE_MEM_E2E_IDENTITY_TEAM_ID=${DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID}" \
    "DENSE_MEM_E2E_IDENTITY_PROFILE_ID=${DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID}" \
    "DENSE_MEM_E2E_IDENTITY_API_KEY=${DENSE_MEM_CI_IDENTITY_UPGRADE_API_KEY}" \
    "DENSE_MEM_E2E_POSTGRES_USER=${DENSE_MEM_CI_IDENTITY_POSTGRES_USER}" \
    "DENSE_MEM_E2E_POSTGRES_PASSWORD=${DENSE_MEM_CI_IDENTITY_POSTGRES_PASSWORD}" \
    "DENSE_MEM_E2E_POSTGRES_DB=${DENSE_MEM_CI_IDENTITY_POSTGRES_DATABASE}" \
    "DENSE_MEM_E2E_POSTGRES_HOST=postgres" \
    "DENSE_MEM_E2E_POSTGRES_PORT=5432" -- \
    go test -tags=integration ./internal/storage/postgres \
      -run '^TestIdentityCleanupComposeSeed$' -count=1 >&2

  local identity_file="${DENSE_MEM_CI_HELPER_DIR}/identity.env"
  printf 'DENSE_MEM_E2E_UPGRADE_TEAM_ID=%s\nDENSE_MEM_E2E_UPGRADE_PROFILE_ID=%s\nDENSE_MEM_E2E_UPGRADE_API_KEY=%s\n' \
    "$DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID" "$DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID" "$DENSE_MEM_CI_IDENTITY_UPGRADE_API_KEY" > "$identity_file"
  chmod 600 "$identity_file"
}

verify_postgres_runtime_migration_state() {
  local runtime_user runtime_password runtime_database migration_state
  runtime_user="$(compose_server_environment_value POSTGRES_USER)"
  runtime_password="$(compose_server_environment_value POSTGRES_PASSWORD)"
  runtime_database="$(compose_server_environment_value POSTGRES_DB)"
  migration_state="$(
    ci_compose exec -T \
      -e "PGPASSWORD=$runtime_password" \
      postgres psql -X -v ON_ERROR_STOP=1 -At -F '|' \
      -h 127.0.0.1 -U "$runtime_user" -d "$runtime_database" \
      -c "
        SELECT
          EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 2026080603 AND is_applied),
          NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dense_mem_portal_session_system'),
          NOT EXISTS (
            SELECT 1 FROM pg_proc AS procedure
            JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
            WHERE namespace.nspname = 'public' AND procedure.proname LIKE 'dense_mem_portal_session_%'
          ),
          table_state.relrowsecurity,
          table_state.relforcerowsecurity,
          pg_get_userbyid(table_state.relowner) = CURRENT_USER,
          policy.polcmd = '*',
          policy.polroles = ARRAY[0::oid],
          pg_get_expr(policy.polqual, policy.polrelid) LIKE '%current_setting%' AND
            pg_get_expr(policy.polqual, policy.polrelid) LIKE '%app.tx_mode%' AND
            pg_get_expr(policy.polqual, policy.polrelid) LIKE '%system%',
          pg_get_expr(policy.polwithcheck, policy.polrelid) LIKE '%current_setting%' AND
            pg_get_expr(policy.polwithcheck, policy.polrelid) LIKE '%app.tx_mode%' AND
            pg_get_expr(policy.polwithcheck, policy.polrelid) LIKE '%system%',
          NOT EXISTS (
            SELECT 1
            FROM pg_class AS relation
            JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
            WHERE namespace.nspname = 'public'
              AND relation.relkind IN ('r', 'p', 'S', 'v', 'm')
              AND pg_get_userbyid(relation.relowner) <> CURRENT_USER
          )
        FROM pg_class AS table_state
        JOIN pg_policy AS policy ON policy.polrelid = table_state.oid
        WHERE table_state.oid = to_regclass('public.user_portal_sessions')
          AND policy.polname = 'user_portal_sessions_system_access'
      "
  )"
  if [[ "$migration_state" != "t|t|t|t|t|t|t|t|t|t|t" ]]; then
    fail "unexpected E2E PostgreSQL migration state: ${migration_state}"
  fi
}

verify_v25_cleanup_catalog() {
  local state
  state="$(identity_postgres_scalar "
    SELECT concat(
      EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 2026081602 AND is_applied), '|',
      to_regclass('public.semantic_team_refs') IS NULL, '|',
      to_regclass('public.semantic_profile_refs') IS NULL, '|',
      to_regclass('public.embedding_config') IS NULL, '|',
      (SELECT count(*) = 32 FROM pg_constraint AS constraint_state
       WHERE constraint_state.contype = 'f'
         AND constraint_state.conrelid::regclass::text = ANY(ARRAY[
           'dream_cycle_runs', 'entity_correction_events', 'entity_correction_plans', 'entity_names',
           'entity_resolution_events', 'evidence_fragments', 'evidence_lifecycle_operations',
           'evidence_quarantines', 'evidence_security_events', 'evidence_security_signals',
           'evidence_source_revisions', 'evidence_sources', 'hypotheses', 'hypothesis_feedback_events',
           'knowledge_ingests', 'relationship_conflict_derived_evidence_tasks', 'relationship_conflict_events',
           'relationship_conflict_evidence_derivations', 'relationship_correction_submissions',
           'relationship_cross_references', 'relationship_evidence_supports', 'relationship_observations',
           'relationship_records', 'relationship_support_decision_events', 'relationship_transition_events',
           'review_tasks', 'search_documents', 'verification_events'
         ]::text[])
         AND constraint_state.confrelid = 'ownership_aliases'::regclass
         AND constraint_state.convalidated AND constraint_state.confdeltype = 'r'), '|',
      (SELECT count(*) = 5 FROM pg_constraint AS constraint_state
       WHERE constraint_state.contype = 'f'
         AND constraint_state.conrelid::regclass::text = ANY(ARRAY[
           'community_snapshot_runs', 'entity_records', 'search_projection_generations',
           'team_predicate_definitions', 'value_records'
         ]::text[])
         AND constraint_state.confrelid = 'teams'::regclass
         AND constraint_state.convalidated AND constraint_state.confdeltype = 'r'), '|',
      NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname LIKE 'dense_mem_v25_%')
    )
  ")"
  [[ "$state" == "true|true|true|true|true|true|true" || "$state" == "t|t|t|t|t|t|t" ]] ||
    fail "v2.5 cleanup catalog is incomplete: ${state}"
}

verify_identity_cleanup_seed_upgrade() {
  local variant="$1" state
  verify_v25_cleanup_catalog
  state="$(identity_postgres_scalar "
    SELECT concat(
      EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 2026081001 AND is_applied), '|',
      EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 2026081501 AND is_applied), '|',
      to_regclass('public.team_profiles') IS NULL, '|',
      to_regclass('public.identity_compatibility_state') IS NULL, '|',
      EXISTS (SELECT 1 FROM credentials WHERE id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID}'::uuid AND status = 'active'), '|',
      EXISTS (SELECT 1 FROM ownership_aliases WHERE team_id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID}'::uuid AND legacy_owner_id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID}'::uuid), '|',
      EXISTS (SELECT 1 FROM credentials AS credential JOIN team_memberships AS membership
        ON membership.actor_identity_id = credential.actor_identity_id AND membership.team_id = credential.team_id
        WHERE credential.id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID}'::uuid AND membership.team_admin), '|',
      (SELECT count(*) FROM ownership_aliases WHERE team_id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID}'::uuid) = 2, '|',
      EXISTS (SELECT 1 FROM usage_metric_buckets WHERE key_id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID}'::uuid AND route = '/identity-upgrade'), '|',
      EXISTS (SELECT 1 FROM usage_metric_buckets WHERE key_id IN (
        SELECT legacy_owner_id FROM ownership_aliases WHERE team_id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID}'::uuid
          AND legacy_owner_id <> '${DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID}'::uuid
      ) AND route = '/identity-upgrade-sso'), '|',
      EXISTS (SELECT 1 FROM user_portal_sessions WHERE key_id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID}'::uuid), '|',
      EXISTS (SELECT 1 FROM sso_sessions AS session JOIN team_memberships AS membership ON membership.id = session.membership_id
        WHERE membership.team_id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_TEAM_ID}'::uuid AND membership.sso_provider_id IS NOT NULL)
    )
  ")"
  [[ "$state" == "true|true|true|true|true|true|true|true|true|true|true|true" || "$state" == "t|t|t|t|t|t|t|t|t|t|t|t" ]] ||
    fail "identity cleanup ${variant} upgrade did not retain the expected canonical state: ${state}"

  ci_compose exec -T \
    -e "DENSE_MEM_IDENTITY_UPGRADE_API_KEY=${DENSE_MEM_CI_IDENTITY_UPGRADE_API_KEY}" \
    server sh -ec 'wget -q -O - --header="Authorization: Bearer ${DENSE_MEM_IDENTITY_UPGRADE_API_KEY}" \
      --header="Content-Type: application/json" --header="Accept: application/json, text/event-stream" \
      --post-data='"'"'{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'"'"' \
      http://127.0.0.1:8080/mcp' |
    node -e 'let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{const trimmed=input.trim();const dataLine=trimmed.split(/\r?\n/).find(line=>line.startsWith("data: "));const payload=dataLine?dataLine.slice(6):trimmed;try{const p=JSON.parse(payload);if(!p.result||p.error)process.exit(1);}catch{process.exit(1);}});'
}

assert_identity_cleanup_bridge_intact() {
  local state
  state="$(identity_postgres_scalar "
    SELECT concat(
      to_regclass('public.team_profiles') IS NOT NULL, '|',
      to_regclass('public.identity_compatibility_state') IS NOT NULL, '|',
      NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 2026081501 AND is_applied)
    )
  ")"
  [[ "$state" == "true|true|true" || "$state" == "t|t|t" ]] ||
    fail "identity cleanup failure did not leave the bridge intact: ${state}"
}

start_identity_cleanup_server() {
  ci_compose up -d --wait --wait-timeout 300 redis prometheus server >/dev/null
  verify_postgres_runtime_migration_state
  verify_v25_cleanup_catalog
}

start_identity_cleanup_server_expect_failure() {
  local expected_log="$1" server_container="" restart_count=0
  ci_compose up -d --force-recreate redis prometheus server >/dev/null 2>&1 || true
  server_container="$(ci_compose ps -q server)"
  [[ -n "$server_container" ]] || fail "identity cleanup failure probe did not create a server container"
  for _ in $(seq 1 90); do
    restart_count="$(docker inspect --format '{{.RestartCount}}' "$server_container" 2>/dev/null || printf '%s' 0)"
    ((restart_count > 0)) && break
    sleep 1
  done
  ((restart_count > 0)) || fail "identity cleanup failure probe did not restart the server"
  ci_compose logs --no-color server 2>&1 | grep -F -- "$expected_log" >/dev/null ||
    fail "identity cleanup failure probe did not report ${expected_log}"
  ci_compose stop server >/dev/null
  assert_identity_cleanup_bridge_intact
}

hold_identity_cleanup_lock() {
  identity_postgres_scalar \
    "BEGIN; LOCK TABLE team_profiles IN ACCESS SHARE MODE; SELECT pg_sleep(3600); COMMIT;" \
    >/dev/null 2>&1 &
  DENSE_MEM_CI_IDENTITY_LOCK_PID=$!
  for _ in $(seq 1 30); do
    if [[ "$(identity_postgres_scalar "
      SELECT EXISTS (
        SELECT 1 FROM pg_locks
        WHERE relation = 'team_profiles'::regclass AND mode = 'AccessShareLock'
          AND granted AND pid <> pg_backend_pid()
      )
    ")" =~ ^(t|true)$ ]]; then
      return 0
    fi
    sleep 1
  done
  fail "timed out waiting for the identity cleanup lock fixture"
}

cleanup_identity_cleanup_lock() {
  if [[ -z "${DENSE_MEM_CI_IDENTITY_LOCK_PID:-}" ]]; then return; fi
  identity_postgres_scalar \
    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND query LIKE '%pg_sleep(3600)%' AND pid <> pg_backend_pid();" \
    >/dev/null 2>&1 || true
  kill "$DENSE_MEM_CI_IDENTITY_LOCK_PID" >/dev/null 2>&1 || true
  wait "$DENSE_MEM_CI_IDENTITY_LOCK_PID" >/dev/null 2>&1 || true
  DENSE_MEM_CI_IDENTITY_LOCK_PID=""
}

run_identity_cleanup_startup_matrix() {
  local source_dir="$1" project="$2" initial_state
  load_identity_postgres_config

  reset_identity_cleanup_database
  start_identity_cleanup_server

  reset_identity_cleanup_database
  seed_identity_cleanup_database "$source_dir" "$project" v2_4_8
  initial_state="$(identity_postgres_scalar "
    SELECT concat(COALESCE(MAX(version_id) FILTER (WHERE is_applied), 0), '|',
      to_regclass('public.team_profiles') IS NOT NULL, '|',
      to_regclass('public.credentials') IS NULL) FROM goose_db_version
  ")"
  [[ "$initial_state" == "2026080905|true|true" || "$initial_state" == "2026080905|t|t" ]] ||
    fail "unexpected populated v2.4.8 identity state: ${initial_state}"
  start_identity_cleanup_server
  verify_identity_cleanup_seed_upgrade v2.4.8

  reset_identity_cleanup_database
  seed_identity_cleanup_database "$source_dir" "$project" bridge
  initial_state="$(identity_postgres_scalar "
    SELECT concat(COALESCE(MAX(version_id) FILTER (WHERE is_applied), 0), '|',
      to_regclass('public.team_profiles') IS NOT NULL, '|',
      to_regclass('public.identity_compatibility_state') IS NOT NULL, '|',
      EXISTS (SELECT 1 FROM credentials WHERE id = '${DENSE_MEM_CI_IDENTITY_UPGRADE_PROFILE_ID}'::uuid AND scopes = ARRAY['read']::text[]))
    FROM goose_db_version
  ")"
  [[ "$initial_state" == "2026081001|true|true|true" || "$initial_state" == "2026081001|t|t|t" ]] ||
    fail "unexpected populated bridge identity state: ${initial_state}"
  start_identity_cleanup_server_expect_failure "usage history missing ownership aliases"
  identity_postgres_scalar "DELETE FROM usage_metric_buckets WHERE route = '/identity-cleanup-mismatch'" >/dev/null

  hold_identity_cleanup_lock
  start_identity_cleanup_server_expect_failure "lock timeout"
  cleanup_identity_cleanup_lock
  ci_compose up -d --wait --wait-timeout 300 redis prometheus server >/dev/null
  verify_postgres_runtime_migration_state
  verify_identity_cleanup_seed_upgrade bridge
}
