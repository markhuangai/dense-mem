"use strict";

const WORKFLOW_PATH = ".github/workflows/development-e2e.yml";
const SHA_PATTERN = /^[0-9a-f]{40}$/;
const RUN_TAG_PATTERN = /^run-([1-9][0-9]*)-([1-9][0-9]*)$/;

function imageName(repository) {
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || "")) {
    throw new Error("invalid development repository");
  }
  return `ghcr.io/${repository.toLowerCase()}-e2e`;
}

function runTag(runId, runAttempt) {
  const tag = `run-${runId}-${runAttempt}`;
  if (!RUN_TAG_PATTERN.test(tag) || !Number.isSafeInteger(Number(runId)) || !Number.isSafeInteger(Number(runAttempt))) {
    throw new Error("invalid development run identity");
  }
  return tag;
}

async function requireAdministrators(api, repository, actors) {
  if (actors.some((actor) => typeof actor !== "string" || !actor)) {
    throw new Error("development dispatch requires authenticated initiating and rerunning actors");
  }
  for (const actor of new Set(actors)) {
    const permission = await api.request(`/repos/${repository}/collaborators/${encodeURIComponent(actor)}/permission`);
    if (permission.permission !== "admin") throw new Error("development dispatch requires repository-admin permission");
  }
}

async function authorizeDevelopmentExecution({ api, repository, ref, actor, triggeringActor, runAttempt }) {
  if (ref !== "refs/heads/main") throw new Error("dispatch the trusted development workflow from main");
  await requireAdministrators(api, repository, [actor, triggeringActor]);
  if (String(runAttempt) !== "1") throw new Error("start a fresh development dispatch; job reruns cannot reuse cleaned images or cached authorization");
}

async function resolveDevelopmentRequest({ api, repository, ref, actor, triggeringActor, sourceRef, runId, runAttempt }) {
  const image = `${imageName(repository)}:${runTag(runId, runAttempt)}`;
  await authorizeDevelopmentExecution({ api, repository, ref, actor, triggeringActor, runAttempt });
  if (typeof sourceRef !== "string" || !sourceRef || sourceRef !== sourceRef.trim() || /[\s\x00-\x1f\x7f]/.test(sourceRef)) {
    throw new Error("source_ref must be a pushed branch or full commit SHA");
  }
  let sourceRevision;
  if (SHA_PATTERN.test(sourceRef)) {
    sourceRevision = (await api.request(`/repos/${repository}/commits/${sourceRef}`)).sha;
  } else {
    const branch = sourceRef.startsWith("refs/heads/") ? sourceRef.slice(11) : sourceRef;
    if (!branch || branch.startsWith("refs/") || /[~^:?*\[\\]|\.\.|@\{/.test(branch)) {
      throw new Error("source_ref must name a same-repository branch");
    }
    const reference = await api.request(`/repos/${repository}/git/ref/heads/${encodeURIComponent(branch)}`);
    if (reference.object?.type !== "commit") throw new Error("source_ref did not resolve to a branch commit");
    sourceRevision = reference.object.sha;
  }
  if (!SHA_PATTERN.test(sourceRevision || "")) throw new Error("source_ref returned an invalid commit SHA");
  return { source_revision: sourceRevision, source_repository: repository, image };
}

function selection(value) {
  if (typeof value !== "string") throw new Error("test selection must be text");
  if (value.trim() === "") return [];
  const names = value.split(",").map((name) => name.trim());
  if (names.some((name) => !name) || new Set(names).size !== names.length || (names.includes("all") && names.length !== 1)) {
    throw new Error("test selection contains empty, duplicate, or mixed all entries");
  }
  return names;
}

async function selectDevelopmentTests(registry, scenarios, postgresShards) {
  const { matrixFor } = await import("../../scripts/e2e-scenario-registry.mjs");
  const available = matrixFor(registry, "all").include;
  const names = selection(scenarios);
  const shards = selection(postgresShards);
  const unknown = names.filter((name) => name !== "all" && !available.some((row) => row.name === name));
  if (unknown.length) throw new Error(`unknown development scenarios: ${unknown.join(", ")}`);
  if (shards.some((shard) => !["all", "0", "1", "2"].includes(shard))) throw new Error("PostgreSQL shards must be 0, 1, 2, or all");
  if (names.length === 0 && shards.length === 0) throw new Error("select at least one E2E scenario or PostgreSQL shard");
  return {
    scenario_matrix: { include: available.filter((row) => names.includes("all") || names.includes(row.name)) },
    postgres_matrix: { shard: shards.includes("all") ? [0, 1, 2] : shards.map(Number).sort() },
    has_scenarios: names.length > 0,
    has_postgres: shards.length > 0,
  };
}

function assertDevelopmentImage({ labels, repository, sourceRevision, runId, runAttempt }) {
  const expected = {
    "org.opencontainers.image.variant": "production",
    "org.opencontainers.image.source": `https://github.com/${repository}`,
    "org.opencontainers.image.revision": sourceRevision,
    "org.opencontainers.image.version": runTag(runId, runAttempt),
    "io.dense-mem.e2e.repository": repository,
    "io.dense-mem.e2e.run-id": String(runId),
    "io.dense-mem.e2e.run-attempt": String(runAttempt),
  };
  if (!SHA_PATTERN.test(sourceRevision || "")) throw new Error("development image has an invalid source revision");
  if (Object.entries(expected).some(([key, value]) => labels?.[key] !== value)) {
    throw new Error("development image labels do not match its production source and run identity");
  }
}

function assertTrustedDevelopmentRun(run, repository) {
  imageName(repository);
  if (run?.path !== WORKFLOW_PATH || run.event !== "workflow_dispatch" || run.head_branch !== "main" ||
      run.repository?.full_name !== repository || !SHA_PATTERN.test(run.head_sha || "")) {
    throw new Error("cleanup requires a trusted main development workflow run in this repository");
  }
  runTag(run.id, run.run_attempt);
}

function assertDevelopmentResults({ authorize, build, scenarios, postgres, hasScenarios, hasPostgres }) {
  if (authorize !== "success" || build !== "success" ||
      (hasScenarios && scenarios !== "success") || (hasPostgres && postgres !== "success")) {
    throw new Error("development validation did not pass every selected test group");
  }
}

module.exports = {
  WORKFLOW_PATH, SHA_PATTERN, RUN_TAG_PATTERN, imageName, runTag,
  requireAdministrators, authorizeDevelopmentExecution, resolveDevelopmentRequest, selectDevelopmentTests,
  assertDevelopmentImage, assertTrustedDevelopmentRun, assertDevelopmentResults,
};
