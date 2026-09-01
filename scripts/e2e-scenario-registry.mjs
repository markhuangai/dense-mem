#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const DEFAULT_REGISTRY = new URL("./e2e-scenarios.json", import.meta.url);
const EXPECTED_SCENARIOS = Object.freeze([
  "mcp_boundaries",
  "oauth_provider_compatibility",
  "mcp_oauth",
  "private_memory_erasure",
  "synchronous_write",
  "synchronous_write_primitives",
  "identity_cleanup",
  "community",
  "conflict",
  "conflict_queue",
  "full",
  "mcp_sdk_parity",
  "mcp_sdk_transport",
  "security_runtime",
  "infrastructure_credentials",
  "submission_terminal_errors",
  "security_intake",
  "memory_space_backfill",
  "memory_space_isolation",
  "space_aware_recall",
  "credential_memory_binding",
]);

const ISOLATIONS = new Set(["exclusive", "shared_team"]);
const HELPER_PROFILES = new Set([
  "conflict_provider",
  "conflict_review",
  "oauth",
  "oauth_compatibility",
  "playwright",
  "synchronous_write",
  "verifier",
]);

function readRegistry(path = fileURLToPath(DEFAULT_REGISTRY)) {
  return JSON.parse(readFileSync(path, "utf8"));
}

function validateRegistry(registry) {
  const errors = [];
  if (!registry || typeof registry !== "object") {
    return ["registry must be an object"];
  }
  if (registry.schema_version !== 1) {
    errors.push("schema_version must be 1");
  }
  if (registry.runtime !== "production") {
    errors.push("registry runtime must be production");
  }
  if (!Array.isArray(registry.scenarios) || registry.scenarios.length === 0) {
    errors.push("scenarios must be a non-empty array");
    return errors;
  }

  const seen = new Set();
  for (const scenario of registry.scenarios) {
    if (!scenario || typeof scenario !== "object") {
      errors.push("each scenario must be an object");
      continue;
    }
    const name = scenario.name;
    if (typeof name !== "string" || !/^[a-z0-9_]+$/.test(name)) {
      errors.push(`invalid scenario name: ${String(name)}`);
    } else if (seen.has(name)) {
      errors.push(`duplicate scenario: ${name}`);
    } else {
      seen.add(name);
    }
    if (!ISOLATIONS.has(scenario.isolation)) {
      errors.push(`${name || "scenario"} has an unknown isolation`);
    }
    if (scenario.runtime !== "production") {
      errors.push(`${name || "scenario"} must use runtime=production`);
    }
    if (
      !Number.isSafeInteger(scenario.timeout_minutes) ||
      scenario.timeout_minutes < 1 ||
      scenario.timeout_minutes > 180
    ) {
      errors.push(`${name || "scenario"} has an invalid timeout_minutes`);
    }
    if (!Array.isArray(scenario.helper_profiles)) {
      errors.push(`${name || "scenario"} helper_profiles must be an array`);
    } else {
      const helpers = new Set();
      for (const helper of scenario.helper_profiles) {
        if (!HELPER_PROFILES.has(helper)) {
          errors.push(`${name || "scenario"} has unknown helper profile ${String(helper)}`);
        } else if (helpers.has(helper)) {
          errors.push(`${name || "scenario"} repeats helper profile ${helper}`);
        }
        helpers.add(helper);
      }
    }
    if (typeof scenario.playwright !== "boolean") {
      errors.push(`${name || "scenario"} playwright must be boolean`);
    } else if (scenario.playwright && !scenario.helper_profiles.includes("playwright")) {
      errors.push(`${name || "scenario"} requires the playwright helper profile`);
    }
  }

  const expected = new Set(EXPECTED_SCENARIOS);
  for (const name of EXPECTED_SCENARIOS) {
    if (!seen.has(name)) errors.push(`missing scenario: ${name}`);
  }
  for (const name of seen) {
    if (!expected.has(name)) errors.push(`unknown scenario: ${name}`);
  }
  return errors;
}

function assertValidRegistry(registry) {
  const errors = validateRegistry(registry);
  if (errors.length > 0) {
    throw new Error(`invalid E2E scenario registry:\n${errors.map((error) => `- ${error}`).join("\n")}`);
  }
  return registry;
}

function scenariosFor(registry, isolation) {
  assertValidRegistry(registry);
  if (isolation === "all") isolation = undefined;
  if (isolation !== undefined && !ISOLATIONS.has(isolation)) {
    throw new Error(`unknown isolation: ${isolation}`);
  }
  return registry.scenarios.filter((scenario) => scenario.isolation === isolation || isolation === undefined);
}

function matrixFor(registry, isolation) {
  const include = scenariosFor(registry, isolation).map((scenario) => ({ ...scenario }));
  return { include };
}

function helperProfilesFor(registry, isolation) {
  return [...new Set(scenariosFor(registry, isolation).flatMap((scenario) => scenario.helper_profiles))].sort();
}

function scenarioFor(registry, name) {
  const scenario = scenariosFor(registry).find((candidate) => candidate.name === name);
  if (!scenario) throw new Error(`unknown scenario: ${name}`);
  return { ...scenario };
}

function classifyScenario(registry, name) {
  if (typeof name !== "string" || !/^[a-z0-9_]+$/.test(name)) {
    throw new Error(`invalid scenario: ${String(name)}`);
  }
  const scenario = registry.scenarios?.find((candidate) => candidate.name === name);
  if (scenario) return { ...scenario, audited: true };
  return {
    name,
    isolation: "exclusive",
    runtime: "production",
    helper_profiles: [],
    timeout_minutes: 30,
    playwright: false,
    audited: false,
  };
}

function usage() {
  process.stderr.write(
    "usage: e2e-scenario-registry.mjs --validate | --matrix <exclusive|shared_team|all> | --helpers <exclusive|shared_team> | --scenario <name>\n",
  );
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const registry = readRegistry(process.env.DENSE_MEM_E2E_SCENARIO_REGISTRY || fileURLToPath(DEFAULT_REGISTRY));
    const command = process.argv[2];
    if (command === "--validate") {
      assertValidRegistry(registry);
      process.stdout.write("E2E scenario registry is valid.\n");
    } else if (command === "--matrix") {
      process.stdout.write(`${JSON.stringify(matrixFor(registry, process.argv[3]))}\n`);
    } else if (command === "--helpers") {
      process.stdout.write(`${JSON.stringify(helperProfilesFor(registry, process.argv[3]))}\n`);
    } else if (command === "--scenario") {
      process.stdout.write(`${JSON.stringify(classifyScenario(registry, process.argv[3]))}\n`);
    } else {
      usage();
      process.exitCode = 2;
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

export {
  EXPECTED_SCENARIOS,
  HELPER_PROFILES,
  ISOLATIONS,
  assertValidRegistry,
  classifyScenario,
  helperProfilesFor,
  matrixFor,
  readRegistry,
  scenarioFor,
  scenariosFor,
  validateRegistry,
};
