#!/usr/bin/env bash

prepare_e2e_prometheus_files() {
  if [[ -e "$E2E_PROMETHEUS_DIR" ]]; then
    echo "Refusing to replace existing E2E Prometheus directory ${E2E_PROMETHEUS_DIR}." >&2
    return 1
  fi
  mkdir "$E2E_PROMETHEUS_DIR"
  chmod 755 "$E2E_PROMETHEUS_DIR"
  E2E_FILES_PREPARED=1

  cat > "${E2E_PROMETHEUS_DIR}/prometheus.yml" <<EOF
${E2E_MARKER}
global:
  scrape_interval: 5s
  evaluation_interval: 5s
scrape_configs:
  - job_name: dense-mem
    metrics_path: /metrics
    authorization:
      credentials_file: /etc/prometheus/telemetry-scrape-token
    static_configs:
      - targets:
          - server:8090
EOF

  printf '%s\n' "$TELEMETRY_SCRAPE_TOKEN" > "${E2E_PROMETHEUS_DIR}/telemetry-scrape-token"
  chmod 644 "${E2E_PROMETHEUS_DIR}/prometheus.yml" "${E2E_PROMETHEUS_DIR}/telemetry-scrape-token"
}

prepare_e2e_prometheus_volume() {
  local container_id
  compose create prometheus >/dev/null
  container_id="$(compose ps -aq prometheus)"
  if [[ -z "$container_id" ]]; then
    echo "Failed to create the E2E Prometheus configuration volume." >&2
    return 1
  fi
  docker cp "${E2E_PROMETHEUS_DIR}/." "${container_id}:/etc/prometheus"
}
