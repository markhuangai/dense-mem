#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

SEMVER_REGEX='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
RC_REGEX='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.(0|[1-9][0-9]*)$'

fail() {
	printf 'prerelease version error: %s\n' "$*" >&2
	exit 1
}

version_is_greater() {
	local candidate="$1"
	local baseline="$2"
	local highest

	[[ "${candidate}" != "${baseline}" ]] || return 1
	highest="$(printf '%s\n%s\n' "${candidate}" "${baseline}" | sort -V | tail -n 1)"
	[[ "${highest}" == "${candidate}" ]]
}

latest_stable() {
	local latest=""
	local tag

	while IFS= read -r tag; do
		if [[ "${tag}" =~ ${SEMVER_REGEX} ]] &&
			{ [[ -z "${latest}" ]] || version_is_greater "${tag}" "${latest}"; }; then
			latest="${tag}"
		fi
	done < <(git tag -l 'v*.*.*')

	printf '%s\n' "${latest:-v0.0.0}"
}

latest_active_base() {
	local stable="$1"
	local active=""
	local base
	local tag

	while IFS= read -r tag; do
		[[ "${tag}" =~ ${RC_REGEX} ]] || continue
		base="${tag%-rc.*}"
		version_is_greater "${base}" "${stable}" || continue
		if [[ -z "${active}" ]] || version_is_greater "${base}" "${active}"; then
			active="${base}"
		fi
	done < <(git tag -l 'v*.*.*-rc.*')

	printf '%s\n' "${active}"
}

latest_rc() {
	local base="$1"
	local latest=""
	local tag

	while IFS= read -r tag; do
		[[ "${tag}" =~ ${RC_REGEX} ]] || continue
		[[ "${tag%-rc.*}" == "${base}" ]] || continue
		if [[ -z "${latest}" ]] || [[ "$(printf '%s\n%s\n' "${tag}" "${latest}" | sort -V | tail -n 1)" == "${tag}" ]]; then
			latest="${tag}"
		fi
	done < <(git tag -l "${base}-rc.*")

	printf '%s\n' "${latest}"
}

resolve_next() {
	local stable
	local base
	local latest
	local rc_number

	stable="$(latest_stable)"
	base="$(latest_active_base "${stable}")"
	if [[ -z "${base}" ]]; then
		[[ "${stable}" =~ ${SEMVER_REGEX} ]] || fail "latest stable tag is invalid: ${stable}"
		base="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((BASH_REMATCH[3] + 1))"
	fi

	latest="$(latest_rc "${base}")"
	if [[ -z "${latest}" ]]; then
		rc_number=0
	elif [[ "${latest}" =~ ${RC_REGEX} ]]; then
		rc_number="$((BASH_REMATCH[4] + 1))"
	else
		fail "latest RC tag is invalid: ${latest}"
	fi

	printf '%s-rc.%s\n' "${base}" "${rc_number}"
}

resolve_start() {
	local base="${1:-}"
	local target="${2:-}"
	local stable
	local active
	local seed
	local latest
	local tagged_commit

	[[ "${base}" =~ ${SEMVER_REGEX} ]] ||
		fail "release base must be a canonical semantic version like v2.5.0"
	[[ "${target}" =~ ^[0-9a-f]{40}$ ]] || fail "target must be a 40-character commit SHA"
	git rev-parse --verify "${target}^{commit}" >/dev/null 2>&1 || fail "target commit does not exist: ${target}"

	stable="$(latest_stable)"
	version_is_greater "${base}" "${stable}" ||
		fail "release base ${base} must be newer than latest stable ${stable}"

	active="$(latest_active_base "${stable}")"
	seed="${base}-rc.0"
	if [[ -n "${active}" && "${base}" == "${active}" ]]; then
		latest="$(latest_rc "${base}")"
		[[ "${latest}" == "${seed}" ]] ||
			fail "prerelease line ${base} already advanced to ${latest}"
		tagged_commit="$(git rev-list -n 1 "refs/tags/${seed}")"
		[[ "${tagged_commit}" == "${target}" ]] ||
			fail "${seed} already exists at ${tagged_commit}; expected ${target}"
		printf '%s\n' "${seed}"
		return
	fi

	if [[ -n "${active}" ]]; then
		version_is_greater "${base}" "${active}" ||
			fail "release base ${base} must be newer than active prerelease base ${active}"
	fi
	[[ -z "$(latest_rc "${base}")" ]] || fail "prerelease line ${base} already exists"

	printf '%s\n' "${seed}"
}

resolve_previous() {
	local target="${1:-}"
	local previous

	[[ "${target}" =~ ${RC_REGEX} ]] ||
		fail "target must be a canonical RC tag like v2.5.1-rc.0"
	git rev-parse --verify "refs/tags/${target}^{commit}" >/dev/null 2>&1 ||
		fail "target tag does not exist: ${target}"

	previous="$(
		git describe \
			--first-parent \
			--tags \
			--abbrev=0 \
			--match 'v[0-9]*' \
			"refs/tags/${target}^" 2>/dev/null
	)" || fail "target ${target} has no prior release tag on first-parent history"
	if [[ ! "${previous}" =~ ${SEMVER_REGEX} && ! "${previous}" =~ ${RC_REGEX} ]]; then
		fail "prior release tag is not canonical: ${previous}"
	fi

	printf '%s\n' "${previous}"
}

usage() {
	printf '%s\n' \
		"usage:" \
		"  $0 next" \
		"  $0 start RELEASE_BASE TARGET_SHA" \
		"  $0 previous TARGET_TAG" >&2
	exit 2
}

case "${1:-}" in
next)
	[[ "$#" -eq 1 ]] || usage
	resolve_next
	;;
start)
	[[ "$#" -eq 3 ]] || usage
	resolve_start "$2" "$3"
	;;
previous)
	[[ "$#" -eq 2 ]] || usage
	resolve_previous "$2"
	;;
*)
	usage
	;;
esac
