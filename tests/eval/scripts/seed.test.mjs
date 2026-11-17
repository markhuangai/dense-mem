import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import imagePolicy from "../../../.github/scripts/image-release-policy.cjs";
import { seedBuildRequest, seedReference, seedBuildVersion, seedMetadata, verifySeedFiles, sealSeed, hashFile, authorizeSeedBuild, remainingSeedRestoreEntries, resolveSeedImage } from "../../../.github/scripts/evaluation-seeds.mjs";
import { location } from "./workflow.mjs";
import { readWorkflow, workflowJob } from "../../uat/workflow_helpers.mjs";

const registry = JSON.parse(fs.readFileSync(new URL("../datasets.json", import.meta.url)));
const registryHash = "a".repeat(64);

test("seed preparation selects complete registered packs and validates worker capacity", () => {
  assert.deepEqual(seedBuildRequest(registry, "all", 20), { dataset: registry.datasets.map((row) => row.id), workers: 20 });
  for (const dataset of registry.datasets) assert.deepEqual(seedBuildRequest(registry, dataset.id, 30), { dataset: [dataset.id], workers: 30 });
  for (const workers of [0, -1, 101, 1.5, "20\n", "invalid"]) assert.throws(() => seedBuildRequest(registry, "all", workers));
  for (const dataset of ["fixtures", "replay", "qasper", "hotpotqa:subset", "bootstrap"]) assert.throws(() => seedBuildRequest(registry, dataset, 20));
});

test("seed tags use UTC timestamps and main release names without a separate version counter", () => {
  const selected = seedBuildVersion("v2.8.1-rc.5", new Date("2026-11-16T15:00:01.234Z"));
  assert.equal(selected, "20261116T150001234Z-v2.8.1-rc.5");
  assert.equal(seedReference("hotpotqa", selected), `ghcr.io/markhuangai/dense-mem-eval-postgres:hotpotqa-${selected}`);
  assert.equal(seedReference("musique"), "ghcr.io/markhuangai/dense-mem-eval-postgres:musique-latest");
  for (const value of ["v1", "main", "v2.8.1-rc.5\n", "pr777"]) assert.throws(() => seedBuildVersion(value));
  for (const value of ["latest\n", "other:tag", "https://example.test", "20261116-v1"]) assert.throws(() => seedReference("hotpotqa", value));
});

test("seed registry resolves image manifests and published indexes without treating indexes as missing", async (t) => {
  const reference = seedReference("bootstrap", seedBuildVersion("v2.8.1-rc.5", new Date("2026-11-17T04:26:41.183Z")));
  let mediaType, status = 200, hash = `sha256:${"a".repeat(64)}`;
  t.mock.method(globalThis, "fetch", async (url, options) => {
    if (new URL(url).pathname === "/token") return Response.json({ token: "registry-test-token" });
    assert.equal(options.method, "HEAD");
    if (!options.headers.Accept.split(/,\s*/).includes(mediaType)) return new Response(null, { status: 404 });
    return new Response(null, { status, headers: { "Content-Type": mediaType, "Docker-Content-Digest": hash } });
  });
  for (mediaType of ["application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json", "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"]) {
    assert.equal(await resolveSeedImage(reference, {}), `ghcr.io/markhuangai/dense-mem-eval-postgres@${hash}`);
  }
  status = 404;
  assert.equal(await resolveSeedImage(reference, {}), null);
  status = 403;
  await assert.rejects(resolveSeedImage(reference, {}), /could not be resolved/);
  status = 200; hash = "invalid";
  await assert.rejects(resolveSeedImage(reference, {}), /could not be resolved/);
});

test("seed CLI retains its failure privately only after an owned runtime exists", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "seed-cli-failure-"));
  t.after(() => fs.rmSync(directory, { recursive: true }));
  const env = { ...process.env, GITHUB_ACTIONS: "true", RUNNER_ENVIRONMENT: "github-hosted", GITHUB_RUN_ID: "42", GITHUB_RUN_ATTEMPT: "1", RUNNER_TEMP: directory, EVAL_MODE: "live", EVAL_DATASET: "bootstrap" };
  const paths = location(env), diagnostic = path.join(paths.directory, "seed-failure.log");
  const run = () => {
    const result = spawnSync(process.execPath, ["tests/eval/scripts/seed-workflow.mjs", "unsupported"], { env, encoding: "utf8", timeout: 10_000 });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /inspect protected runner logs/);
    assert.doesNotMatch(result.stdout + result.stderr, /Unsupported seed workflow operation/);
  };
  run();
  assert.equal(fs.existsSync(diagnostic), false);
  fs.mkdirSync(paths.directory, { recursive: true, mode: 0o700 });
  fs.writeFileSync(paths.stateFile, JSON.stringify({ runID: "foreign" }));
  run();
  assert.equal(fs.existsSync(diagnostic), false);
  fs.writeFileSync(paths.stateFile, JSON.stringify({ runID: paths.runID }));
  run();
  assert.deepEqual(JSON.parse(fs.readFileSync(diagnostic)), { message: "Unsupported seed workflow operation" });
  assert.equal(fs.statSync(diagnostic).mode & 0o777, 0o600);
});

