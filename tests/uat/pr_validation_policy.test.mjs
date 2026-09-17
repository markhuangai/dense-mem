import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import policy from "../../.github/scripts/image-release-policy.cjs";

const repository = "markhuangai/dense-mem";
const head = "a".repeat(40);
const pull = {
  number: 42,
  state: "open",
  base: { ref: "main", sha: "b".repeat(40) },
  user: { login: "contributor" },
  head: { sha: head, repo: { full_name: repository } },
  labels: [],
};

test("CI workflow activates exactly one routing path without changing bootstrap behavior", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/ci-pr.yml", import.meta.url), "utf8");
  const condition = workflow.split("  ci:\n")[1]?.split("    if: >-\n")[1]
    ?.split("    uses:")[0]?.trim().replace(/^\$\{\{\s*|\s*\}\}$/g, "");
  assert.ok(condition);
  const permits = new Function("vars", "github", "needs", "always", `return (${condition});`);
  for (const enabled of ["", "false", "true"]) {
    for (const event of ["pull_request", "pull_request_target"]) {
      for (const run of ["", "false", "true"]) {
        const expected = enabled === "true" ? event === "pull_request_target" && run === "true" : event === "pull_request";
        assert.equal(permits({ REVIEW_GATED_CI: enabled }, { event_name: event },
          { resolve: { outputs: { run_ci: run } } }, () => true), expected);
      }
    }
  }
  assert.match(workflow, /ref: \$\{\{ github\.sha \}\}/);
  const verified = { head_repository: "trusted/repository", head_sha: "c".repeat(40), base_sha: "d".repeat(40) };
  const fields = { "source-repository": "head_repository", "source-revision": "head_sha", "migration-base-ref": "base_sha" };
  for (const [input, field] of Object.entries(fields)) {
    const expression = workflow.match(new RegExp(`^      ${input}: \\$\\{\\{ (.+) \\}\\}$`, "m"))?.[1];
    assert.ok(expression, `${input} must be explicit`);
    const evaluate = new Function("needs", "github", `return (${expression});`);
    const github = { repository, sha: "e".repeat(40), event: { pull_request: pull } };
    assert.equal(evaluate({ resolve: { outputs: verified } }, github), verified[field]);
    const fallback = { head_repository: repository, head_sha: github.sha, base_sha: pull.base.sha };
    for (const headRepository of [repository, "contributor/dense-mem"]) {
      github.event.pull_request = { ...pull, head: { ...pull.head, repo: { full_name: headRepository } } };
      assert.equal(evaluate({ resolve: { outputs: {} } }, github), fallback[field]);
    }
  }
  const shared = await readFile(new URL("../../.github/workflows/ci-shared.yml", import.meta.url), "utf8");
  assert.match(shared, /git fetch --no-tags "\$\{GITHUB_SERVER_URL\}\/\$\{GITHUB_REPOSITORY\}\.git" "\$\{MIGRATION_BASE\}"/);
});

test("review-first ordinary CI runs only for other same-repository authors", () => {
  assert.equal(policy.runsAutomaticRepositoryCI(pull, repository), true);
  assert.equal(policy.runsAutomaticRepositoryCI({ ...pull, user: { login: "Z-M-Huang" } }, repository), false);
  for (const author of ["new-contributor", "returning-contributor", "Z-M-Huang"]) {
    assert.equal(policy.runsAutomaticRepositoryCI({
      ...pull,
      user: { login: author },
      head: { ...pull.head, repo: { full_name: "contributor/dense-mem" } },
    }, repository), false);
  }
  assert.throws(() => policy.runsAutomaticRepositoryCI({ ...pull, head: { sha: head } }, repository), /head repository/);
});

