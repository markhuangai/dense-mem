#!/usr/bin/env bash
set -euo pipefail

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

BASE_SHA="4293ba460c5f24ae9123c6d141e17c34abe582a1"
HEAD_SHA="54b0fa5a6a4f38ac70f27bd36ce8a9993e8493e8"
DEFAULT_LEDGER="docs/v2/pr71-parity-ledger.tsv"
DEFAULT_MANIFEST="docs/v2/pr71-path-manifest.txt"
DEFAULT_WIKI_GAP_MANIFEST="docs/v2/wiki-gap-manifest.txt"

usage() {
	printf 'usage: %s [--self-test] [ledger-path]\n' "$0" >&2
}

validate_ledger() {
	local ledger="$1"
	local expected
	local actual
	local expected_wiki_gaps
	local actual_wiki_gaps

	if [[ ! -f "${ledger}" ]]; then
		printf 'missing parity ledger: %s\n' "${ledger}" >&2
		return 1
	fi

	if [[ ! -f "${DEFAULT_MANIFEST}" ]]; then
		printf 'missing PR #71 path manifest: %s\n' "${DEFAULT_MANIFEST}" >&2
		return 1
	fi
	if [[ ! -f "${DEFAULT_WIKI_GAP_MANIFEST}" ]]; then
		printf 'missing wiki-gap manifest: %s\n' "${DEFAULT_WIKI_GAP_MANIFEST}" >&2
		return 1
	fi

	expected="$(mktemp "${TMP_DIR}/expected.XXXXXX")"
	actual="$(mktemp "${TMP_DIR}/actual.XXXXXX")"
	expected_wiki_gaps="$(mktemp "${TMP_DIR}/expected-wiki-gaps.XXXXXX")"
	actual_wiki_gaps="$(mktemp "${TMP_DIR}/actual-wiki-gaps.XXXXXX")"

	sort "${DEFAULT_MANIFEST}" > "${expected}"
	sort "${DEFAULT_WIKI_GAP_MANIFEST}" > "${expected_wiki_gaps}"

	awk -F '\t' -v actual="${actual}" -v actual_wiki_gaps="${actual_wiki_gaps}" '
		BEGIN {
			expected_header = "source\tpath\tdisposition\towner_issue\twiki_invariant\tverification\trationale"
			disposition["retain"] = 1
			disposition["replace"] = 1
			disposition["exclude"] = 1
			disposition["add"] = 1
		}
		NR == 1 {
			if ($0 != expected_header) {
				printf("invalid header: %s\n", $0) > "/dev/stderr"
				bad = 1
			}
			next
		}
		{
			if (NF != 7) {
				printf("line %d has %d fields, want 7\n", NR, NF) > "/dev/stderr"
				bad = 1
				next
			}

			source = $1
			path = $2
			disp = $3
			owner = $4
			invariant = $5
			verification = $6
			rationale = $7

			key = source "\t" path
			if (seen[key]++) {
				printf("duplicate row for %s %s\n", source, path) > "/dev/stderr"
				bad = 1
			}
			if (path == "" || owner == "" || invariant == "" || verification == "" || rationale == "") {
				printf("line %d has a missing required field\n", NR) > "/dev/stderr"
				bad = 1
			}
			if (!(disp in disposition)) {
				printf("line %d has invalid disposition %s\n", NR, disp) > "/dev/stderr"
				bad = 1
			}
			if (owner !~ /^#[0-9]+$/) {
				printf("line %d has invalid owner_issue %s\n", NR, owner) > "/dev/stderr"
				bad = 1
			} else {
				issue = substr(owner, 2) + 0
				if (issue < 74 || issue > 95) {
					printf("line %d owner_issue out of active range: %s\n", NR, owner) > "/dev/stderr"
					bad = 1
				}
			}

			if (source == "pr71") {
				print path > actual
				pr71_count++
			} else if (source == "wiki-gap") {
				print path > actual_wiki_gaps
				wiki_gap_count++
				if (disp != "add" && disp != "replace") {
					printf("line %d wiki-gap disposition must be add or replace\n", NR) > "/dev/stderr"
					bad = 1
				}
			} else {
				printf("line %d has invalid source %s\n", NR, source) > "/dev/stderr"
				bad = 1
			}
		}
		END {
			if (pr71_count != 189) {
				printf("expected 189 pr71 rows, got %d\n", pr71_count) > "/dev/stderr"
				bad = 1
			}
			if (wiki_gap_count != 10) {
				printf("expected 10 wiki-gap rows, got %d\n", wiki_gap_count) > "/dev/stderr"
				bad = 1
			}
			exit bad ? 1 : 0
		}
	' "${ledger}"

	sort -o "${actual}" "${actual}"
	if ! diff -u "${expected}" "${actual}"; then
		printf 'ledger PR #71 paths do not match %s generated from %s..%s\n' "${DEFAULT_MANIFEST}" "${BASE_SHA}" "${HEAD_SHA}" >&2
		return 1
	fi
	sort -o "${actual_wiki_gaps}" "${actual_wiki_gaps}"
	if ! diff -u "${expected_wiki_gaps}" "${actual_wiki_gaps}"; then
		printf 'ledger wiki-gap identities do not match %s\n' "${DEFAULT_WIKI_GAP_MANIFEST}" >&2
		return 1
	fi
}

self_test() {
	local ledger="$1"
	local bad_ledger

	validate_ledger "${ledger}"

	bad_ledger="$(mktemp "${TMP_DIR}/bad-ledger.XXXXXX")"
	awk -F '\t' 'BEGIN { OFS = "\t" } NR == 1 { print; next } $1 == "pr71" && !dropped { dropped = 1; next } { print }' "${ledger}" > "${bad_ledger}"

	if validate_ledger "${bad_ledger}" >/dev/null 2>&1; then
		printf 'negative validation unexpectedly passed\n' >&2
		return 1
	fi

	bad_ledger="$(mktemp "${TMP_DIR}/bad-wiki-gap-ledger.XXXXXX")"
	awk -F '\t' 'BEGIN { OFS = "\t" } NR == 1 { print; next } $1 == "wiki-gap" && !dropped { dropped = 1; next } { print }' "${ledger}" > "${bad_ledger}"
	if validate_ledger "${bad_ledger}" >/dev/null 2>&1; then
		printf 'wiki-gap negative validation unexpectedly passed\n' >&2
		return 1
	fi

	printf 'parity ledger validation and negative self-test passed\n'
}

self_test_mode=false
ledger="${DEFAULT_LEDGER}"

while [[ $# -gt 0 ]]; do
	case "$1" in
		--self-test)
			self_test_mode=true
			shift
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			ledger="$1"
			shift
			;;
	esac
done

if [[ "${self_test_mode}" == "true" ]]; then
	self_test "${ledger}"
else
	validate_ledger "${ledger}"
fi
