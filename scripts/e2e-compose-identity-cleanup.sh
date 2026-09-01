E2E_IDENTITY_LOCK_PID=""
IDENTITY_POSTGRES_USER=""
IDENTITY_POSTGRES_PASSWORD=""
IDENTITY_POSTGRES_DATABASE=""
IDENTITY_UPGRADE_TEAM_ID=""
IDENTITY_UPGRADE_PROFILE_ID=""
IDENTITY_UPGRADE_API_KEY=""

load_identity_postgres_config() {
  IDENTITY_POSTGRES_USER="$(compose_server_environment_value POSTGRES_USER)"
  IDENTITY_POSTGRES_PASSWORD="$(compose_server_environment_value POSTGRES_PASSWORD)"
  IDENTITY_POSTGRES_DATABASE="$(compose_server_environment_value POSTGRES_DB)"
}

identity_postgres_scalar() {
  local statement="$1"
  compose exec -T \
    -e "PGPASSWORD=$IDENTITY_POSTGRES_PASSWORD" \
    -e "PGOPTIONS=-c app.tx_mode=system" \
    postgres psql -X -v ON_ERROR_STOP=1 -At \
    -h 127.0.0.1 \
    -U "$IDENTITY_POSTGRES_USER" \
    -d "$IDENTITY_POSTGRES_DATABASE" \
    -c "$statement"
}

reset_identity_cleanup_database() {
  compose down -v --remove-orphans >/dev/null
  compose up -d postgres
  wait_for_postgres_service
  provision_postgres_runtime_role
  load_identity_postgres_config
}

seed_identity_cleanup_database() {
  local variant="$1"
  IDENTITY_UPGRADE_TEAM_ID="$(node -e 'process.stdout.write(require("node:crypto").randomUUID())')"
  IDENTITY_UPGRADE_PROFILE_ID="$(node -e 'process.stdout.write(require("node:crypto").randomUUID())')"
  IDENTITY_UPGRADE_API_KEY="dm_upgrade_$(node -e 'process.stdout.write(require("node:crypto").randomBytes(32).toString("base64url"))')"

  DENSE_MEM_E2E_IDENTITY_SEED_VARIANT="$variant" \
  DENSE_MEM_E2E_IDENTITY_TEAM_ID="$IDENTITY_UPGRADE_TEAM_ID" \
  DENSE_MEM_E2E_IDENTITY_PROFILE_ID="$IDENTITY_UPGRADE_PROFILE_ID" \
  DENSE_MEM_E2E_IDENTITY_API_KEY="$IDENTITY_UPGRADE_API_KEY" \
  DENSE_MEM_E2E_POSTGRES_USER="$IDENTITY_POSTGRES_USER" \
  DENSE_MEM_E2E_POSTGRES_PASSWORD="$IDENTITY_POSTGRES_PASSWORD" \
  DENSE_MEM_E2E_POSTGRES_DB="$IDENTITY_POSTGRES_DATABASE" \
  DENSE_MEM_E2E_POSTGRES_PORT="$POSTGRES_HOST_PORT" \
  go test -tags=integration ./internal/storage/postgres \
    -run '^TestIdentityCleanupComposeSeed$' \
    -count=1
}

start_identity_cleanup_server() {
  compose up -d --no-build redis prometheus server
  wait_for_url "identity cleanup API readiness" "${USER_URL}/ready"
  verify_postgres_runtime_migration_state
  verify_v25_cleanup_catalog
}