test("trusted CI resolution refuses stale, closed, or retargeted requests", async () => {
  for (const [current, shouldRun] of [
    [pull, true],
    [{ ...pull, state: "closed" }, false],
    [{ ...pull, base: { ...pull.base, ref: "release" } }, false],
    [{ ...pull, head: { ...pull.head, sha: "c".repeat(40) } }, false],
    [{ ...pull, user: { login: "Z-M-Huang" } }, false],
    [{ ...pull, head: { ...pull.head, repo: { full_name: "contributor/dense-mem" } } }, false],
  ]) {
    const result = await policy.resolveRepositoryCI({
      github: { rest: { pulls: { get: async () => ({ data: current }) } } },
      context: {
        repo: { owner: "markhuangai", repo: "dense-mem" },
        payload: { action: "synchronize", pull_request: pull },
      },
    });
    assert.equal(result.shouldRun, shouldRun);
    if (shouldRun) {
      assert.equal(result.headSha, head);
      assert.equal(result.headRepository, repository);
      assert.equal(result.baseSha, pull.base.sha);
    }
  }
});

test("review-first preview requests require a fresh admin label even for the owner", () => {
  const event = {
    action: "synchronize",
    eventLabel: "",
    hasPreviewLabel: false,
    triggerHeadMatches: true,
    actorPermission: "admin",
    pullRequestAuthor: "Z-M-Huang",
    pullRequestAuthorPermission: "admin",
    pullRequestState: "open",
    pullRequestBase: "main",
    reviewGatedCI: true,
  };
  assert.equal(policy.decidePreviewEvent(event).mode, "skipped");
  assert.equal(policy.decidePreviewEvent({ ...event, reviewGatedCI: false }).mode, "attempt");
  const approved = { ...event, action: "labeled", eventLabel: "deploy-test-image", hasPreviewLabel: true };
  assert.deepEqual(policy.decidePreviewEvent(approved), { mode: "attempt", reason: "admin_label", removeLabel: true });
  for (const actorPermission of ["none", "read", "triage", "write", "maintain"]) {
    assert.equal(policy.decidePreviewEvent({ ...approved, actorPermission }).mode, "skipped");
  }
  assert.deepEqual(policy.decidePreviewEvent({ ...approved, triggerHeadMatches: false }), { mode: "noop", reason: "stale_event" });
  assert.equal(policy.decidePreviewEvent({ ...approved, action: "synchronize" }).mode, "skipped");
});

test("approved previews run deferred CI without duplicating another same-repository author's CI", async () => {
  for (const [current, runCI] of [
    [pull, false],
    [{ ...pull, user: { login: "Z-M-Huang" } }, true],
    [{ ...pull, head: { ...pull.head, repo: { full_name: "contributor/dense-mem" } } }, true],
  ]) {
    const result = await policy.resolvePreviewAttempt({
      github: { rest: { pulls: { get: async () => ({ data: current }) } } },
      context: {
        repo: { owner: "markhuangai", repo: "dense-mem" },
        payload: { action: "labeled", label: { name: "deploy-test-image" }, pull_request: pull },
      },
      actorPermission: "admin",
      authorPermission: "write",
      reviewGatedCI: true,
    });
    assert.equal(result.mode, "attempt");
    assert.equal(result.runCI, runCI);
    assert.equal(result.headSha, head);
  }
});

test("CodeQL aggregate requires each language's latest execution and permits partial retries", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/codeql.yml", import.meta.url), "utf8");
  const script = workflow.split("      - name: Require all language analyses\n")[1]
    ?.split("          script: |\n")[1]?.replace(/^            /gm, "");
  assert.ok(script, "aggregate script must exist");
  const run = new (Object.getPrototypeOf(async function () {}).constructor)("github", "context", "core", "process", script);
  const passed = ["actions", "go", "javascript-typescript"].map((language) => ({
    name: `Analyze (${language})`, status: "completed", conclusion: "success", run_attempt: 2,
  }));
  const cases = [[passed, true], [passed.slice(1), false], [[...passed, passed[0]], false]];
  cases.push([[{ ...passed[0], run_attempt: 1 }, ...passed.slice(1)], true]);
  cases.push([[{ ...passed[0], run_attempt: 1, conclusion: "failure" }, ...passed], true]);
  cases.push([[{ ...passed[0], run_attempt: 3 }, ...passed.slice(1)], false]);
  for (const conclusion of ["failure", "skipped", "cancelled", "timed_out", null]) {
    cases.push([[{ ...passed[0], conclusion }, ...passed.slice(1)], false]);
  }
  for (const [jobs, succeeds] of cases) {
    let failure;
    await run({
      rest: { actions: { listJobsForWorkflowRun: "jobs" } },
      paginate: async (_endpoint, input) => {
        assert.equal(input.filter, "all");
        assert.equal(input.run_id, 10);
        return jobs;
      },
    }, { repo: { owner: "markhuangai", repo: "dense-mem" }, runId: 10 }, {
      setFailed: (message) => { failure = message; },
    }, { env: { GITHUB_RUN_ATTEMPT: "2" } });
    assert.equal(!failure, succeeds);
  }
});

