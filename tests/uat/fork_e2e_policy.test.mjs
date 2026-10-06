import assert from "node:assert/strict";
import { access, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { test } from "node:test";
import policy from "../../.github/scripts/fork-e2e-policy.cjs";

const registry = JSON.parse(await readFile(new URL("../../scripts/e2e-scenarios.json", import.meta.url), "utf8"));
const scenarios = registry.scenarios.map((entry) => entry.name);
const approved = { version: 1, pr_number: 42, source_sha: "a".repeat(40), source_repository: "contributor/dense-mem",
  image: `ghcr.io/markhuangai/dense-mem:test-42@sha256:${"b".repeat(64)}`, trusted_revision: "c".repeat(40),
  run_id: "100", run_attempt: "2", approved_at: "2026-10-01T00:00:00Z" };
const run = { id: 200, run_attempt: 1, repository: { full_name: approved.source_repository }, head_sha: approved.source_sha,
  event: "workflow_dispatch", path: policy.REQUEST_WORKFLOW, status: "completed", conclusion: "success",
  run_started_at: "2026-10-01T00:01:00Z", referenced_workflows: [{ path: `${policy.SIGNER_WORKFLOW}@${approved.trusted_revision}`, sha: approved.trusted_revision }] };
const jobs = policy.requiredForkJobs(scenarios).map((name) => ({ name: `Contributor E2E / ${name}`, conclusion: "success",
  steps: name.startsWith("Fork scenario ") ? ["Run scenario", "Stop scenario stack"].map((name) => ({ name, conclusion: "success" })) : [] }));

test("fork receipt binds approved source, image, preview attempt, workflow revision, run, and complete results", () => {
  policy.requireForkRun(run, approved);
  policy.requireCompleteForkJobs(jobs, scenarios);
  const receipt = policy.successReceipt(approved, run, scenarios);
  assert.equal(receipt.source_sha, approved.source_sha);
  assert.equal(receipt.image, approved.image);
  assert.equal(receipt.preview_run_attempt, "2");
  assert.equal(receipt.fork_run_id, "200");
  assert.equal(receipt.trusted_revision, approved.trusted_revision);
  assert.deepEqual(receipt.postgres_prechecks, ["success", "success", "success"]);
  assert.equal(receipt.scenarios.length, 25);
  assert.ok(receipt.scenarios.every((entry) => entry.result === "success" && entry.cleanup === "success"));
});

test("a fake green fork workflow, wrong source, stale attempt, and changed signer revision fail closed", () => {
  for (const changes of [{ referenced_workflows: [] }, { head_sha: "d".repeat(40) }, { conclusion: "failure" },
    { repository: { full_name: "attacker/dense-mem" } }, { event: "push" }, { path: ".github/workflows/fake-green.yml" },
    { run_started_at: "2026-09-30T23:59:59Z" }, { run_attempt: 0 },
    { referenced_workflows: [{ path: `${policy.SIGNER_WORKFLOW}@main`, sha: "d".repeat(40) }] }]) {
    assert.throws(() => policy.requireForkRun({ ...run, ...changes }, approved));
  }
});

test("every scenario, PostgreSQL shard, rootless precheck, and signer job is mandatory", () => {
  for (let index = 0; index < jobs.length; index += 1) {
    assert.throws(() => policy.requireCompleteForkJobs(jobs.filter((_, value) => value !== index), scenarios), /required fork job/);
    for (const conclusion of ["failure", "skipped", "cancelled", null]) {
      assert.throws(() => policy.requireCompleteForkJobs(jobs.map((job, value) => value === index ? { ...job, conclusion } : job), scenarios));
    }
  }
  assert.throws(() => policy.requireCompleteForkJobs([...jobs, jobs[0]], scenarios), /ambiguous/);
  assert.throws(() => policy.successReceipt(approved, run, scenarios.slice(1)), /all 25/);
});

test("attestation verification constrains signer, source, repository, and hosted runner identity", () => {
  const args = policy.attestationArguments("receipt.json", "bundles.jsonl", approved);
  assert.equal(args[args.indexOf("--signer-workflow") + 1], policy.SIGNER_WORKFLOW);
  assert.equal(args[args.indexOf("--signer-digest") + 1], approved.trusted_revision);
  assert.equal(args[args.indexOf("--source-digest") + 1], approved.source_sha);
  assert.equal(args[args.indexOf("--repo") + 1], approved.source_repository);
  assert.ok(args.includes("--deny-self-hosted-runners"));
});

test("scenario cleanup must pass before signing and upstream acceptance, even when the job is green", () => {
  for (const name of ["Run scenario", "Stop scenario stack"]) {
    for (const conclusion of ["skipped", "failure", "cancelled", null]) {
      const adverse = jobs.map((job) => ({ ...job, steps: job.steps.map((step) => step.name === name ? { ...step, conclusion } : step) }));
      assert.throws(() => policy.requireCompleteForkJobs(adverse, scenarios, { includeSigner: false }), /execution or cleanup/);
      assert.throws(() => policy.requireCompleteForkJobs(adverse, scenarios), /execution or cleanup/);
    }
    const absent = jobs.map((job) => ({ ...job, steps: job.steps.filter((step) => step.name !== name) }));
    assert.throws(() => policy.requireCompleteForkJobs(absent, scenarios), /execution or cleanup/);
  }
  policy.requireCompleteForkJobs(jobs.filter((job) => !job.name.endsWith("Sign fork E2E receipt")), scenarios, { includeSigner: false });
});

test("missing contributor credentials or account configuration cannot prepare fork E2E", async (t) => {
  for (const missing of ["CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"]) {
    const directory = await mkdtemp(join(tmpdir(), "dense-mem-fork-credentials-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const env = { ...process.env, RUNNER_TEMP: directory, GITHUB_ENV: join(directory, "github-env"),
      DOCKER_HOST: "unix:///tmp/docker.sock", CLOUDFLARE_ACCOUNT_ID: "a".repeat(32), CLOUDFLARE_API_TOKEN: "contributor-fixture-token" };
    delete env[missing];
    const result = spawnSync("bash", [new URL("../../scripts/e2e-ci-bootstrap.sh", import.meta.url).pathname], { env, encoding: "utf8", timeout: 10000 });
    assert.ifError(result.error);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /Cloudflare account, token, or rootless Docker socket is unavailable/);
    assert.doesNotMatch(result.stdout + result.stderr, /contributor-fixture-token/);
    await assert.rejects(access(join(directory, "dense-mem-ci/.env")), { code: "ENOENT" });
    await assert.rejects(access(join(directory, "github-env")), { code: "ENOENT" });
  }
});

test("the actual scenario workflow fails without a stack and gates cleanup before signing", async () => {
  const scenario = await readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8");
  const runSection = scenario.split("      - name: Run scenario\n")[1].split("      - name: Stop scenario stack\n")[0];
  const shell = runSection.split("        run: |\n")[1].split("\n").map((line) => line.startsWith("          ") ? line.slice(10) : line).join("\n");
  const result = spawnSync("bash", ["-c", shell], { env: { ...process.env, PROJECT: "", SCENARIO: "fixture" }, encoding: "utf8", timeout: 10000 });
  assert.ifError(result.error);
  assert.equal(result.status, 1);
  assert.match(result.stdout, /scenario setup did not produce a Compose project/);
  assert.match(scenario.split("      - name: Stop scenario stack\n")[1], /if: always\(\) && steps\.stack\.outputs\.project != ''/);
  const workflow = await readFile(new URL("../../.github/workflows/fork-e2e.yml", import.meta.url), "utf8");
  const signer = workflow.split("  sign:\n")[1];
  assert.match(signer, /needs: \[authorize, rootless, database, scenarios\]/);
  assert.match(signer, /needs\.rootless\.result == 'success' && needs\.database\.result == 'success' && needs\.scenarios\.result == 'success'/);
  assert.ok(signer.indexOf("policy.requireCompleteForkJobs(") < signer.indexOf("policy.successReceipt("));
});

test("approval requires a current open public fork with the exact PR head", () => {
  const pull = { state: "open", draft: false, base: { ref: "main", repo: { full_name: policy.UPSTREAM } },
    head: { sha: approved.source_sha, repo: { full_name: approved.source_repository, private: false } } };
  policy.requireForkPull(pull, approved.source_sha);
  for (const changes of [{ state: "closed" }, { draft: true }, { base: { ref: "release" } },
    { head: { sha: "d".repeat(40), repo: pull.head.repo } },
    { head: { ...pull.head, repo: { ...pull.head.repo, private: true } } }]) {
    assert.throws(() => policy.requireForkPull({ ...pull, ...changes }, approved.source_sha));
  }
});
