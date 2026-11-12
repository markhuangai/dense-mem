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
source "$(dirname -- "${BASH_SOURCE[0]}")/staging-image.sh"
cd -- "$stage_directory"
exec 9>migration-rehearsal.lock
flock --wait 600 9 || fail "Timed out waiting for the staging migration lock."

pin_staging_image

startup_attempted=true
docker compose up -d --pull always --no-deps --wait --wait-timeout 3000 server || fail "The verified candidate did not become healthy."
healthy=true
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  printf 'image_digest=%s\nsource_revision=%s\n' "$image_digest" "$expected_revision" >> "$GITHUB_OUTPUT"
fi
printf 'Staging is healthy at source %s and image %s.\n' "$expected_revision" "$image_digest"
