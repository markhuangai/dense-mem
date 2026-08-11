#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

packages="$(scripts/go-packages.sh)"
printf '%s\n' "${packages}"
go tool staticcheck -checks=inherit,-U1000 ${packages}

production_files="$(
	git ls-files '*.go' |
	while IFS= read -r file; do
		if [[ -f "${file}" && "${file}" != *_test.go && "${file}" != tests/* ]]; then
			printf '%s\n' "${file}"
		fi
	done
)"
if [[ -z "${production_files}" ]]; then
	echo "no tracked production Go files found" >&2
	exit 1
fi
printf '%s\n' "${production_files}" | go tool dupl -t 250 -files
