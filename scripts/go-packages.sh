#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PRODUCTION_ONLY=false
GO_TAGS=""

usage() {
	cat >&2 <<'EOF'
usage: scripts/go-packages.sh [--production] [--tags <tags>] [--root <dir>]

Print import paths for Go directories tracked by the repository. The default
includes the historical test packages; --production limits discovery to
runtime packages and commands. --tags selects an additional Go build-tag
profile without changing the default invocation used by CI.
EOF
}

while (($# > 0)); do
	case "$1" in
		--production)
			PRODUCTION_ONLY=true
			shift
			;;
		--tags)
			if (($# < 2)) || [[ -z "$2" ]]; then
				usage
				exit 2
			fi
			GO_TAGS="$2"
			shift 2
			;;
		--root)
			if (($# < 2)) || [[ -z "$2" ]]; then
				usage
				exit 2
			fi
			ROOT_DIR="$2"
			shift 2
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			echo "unknown option: $1" >&2
			usage
			exit 2
			;;
	esac
done

ROOT_DIR="$(cd "${ROOT_DIR}" && pwd)"
cd "${ROOT_DIR}"

go_list_args=()
if [[ -n "${GO_TAGS}" ]]; then
	go_list_args+=("-tags" "${GO_TAGS}")
fi

is_excluded() {
	local path="$1"
	if [[ "${path}" == tests/* || "${path}" == */tests/* ]]; then
		return 0
	fi
	if [[ "${path}" == cmd/eval-* || "${path}" == internal/evalharness ]]; then
		return 0
	fi
	return 1
}

mapfile -t tracked_files < <(
	if git -C "${ROOT_DIR}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		git -C "${ROOT_DIR}" ls-files '*.go'
	else
		find "${ROOT_DIR}" -type f -name '*.go' -print | sed "s#^${ROOT_DIR}/##"
	fi
)

mapfile -t tracked_dirs < <(
	printf '%s\n' "${tracked_files[@]}" |
	awk '{ file = $0; if (file ~ /\//) { sub(/\/[^\/]+$/, "", file) } else { file = "." }; print file }' |
	while IFS= read -r dir; do
		if [[ "${PRODUCTION_ONLY}" == true ]] && is_excluded "${dir}"; then
			continue
		fi
		if [[ "${dir}" != tests/uat/* && "${dir}" != tests/eval/runtime/* ]]; then
			printf '%s\n' "${dir}"
		fi
	done |
	sort -u
)

if ((${#tracked_dirs[@]} == 0)); then
	echo "no tracked Go packages found" >&2
	exit 1
fi

packages=()
for dir in "${tracked_dirs[@]}"; do
	if ! package=$(go list "${go_list_args[@]}" "./${dir#./}"); then
		echo "failed to resolve tracked Go package: ${dir}" >&2
		exit 1
	fi
	packages+=("${package}")
done

printf '%s\n' "${packages[@]}" | sort -u