verify_v25_cleanup_catalog() {
  local state
  state="$(identity_postgres_scalar "
    SELECT concat(
      EXISTS (
        SELECT 1 FROM goose_db_version
        WHERE version_id = 2026081602 AND is_applied
      ), '|',
      to_regclass('public.semantic_team_refs') IS NULL, '|',
      to_regclass('public.semantic_profile_refs') IS NULL, '|',
      to_regclass('public.embedding_config') IS NULL, '|',
      (
        SELECT count(*) = 32
        FROM pg_constraint AS constraint_state
        WHERE constraint_state.contype = 'f'
          AND constraint_state.conrelid::regclass::text = ANY(ARRAY[
            'dream_cycle_runs', 'entity_correction_events', 'entity_correction_plans',
            'entity_names', 'entity_resolution_events', 'evidence_fragments', 'evidence_lifecycle_operations',
            'evidence_quarantines', 'evidence_security_events', 'evidence_security_signals',
            'evidence_source_revisions', 'evidence_sources', 'hypotheses', 'hypothesis_feedback_events',
            'knowledge_ingests',
            'relationship_conflict_derived_evidence_tasks', 'relationship_conflict_events',
            'relationship_conflict_evidence_derivations', 'relationship_correction_submissions',
            'relationship_cross_references', 'relationship_evidence_supports', 'relationship_observations',
            'relationship_records', 'relationship_support_decision_events', 'relationship_transition_events',
            'review_tasks', 'search_documents', 'verification_events'
          ]::text[])
          AND constraint_state.confrelid = 'ownership_aliases'::regclass
          AND constraint_state.convalidated
          AND constraint_state.confdeltype = 'r'
      ), '|',
      (
        SELECT count(*) = 5
        FROM pg_constraint AS constraint_state
        WHERE constraint_state.contype = 'f'
          AND constraint_state.conrelid::regclass::text = ANY(ARRAY[
            'community_snapshot_runs', 'entity_records', 'search_projection_generations',
            'team_predicate_definitions', 'value_records'
          ]::text[])
          AND constraint_state.confrelid = 'teams'::regclass
          AND constraint_state.convalidated
          AND constraint_state.confdeltype = 'r'
      ), '|',
      NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname LIKE 'dense_mem_v25_%'
      )
    )
  ")"
  if [[ "$state" != "true|true|true|true|true|true|true" && "$state" != "t|t|t|t|t|t|t" ]]; then
    echo "V2.5 cleanup catalog is incomplete: ${state}" >&2
    return 1
  fi
}

verify_identity_cleanup_seed_upgrade() {
  local variant="$1"
  local state
  verify_v25_cleanup_catalog
  state="$(identity_postgres_scalar "
    SELECT concat(
      EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 2026081001 AND is_applied), '|',
      EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 2026081501 AND is_applied), '|',
      to_regclass('public.team_profiles') IS NULL, '|',
      to_regclass('public.identity_compatibility_state') IS NULL, '|',
      EXISTS (
        SELECT 1 FROM credentials
        WHERE id = '${IDENTITY_UPGRADE_PROFILE_ID}'::uuid AND status = 'active'
      ), '|',
      EXISTS (
        SELECT 1 FROM ownership_aliases
        WHERE team_id = '${IDENTITY_UPGRADE_TEAM_ID}'::uuid
          AND legacy_owner_id = '${IDENTITY_UPGRADE_PROFILE_ID}'::uuid
      ), '|',
      EXISTS (
        SELECT 1
        FROM credentials AS credential
        JOIN team_memberships AS membership
          ON membership.actor_identity_id = credential.actor_identity_id
         AND membership.team_id = credential.team_id
        WHERE credential.id = '${IDENTITY_UPGRADE_PROFILE_ID}'::uuid
          AND membership.team_admin
      ), '|',
      (SELECT count(*) FROM ownership_aliases WHERE team_id = '${IDENTITY_UPGRADE_TEAM_ID}'::uuid) = 2, '|',
      EXISTS (
        SELECT 1 FROM usage_metric_buckets
        WHERE key_id = '${IDENTITY_UPGRADE_PROFILE_ID}'::uuid AND route = '/identity-upgrade'
      ), '|',
      EXISTS (
        SELECT 1 FROM usage_metric_buckets
        WHERE key_id IN (
          SELECT legacy_owner_id
          FROM ownership_aliases
          WHERE team_id = '${IDENTITY_UPGRADE_TEAM_ID}'::uuid
            AND legacy_owner_id <> '${IDENTITY_UPGRADE_PROFILE_ID}'::uuid
        ) AND route = '/identity-upgrade-sso'
      ), '|',
      EXISTS (
        SELECT 1 FROM user_portal_sessions
        WHERE key_id = '${IDENTITY_UPGRADE_PROFILE_ID}'::uuid
      ), '|',
      EXISTS (
        SELECT 1
        FROM sso_sessions AS session
        JOIN team_memberships AS membership ON membership.id = session.membership_id
        WHERE membership.team_id = '${IDENTITY_UPGRADE_TEAM_ID}'::uuid
          AND membership.sso_provider_id IS NOT NULL
      )
    )
  ")"
  if [[ "$state" != "true|true|true|true|true|true|true|true|true|true|true|true" && "$state" != "t|t|t|t|t|t|t|t|t|t|t|t" ]]; then
    echo "Identity cleanup ${variant} upgrade did not retain the expected canonical state: ${state}" >&2
    return 1
  fi

  curl -fsS \
    -H "Authorization: Bearer ${IDENTITY_UPGRADE_API_KEY}" \
    -H "Accept: application/json" \
    -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
    "${USER_URL}/mcp" | node -e '
      let body = "";
      process.stdin.on("data", (chunk) => { body += chunk; });
      process.stdin.on("end", () => {
        const payload = JSON.parse(body);
        if (!payload.result || payload.error) process.exit(1);
      });
    '
}

