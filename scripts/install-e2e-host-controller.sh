#!/usr/bin/env bash
set -euo pipefail

SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DESTINATION="${1:-${HOME}/dense-mem-ci}"

if [[ ! "$DESTINATION" =~ ^/ ]]; then
  echo "destination must be an absolute path" >&2
  exit 2
fi

mkdir -p "$DESTINATION"
chmod 700 "$DESTINATION"
install -m 700 "${SOURCE_DIR}/e2e-host-controller.sh" "${DESTINATION}/e2e-stack.sh"
install -m 700 "${SOURCE_DIR}/e2e-host-controller-stack.sh" "${DESTINATION}/e2e-host-controller-stack.sh"
install -m 700 "${SOURCE_DIR}/e2e-host-controller-runtime.sh" "${DESTINATION}/e2e-host-controller-runtime.sh"
install -m 700 "${SOURCE_DIR}/e2e-redact-diagnostics.mjs" "${DESTINATION}/e2e-redact-diagnostics.mjs"
install -m 700 "${SOURCE_DIR}/e2e-docker-proxy.mjs" "${DESTINATION}/e2e-docker-proxy.mjs"
install -m 700 "${SOURCE_DIR}/e2e-runtime-adapter.mjs" "${DESTINATION}/e2e-runtime-adapter.mjs"
install -m 700 "${SOURCE_DIR}/e2e-scenario-registry.mjs" "${DESTINATION}/e2e-scenario-registry.mjs"
install -m 600 "${SOURCE_DIR}/e2e-scenarios.json" "${DESTINATION}/e2e-scenarios.json"
install -m 600 "${SOURCE_DIR}/e2e-ci-compose.yml" "${DESTINATION}/docker-compose.yml"
PROMETHEUS_SOURCE="${SOURCE_DIR}/../examples/prometheus.yml"
[[ -f "${PROMETHEUS_SOURCE}" ]] || { echo "missing tracked Prometheus configuration: ${PROMETHEUS_SOURCE}" >&2; exit 1; }
install -m 644 "${PROMETHEUS_SOURCE}" "${DESTINATION}/prometheus.yml"
if [[ ! -e "${DESTINATION}/.env" ]]; then
  printf '%s\n' "Create ${DESTINATION}/.env with CI-only provider credentials and mode 0600." >&2
  exit 1
fi
chmod 600 "${DESTINATION}/.env"
if [[ ! -f "${DESTINATION}/telemetry-scrape-token" ]]; then
  printf '%s\n' "Create ${DESTINATION}/telemetry-scrape-token with the CI-only telemetry token and mode 0600." >&2
  exit 1
fi
chmod 600 "${DESTINATION}/telemetry-scrape-token"
printf '%s\n' "Installed dense-mem-ci-e2e.v1 controller in ${DESTINATION}."
