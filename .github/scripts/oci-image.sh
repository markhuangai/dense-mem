#!/usr/bin/env bash
set -euo pipefail

REGCTL_BIN="${REGCTL_BIN:-regctl}"

fail() {
	printf 'image policy error: %s\n' "$*" >&2
	exit 1
}

require_tools() {
	command -v "${REGCTL_BIN}" >/dev/null || fail "regctl is not available"
	command -v jq >/dev/null || fail "jq is not available"
}

manifest_json() {
	"${REGCTL_BIN}" manifest get "$1" --format raw-body
}

require_platforms() {
	manifest_json "$1" |
		jq -e '
			.mediaType == "application/vnd.oci.image.index.v1+json" and
			([.manifests[] | (.platform.os + "/" + .platform.architecture)] | sort) ==
			["linux/amd64", "linux/arm64"]
		' >/dev/null || fail "$1 does not contain exactly linux/amd64 and linux/arm64"
}

require_labels() {
	local ref="$1"
	local version="$2"
	local revision="$3"
	local pull_number="$4"
	local main_revision="$5"
	local run_id="$6"
	local run_attempt="$7"
	local platform

	for platform in linux/amd64 linux/arm64; do
		"${REGCTL_BIN}" image inspect "${ref}" --platform "${platform}" --format '{{json .}}' |
			jq -e \
				--arg version "${version}" \
				--arg revision "${revision}" \
				--arg pull_number "${pull_number}" \
				--arg main_revision "${main_revision}" \
				--arg run_id "${run_id}" \
				--arg run_attempt "${run_attempt}" '
					.config.Labels["org.opencontainers.image.version"] == $version and
					.config.Labels["org.opencontainers.image.revision"] == $revision and
					.config.Labels["org.opencontainers.image.variant"] == "production" and
					.config.Labels["io.dense-mem.preview.pr"] == $pull_number and
					.config.Labels["io.dense-mem.preview.head"] == $revision and
					.config.Labels["io.dense-mem.preview.main"] == $main_revision and
					.config.Labels["io.dense-mem.preview.run-id"] == $run_id and
					.config.Labels["io.dense-mem.preview.run-attempt"] == $run_attempt
				' >/dev/null || fail "${ref} has invalid ${platform} preview labels"
	done
}

require_preview() {
	local ref="$1"
	local pull_number="$2"
	local head_revision="$3"
	local main_revision="$4"
	local run_id="$5"
	local run_attempt="$6"

	require_platforms "${ref}"
	require_labels \
		"${ref}" \
		"test-${pull_number}" \
		"${head_revision}" \
		"${pull_number}" \
		"${main_revision}" \
		"${run_id}" \
		"${run_attempt}"
}

platform_layers() {
	local ref="$1"
	local platform="$2"
	local digest

	digest="$(
		manifest_json "${ref}" |
			jq -er --arg platform "${platform}" '
				.manifests[] |
				select((.platform.os + "/" + .platform.architecture) == $platform) |
				.digest
			'
	)"
	manifest_json "${ref%@*}@${digest}" | jq -cS '.layers'
}

publish_preview() {
	local layout="$1"
	local target_ref="$2"
	local pull_number="$3"
	local head_revision="$4"
	local main_revision="$5"
	local run_id="$6"
	local run_attempt="$7"
	local source_ref="ocidir://${layout}"
	local source_digest
	local target_digest

	require_preview \
		"${source_ref}" \
		"${pull_number}" \
		"${head_revision}" \
		"${main_revision}" \
		"${run_id}" \
		"${run_attempt}"
	source_digest="$("${REGCTL_BIN}" manifest head "${source_ref}")"
	"${REGCTL_BIN}" image copy --force-recursive "${source_ref}@${source_digest}" "${target_ref}"
	target_digest="$("${REGCTL_BIN}" manifest head "${target_ref}")"
	[[ "${target_digest}" == "${source_digest}" ]] ||
		fail "published digest ${target_digest} does not match artifact ${source_digest}"
	require_preview \
		"${target_ref}@${target_digest}" \
		"${pull_number}" \
		"${head_revision}" \
		"${main_revision}" \
		"${run_id}" \
		"${run_attempt}"
	printf '%s\n' "${target_digest}"
}