assert_identity_cleanup_bridge_intact() {
  local state
  state="$(identity_postgres_scalar "
    SELECT concat(
      to_regclass('public.team_profiles') IS NOT NULL, '|',
      to_regclass('public.identity_compatibility_state') IS NOT NULL, '|',
      NOT EXISTS (
        SELECT 1 FROM goose_db_version
        WHERE version_id = 2026081501 AND is_applied
      )
    )
  ")"
  if [[ "$state" != "true|true|true" && "$state" != "t|t|t" ]]; then
    echo "Identity cleanup failure did not leave the bridge intact: ${state}" >&2
    return 1
  fi
}

start_identity_cleanup_server_expect_failure() {
  local expected_log="$1"
  local server_container=""
  local restart_count=0
  compose up -d --no-build --force-recreate redis prometheus server
  server_container="$(compose ps -q server)"
  if [[ -z "$server_container" ]]; then
    echo "Identity cleanup failure probe did not create a server container." >&2
    return 1
  fi
  for _ in $(seq 1 90); do
    restart_count="$(docker inspect --format '{{.RestartCount}}' "$server_container" 2>/dev/null || printf '%s' 0)"
    if (( restart_count > 0 )); then
      break
    fi
    sleep 1
  done
  if (( restart_count == 0 )); then
    echo "Identity cleanup failure probe did not restart the server." >&2
    return 1
  fi
  if ! compose logs --no-color server 2>&1 | grep -F -- "$expected_log" >/dev/null; then
    echo "Identity cleanup failure probe did not report ${expected_log}." >&2
    return 1
  fi
  compose stop server >/dev/null
  assert_identity_cleanup_bridge_intact
}

hold_identity_cleanup_lock() {
  identity_postgres_scalar \
    "BEGIN; LOCK TABLE team_profiles IN ACCESS SHARE MODE; SELECT pg_sleep(3600); COMMIT;" \
    >/dev/null 2>&1 &
  E2E_IDENTITY_LOCK_PID=$!
  for _ in $(seq 1 30); do
    if [[ "$(identity_postgres_scalar "
      SELECT EXISTS (
        SELECT 1 FROM pg_locks
        WHERE relation = 'team_profiles'::regclass
          AND mode = 'AccessShareLock'
          AND granted
          AND pid <> pg_backend_pid()
      )
    ")" =~ ^(t|true)$ ]]; then
      return 0
    fi
    sleep 1
  done
  echo "Timed out waiting for the identity cleanup lock fixture." >&2
  return 1
}