test("seed workflow has only public dataset and worker inputs, serial failure-stopping jobs and owned cleanup", async () => {
  const workflow = await readWorkflow("build-evaluation-seeds.yml");
  assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
  assert.deepEqual(Object.keys(workflow.on.workflow_dispatch.inputs), ["dataset", "workers"]);
  assert.equal(workflow.on.workflow_dispatch.inputs.workers.default, 20);
  assert.deepEqual(workflow.on.workflow_dispatch.inputs.dataset.options, ["all", ...registry.datasets.map((row) => row.id)]);
  assert.equal(workflow.concurrency["cancel-in-progress"], false);
  const datasets = workflowJob(workflow, "datasets");
  assert.equal(datasets.strategy["max-parallel"], 1);
  assert.equal(datasets.strategy["fail-fast"], true);
  assert.equal(datasets["timeout-minutes"], 360);
  assert.equal(datasets.env.EVAL_MODE, "live");
  assert.equal(datasets.env.DENSE_MEM_EVAL_IMPORT_WORKERS, "${{ needs.authorize.outputs.workers }}");
  assert.equal(datasets.env.DENSE_MEM_EVAL_TEAM_API_KEY, "${{ secrets.DENSE_MEM_EVAL_TEAM_API_KEY }}");
  assert.equal(datasets.env.DENSE_MEM_EVAL_DATABASE_PASSWORD, "${{ secrets.DENSE_MEM_EVAL_DATABASE_PASSWORD }}");
  assert.ok(datasets.steps.some((step) => step.run?.includes("330 * 60")));
  assert.ok(datasets.steps.some((step) => step.if === "always()" && step.run?.endsWith("seed-workflow.mjs cleanup")));
  const authorization = JSON.stringify(workflowJob(workflow, "authorize"));
  assert.doesNotMatch(authorization, /secrets\./);
  assert.match(authorization, /authorizeSeedBuild/);
});

test("seed authorization rejects reruns, foreign repositories and non-admin actors before image selection", async () => {
  const context = { repo: { owner: "markhuangai", repo: "dense-mem" }, runAttempt: 1, ref: "refs/heads/main", actor: "admin" };
  const github = { rest: { repos: { getCollaboratorPermissionLevel: async () => ({ data: { permission: "write" } }) } } };
  await assert.rejects(authorizeSeedBuild(github, context, "admin"), /repository admins/);
  await assert.rejects(authorizeSeedBuild(github, { ...context, runAttempt: 2 }, "admin"), /fresh/);
  await assert.rejects(authorizeSeedBuild(github, { ...context, repo: { owner: "foreign", repo: "dense-mem" } }, "admin"), /same-repository/);
});

test("seed authorization verifies the publisher attempt separately from successful validation retries", async () => {
  const repository = "markhuangai/dense-mem", revision = "b".repeat(40), main = "c".repeat(40);
  const target = `https://github.com/${repository}/actions/runs/100`;
  const image = `ghcr.io/${repository}:test-42@sha256:${"a".repeat(64)}`;
  const pull = { number: 42, draft: false, user: { login: "owner" }, head: { sha: revision, repo: { full_name: repository } } };
  const context = { repo: { owner: "markhuangai", repo: "dense-mem" }, runAttempt: 1, ref: "refs/heads/feature", sha: revision, actor: "owner", serverUrl: "https://github.com" };
  const statuses = [{ id: 2, context: "PR test image policy", state: "success", description: "Published test-42 (run 100, attempt 1).", target_url: target }, { id: 3, context: "Production image E2E", state: "success", creator: { login: "github-actions[bot]" }, target_url: target }];
  const latest = { id: 100, run_attempt: 2, event: "pull_request_target", path: ".github/workflows/pr-test-image.yml", conclusion: "success" };
  const published = { ...latest, run_attempt: 1, conclusion: "failure" };
  const jobs = [{ name: "Publish trusted preview", conclusion: "success" }];
  const receipt = { version: 1, pr_number: 42, source_sha: revision, image, run_id: "100", run_attempt: "1", trusted_revision: main };
  const comments = [{ user: { login: "github-actions[bot]" }, body: imagePolicy.formatPreviewReceipt(receipt, target) }];
  const github = { paginate: (method, args) => method(args), request: async () => ({ data: { status: "identical" } }), rest: {
    repos: { getCollaboratorPermissionLevel: async () => ({ data: { permission: "admin" } }), getBranch: async () => ({ data: { commit: { sha: main } } }), listReleases: async () => [{ tag_name: "v2.8.1-rc.5", draft: false }], listCommitStatusesForRef: async () => statuses },
    git: { getRef: async () => ({ data: { object: { type: "commit", sha: main } } }) },
    pulls: { list: async () => [pull] }, issues: { listComments: async () => comments },
    actions: { getWorkflowRun: async () => ({ data: latest }), getWorkflowRunAttempt: async ({ run_id, attempt_number }) => { assert.equal(run_id, 100); assert.equal(attempt_number, 1); return { data: published }; }, listJobsForWorkflowRunAttempt: async ({ run_id, attempt_number }) => { assert.equal(run_id, 100); assert.equal(attempt_number, 1); return jobs; } },
  } };
  assert.equal((await authorizeSeedBuild(github, context, "owner")).image, image);
  for (const change of [{ conclusion: "failure" }, { conclusion: null }, { run_attempt: 0 }, { event: "push" }]) {
    const original = { ...latest }; Object.assign(latest, change);
    await assert.rejects(authorizeSeedBuild(github, context, "owner"), /full production-image E2E receipt/);
    Object.assign(latest, original);
  }
  published.run_attempt = 2;
  await assert.rejects(authorizeSeedBuild(github, context, "owner"), /full production-image E2E receipt/);
  published.run_attempt = 1;
  jobs[0].conclusion = "failure";
  await assert.rejects(authorizeSeedBuild(github, context, "owner"), /trusted preview publication/);
  jobs[0].conclusion = "success";
  statuses[1].state = "failure";
  await assert.rejects(authorizeSeedBuild(github, context, "owner"), /full production-image E2E receipt/);
  statuses[1].state = "success";
  comments[0].body = imagePolicy.formatPreviewReceipt({ ...receipt, source_sha: "d".repeat(40) }, target);
  await assert.rejects(authorizeSeedBuild(github, context, "owner"), /preview receipt is missing/);
});

