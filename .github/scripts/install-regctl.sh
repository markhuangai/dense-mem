#!/usr/bin/env bash
set -euo pipefail

binary="${1:?regctl install path is required}"
export REGCTL_CONFIG="${2:?regctl config path is required}"

version=v0.11.5
sha256=c93aa7638749f5aaac1a8e01787321889c78f0101809bb2880343478d0ba0467
download="$(mktemp "${binary}.XXXXXX")"
trap 'rm -f -- "${download}"' EXIT

curl -fsSL -o "${download}" "https://github.com/regclient/regclient/releases/download/${version}/regctl-linux-amd64"
if ! printf '%s  %s\n' "${sha256}" "${download}" | sha256sum --check --status; then
  printf '::error::regctl %s linux-amd64 SHA-256 verification failed\n' "${version}" >&2
  exit 1
fi
chmod +x "${download}"
"${download}" config set --docker-cred=false --docker-cert=false
mv -f -- "${download}" "${binary}"

if [[ -n "${GITHUB_ENV:-}" ]]; then
  printf 'REGCTL_CONFIG=%s\n' "${REGCTL_CONFIG}" >> "${GITHUB_ENV}"
fi
