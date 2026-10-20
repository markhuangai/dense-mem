#!/usr/bin/env bash
set -euo pipefail

stage_directory="${1:-}"
image_tag="${2:-}"
expected_revision="${3:-}"
image_digest="unresolved"
startup_attempted=false
healthy=false
failure_reason="Deployment did not complete."

fail() {
  failure_reason="$1"
  printf '::error::%s\n' "$failure_reason" >&2
  exit 1
}

finish() {
  local status=$?
  # The bounded cleanup process group must survive the wrapper's first termination signal.
  trap '' INT TERM
  trap - EXIT
  if [[ "$startup_attempted" == true && "$healthy" != true ]]; then
    if ! timeout --signal=TERM --kill-after=5s 40s docker compose stop --timeout 30 server; then
      failure_reason="Candidate startup failed and stopping its server also failed; inspect staging before recovery."
      printf '::error::%s\n' "$failure_reason" >&2
      status=1
    fi
  fi
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    {
      printf '## Staging recovery\n\n'
      printf -- '- Requested tag: `%s`\n' "$image_tag"
      printf -- '- Rehearsed source: `%s`\n' "$expected_revision"
      printf -- '- Image digest: `%s`\n' "$image_digest"
      printf -- '- Healthy: `%s`\n' "$healthy"
      if ((status != 0)); then
        printf -- '- Failure: %s\n' "$failure_reason"
        printf '\nNo previous image was restarted. Inspect staging and dispatch Deploy staging from main with a compatible image tag.\n'
      fi
    } >> "$GITHUB_STEP_SUMMARY"
  fi
  exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

[[ -d "$stage_directory" ]] || fail "The staging directory is missing."
[[ "$image_tag" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$ ]] || fail "A valid image tag is required."
[[ "$expected_revision" =~ ^[0-9a-f]{40}$ ]] || fail "An exact successful rehearsal revision is required."
cd -- "$stage_directory"
exec 9>migration-rehearsal.lock
flock --wait 600 9 || fail "Timed out waiting for the staging migration lock."

export DENSE_MEM_IMAGE="$image_tag"
image="$(docker compose config --images server)" || fail "Could not resolve the staging server image."
[[ -n "$image" && "$image" != *$'\n'* && "$image" != *@* ]] || fail "Compose must resolve one tagged server image."
docker pull "$image" || fail "The requested image is unavailable; staging was not restarted."
image_id="$(docker image inspect --format '{{.Id}}' "$image")" || fail "Could not inspect the pulled image."
revision="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image_id")" || fail "Could not read the image revision."
variant="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.variant"}}' "$image_id")" || fail "Could not read the image variant."
[[ "$revision" == "$expected_revision" ]] || fail "The image revision does not match the successful rehearsal; staging was not restarted."
[[ "$variant" == production ]] || fail "The image is not the production variant; staging was not restarted."
repository="${image%:*}"
repo_digests="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$image_id")" || fail "Could not read the image digest."
while IFS= read -r reference; do
  if [[ "$reference" == "$repository@"* ]]; then
    image_digest="${reference#*@}"
    break
  fi
done <<< "$repo_digests"
[[ "$image_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "The pulled image has no matching immutable registry digest."
export DENSE_MEM_IMAGE="${image_tag}@${image_digest}"
pinned_image="$(docker compose config --images server)" || fail "Could not resolve the pinned staging image."
[[ "$pinned_image" == "${image}@${image_digest}" ]] || fail "Compose did not preserve the verified image digest."
[[ "$(docker image inspect --format '{{.Id}}' "$pinned_image")" == "$image_id" ]] || fail "The pinned image differs from the verified image."

startup_attempted=true
docker compose up -d --pull always --no-deps --wait --wait-timeout 3000 server || fail "The verified candidate did not become healthy."
healthy=true
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  printf 'image_digest=%s\nsource_revision=%s\n' "$image_digest" "$expected_revision" >> "$GITHUB_OUTPUT"
fi
printf 'Staging is healthy at source %s and image %s.\n' "$expected_revision" "$image_digest"