preview_receipt() {
	local ref="$1"
	local pull_number="$2"
	local head_revision="$3"
	local run_id="$4"
	local run_attempt="$5"
	local source_digest
	local main_revision

	source_digest="$("${REGCTL_BIN}" manifest head "${ref}")"
	[[ "${source_digest}" =~ ^sha256:[0-9a-f]{64}$ ]] ||
		fail "${ref} returned an invalid manifest digest"
	main_revision="$(
		"${REGCTL_BIN}" image inspect "${ref}@${source_digest}" \
			--platform linux/amd64 \
			--format '{{json .}}' |
			jq -er '.config.Labels["io.dense-mem.preview.main"]'
	)"
	[[ "${main_revision}" =~ ^[0-9a-f]{40}$ ]] ||
		fail "${ref} contains an invalid tested main revision"
	require_preview \
		"${ref}@${source_digest}" \
		"${pull_number}" \
		"${head_revision}" \
		"${main_revision}" \
		"${run_id}" \
		"${run_attempt}"
	printf '%s\t%s\n' "${source_digest}" "${main_revision}"
}

promote_preview() {
	local source_ref="$1"
	local target_ref="$2"
	local version="$3"
	local revision="$4"
	local created="$5"
	local source_layers_amd64
	local source_layers_arm64
	local platform
	local target_digest

	require_platforms "${source_ref}"
	source_layers_amd64="$(platform_layers "${source_ref}" linux/amd64)"
	source_layers_arm64="$(platform_layers "${source_ref}" linux/arm64)"
	"${REGCTL_BIN}" image mod "${source_ref}" \
		--create "${target_ref}" \
		--label "org.opencontainers.image.version=${version}" \
		--label "org.opencontainers.image.revision=${revision}" \
		--label "org.opencontainers.image.created=${created}"
	target_digest="$("${REGCTL_BIN}" manifest head "${target_ref}")"
	for platform in linux/amd64 linux/arm64; do
		"${REGCTL_BIN}" image inspect "${target_ref}@${target_digest}" \
			--platform "${platform}" \
			--format '{{json .}}' |
			jq -e \
				--arg version "${version}" \
				--arg revision "${revision}" \
				--arg created "${created}" '
					.config.Labels["org.opencontainers.image.version"] == $version and
					.config.Labels["org.opencontainers.image.revision"] == $revision and
					.config.Labels["org.opencontainers.image.created"] == $created
				' >/dev/null || fail "${target_ref} has invalid ${platform} RC metadata"
	done
	[[ "$(platform_layers "${target_ref}@${target_digest}" linux/amd64)" == "${source_layers_amd64}" ]] ||
		fail "${target_ref} changed linux/amd64 layer descriptors"
	[[ "$(platform_layers "${target_ref}@${target_digest}" linux/arm64)" == "${source_layers_arm64}" ]] ||
		fail "${target_ref} changed linux/arm64 layer descriptors"
	printf '%s\n' "${target_digest}"
}

usage() {
	printf '%s\n' \
		"usage:" \
		"  $0 validate-preview REF PR HEAD MAIN RUN_ID RUN_ATTEMPT" \
		"  $0 preview-receipt REF PR HEAD RUN_ID RUN_ATTEMPT" \
		"  $0 publish-preview LAYOUT TARGET_REF PR HEAD MAIN RUN_ID RUN_ATTEMPT" \
		"  $0 promote-preview SOURCE_REF TARGET_REF VERSION REVISION CREATED" >&2
	exit 2
}

require_tools

case "${1:-}" in
validate-preview)
	[[ "$#" -eq 7 ]] || usage
	require_preview "$2" "$3" "$4" "$5" "$6" "$7"
	;;
publish-preview)
	[[ "$#" -eq 8 ]] || usage
	publish_preview "$2" "$3" "$4" "$5" "$6" "$7" "$8"
	;;
preview-receipt)
	[[ "$#" -eq 6 ]] || usage
	preview_receipt "$2" "$3" "$4" "$5" "$6"
	;;
promote-preview)
	[[ "$#" -eq 6 ]] || usage
	promote_preview "$2" "$3" "$4" "$5" "$6"
	;;
*)
	usage
	;;
esac