run_identity_cleanup_startup_matrix() {
  local initial_state

  echo "Proving a fresh database reaches the clean identity catalog."
  reset_identity_cleanup_database
  start_identity_cleanup_server

  echo "Proving a populated v2.4.8 database applies the bridge and cleanup in one current-image startup."
  reset_identity_cleanup_database
  seed_identity_cleanup_database v2_4_8
  initial_state="$(identity_postgres_scalar "
    SELECT concat(
      COALESCE(MAX(version_id) FILTER (WHERE is_applied), 0), '|',
      to_regclass('public.team_profiles') IS NOT NULL, '|',
      to_regclass('public.credentials') IS NULL
    ) FROM goose_db_version
  ")"
  if [[ "$initial_state" != "2026080905|true|true" && "$initial_state" != "2026080905|t|t" ]]; then
    echo "Unexpected populated v2.4.8 identity state: ${initial_state}" >&2
    return 1
  fi
  start_identity_cleanup_server
  verify_identity_cleanup_seed_upgrade v2.4.8

  echo "Proving bridge mismatch and lock contention roll back before a successful #210 startup."
  reset_identity_cleanup_database
  seed_identity_cleanup_database bridge
  initial_state="$(identity_postgres_scalar "
    SELECT concat(
      COALESCE(MAX(version_id) FILTER (WHERE is_applied), 0), '|',
      to_regclass('public.team_profiles') IS NOT NULL, '|',
      to_regclass('public.identity_compatibility_state') IS NOT NULL, '|',
      EXISTS (
        SELECT 1 FROM credentials
        WHERE id = '${IDENTITY_UPGRADE_PROFILE_ID}'::uuid AND scopes = ARRAY['read']::text[]
      )
    ) FROM goose_db_version
  ")"
  if [[ "$initial_state" != "2026081001|true|true|true" && "$initial_state" != "2026081001|t|t|t" ]]; then
    echo "Unexpected populated bridge identity state: ${initial_state}" >&2
    return 1
  fi
  start_identity_cleanup_server_expect_failure "usage history missing ownership aliases"
  identity_postgres_scalar \
    "DELETE FROM usage_metric_buckets WHERE route = '/identity-cleanup-mismatch'" \
    >/dev/null

  hold_identity_cleanup_lock
  start_identity_cleanup_server_expect_failure "lock timeout"
  cleanup_identity_cleanup_lock
  E2E_IDENTITY_LOCK_PID=""

  compose up -d --no-build --force-recreate redis prometheus server
  wait_for_url "identity cleanup API readiness" "${USER_URL}/ready"
  verify_postgres_runtime_migration_state
  verify_identity_cleanup_seed_upgrade bridge
  echo "Identity cleanup startup matrix passed for fresh, skipped-release, bridge, mismatch, and lock-contended states."
}

start_compose_stack_for_scenario() {
  if [[ "$E2E_SCENARIO" == "identity_cleanup" ]]; then
    compose build server
    run_identity_cleanup_startup_matrix
    compose up -d --no-build
    return
  fi
  compose up -d --build
}

cleanup_identity_cleanup_lock() {
  if [[ -z "$E2E_IDENTITY_LOCK_PID" ]]; then
    return
  fi
  identity_postgres_scalar \
    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND query LIKE '%pg_sleep(3600)%' AND pid <> pg_backend_pid();" \
    >/dev/null 2>&1 || true
  kill "$E2E_IDENTITY_LOCK_PID" >/dev/null 2>&1 || true
  wait "$E2E_IDENTITY_LOCK_PID" >/dev/null 2>&1 || true
}

run_identity_cleanup_consumer_e2e() {
  local team_id="$1"
  local credential_id="$2"
  local api_key="$3"
  echo "Running compose-backed canonical identity cleanup and A/B/C isolation e2e."
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_CREDENTIAL_ID="$credential_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_UPGRADE_TEAM_ID="$IDENTITY_UPGRADE_TEAM_ID" \
  DENSE_MEM_E2E_UPGRADE_PROFILE_ID="$IDENTITY_UPGRADE_PROFILE_ID" \
  DENSE_MEM_E2E_UPGRADE_API_KEY="$IDENTITY_UPGRADE_API_KEY" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  node "$ROOT_DIR/tests/uat/identity_cleanup_e2e.mjs"
}
