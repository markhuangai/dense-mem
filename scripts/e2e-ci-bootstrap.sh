#!/usr/bin/env bash
set -euo pipefail

[[ -n "${RUNNER_TEMP:-}" && -n "${GITHUB_ENV:-}" ]] || {
  printf '%s\n' 'hosted E2E requires RUNNER_TEMP and GITHUB_ENV' >&2
  exit 1
}

node <<'NODE'
const { randomBytes } = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const account = process.env.CLOUDFLARE_ACCOUNT_ID || "";
const token = process.env.CLOUDFLARE_API_TOKEN || "";
const dockerHost = process.env.DOCKER_HOST || "";
if (!/^[0-9a-f]{32}$/.test(account) || !token || /[\r\n]/.test(token) ||
    !/^unix:\/\/\/.+\/docker\.sock$/.test(dockerHost)) {
  throw new Error("Cloudflare account, token, or rootless Docker socket is unavailable");
}

const secret = () => randomBytes(32).toString("base64url");
const postgresPassword = secret();
const controlToken = secret();
const telemetryToken = secret();
for (const value of [postgresPassword, controlToken, telemetryToken]) {
  process.stdout.write(`::add-mask::${value}\n`);
}

const directory = path.join(process.env.RUNNER_TEMP, "dense-mem-ci");
fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
fs.chmodSync(directory, 0o700);
const values = {
  LOG_LEVEL: "info",
  CONTROL_PORTAL_TOKEN: controlToken,
  TELEMETRY_ENABLED: "true",
  TELEMETRY_SCRAPE_TOKEN: telemetryToken,
  POSTGRES_USER: "densemem",
  POSTGRES_PASSWORD: postgresPassword,
  POSTGRES_DB: "densemem",
  POSTGRES_SSLMODE: "disable",
  DOCKER_HOST: dockerHost,
  AI_API_URL: `https://api.cloudflare.com/client/v4/accounts/${account}/ai/v1`,
  AI_API_KEY: token,
  AI_API_EMBEDDING_MODEL: "@cf/baai/bge-m3",
  AI_API_EMBEDDING_DIMENSIONS: "1024",
  AI_API_EMBEDDING_MAX_BATCH_ITEMS: "100",
  AI_API_EMBEDDING_MAX_CONCURRENCY: "2",
  AI_VERIFIER_API_URL: "http://synchronous-write-provider:8787/v1",
  AI_VERIFIER_API_KEY: "dense-mem-e2e-verifier-key",
  AI_VERIFIER_MODEL: "dense-mem-e2e-verifier",
  AI_REMEMBER_MODEL: "dense-mem-e2e-remember",
  AI_CONFLICT_REVIEW_MODEL: "dense-mem-e2e-conflict-review",
  AI_DREAM_GRAPH_MODEL: "dense-mem-e2e-dream-graph",
  AI_DREAM_EVIDENCE_MODEL: "dense-mem-e2e-dream-evidence",
  AI_COMMUNITY_SUMMARY_MODEL: "dense-mem-e2e-community-summary",
  AI_VERIFIER_DISABLE_TEMPERATURE: "true",
  DENSE_MEM_CI_GO_TEST_IMAGE: "golang:1.26.6-bookworm",
};
fs.writeFileSync(path.join(directory, ".env"),
  Object.entries(values).map(([key, value]) => `${key}=${value}`).join("\n") + "\n", { mode: 0o600 });
fs.writeFileSync(path.join(directory, "telemetry-scrape-token"), `${telemetryToken}\n`, { mode: 0o600 });
fs.appendFileSync(process.env.GITHUB_ENV,
  `DENSE_MEM_CI_CONFIG_DIR=${directory}\nDENSE_MEM_CI_HOSTED=1\n`);
NODE
