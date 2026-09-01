#!/usr/bin/env node

import { readFileSync } from "node:fs";

const REQUIRED_URLS = Object.freeze({
  user: "http://server:8080",
  control: "http://server:8090",
  prometheus: "http://prometheus:9090",
  postgres: "postgres:5432",
});

function readManifest(path) {
  const manifest = JSON.parse(readFileSync(path, "utf8"));
  validateManifest(manifest);
  return manifest;
}

function validateManifest(manifest) {
  if (!manifest || typeof manifest !== "object") throw new Error("manifest must be an object");
  if (manifest.contract_version !== "dense-mem-ci-e2e.v1") throw new Error("unsupported controller contract");
  if (manifest.runtime !== "production") throw new Error("runtime must be production");
  if (!Number.isSafeInteger(manifest.run_id) || manifest.run_id < 1) throw new Error("invalid run_id");
  if (!Number.isSafeInteger(manifest.run_attempt) || manifest.run_attempt < 1) throw new Error("invalid run_attempt");
  if (!/^(exclusive|shared)$/.test(manifest.phase)) throw new Error("invalid phase");
  if (!/^[a-z0-9_]+$/.test(manifest.scenario)) throw new Error("invalid scenario");
  if (!/^densemem-ci-[a-z0-9][a-z0-9-]{0,50}$/.test(manifest.compose_project)) throw new Error("invalid Compose project");
  if (!/^densemem-ci-[a-z0-9][a-z0-9-]{0,50}_ci$/.test(manifest.network)) throw new Error("invalid network");
  if (!/^sha256:[0-9a-f]{64}$/.test(manifest.image_digest)) throw new Error("invalid image digest");
  if (!/^[0-9a-f]{40}$/.test(manifest.source_revision)) throw new Error("invalid source revision");
  if (!manifest.urls || Object.keys(REQUIRED_URLS).some((key) => manifest.urls[key] !== REQUIRED_URLS[key])) {
    throw new Error("service URLs must use the fixed Compose DNS contract");
  }
  if (typeof manifest.client_env_volume !== "string" || !manifest.client_env_volume.startsWith(`${manifest.compose_project}_`)) {
    throw new Error("client environment volume is not run-scoped");
  }
  if (manifest.compose_file !== undefined && manifest.compose_file !== "runtime-compose.yml") {
    throw new Error("runtime manifest must use the non-secret Compose view");
  }
  if (manifest.helper_overlay !== undefined && manifest.helper_overlay !== "" && manifest.helper_overlay !== "helper-compose.yml") {
    throw new Error("runtime manifest helper overlay is invalid");
  }
  if (!Array.isArray(manifest.helper_profiles) || manifest.helper_profiles.some((profile) => !/^[a-z0-9_]+$/.test(profile))) {
    throw new Error("invalid helper profiles");
  }
  return manifest;
}

function clientEnvironment(manifest) {
  validateManifest(manifest);
  return {
    DENSE_MEM_USER_URL: manifest.urls.user,
    DENSE_MEM_CONTROL_URL: manifest.urls.control,
    DENSE_MEM_PROMETHEUS_URL: manifest.urls.prometheus,
    DENSE_MEM_E2E_NETWORK: manifest.network,
    DENSE_MEM_E2E_COMPOSE_PROJECT: manifest.compose_project,
    DENSE_MEM_E2E_SCENARIO: manifest.scenario,
    DENSE_MEM_E2E_RUNTIME: manifest.runtime,
    DENSE_MEM_E2E_SOURCE_REVISION: manifest.source_revision,
  };
}

function usage() {
  process.stderr.write("usage: e2e-runtime-adapter.mjs --validate MANIFEST | --env MANIFEST | --json MANIFEST\n");
}

if (process.argv[1] && process.argv[1].endsWith("e2e-runtime-adapter.mjs")) {
  try {
    const command = process.argv[2];
    const manifest = readManifest(process.argv[3]);
    if (command === "--validate") {
      process.stdout.write("runtime manifest is valid\n");
    } else if (command === "--env") {
      for (const [key, value] of Object.entries(clientEnvironment(manifest))) process.stdout.write(`${key}=${value}\n`);
    } else if (command === "--json") {
      process.stdout.write(`${JSON.stringify(manifest)}\n`);
    } else {
      usage();
      process.exitCode = 2;
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

export { REQUIRED_URLS, clientEnvironment, readManifest, validateManifest };
