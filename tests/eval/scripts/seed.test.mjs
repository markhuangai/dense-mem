import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import imagePolicy from "../../../.github/scripts/image-release-policy.cjs";
import { seedBuildRequest, seedReference, seedBuildVersion, seedMetadata, verifySeedFiles, sealSeed, hashFile, authorizeSeedBuild, remainingSeedRestoreEntries, resolveSeedImage } from "../../../.github/scripts/evaluation-seeds.mjs";
import { location } from "./workflow.mjs";
import { seedValidationState } from "../../../.github/scripts/seed-validation.mjs";
import { startSeedValidation } from "./seed-workflow.mjs";
import { resolveEvaluationImage } from "../../../.github/scripts/evaluation-policy.mjs";
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
  assert.match(datasets.env.EVAL_VALIDATION, /needs\.authorize\.outputs\.validation/);
  const bootstrap = workflowJob(workflow, "bootstrap");
  assert.deepEqual(bootstrap.needs, ["authorize", "preflight"]);
  const preflight = workflowJob(workflow, "preflight");
  assert.equal(preflight.needs, "authorize");
  assert.ok(preflight.steps.some((step) => step.run?.includes("seed_docker.test.mjs")));
  assert.doesNotMatch(JSON.stringify(preflight), /secrets\./);
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