test("seed checks bind public source counts, scope and real file checksums", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "seed-check-"));
  t.after(() => fs.rmSync(directory, { recursive: true }));
  fs.writeFileSync(path.join(directory, "database.dump"), "synthetic binary checksum fixture");
  fs.writeFileSync(path.join(directory, "mapping.json"), "{}\n");
  const metadata = { version: 1, dataset: "hotpotqa", registry_sha256: registryHash, base_release: "v2.8.1-rc.5", source_revision: "b".repeat(40), postgres_image: "pgvector/pgvector:0.8.2-pg18-trixie", embedding_model: "text-embedding-3-small", embedding_dimensions: 1536, schema_version: 20261114010000, corpus_rows: 9811, canonical_sha256: "c".repeat(64), search_sha256: "d".repeat(64), mapping_sha256: await hashFile(path.join(directory, "mapping.json")), scope: { team_id: "11111111-1111-4111-8111-111111111111", credential_id: "22222222-2222-4222-8222-222222222222", owner_id: "22222222-2222-4222-8222-222222222222", space_id: "33333333-3333-4333-8333-333333333333", generation: 1 } };
  fs.writeFileSync(path.join(directory, "seed.json"), JSON.stringify(metadata));
  const sealed = await sealSeed(directory);
  assert.deepEqual(await verifySeedFiles(directory, "hotpotqa", registryHash, 9811), sealed);
  for (const changed of [{ ...sealed, dataset: "musique" }, { ...sealed, corpus_rows: 9810 }, { ...sealed, embedding_dimensions: 1024 }, { ...sealed, dump_sha256: "" }]) assert.throws(() => seedMetadata(changed, "hotpotqa", registryHash, 9811));
  fs.appendFileSync(path.join(directory, "database.dump"), "corruption");
  await assert.rejects(verifySeedFiles(directory, "hotpotqa", registryHash, 9811), /checksum or size/);
});

test("live evaluation pins every seed before allocating provider-backed jobs", async () => {
  const workflow = await readWorkflow("evaluation.yml");
  const registryJob = workflowJob(workflow, "registry");
  assert.ok(registryJob.steps.some((step) => step.run === "node tests/eval/scripts/seed-workflow.mjs pin"));
  const datasets = workflowJob(workflow, "datasets");
  assert.equal(datasets.needs, "registry");
  assert.match(datasets.env.EVAL_SEED_IMAGE, /needs\.registry\.outputs\.seed_images/);
  assert.equal(datasets.env.DENSE_MEM_EVAL_IMPORT_WORKERS, undefined);
});

test("staged seed restore consumes space rows once and preserves all other data and post-data entries", () => {
  const entries = ["; Archive created by pg_dump", "42; 0 123 TABLE DATA public teams densemem", "43; 0 124 TABLE DATA public memory_spaces densemem", "44; 0 125 TABLE DATA public ontology_maintenance_markers densemem", "45; 1259 126 INDEX public memory_spaces_team_shared_unique densemem", "46; 2606 127 FK CONSTRAINT public memory_spaces team_fk densemem", ""];
  const remaining = remainingSeedRestoreEntries(entries.join("\n")).split("\n");
  assert.equal(remaining[2], `;${entries[2]}`);
  assert.deepEqual(remaining.filter((_, index) => index !== 2), entries.filter((_, index) => index !== 2));
  for (const invalid of ["", entries.filter((_, index) => index !== 2).join("\n"), [...entries, entries[2]].join("\n")]) assert.throws(() => remainingSeedRestoreEntries(invalid), /exactly one memory_spaces/);
});
