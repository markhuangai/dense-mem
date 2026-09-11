#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

usage() {
	cat >&2 <<'EOF'
usage: scripts/coverage-report.sh --transitional | --complete

--transitional runs the existing Go CI inventory and enforces its 90 percent
gate. --complete accounts for every first-party root-module package, the
evaluation profile, the nested E2E module, and cross-package execution
without enforcing the final architecture-program threshold yet.
EOF
}

if (($# != 1)) || [[ "$1" != "--transitional" && "$1" != "--complete" ]]; then
	usage
	exit 2
fi

COVERAGE_DIR="${COVERAGE_OUTPUT_DIR:-${ROOT_DIR}/coverage}"
mkdir -p "${COVERAGE_DIR}"

report_total() {
	local profile="$1" report="$2" module_dir="$3" total
	(
		cd "${module_dir}"
		go tool cover -func="${profile}"
	) | tee "${report}"
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
			printf "0 0 0.0"
			exit
		}
		printf "%d %d %.1f", covered, total, (covered / total) * 100
	}' "$1"
}

merge_profiles() {
	local output="$1"
	shift
	local mode profile
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
	go test "${packages[@]}" -covermode=atomic -coverprofile="${profile}" -count=1
	report_total "${profile}" "${COVERAGE_DIR}/go-transitional.txt" "${ROOT_DIR}"

	local total
	total="$(awk '/^total:/ { gsub(/%/, "", $3); print $3 }' "${COVERAGE_DIR}/go-transitional.txt")"
	awk \
		-v total="${total}" \
		-v threshold="${COVERAGE_THRESHOLD:-90.0}" \
		'BEGIN {
			if ((total + 0) < (threshold + 0)) {
				printf("coverage %.1f%% is below required %.1f%%\n", total, threshold)
				exit 1
			}
			printf("coverage %.1f%% meets required %.1f%%\n", total, threshold)
		}'
}

run_complete() {
	local root_profile="${COVERAGE_DIR}/go-root-complete.raw"
	local evaluation_profile="${COVERAGE_DIR}/go-evaluation-complete.raw"
	local e2e_profile="${COVERAGE_DIR}/go-e2e-complete.raw"
	local root_dedup_profile="${COVERAGE_DIR}/go-root-complete.out"
	local evaluation_dedup_profile="${COVERAGE_DIR}/go-evaluation-complete.out"
	local e2e_dedup_profile="${COVERAGE_DIR}/go-e2e-complete.out"
	local merged_profile="${COVERAGE_DIR}/go-complete.out"
	local root_report="${COVERAGE_DIR}/go-root-complete.txt"
	local evaluation_report="${COVERAGE_DIR}/go-evaluation-complete.txt"
	local e2e_report="${COVERAGE_DIR}/go-e2e-complete.txt"
	local complete_report="${COVERAGE_DIR}/go-complete.txt"
	local -a packages evaluation_packages cover_packages evaluation_cover_packages
	mapfile -t packages < <(scripts/go-packages.sh --coverage | grep -v '/cmd/server$')
	mapfile -t evaluation_packages < <(scripts/go-packages.sh --coverage --tags evaluation | grep -v '/cmd/server$')
	cover_packages=("${packages[@]}")
	evaluation_cover_packages=("${evaluation_packages[@]}")
	local coverpkg
	coverpkg="$(IFS=,; printf '%s' "${cover_packages[*]}")"
	local evaluation_coverpkg
	evaluation_coverpkg="$(IFS=,; printf '%s' "${evaluation_cover_packages[*]}")"
	printf '%s\n' "${packages[@]}"
	go test "${packages[@]}" -covermode=atomic -coverpkg="${coverpkg}" -coverprofile="${root_profile}" -count=1
	go test -tags evaluation "${evaluation_packages[@]}" -covermode=atomic -coverpkg="${evaluation_coverpkg}" -coverprofile="${evaluation_profile}" -count=1
	go -C cmd/e2e test ./... -covermode=atomic -coverprofile="${e2e_profile}" -count=1
	merge_profiles "${root_dedup_profile}" "${root_profile}"
	merge_profiles "${evaluation_dedup_profile}" "${evaluation_profile}"
	merge_profiles "${e2e_dedup_profile}" "${e2e_profile}"
	merge_profiles "${merged_profile}" "${root_dedup_profile}" "${evaluation_dedup_profile}" "${e2e_dedup_profile}"
	report_total "${root_dedup_profile}" "${root_report}" "${ROOT_DIR}"
	report_total "${evaluation_dedup_profile}" "${evaluation_report}" "${ROOT_DIR}"
	report_total "${e2e_dedup_profile}" "${e2e_report}" "${ROOT_DIR}/cmd/e2e"

	local -a totals
	read -r -a totals <<< "$(profile_totals "${merged_profile}")"
	{
		printf 'production profile\n'
		cat "${root_report}"
		printf '\nevaluation profile\n'
		cat "${evaluation_report}"
		printf '\ncmd/e2e module\n'
		cat "${e2e_report}"
		printf '\ncomplete total: %d/%d %.1f%%\n' "${totals[0]}" "${totals[1]}" "${totals[2]}"
	} | tee "${complete_report}"
}

case "$1" in
	--transitional) run_transitional ;;
	--complete) run_complete ;;
esac
