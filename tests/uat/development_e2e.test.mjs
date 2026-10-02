import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { GitHubApi } from "../../.github/scripts/pr-image-cleanup.cjs";
import policy from "../../.github/scripts/development-e2e-policy.cjs";
import { readRegistry } from "../../scripts/e2e-scenario-registry.mjs";

const repository = "markhuangai/dense-mem";
const revision = "a".repeat(40);

async function apiFixture(handler, run) {
  const server = createServer((request, response) => {
    const body = handler(request);
    response.writeHead(200, { "Content-Type": "application/json" });
    response.end(JSON.stringify(body));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    await run(new GitHubApi({ apiUrl: `http://127.0.0.1:${server.address().port}`, token: "fixture", repository }));
  } finally {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
}

function request(api, overrides = {}) {
  return { api, repository, ref: "refs/heads/main", actor: "owner", triggeringActor: "owner", sourceRef: "issue/development", runId: 42, runAttempt: 1, ...overrides };
}

test("development request pins a pushed branch once without needing a PR", async () => {
  let resolutions = 0;
  await apiFixture((incoming) => {
    if (incoming.url.endsWith("/permission")) return { permission: "admin" };
    assert.equal(incoming.url, "/repos/markhuangai/dense-mem/git/ref/heads/issue%2Fdevelopment");
    resolutions += 1;
    return { object: { type: "commit", sha: resolutions === 1 ? revision : "b".repeat(40) } };
  }, async (api) => {
    const resolved = await policy.resolveDevelopmentRequest(request(api));
    assert.equal(resolved.source_revision, revision);
    assert.equal(resolved.source_repository, repository);
    assert.equal(resolved.image, "ghcr.io/markhuangai/dense-mem-e2e:run-42-1");
  });
  assert.equal(resolutions, 1);
});

test("development request accepts a full commit and rejects a non-admin rerunner", async () => {
  await apiFixture((incoming) => incoming.url.endsWith("/permission") ? { permission: "admin" } : { sha: revision }, async (api) => {
    assert.equal((await policy.resolveDevelopmentRequest(request(api, { sourceRef: revision }))).source_revision, revision);
    await assert.rejects(policy.resolveDevelopmentRequest(request(api, { ref: "refs/heads/topic" })), /from main/);
  });
  await apiFixture((incoming) => ({ permission: incoming.url.includes("/outsider/") ? "write" : "admin" }), async (api) => {
    await assert.rejects(policy.resolveDevelopmentRequest(request(api, { triggeringActor: "outsider" })), /repository-admin/);
    await assert.rejects(policy.resolveDevelopmentRequest(request(api, { triggeringActor: "" })), /authenticated/);
  });
});

test("development request rejects malformed, pull-ref, and foreign-repository inputs", async () => {
  await apiFixture(() => ({ permission: "admin" }), async (api) => {
    for (const sourceRef of ["", "topic\ncommand", "refs/pull/42/head", "other/repo@" + revision, "main~1", " main"]) {
      await assert.rejects(policy.resolveDevelopmentRequest(request(api, { sourceRef })));
    }
  });
});

test("cached attempt-one outputs cannot authorize admin or non-admin selective reruns", async () => {
  const cached = { image: "ghcr.io/markhuangai/dense-mem-e2e:run-42-1", source_revision: revision };
  await apiFixture((incoming) => ({ permission: incoming.url.includes("/outsider/") ? "write" : "admin" }), async (api) => {
    for (const triggeringActor of ["owner", "outsider"]) {
      await assert.rejects(policy.authorizeDevelopmentExecution(request(api, { triggeringActor, runAttempt: 2, ...cached })),
        triggeringActor === "owner" ? /fresh development dispatch/ : /repository-admin/);
    }
    await assert.rejects(policy.resolveDevelopmentRequest(request(api, { runAttempt: 2 })), /fresh development dispatch/);
  });
  assert.equal(cached.source_revision, revision);
  const labels = {
    "org.opencontainers.image.variant": "production", "org.opencontainers.image.source": `https://github.com/${repository}`,
    "org.opencontainers.image.revision": revision, "org.opencontainers.image.version": "run-42-1",
    "io.dense-mem.e2e.repository": repository, "io.dense-mem.e2e.run-id": "42", "io.dense-mem.e2e.run-attempt": "1",
  };
  assert.doesNotThrow(() => policy.assertDevelopmentImage({ labels, repository, sourceRevision: revision, runId: 42, runAttempt: 1 }));
  assert.throws(() => policy.assertDevelopmentImage({ labels, repository, sourceRevision: revision, runId: 42, runAttempt: 2 }), /labels/);
});

test("development selections support E2E-only, PostgreSQL-only, subsets, and all", async () => {
  const registry = readRegistry();
  const scenarios = await policy.selectDevelopmentTests(registry, "conflict, mcp_oauth", "");
  assert.deepEqual(scenarios.scenario_matrix.include.map(({ name }) => name).sort(), ["conflict", "mcp_oauth"]);
  assert.equal(scenarios.has_postgres, false);
  const postgres = await policy.selectDevelopmentTests(registry, "", "2,0");
  assert.equal(postgres.has_scenarios, false);
  assert.deepEqual(postgres.postgres_matrix.shard, [0, 2]);
  const all = await policy.selectDevelopmentTests(registry, "all", "all");
  assert.equal(all.scenario_matrix.include.length, registry.scenarios.length);
  assert.deepEqual(all.postgres_matrix.shard, [0, 1, 2]);
  assert.deepEqual((await policy.selectDevelopmentTests(registry, "full", "")).scenario_matrix.include.map(({ name }) => name), ["full"]);
  const extended = structuredClone(registry);
  extended.scenarios.push({ ...extended.scenarios[0], name: "new_branch_scenario" });
  assert.equal((await policy.selectDevelopmentTests(extended, "new_branch_scenario", "")).scenario_matrix.include[0].name, "new_branch_scenario");
});

test("development selections reject empty, malformed, unknown, and duplicate lists", async () => {
  for (const [scenarios, postgres] of [["", ""], ["unknown", ""], ["conflict,,mcp_oauth", ""], ["all,conflict", ""], ["conflict,conflict", ""], ["", "3"], ["", "all,0"], ["", "0,0"]]) {
    await assert.rejects(policy.selectDevelopmentTests(readRegistry(), scenarios, postgres));
  }
});

test("selected group failures and skips cannot produce successful development results", () => {
  const passing = { authorize: "success", build: "success", scenarios: "success", postgres: "skipped", hasScenarios: true, hasPostgres: false };
  assert.doesNotThrow(() => policy.assertDevelopmentResults(passing));
  for (const result of ["failure", "cancelled", "skipped"]) {
    assert.throws(() => policy.assertDevelopmentResults({ ...passing, scenarios: result }));
    assert.throws(() => policy.assertDevelopmentResults({ ...passing, hasPostgres: true, postgres: result }));
  }
  assert.throws(() => policy.assertDevelopmentResults({ ...passing, build: "failure" }));
});

test("development workflow runs selected groups independently with one pinned GHCR image", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/development-e2e.yml", import.meta.url), "utf8");
  assert.match(workflow, /workflow_dispatch:/);
  assert.match(workflow, /TRIGGERING_ACTOR: \$\{\{ github.triggering_actor \}\}/);
  assert.match(workflow, /--platform linux\/amd64 --target production --load/);
  assert.match(workflow, /--provenance=false/);
  assert.equal((workflow.match(/docker buildx build/g) || []).length, 1);
  assert.doesNotMatch(workflow, /upload-artifact|build-push-action|statuses: write|secrets:\s*inherit/);
  assert.match(workflow, /uses: \.\/\.github\/workflows\/production-e2e-scenario\.yml/);
  assert.equal((workflow.match(/needs: \[authorize, build\]/g) || []).length, 2);
  assert.match(workflow, /max-parallel: 4/);
  assert.match(workflow, /max-parallel: 3/);
  assert.match(workflow, /source_revision: \$\{\{ needs.authorize.outputs.source_revision \}\}/);
  assert.match(workflow, /scripts\/e2e-host-controller\.sh precheck/);
  assert.match(workflow, /if: always\(\)/);
  assert.match(workflow, /policy.assertDevelopmentResults/);
  assert.equal((workflow.match(/policy.authorizeDevelopmentExecution/g) || []).length, 2);
  assert.match(workflow, /development: true/);
  const reusable = await readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8");
  assert.match(reusable, /development:\n\s+required: false\n\s+default: false\n\s+type: boolean/);
  assert.match(reusable, /if: inputs.development/);
  assert.ok(reusable.indexOf("policy.authorizeDevelopmentExecution") < reusable.indexOf("- name: Checkout exact tested revision"));
});

test("development cleanup uses trusted completion events and an hourly recovery sweep", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/development-e2e-cleanup.yml", import.meta.url), "utf8");
  assert.match(workflow, /workflows: \[Development E2E\]/);
  assert.match(workflow, /types: \[completed\]/);
  assert.match(workflow, /cron: "17 \* \* \* \*"/);
  assert.doesNotMatch(workflow, /workflow_run.conclusion ==|head_sha|head_repository|upload-artifact|download-artifact/);
  assert.match(workflow, /ref: \$\{\{ github.workflow_sha \}\}/);
  assert.match(workflow, /packages: write/);
  assert.match(workflow, /REGCTL_SHA256/);
  assert.match(workflow, /node \.github\/scripts\/development-e2e-cleanup\.cjs/);
});
