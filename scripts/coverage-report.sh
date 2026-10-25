#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

usage() {
	cat >&2 <<'EOF'
usage: scripts/coverage-report.sh --transitional | --complete | --complete-part core|postgres | --merge-complete

--transitional runs the legacy Go inventory. --complete accounts for every
first-party root-module package, the evaluation profile, focused PostgreSQL
application coverage, the nested E2E module, and cross-package execution, then
enforces the strict above-90-percent gate on the deduplicated merged inventory.
--complete-part produces one source-bound profile set; --merge-complete requires
both sets from the same source and workflow attempt before enforcing the gate.
EOF
}

case "${1:-}" in
	--transitional|--complete|--merge-complete) [[ $# == 1 ]] || { usage; exit 2; } ;;
	--complete-part) [[ $# == 2 && ( "$2" == core || "$2" == postgres ) ]] || { usage; exit 2; } ;;
	*) usage; exit 2 ;;
esac

COVERAGE_DIR="${COVERAGE_OUTPUT_DIR:-${ROOT_DIR}/coverage}"
mkdir -p "${COVERAGE_DIR}"

report_total() {
	local profile="$1" report="$2" module_dir="$3" total
	(
		cd "${module_dir}"
		go tool cover -func="${profile}"
	) | sed '/^$/d' | tee "${report}"
	total="$(awk '/^total:/ { gsub(/%/, "", $3); print $3 }' "${report}")"
	printf 'coverage %.1f%%\n' "${total}"
}

profile_totals() {
	awk '
	NR > 1 && NF >= 3 {
		total += $2
		if (($3 + 0) > 0) covered += $2
	}
	END {
		if (total == 0) {
			printf "0 0 0.0\n"
			exit
		}
		printf "%d %d %.1f\n", covered, total, (covered / total) * 100
	}' "$1"
}

enforce_profile_threshold() {
	local profile="$1" label="$2" covered total
	read -r covered total _ < <(profile_totals "${profile}")
	if (( total <= 0 )); then
		echo "${label} coverage report is empty" >&2
		return 1
	fi
	if (( covered * 10 <= total * 9 )); then
		printf '%s coverage %d/%d does not exceed 90%%\n' "${label}" "${covered}" "${total}" >&2
		return 1
	fi
	printf '%s coverage %d/%d exceeds 90%%\n' "${label}" "${covered}" "${total}"
}

merge_profiles() {
	local output="$1"
	shift
	local mode profile
	for profile in "$@"; do
		if [[ ! -s "${profile}" ]] || ! awk 'NR > 1 && NF >= 3 { found = 1 } END { exit !found }' "${profile}"; then
			echo "coverage profile is missing or empty: ${profile}" >&2
			return 1
		fi
	done
	mode="$(head -n 1 "$1")"
	: > "${output}"
	printf '%s\n' "${mode}" > "${output}"
	for profile in "$@"; do
		if [[ "$(head -n 1 "${profile}")" != "${mode}" ]]; then
			echo "coverage profiles use different modes" >&2
			exit 1
		fi
	done
	for profile in "$@"; do
		tail -n +2 "${profile}"
	done | awk '
	NF < 3 { next }
	{
		key = $1 SUBSEP $2
		count = $3 + 0
		if (!(key in counts) || count > counts[key]) {
			counts[key] = count
			lines[key] = $0
		}
	}
	END {
		for (key in lines) print lines[key]
	}' | LC_ALL=C sort >> "${output}"
}

run_transitional() {
	local profile="${ROOT_DIR}/coverage.out"
	local -a packages
	mapfile -t packages < <(
		go list -f '{{if .TestGoFiles}}{{.ImportPath}}{{end}}' ./internal/... |
			sed '/^$/d' |
			grep -Ev '/(evalharness|repository|knowledge/postgres|dream/postgres|trace/postgres|graph/postgres)$|/storage/(postgres|redis)$'
	)
	printf '%s\n' "${packages[@]}"
	go test "${packages[@]}" -timeout=30m -covermode=atomic -coverprofile="${profile}" -count=1
	report_total "${profile}" "${COVERAGE_DIR}/go-transitional.txt" "${ROOT_DIR}"

	local total
	total="$(awk '/^total:/ { gsub(/%/, "", $3); print $3 }' "${COVERAGE_DIR}/go-transitional.txt")"
	awk \
		-v total="${total}" \
		-v threshold="${COVERAGE_THRESHOLD:-90.0}" \
		'BEGIN {
			if ((total * 10) <= (threshold * 10)) {
				printf("coverage %.1f%% does not exceed required %.1f%%\n", total, threshold)
				exit 1
			}
			printf("coverage %.1f%% exceeds required %.1f%%\n", total, threshold)
		}'
}

complete_paths() {
	root_profile="${COVERAGE_DIR}/core/go-root-complete.raw"
	evaluation_profile="${COVERAGE_DIR}/core/go-evaluation-complete.raw"
	postgres_profile="${COVERAGE_DIR}/postgres/go-postgres-complete.raw"
	community_scale_profile="${COVERAGE_DIR}/postgres/go-community-scale-complete.raw"
	audit_profile="${COVERAGE_DIR}/postgres/go-audit-complete.raw"
	session_profile="${COVERAGE_DIR}/postgres/go-session-complete.raw"
	e2e_profile="${COVERAGE_DIR}/core/go-e2e-complete.raw"
	root_dedup_profile="${COVERAGE_DIR}/go-root-complete.out"
	evaluation_dedup_profile="${COVERAGE_DIR}/go-evaluation-complete.out"
	postgres_dedup_profile="${COVERAGE_DIR}/go-postgres-complete.out"
	e2e_dedup_profile="${COVERAGE_DIR}/go-e2e-complete.out"
	merged_profile="${COVERAGE_DIR}/go-complete.out"
	root_report="${COVERAGE_DIR}/go-root-complete.txt"
	evaluation_report="${COVERAGE_DIR}/go-evaluation-complete.txt"
	postgres_report="${COVERAGE_DIR}/go-postgres-complete.txt"
	e2e_report="${COVERAGE_DIR}/go-e2e-complete.txt"
	complete_report="${COVERAGE_DIR}/go-complete.txt"
	core_profiles=("${root_profile}" "${evaluation_profile}" "${e2e_profile}")
	postgres_profiles=("${postgres_profile}" "${community_scale_profile}" "${audit_profile}" "${session_profile}")
}

coverage_identity() {
	printf '%s\n' "$(git rev-parse HEAD)" "${GITHUB_RUN_ID:-local}" "${GITHUB_RUN_ATTEMPT:-1}"
}

seal_complete_part() {
	local part="$1"
	shift
	for profile in "$@"; do
		if [[ ! -s "$profile" ]] || ! awk 'NR > 1 && NF >= 3 { found = 1 } END { exit !found }' "$profile"; then
			echo "coverage profile is missing or empty: ${profile}" >&2
			return 1
		fi
	done
	coverage_identity > "${COVERAGE_DIR}/${part}/identity.txt"
	(cd "${COVERAGE_DIR}/${part}"; sha256sum go-*-complete.raw > profiles.sha256)
}

verify_complete_parts() {
	local part expected profile
	expected="$(coverage_identity)"
	for part in core postgres; do
		[[ -f "${COVERAGE_DIR}/${part}/identity.txt" && "$(cat "${COVERAGE_DIR}/${part}/identity.txt")" == "$expected" ]] || {
			echo "coverage ${part} identity does not match the source and workflow attempt" >&2
			return 1
		}
		local -a expected_profiles
		if [[ "$part" == core ]]; then expected_profiles=("${core_profiles[@]}"); else expected_profiles=("${postgres_profiles[@]}"); fi
		local expected_checksums
		expected_checksums="$(for profile in "${expected_profiles[@]}"; do
			(cd "$(dirname "$profile")"; sha256sum "$(basename "$profile")") || return 1
		done | LC_ALL=C sort)"
		[[ -f "${COVERAGE_DIR}/${part}/profiles.sha256" && "$(LC_ALL=C sort "${COVERAGE_DIR}/${part}/profiles.sha256")" == "$expected_checksums" ]] || {
			echo "coverage ${part} profile inventory or checksum does not match" >&2
			return 1
		}
		(cd "${COVERAGE_DIR}/${part}"; sha256sum --check --status profiles.sha256) || {
			echo "coverage ${part} profile checksum verification failed" >&2
			return 1
		}
	done
}