test("repository CI aggregate rejects absent and non-successful mandatory jobs", async (t) => {
  const workflow = await readFile(new URL("../../.github/workflows/ci-shared.yml", import.meta.url), "utf8");
  const script = workflow.split("      - name: Require every CI job\n")[1]
    ?.split("        run: |\n")[1]?.replace(/^          /gm, "");
  assert.ok(script, "aggregate script must exist");
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-ci-result-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const env = {
    NPM_RESULT: "success", MIGRATIONS_RESULT: "success", QUALITY_RESULT: "success",
    GITHUB_OUTPUT: join(directory, "output"),
  };
  assert.equal(spawnSync("bash", ["-c", script], { env }).status, 0);
  assert.equal(await readFile(env.GITHUB_OUTPUT, "utf8"), "passed=true\n");
  assert.equal(workflowValue(workflowJob(workflow, "result"), "passed", {
    steps: { result: { outputs: { passed: "true" } } },
  }), "true");
  assert.equal(workflowValue(workflow, "value", {
    jobs: { result: { outputs: { passed: "true" } } },
  }), "true");
  for (const name of ["NPM_RESULT", "MIGRATIONS_RESULT", "QUALITY_RESULT"]) {
    for (const result of ["", "failure", "skipped", "cancelled", "pending"]) {
      const output = join(directory, `${name}-${result || "missing"}`);
      assert.notEqual(spawnSync("bash", ["-c", script], { env: { ...env, [name]: result, GITHUB_OUTPUT: output } }).status, 0);
      await assert.rejects(readFile(output), { code: "ENOENT" });
    }
  }
});

function workflowJob(workflow, name) {
  const job = workflow.split(`  ${name}:\n`)[1]?.split(/^  [a-z][a-z-]+:/m)[0];
  assert.ok(job, `${name} job must exist`);
  return job;
}

function workflowValue(block, name, context) {
  const expression = block.match(new RegExp(`^ +${name}: \\$\\{\\{ (.+) \\}\\}$`, "m"))?.[1];
  assert.ok(expression, `${name} expression must exist`);
  const javascript = expression.replace(/inputs\.([a-z][a-z-]*)/g, 'inputs["$1"]');
  return new Function(...Object.keys(context), `return (${javascript});`)(...Object.values(context));
}

function workflowScript(workflow, name) {
  const step = workflow.split(`      - name: ${name}\n`)[1]?.split(/^      - |^  [a-z][a-z-]+:/m)[0];
  const script = step?.split("          script: |\n")[1]?.replace(/^            /gm, "");
  assert.ok(script, `${name} script must exist`);
  return new (Object.getPrototypeOf(async function () {}).constructor)("github", "context", "core", "process", "require", script);
}

const previewWorkflow = await readFile(new URL("../../.github/workflows/pr-test-image.yml", import.meta.url), "utf8");
const ownerPull = { ...pull, user: { login: "Z-M-Huang" } };
const forkPull = { ...pull, head: { ...pull.head, repo: { full_name: "contributor/dense-mem" } } };
const deferredDescription = previewWorkflow.match(/^  DEFERRED_VALIDATION_DESCRIPTION: (.+)$/m)[1];

