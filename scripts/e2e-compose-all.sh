E2E_ALL_SCENARIOS=(
  mcp_boundaries
  mcp_sdk_parity
  mcp_sdk_transport
  oauth_provider_compatibility
  mcp_oauth
  private_memory_erasure
  security_runtime
  infrastructure_credentials
  submission_terminal_errors
  security_intake
  submission_assessment
  identity_cleanup
  community
  conflict
  conflict_queue
  embedding_resilience
  memory_space_backfill
  memory_space_isolation
  space_aware_recall
  credential_memory_binding
  full
)

E2E_ALL_ACTIVE_PIDS=()

run_disposable_postgres_prechecks() {
  DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 go test \
    ./internal/repository \
    ./internal/http \
    -run '^(TestSSORuntimeEntitlementsExcludeArchivedTeams|TestDreamControlRepositoryIsTeamScopedAndAuditsAtomicRefresh|TestDreamRepositoryPersistsEvidenceGroundedHypothesisAndPathAssessment|TestScheduledDreamRecoveryFencesExpiredLease|TestScheduledDreamsAreTeamOwnedAndFeedbackIsActorAudited|TestSSOOIDCCallbackSkipsArchivedTeamMappingIntegration)$' \
    -count=1
}

terminate_e2e_all_process_groups() {
  local pid
  local any_alive
  local deadline=$((SECONDS + 60))
  for pid in "$@"; do
    if [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null; then
      kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
    fi
  done
  while ((SECONDS < deadline)); do
    any_alive=0
    for pid in "$@"; do
      if [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null; then
        any_alive=1
        break
      fi
    done
    if [[ "$any_alive" == "0" ]]; then
      break
    fi
    sleep 0.2
  done
  for pid in "$@"; do
    if [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null; then
      kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
    fi
  done
  for pid in "$@"; do
    if [[ "$pid" =~ ^[1-9][0-9]*$ ]]; then
      wait "$pid" 2>/dev/null || true
    fi
  done
}

interrupt_e2e_all_scenarios() {
  trap - INT TERM
  echo "Stopping active compose e2e scenarios." >&2
  terminate_e2e_all_process_groups "${E2E_ALL_ACTIVE_PIDS[@]}"
  exit 130
}

run_all_e2e_scenarios() {
  local parallelism="${DENSE_MEM_E2E_PARALLELISM:-4}"
  local skip_prechecks="${DENSE_MEM_E2E_SKIP_PRECHECKS:-0}"
  local base_run_id
  local all_file_id
  local base_project_name
  local log_dir
  local scenario_count="${#E2E_ALL_SCENARIOS[@]}"
  local ports_per_scenario=12
  local port_count=$((scenario_count * ports_per_scenario))
  local reserved_raw
  local next_index=0
  local first_failure_status=0
  local first_failure_scenario=""
  local finished_pid=""
  local finished_status=0
  local scenario=""
  local run_id=""
  local project_name=""
  local prometheus_name=""
  local log_file=""
  local offset=0
  local pid=""
  local -a reserved_ports=()
  local -a active_pids=()
  local -a remaining_pids=()
  local -A pid_scenario=()
  local -A pid_log=()
  local -A scenario_status=()

  if [[ ! "$parallelism" =~ ^[1-9][0-9]*$ ]] || ((parallelism > 16)); then
    echo "DENSE_MEM_E2E_PARALLELISM must be an integer between 1 and 16." >&2
    return 1
  fi
  if ((BASH_VERSINFO[0] < 5)); then
    echo "DENSE_MEM_E2E_SCENARIO=all requires Bash 5 or newer." >&2
    return 1
  fi
  if ! command -v setsid >/dev/null 2>&1; then
    echo "DENSE_MEM_E2E_SCENARIO=all requires setsid for process-group cleanup." >&2
    return 1
  fi

  if [[ "$skip_prechecks" == "1" ]]; then
    echo "Skipping disposable PostgreSQL prechecks once by DENSE_MEM_E2E_SKIP_PRECHECKS."
  else
    echo "Running disposable PostgreSQL prechecks once before the compose matrix."
    run_disposable_postgres_prechecks
    echo "Disposable PostgreSQL prechecks completed."
  fi

  base_run_id="${DENSE_MEM_E2E_RUN_ID:-all-$(date -u +%Y%m%d%H%M%S)-$$}"
  all_file_id="$(sanitize_project_name "$base_run_id")"
  base_project_name="$(sanitize_project_name "${DENSE_MEM_E2E_PROJECT_NAME:-densemem-e2e-${base_run_id}}")"
  log_dir="${DENSE_MEM_E2E_LOG_DIR:-${ROOT_DIR}/tests/uat/test-results/compose-all-${all_file_id}}"
  if [[ -e "$log_dir" ]]; then
    echo "Refusing to replace existing compose e2e log directory ${log_dir}." >&2
    return 1
  fi
  mkdir -p "$(dirname "$log_dir")"
  mkdir "$log_dir"

  echo "Reserving ${ports_per_scenario} unique host ports for each of ${scenario_count} scenarios."
  reserved_raw="$(pick_ports "$port_count")"
  read -r -a reserved_ports <<< "$reserved_raw"
  if [[ "${#reserved_ports[@]}" -ne "$port_count" ]]; then
    echo "Expected ${port_count} reserved ports, received ${#reserved_ports[@]}." >&2
    return 1
  fi

  for scenario in "${E2E_ALL_SCENARIOS[@]}"; do
    scenario_status["$scenario"]="pending"
  done
  trap interrupt_e2e_all_scenarios INT TERM

  while ((next_index < scenario_count || ${#active_pids[@]} > 0)); do
    while ((next_index < scenario_count && ${#active_pids[@]} < parallelism)); do
      scenario="${E2E_ALL_SCENARIOS[$next_index]}"
      offset=$((next_index * ports_per_scenario))
      run_id="${base_run_id}-$(printf '%02d' "$((next_index + 1))")-${scenario}"
      project_name="$(sanitize_project_name "${base_project_name}-$((next_index + 1))")"
      prometheus_name="${project_name}-prometheus"
      log_file="${log_dir}/${scenario}.log"
      echo "Starting compose e2e scenario ${scenario}; log: ${log_file}"
      setsid --wait env \
        DENSE_MEM_E2E_MODE=standard \
        DENSE_MEM_E2E_SCENARIO="$scenario" \
        DENSE_MEM_E2E_RUN_ID="$run_id" \
        DENSE_MEM_E2E_PROJECT_NAME="$project_name" \
        DENSE_MEM_E2E_PROMETHEUS_CONTAINER_NAME="$prometheus_name" \
        DENSE_MEM_E2E_PRECHECKS_COMPLETED=1 \
        DENSE_MEM_E2E_SKIP_PRECHECKS=0 \
        DENSE_MEM_E2E_API_PORT="${reserved_ports[$offset]}" \
        DENSE_MEM_E2E_CONTROL_PORT="${reserved_ports[$((offset + 1))]}" \
        DENSE_MEM_E2E_PROMETHEUS_PORT="${reserved_ports[$((offset + 2))]}" \
        DENSE_MEM_E2E_POSTGRES_PORT="${reserved_ports[$((offset + 3))]}" \
        DENSE_MEM_E2E_NEO4J_HTTP_PORT="${reserved_ports[$((offset + 4))]}" \
        DENSE_MEM_E2E_NEO4J_BOLT_PORT="${reserved_ports[$((offset + 5))]}" \
        DENSE_MEM_E2E_REDIS_PORT="${reserved_ports[$((offset + 6))]}" \
        DENSE_MEM_E2E_ENTRA_PORT="${reserved_ports[$((offset + 7))]}" \
        DENSE_MEM_E2E_CONFLICT_PROVIDER_PORT="${reserved_ports[$((offset + 8))]}" \
        DENSE_MEM_E2E_OAUTH_PROVIDER_PORT="${reserved_ports[$((offset + 9))]}" \
        DENSE_MEM_E2E_OAUTH_HARNESS_PORT="${reserved_ports[$((offset + 10))]}" \
        DENSE_MEM_E2E_EMBEDDING_PROXY_PORT="${reserved_ports[$((offset + 11))]}" \
        "${ROOT_DIR}/scripts/e2e-compose.sh" >"$log_file" 2>&1 &
      pid=$!
      active_pids+=("$pid")
      E2E_ALL_ACTIVE_PIDS+=("$pid")
      pid_scenario["$pid"]="$scenario"
      pid_log["$pid"]="$log_file"
      scenario_status["$scenario"]="running"
      next_index=$((next_index + 1))
    done

    finished_pid=""
    if wait -n -p finished_pid "${active_pids[@]}"; then
      finished_status=0
    else
      finished_status=$?
    fi
    scenario="${pid_scenario[$finished_pid]-unknown}"
    remaining_pids=()
    for pid in "${active_pids[@]}"; do
      if [[ "$pid" != "$finished_pid" ]]; then
        remaining_pids+=("$pid")
      fi
    done
    active_pids=("${remaining_pids[@]}")
    E2E_ALL_ACTIVE_PIDS=("${active_pids[@]}")

    if [[ "$finished_status" == "0" ]]; then
      scenario_status["$scenario"]="passed"
      echo "Compose e2e scenario ${scenario} passed."
      continue
    fi

    scenario_status["$scenario"]="failed (${finished_status})"
    first_failure_status="$finished_status"
    first_failure_scenario="$scenario"
    echo "Compose e2e scenario ${scenario} failed with status ${finished_status}; terminating active scenarios." >&2
    for pid in "${active_pids[@]}"; do
      scenario="${pid_scenario[$pid]-unknown}"
      scenario_status["$scenario"]="canceled"
    done
    for ((offset = next_index; offset < scenario_count; offset += 1)); do
      scenario_status["${E2E_ALL_SCENARIOS[$offset]}"]="canceled"
    done
    terminate_e2e_all_process_groups "${active_pids[@]}"
    active_pids=()
    E2E_ALL_ACTIVE_PIDS=()
    break
  done

  trap - INT TERM
  echo "Compose e2e all summary:"
  for scenario in "${E2E_ALL_SCENARIOS[@]}"; do
    printf '  %-34s %-12s %s\n' "$scenario" "${scenario_status[$scenario]}" "${log_dir}/${scenario}.log"
  done
  if [[ "$first_failure_status" != "0" ]]; then
    echo "Last 120 lines from failed scenario ${first_failure_scenario}:" >&2
    tail -n 120 "${log_dir}/${first_failure_scenario}.log" >&2 || true
    return "$first_failure_status"
  fi
  echo "All compose e2e scenarios passed. Logs: ${log_dir}"
}