run_complete_part() {
	local part="$1"
	complete_paths
	mkdir -p "${COVERAGE_DIR}/${part}"
	rm -f -- "${COVERAGE_DIR}/${part}/identity.txt" "${COVERAGE_DIR}/${part}/profiles.sha256"
	local -a packages evaluation_packages cover_packages evaluation_cover_packages
	mapfile -t packages < <(scripts/go-packages.sh --coverage | grep -v '/cmd/server$')
	mapfile -t evaluation_packages < <(scripts/go-packages.sh --coverage --tags evaluation | grep -v '/cmd/server$')
	cover_packages=("${packages[@]}")
	evaluation_cover_packages=("${evaluation_packages[@]}")
	local coverpkg
	coverpkg="$(IFS=,; printf '%s' "${cover_packages[*]}")"
	local evaluation_coverpkg
	evaluation_coverpkg="$(IFS=,; printf '%s' "${evaluation_cover_packages[*]}")"
	if [[ "$part" == core ]]; then
		printf '%s\n' "${packages[@]}"
		go test "${packages[@]}" -timeout=30m -covermode=atomic -coverpkg="${coverpkg}" -coverprofile="${root_profile}" -count=1
		go test -tags evaluation "${evaluation_packages[@]}" -timeout=30m -covermode=atomic -coverpkg="${evaluation_coverpkg}" -coverprofile="${evaluation_profile}" -count=1
		go -C cmd/e2e test ./... -covermode=atomic -coverprofile="${e2e_profile}" -count=1
		seal_complete_part core "${core_profiles[@]}"
	else
		DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 go test -tags integration ./internal/ontology/postgres ./internal/community/postgres ./internal/dream/postgres ./cmd/internal/serverapp \
			-run '^(TestOntologyMaintenance(Finalization.*|Parallel.*|Revalidation.*|CurrentReceiptCompletion|Cohort|RegenerationFailureRetry|ProviderPauseDrain|AccountingFailurePreventsPublication|ContextConfigurationAndManualReuse|BudgetRetirement.*|GlobalAccounting|LeaseInterruptionReturnsPending|ProgressBeyondFormerBudgets|ProviderFailureReleasesCapacity|DirectOrganizationRejectsMissingContext|ControlInterruptionResumesSameWindow|PauseCommandsAndWindows|OperatorHTTPAndScheduler|CommandAuditIsAtomic)|TestOntologyAutomaticRetirementPreservesGroupingOverrides|TestOntologyCurrentReadersDistinguishDependencyAndSourceChanges|TestOntologyGroupingOverridesPreserveAssignmentFreshness|TestOntologyCurrentReadersReportCanonicalFingerprintAndVersionChanges|TestOntologyVocabularyUsesCurrentCandidateWindow|TestOntologyVocabularyWindowPreservesTeamIsolationAndNameReuse|TestOntologyFollowupCohort|TestOntologyMaintenanceBoundedCompletionUsesPersistedOutcomes|TestOntologyMaintenanceEarlierAmbiguityKeepsBoundedRunIncomplete|TestOntologyMaintenanceScheduledRecoveryClearsCurrentReason|TestOntologyMaintenanceCompletedRunDoesNotHideUnrelatedFailures|TestOntologyMaintenanceConcurrentInterruptionPreservesRunWork|TestOntologyMixedSeparationOverrideProtectsEvidenceGroups|TestOntologyManagement.*|TestCommunityTopic.*|TestRetiredDreamHistoryPreservesProvenanceIsolationAndRetention|TestServerLifecycle.*|TestRunActiveServerCanceledBeforeWorkersDoesNotBuildRuntimeWorker|TestPrivateMemoryCommandsInspectFreshDatabaseWithoutStartingOrMigrating|TestPrivateMemoryPreflightCommandBoundsLegacyReportAndReturnsBlockers)$' \
			-skip '^TestCommunityTopicLargeFanoutResumesAndRotates$' \
			-count=1 -timeout=30m -covermode=atomic -coverpkg="${coverpkg}" -coverprofile="${postgres_profile}"
		DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 go test -tags integration ./internal/community/postgres \
			-run '^TestCommunityTopicLargeFanoutResumesAndRotates$' \
			-count=1 -timeout=15m -covermode=atomic -coverpkg="${coverpkg}" -coverprofile="${community_scale_profile}"
		DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 go test -tags integration ./internal/service \
			-run '^TestAudit' -count=1 -timeout=5m -covermode=atomic -coverpkg="${coverpkg}" -coverprofile="${audit_profile}"
		DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 DENSE_MEM_REQUIRE_POSTGRES_TESTS=1 go test -tags integration ./internal/knowledge/postgres \
			-run '^TestSession' -count=1 -timeout=15m -covermode=atomic -coverpkg="${coverpkg}" -coverprofile="${session_profile}"
		seal_complete_part postgres "${postgres_profiles[@]}"
	fi
}