function previewFixture({ current = ownerPull, action = "labeled", triggerHead = head, statuses = [], failAt, gated = "true" } = {}) {
  const writes = [], removedLabels = [], outputs = {};
  const env = { REVIEW_GATED_CI: gated, ACTOR_PERMISSION: "admin", AUTHOR_PERMISSION: "admin", PREVIEW_LABEL: "deploy-test-image",
    POLICY_STATUS_CONTEXT: "PR test image policy", E2E_STATUS_CONTEXT: "Production image E2E", CI_STATUS_CONTEXT: "Repository CI",
    DEFERRED_VALIDATION_DESCRIPTION: deferredDescription };
  const context = { repo: { owner: "markhuangai", repo: "dense-mem" }, serverUrl: "https://github.com", runId: 10,
    payload: { action, label: { name: "deploy-test-image" }, pull_request: { ...current, head: { ...current.head, sha: triggerHead } } } };
  const repos = {
    getBranch: async () => ({ data: { commit: { sha: pull.base.sha } } }),
    compareCommitsWithBasehead: async () => ({ data: { status: "ahead" } }),
    getCommit: async () => ({ data: { commit: { committer: { date: "2026-09-16T00:00:00Z" } } } }),
    createCommitStatus: async (status) => { writes.push(status); },
    listCommitStatusesForRef: "statuses",
  };
  if (failAt) repos[failAt] = async () => { throw new Error(`failed ${failAt}`); };
  const github = { rest: { repos, pulls: { get: async () => ({ data: current }) },
    issues: { removeLabel: async (label) => { removedLabels.push(label); } } },
  paginate: async (endpoint, input) => {
    assert.equal(endpoint, "statuses");
    assert.equal(input.ref, current.head.sha);
    return statuses;
  } };
  const core = { info() {}, setOutput: (name, value) => { outputs[name] = value; } };
  const run = (name, extraEnv = {}) => workflowScript(previewWorkflow, name)(github, context, core, { env: { ...env, ...extraEnv } },
    (path) => { assert.equal(path, "./.github/scripts/image-release-policy.cjs"); return policy; });
  return { run, writes, removedLabels, outputs, context };
}

test("controller failure reports deferred CI without replacing another author's automatic CI", async () => {
  for (const current of [ownerPull, forkPull, pull]) {
    for (const failAt of ["getBranch", "compareCommitsWithBasehead", "getCommit"]) {
      const fixture = previewFixture({ current, failAt });
      await assert.rejects(fixture.run("Resolve current pull request state"), new RegExp(`failed ${failAt}`));
      assert.equal(fixture.removedLabels.length, 1);
      const deferred = current !== pull;
      assert.equal(fixture.outputs.report_ci, String(deferred));
      assert.equal(fixture.outputs.should_run_ci, undefined, "failed resolution must not authorize CI");
      const report = workflowValue(workflowJob(previewWorkflow, "resolve"), "report_ci", {
        steps: { resolve: { outputs: fixture.outputs } },
      });
      const reportEnv = workflowValue(workflowJob(previewWorkflow, "resolve-failure"), "REPORT_CI", {
        needs: { resolve: { outputs: { report_ci: report } } },
      });
      await fixture.run("Publish controller failure", { REPORT_CI: reportEnv });
      assert.deepEqual(fixture.writes.map((status) => status.context), [
        "PR test image policy", "Production image E2E", ...(deferred ? ["Repository CI"] : []),
      ]);
      assert.ok(fixture.writes.every((status) => status.sha === head && status.state === "failure"));
    }
  }
});

test("both repository CI publishers require success and the reusable passed receipt on the expected head", async () => {
  const ciWorkflow = await readFile(new URL("../../.github/workflows/ci-pr.yml", import.meta.url), "utf8");
  for (const [workflow, job] of [[ciWorkflow, "report"], [previewWorkflow, "report-ci"]]) {
    const reporter = workflowJob(workflow, job);
    const run = workflowScript(reporter, "Publish repository CI result");
    for (const result of ["success", "failure", "skipped", "cancelled", ""]) {
      for (const passed of ["true", "false", ""]) {
        const values = { needs: { resolve: { outputs: { head_sha: head } }, ci: { result, outputs: { passed } } } };
        const env = { CI_STATUS_CONTEXT: "Repository CI" };
        for (const name of ["EXPECTED_HEAD", "CI_RESULT", "CI_PASSED"]) env[name] = workflowValue(reporter, name, values);
        const writes = [];
        await run({ rest: { repos: { createCommitStatus: async (status) => { writes.push(status); } } } },
          { repo: { owner: "markhuangai", repo: "dense-mem" }, runId: 10, serverUrl: "https://github.com" }, {}, { env });
        assert.equal(writes.length, 1);
        assert.equal(writes[0].sha, head);
        assert.equal(writes[0].context, "Repository CI");
        assert.equal(writes[0].state, result === "success" && passed === "true" ? "success" : "failure");
        assert.equal(writes[0].target_url, `https://github.com/${repository}/actions/runs/10`);
      }
    }
  }
});

