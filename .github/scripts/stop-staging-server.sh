#!/usr/bin/env bash
set -euo pipefail

stage_directory="${1:?The staging directory is required.}"
containers="$(docker ps --no-trunc \
  --filter "label=com.docker.compose.project.working_dir=${stage_directory}" \
  --filter label=com.docker.compose.service=server --format '{{.ID}}')"
if [[ -n "$containers" ]]; then
  readarray -t container_ids <<< "$containers"
  exec docker stop --time 30 "${container_ids[@]}"
fi