merge_complete() {
	complete_paths
	verify_complete_parts
	merge_profiles "${root_dedup_profile}" "${root_profile}"
	merge_profiles "${evaluation_dedup_profile}" "${evaluation_profile}"
	merge_profiles "${postgres_dedup_profile}" "${postgres_profile}" "${community_scale_profile}" "${audit_profile}" "${session_profile}"
	merge_profiles "${e2e_dedup_profile}" "${e2e_profile}"
	merge_profiles "${merged_profile}" "${root_dedup_profile}" "${evaluation_dedup_profile}" "${postgres_dedup_profile}" "${e2e_dedup_profile}"
	report_total "${root_dedup_profile}" "${root_report}" "${ROOT_DIR}"
	report_total "${evaluation_dedup_profile}" "${evaluation_report}" "${ROOT_DIR}"
	report_total "${postgres_dedup_profile}" "${postgres_report}" "${ROOT_DIR}"
	report_total "${e2e_dedup_profile}" "${e2e_report}" "${ROOT_DIR}/cmd/e2e"

	local -a totals
	read -r -a totals <<< "$(profile_totals "${merged_profile}")"
	{
		printf 'production profile\n'
		cat "${root_report}"
		printf '\nevaluation profile\n'
		cat "${evaluation_report}"
		printf '\nPostgreSQL application profile\n'
		cat "${postgres_report}"
		printf '\ncmd/e2e module\n'
		cat "${e2e_report}"
		printf '\ncomplete total: %d/%d %.1f%%\n' "${totals[0]}" "${totals[1]}" "${totals[2]}"
	} | tee "${complete_report}"
	enforce_profile_threshold "${merged_profile}" "complete Go"
}

run_complete() {
	run_complete_part core
	run_complete_part postgres
	merge_complete
}

case "$1" in
	--transitional) run_transitional ;;
	--complete) run_complete ;;
	--complete-part) run_complete_part "$2" ;;
	--merge-complete) merge_complete ;;
esac