test("seed authorization verifies the publisher attempt separately from successful validation retries", async (t) => {
  const repository = "markhuangai/dense-mem", revision = "b".repeat(40), main = "c".repeat(40);
  const target = `https://github.com/${repository}/actions/runs/100`;
  const image = `ghcr.io/${repository}:test-42@sha256:${"a".repeat(64)}`;
  const canonical = `ghcr.io/${repository}@sha256:${"a".repeat(64)}`;
  const pull = { number: 42, draft: false, user: { login: "Z-M-Huang" }, head: { sha: revision, repo: { full_name: repository } } };
  const context = { repo: { owner: "markhuangai", repo: "dense-mem" }, runAttempt: 1, ref: "refs/heads/feature", sha: revision, actor: "Z-M-Huang", serverUrl: "https://github.com" };
  const statuses = [{ id: 2, context: "PR test image policy", state: "success", creator: { login: "github-actions[bot]" }, description: "Published test-42 (run 100, attempt 1).", target_url: target }, { id: 3, context: "Production image E2E", state: "success", creator: { login: "github-actions[bot]" }, target_url: target }, { id: 4, context: "Repository CI", state: "success", creator: { login: "github-actions[bot]" }, target_url: target }];
  const latest = { id: 100, run_attempt: 2, event: "pull_request_target", path: ".github/workflows/pr-test-image.yml", display_title: "PR test image: PR #42", status: "completed", conclusion: "success" };
  const published = { ...latest, run_attempt: 1, conclusion: "failure" };
  const jobs = [{ name: "Publish trusted preview", conclusion: "success" }];
  const receipt = { version: 1, pr_number: 42, source_sha: revision, image, run_id: "100", run_attempt: "1", trusted_revision: main };
  const comments = [{ user: { login: "github-actions[bot]" }, body: imagePolicy.formatPreviewReceipt(receipt, target) }];
  const checks = ["CodeQL", "CodeQL analysis"].map((name, id) => ({ id, name, head_sha: revision, status: "completed", conclusion: "success", app: { slug: name === "CodeQL" ? "github-advanced-security" : "github-actions" } }));
  const github = { paginate: (method, args) => method(args), request: async (route) => {
    if (route.includes("/compare/")) return { data: { status: "identical" } };
    if (route.endsWith("/pulls/42")) return { data: { ...pull, state: "open" } };
    if (route.endsWith("/actions/runs/100")) return { data: latest };
    if (route.endsWith("/jobs")) return { data: { jobs } };
    if (route.endsWith("/statuses")) return { data: statuses };
    if (route.endsWith("/check-runs")) return { data: { check_runs: checks } };
    throw new Error(`Unexpected API route ${route}`);
  }, rest: {
    repos: { getCollaboratorPermissionLevel: async () => ({ data: { permission: "admin" } }), getBranch: async () => ({ data: { commit: { sha: main } } }), listReleases: async () => [{ tag_name: "v2.8.1-rc.5", draft: false }], listCommitStatusesForRef: async () => statuses },
    git: { getRef: async () => ({ data: { object: { type: "commit", sha: main } } }) },
    pulls: { list: async () => [pull] }, issues: { listComments: async () => comments },
    actions: { getWorkflowRun: async () => ({ data: latest }), getWorkflowRunAttempt: async ({ run_id, attempt_number }) => { assert.equal(run_id, 100); assert.equal(attempt_number, 1); return { data: published }; }, listJobsForWorkflowRunAttempt: async ({ run_id, attempt_number }) => { assert.equal(run_id, 100); assert.equal(attempt_number, 1); return jobs; } },
  } };
  assert.equal((await authorizeSeedBuild(github, context, "Z-M-Huang")).image, canonical);
  const authorized = await authorizeSeedBuild(github, context, "Z-M-Huang");
  const proof = JSON.parse(authorized.validation);
  assert.equal(await seedValidationState(github, proof), "success");
  latest.status = "in_progress"; latest.conclusion = null;
  statuses[1].state = statuses[2].state = "pending";
  assert.equal((await authorizeSeedBuild(github, context, "Z-M-Huang")).image, canonical);
  assert.equal(await seedValidationState(github, proof), "pending");
  const env = { EVAL_VALIDATION: authorized.validation, EVAL_SOURCE_REVISION: revision, EVAL_IMAGE: resolveEvaluationImage(image, repository, { Os: "linux", Architecture: "amd64", RepoDigests: [canonical], Config: { Labels: { "org.opencontainers.image.variant": "production", "org.opencontainers.image.source": `https://github.com/${repository}`, "org.opencontainers.image.revision": revision } } }).image, EVAL_MODEL_DEADLINE_EPOCH: String(Math.floor(Date.now()/1000)+60) };
  const monitor = await startSeedValidation(env, github);
  latest.status = "completed"; latest.conclusion = "success";
  statuses[1].state = statuses[2].state = "success";
  await monitor.beforePublication();
  latest.conclusion = "failure";
  await assert.rejects(monitor.beforePublication(), /validation failed/);
  assert.equal(monitor.signal.aborted, true);
  await assert.rejects(monitor.finish(), /validation failed/);
  latest.conclusion = "success";
  await assert.rejects(startSeedValidation({ ...env, EVAL_IMAGE: canonical.replace("a".repeat(64), "f".repeat(64)) }, github), /differs from validation identity/);
  for (const change of [{ conclusion: "failure" }, { run_attempt: 0 }, { event: "push" }]) {
    const original = { ...latest }; Object.assign(latest, change);
    await assert.rejects(authorizeSeedBuild(github, context, "Z-M-Huang"), /validation failed|publisher workflow receipt/);
    Object.assign(latest, original);
  }
  published.run_attempt = 2;
  await assert.rejects(authorizeSeedBuild(github, context, "Z-M-Huang"), /publisher workflow receipt/);
  published.run_attempt = 1;
  jobs[0].conclusion = "failure";
  await assert.rejects(authorizeSeedBuild(github, context, "Z-M-Huang"), /trusted preview publication/);
  jobs[0].conclusion = "success";
  statuses[1].state = "failure";
  await assert.rejects(authorizeSeedBuild(github, context, "Z-M-Huang"), /status is missing, stale or failed/);
  statuses[1].state = "success";
  for (const mutate of [() => { pull.head.sha = "d".repeat(40); }, () => { latest.run_attempt = 3; }, () => { jobs.push({ conclusion: "timed_out" }); }, () => { checks[0].conclusion = "failure"; }, () => { statuses.push({ ...statuses[1], id: 20, state: "failure" }); }]) {
    const oldPull = pull.head.sha, oldAttempt = latest.run_attempt;
    mutate();
    await assert.rejects(seedValidationState(github, proof));
    pull.head.sha = oldPull; latest.run_attempt = oldAttempt;
    if (jobs.length > 1) jobs.pop(); checks[0].conclusion = "success";
    if (statuses.length > 3) statuses.pop();
  }
  t.mock.timers.enable({ apis: ["setInterval"] });
  const watched = await startSeedValidation(env, github);
  latest.conclusion = "cancelled";
  t.mock.timers.tick(60_000);
  for (let turn = 0; turn < 20 && !watched.signal.aborted; turn++) await Promise.resolve();
  assert.equal(watched.signal.aborted, true, "the periodic validation check must abort paid work");
  await assert.rejects(watched.finish(), /validation failed/);
  t.mock.timers.reset();
  latest.conclusion = "success";
  const originalRequest = github.request;
  for (const first of ["poll", "publication"]) {
    t.mock.timers.enable({ apis: ["setInterval"] });
    const publication = await startSeedValidation(env, github);
    let entered, release;
    const checkingRun = new Promise((resolve) => { entered = resolve; });
    const heldRun = new Promise((resolve) => { release = resolve; });
    let hold = true;
    github.request = async (route, query) => {
      if (route.endsWith("/actions/runs/100") && hold) { hold = false; entered(); await heldRun; }
      return originalRequest(route, query);
    };
    let publishing;
    if (first === "poll") t.mock.timers.tick(60_000);
    else publishing = publication.beforePublication();
    await checkingRun;
    if (first === "poll") publishing = publication.beforePublication();
    pull.head.sha = "d".repeat(40);
    if (first === "publication") t.mock.timers.tick(60_000);
    for (let turn = 0; turn < 20; turn++) await Promise.resolve();
    const aborted = publication.signal.aborted;
    release();
    const failure = await publishing.then(() => null, (error) => error);
    if (first === "poll" || aborted) assert.match(failure?.message || "", /PR head or ownership/, "a recorded validation failure must prevent publication");
    else assert.equal(failure, null);
    await assert.rejects(publication.beforePublication(), /PR head or ownership/);
    assert.equal(publication.signal.aborted, true);
    await assert.rejects(publication.finish(), /PR head or ownership/);
    github.request = originalRequest;
    pull.head.sha = revision;
    t.mock.timers.reset();
  }

  const automaticRun = { id: 200, run_attempt: 1, head_sha: revision, event: "pull_request_target", path: ".github/workflows/ci-pr.yml", status: "completed", conclusion: "success" };
  const automaticJobs = [{ name: "CI / Go verification", conclusion: "success" }];
  github.request = async (route, query) => {
    if (route.endsWith("/actions/runs/200")) return { data: automaticRun };
    if (route.endsWith("/actions/runs/200/attempts/1/jobs")) return { data: { jobs: automaticJobs } };
    if (route.includes("/workflows/ci-pr.yml/runs")) return { data: { workflow_runs: [automaticRun] } };
    return originalRequest(route, query);
  };
  pull.user.login = "other-admin";
  statuses[2].target_url = `https://github.com/${repository}/actions/runs/200`;
  const automatic = await authorizeSeedBuild(github, { ...context, actor: "other-admin" }, "other-admin");
  const automaticProof = JSON.parse(automatic.validation);
  assert.equal(await seedValidationState(github, automaticProof), "success");
  automaticRun.status = "in_progress"; automaticRun.conclusion = null;
  statuses[2].target_url = target; statuses[2].state = "pending";
  assert.equal(await seedValidationState(github, automaticProof), "pending", "pending preview placeholders must follow the actual automatic CI producer");
  automaticJobs[0].conclusion = "failure";
  await assert.rejects(seedValidationState(github, automaticProof), /validation job failed/);
  automaticRun.status = "completed"; automaticRun.conclusion = "success"; automaticJobs[0].conclusion = "success";
  statuses[2].state = "success"; statuses[2].target_url = `https://github.com/${repository}/actions/runs/200`;
  for (const change of [{ head_sha: "d".repeat(40) }, { event: "push" }, { path: ".github/workflows/other.yml" }]) {
    const previous = { ...automaticRun }; Object.assign(automaticRun, change);
    await assert.rejects(seedValidationState(github, automaticProof), /workflow or source revision/);
    Object.assign(automaticRun, previous);
  }
  statuses[2].creator.login = "untrusted";
  await assert.rejects(seedValidationState(github, automaticProof), /status is missing/);
  statuses[2].creator.login = "github-actions[bot]";
  statuses[2].target_url = "https://github.com/foreign/repository/actions/runs/200";
  await assert.rejects(seedValidationState(github, automaticProof), /status source is invalid/);
  statuses[2].target_url = target;
  await assert.rejects(seedValidationState(github, automaticProof), /wrong workflow producer/);
  pull.user.login = "Z-M-Huang";
  github.request = originalRequest;
  const merged = { ...pull, state: "closed", merged: true, merged_at: "2026-11-17T12:00:00Z", merge_commit_sha: main, base: { ref: "main" } };
  const mainCI = { id: 300, run_attempt: 1, head_sha: main, event: "push", status: "completed", conclusion: "success" };
  let mainHead = main;
  const mainGithub = { ...github, request: async (route, query) => {
    if (route.endsWith("/branches/main")) return { data: { commit: { sha: mainHead } } };
    if (route.includes("/workflows/ci-push.yml/runs")) return { data: { workflow_runs: [mainCI] } };
    if (route.endsWith("/pulls/42")) return { data: merged };
    return github.request(route, query);
  }, rest: { ...github.rest, repos: { ...github.rest.repos, listPullRequestsAssociatedWithCommit: async () => [merged] }, pulls: { ...github.rest.pulls, get: async () => ({ data: merged }) } } };
  const mainResult = await authorizeSeedBuild(mainGithub, { ...context, ref: "refs/heads/main", sha: main }, "Z-M-Huang");
  assert.equal(mainResult.image, "ghcr.io/markhuangai/dense-mem:v2.8.1-rc.5");
  const mainProof = JSON.parse(mainResult.validation);
  assert.equal(mainProof.validation_revision, revision);
  assert.equal(await seedValidationState(mainGithub, mainProof), "success");
  mainCI.status = "in_progress"; mainCI.conclusion = null;
  assert.equal(await seedValidationState(mainGithub, mainProof), "pending");
  mainCI.conclusion = "failure";
  await assert.rejects(seedValidationState(mainGithub, mainProof), /validation failed/);
  mainCI.status = "completed"; mainCI.conclusion = "success";
  mainHead = "d".repeat(40);
  await assert.rejects(seedValidationState(mainGithub, mainProof), /no longer matches main/);
  mainHead = main;
  merged.merge_commit_sha = "d".repeat(40);
  await assert.rejects(seedValidationState(mainGithub, mainProof), /PR head or ownership/);
  merged.merge_commit_sha = main;
  comments[0].body = imagePolicy.formatPreviewReceipt({ ...receipt, source_sha: "d".repeat(40) }, target);
  await assert.rejects(authorizeSeedBuild(github, context, "Z-M-Huang"), /preview receipt is missing/);
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