test("deferred placeholders preserve the latest started and terminal validation statuses", async () => {
  assert.equal(deferredDescription, "Awaiting repository-admin validation request.");
  const contexts = ["PR test image policy", "Production image E2E", "Repository CI"];
  for (const current of [ownerPull, forkPull]) {
    for (const existing of [null, { state: "pending", description: deferredDescription },
      { state: "pending", description: "Building the requested test image." },
      ...["success", "failure", "error"].map((state) => ({ state, description: deferredDescription }))]) {
      const statuses = contexts.flatMap((context) => existing ? [
        { id: 2, context, ...existing }, { id: 1, context, state: "pending", description: deferredDescription },
      ] : []);
      const fixture = previewFixture({ current, action: "synchronize", statuses });
      await fixture.run("Resolve current pull request state");
      const expected = !existing || (existing.state === "pending" && existing.description === deferredDescription) ? contexts : [];
      assert.deepEqual(fixture.writes.map((status) => status.context), expected);
      assert.ok(fixture.writes.every((status) => status.sha === head && status.state === "pending" && status.description === deferredDescription));
      assert.equal(fixture.outputs.should_build, "false");
      assert.equal(fixture.outputs.should_run_ci, undefined);
    }
    const mixed = previewFixture({ current, action: "synchronize", statuses: [
      { id: 3, context: "unrelated", state: "success" },
      { id: 2, context: "Production image E2E", state: "failure" },
      { id: 1, context: "PR test image policy", state: "success" },
    ] });
    await mixed.run("Resolve current pull request state");
    assert.deepEqual(mixed.writes.map((status) => status.context), ["Repository CI"]);
  }
});

test("approved owner and fork CI handoffs preserve the resolved source through every shared checkout", async () => {
  const shared = await readFile(new URL("../../.github/workflows/ci-shared.yml", import.meta.url), "utf8");
  const caller = workflowJob(previewWorkflow, "ci");
  const condition = caller.match(/^    if: (.+)$/m)[1];
  const shouldRun = new Function("needs", `return (${condition});`);
  for (const current of [ownerPull, forkPull, pull]) {
    const fixture = previewFixture({ current });
    await fixture.run("Resolve current pull request state");
    const needs = { resolve: { outputs: fixture.outputs } };
    assert.equal(shouldRun(needs), current !== pull);
    const inputs = Object.fromEntries(["source-repository", "source-revision", "migration-base-ref"]
      .map((name) => [name, workflowValue(caller, name, { needs })]));
    assert.deepEqual(inputs, { "source-repository": current.head.repo.full_name, "source-revision": head, "migration-base-ref": pull.base.sha });
    for (const name of ["npm-audit", "postgres-migrations", "quality"]) {
      const checkout = workflowJob(shared, name);
      const values = { inputs, github: { repository, sha: "e".repeat(40) } };
      assert.equal(workflowValue(checkout, "repository", values), current.head.repo.full_name);
      assert.equal(workflowValue(checkout, "ref", values), head);
      assert.match(checkout, /persist-credentials: false/);
    }
    const stale = previewFixture({ current, triggerHead: "f".repeat(40) });
    await stale.run("Resolve current pull request state");
    assert.equal(shouldRun({ resolve: { outputs: stale.outputs } }), false);
    assert.deepEqual(stale.writes, []);
    assert.deepEqual(stale.removedLabels, []);
  }
  assert.match(workflowJob(shared, "postgres-migrations"), /MIGRATION_BASE: \$\{\{ inputs\.migration-base-ref \}\}/);
});
