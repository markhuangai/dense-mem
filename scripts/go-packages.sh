#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

mapfile -t tracked_dirs < <(
	git ls-files '*.go' |
	awk '
		!/^tests\/(uat|eval\/runtime)\// {
			file = $0
			sub(/\/[^\/]+$/, "", file)
			print file
		}
	' |
	sort -u
)

if ((${#tracked_dirs[@]} == 0)); then
	echo "no tracked Go packages found" >&2
	exit 1
fi

packages=()
for dir in "${tracked_dirs[@]}"; do
	if ! package=$(go list "./${dir#./}"); then
		echo "failed to resolve tracked Go package: ${dir}" >&2
		exit 1
	fi
	packages+=("${package}")
done

printf '%s\n' "${packages[@]}" | sort -u
