#!/usr/bin/env bash

pin_staging_image() {
  local image image_id revision variant repository repo_digests reference pinned_image
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
}
