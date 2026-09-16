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
    const github = { event: { pull_request: pull } };
    assert.equal(evaluate({ resolve: { outputs: verified } }, github), verified[field]);
    const fallback = { head_repository: repository, head_sha: head, base_sha: pull.base.sha };
    assert.equal(evaluate({ resolve: { outputs: {} } }, github), fallback[field]);
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
  for (const name of ["NPM_RESULT", "MIGRATIONS_RESULT", "QUALITY_RESULT"]) {
    for (const result of ["", "failure", "skipped", "cancelled", "pending"]) {
      assert.notEqual(spawnSync("bash", ["-c", script], { env: { ...env, [name]: result } }).status, 0);
    }
  }
});
