import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import imagePolicy from "../../../.github/scripts/image-release-policy.cjs";
import { seedBuildRequest, seedReference, seedBuildVersion, seedMetadata, verifySeedFiles, sealSeed, hashFile, resolveSeedBuildSource, requireSeedBuildValidation, remainingSeedRestoreEntries, resolveSeedImage, publishSeed } from "../../../.github/scripts/evaluation-seeds.mjs";
import { location, setup } from "./workflow.mjs";
import { localSeedRequest, lockSeedBuild, savedSeedRequest } from "./local-seeds.mjs";
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

test("seed publication protects dated tags and verifies each push and alias digest", async (t) => {
  const selected = seedBuildVersion("v2.8.1-rc.5", new Date("2026-11-17T04:26:41.183Z"));
  const dated = seedReference("hotpotqa", selected);
  const latest = seedReference("hotpotqa");
  const digest = `sha256:${"a".repeat(64)}`;
  const collision = ["resolve:dated"];
  const validationAfterBuild = [...collision, "build", "validate"];
  const validationBeforeAlias = [...validationAfterBuild, "push:dated", "resolve:dated", "tag:latest", "validate"];
  const published = [...validationBeforeAlias, "push:latest", "resolve:latest"];
  for (const scenario of [
    { name: "existing dated tag", existing: true, error: /already exists/, expected: collision },
    { name: "validation changes during build", rejectGate: 1, error: /stale validation/, expected: validationAfterBuild },
    { name: "validation changes after dated publication", rejectGate: 2, error: /stale validation/, expected: validationBeforeAlias },
    { name: "alias digest differs", aliasDigest: `sha256:${"b".repeat(64)}`, error: /latest alias differs/, expected: published },
    { name: "both tags resolve to the verified digest", expected: published },
  ]) {
    await t.test(scenario.name, async (t) => {
      const directory = fs.mkdtempSync(path.join(os.tmpdir(), "seed-publish-"));
      t.after(() => fs.rmSync(directory, { recursive: true }));
      for (const file of ["database.dump", "seed.json", "mapping.json", "NOTICE.md"]) fs.writeFileSync(path.join(directory, file), "{}");
      const events = [];
      const manifests = new Map(scenario.existing ? [[dated, digest]] : []);
      const label = (ref) => ref === dated ? "dated" : "latest";
      t.mock.method(globalThis, "fetch", async (url, options) => {
        const route = new URL(url).pathname;
        if (route === "/token") return Response.json({ token: "registry-test-token" });
        assert.equal(options.method, "HEAD");
        const ref = [dated, latest].find((item) => route.endsWith("/manifests/" + item.split(":").at(-1)));
        assert(ref, "only the tested dataset tags may be resolved");
        events.push("resolve:" + label(ref));
        const hash = manifests.get(ref);
        return hash ? new Response(null, { headers: { "Docker-Content-Digest": hash } }) : new Response(null, { status: 404 });
      });
      const docker = (args) => {
        if (args[0] === "build") { events.push("build"); return; }
        const ref = args.at(-1);
        assert([dated, latest].includes(ref));
        events.push(args[0] + ":" + label(ref));
        if (args[0] === "push") manifests.set(ref, ref === latest ? (scenario.aliasDigest || digest) : digest);
      };
      let gateCalls = 0;
      const validate = async () => {
        events.push("validate");
        if (++gateCalls === scenario.rejectGate) throw new Error("stale validation");
      };
      const result = publishSeed(docker, directory, "hotpotqa", selected, {}, validate);
      if (scenario.error) await assert.rejects(result, scenario.error);
      else assert.equal(await result, dated.split(":")[0] + "@" + digest);
      assert.deepEqual(events, scenario.expected);
    });
  }
});

test("local seed arguments default to every dataset with twenty workers", () => {
  const all = registry.datasets.map(({ id }) => id);
  assert.deepEqual(localSeedRequest([], registry), { pr: undefined, dataset: all, workers: 20, skipAISecurityReview: false });
  assert.deepEqual(localSeedRequest(["--pr", "777"], registry), { pr: 777, dataset: all, workers: 20, skipAISecurityReview: false });
  assert.deepEqual(localSeedRequest(["--pr", "777", "--dataset", "hotpotqa", "--workers", "40", "--skip-ai-security-check"], registry), { pr: 777, dataset: ["hotpotqa"], workers: 40, skipAISecurityReview: true });
  for (const args of [["--pr", "0"], ["--pr", "1.5"], ["--pr", "9007199254740992"], ["--workers", "0"], ["--workers", "101"], ["--dataset", "fixtures"], ["--mode", "live"]]) {
    assert.throws(() => localSeedRequest(args, registry));
  }
});

test("resume restores the original seed build settings and locks its local directory", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "seed-build-lock-"));
  t.after(() => fs.rmSync(directory, { recursive: true }));
  const id = `local-${"a".repeat(32)}`;
  const registryHash = "b".repeat(64);
  const version = seedBuildVersion("v2.8.1-rc.5");
  const receipt = { resume_version: 1, build_id: id, registry_sha256: registryHash, pr: 777, version: version,
    workers: 20, datasets: ["hotpotqa"], skip_ai_security_review: true };
  assert.deepEqual(localSeedRequest(["--resume", id], registry), { resume: id });
  for (const args of [["--resume", "../seed"], ["--resume", id, "--workers", "5"], ["--resume", id, "--dataset", "musique"]]) {
    assert.throws(() => localSeedRequest(args, registry), /saved local build ID/);
  }
  assert.deepEqual(savedSeedRequest(receipt, id, registry, registryHash), {
    pr: 777, dataset: ["hotpotqa"], workers: 20, skipAISecurityReview: true,
  });
  assert.throws(() => savedSeedRequest(receipt, id, registry, "c".repeat(64)), /incompatible/);
  assert.throws(() => savedSeedRequest({ ...receipt, datasets: ["hotpotqa", "musique"] }, id, registry, registryHash), /selection is invalid/);
  const unlock = lockSeedBuild(directory);
  try { assert.throws(() => lockSeedBuild(directory), /already running/); }
  finally { unlock(); }
  lockSeedBuild(directory)();
});

test("seed publication resume checks the dated image bytes before updating latest", async (t) => {
  const version = seedBuildVersion("v2.8.1-rc.5");
  const dated = seedReference("hotpotqa", version);
  const latest = seedReference("hotpotqa");
  const repository = latest.slice(0, latest.lastIndexOf(":"));
  const pinned = `${repository}@sha256:${"a".repeat(64)}`;
  const previousLatest = `${repository}@sha256:${"b".repeat(64)}`;
  const files = ["database.dump", "seed.json", "mapping.json", "NOTICE.md"];
  const runID = `local-${"a".repeat(32)}-hotpotqa`;
  for (const scenario of [
    { name: "dated push succeeded and latest still points to the prior image", alias: previousLatest, push: true },
    { name: "both pushes already succeeded", alias: pinned, push: false },
    { name: "dated data conflict", alias: previousLatest, changed: true, error: /differs from the verified local export/ },
    { name: "a newer alias appeared", alias: `${repository}@sha256:${"c".repeat(64)}`, error: /alias changed/ },
  ]) {
    await t.test(scenario.name, async (t) => {
      const directory = fs.mkdtempSync(path.join(os.tmpdir(), "seed-publish-resume-"));
      t.after(() => fs.rmSync(directory, { recursive: true }));
      for (const file of files) fs.writeFileSync(path.join(directory, file), "{}");
      let currentAlias = scenario.alias, gated = false;
      const mutations = [];
      t.mock.method(globalThis, "fetch", async (url) => {
        const route = new URL(url).pathname;
        if (route === "/token") return Response.json({ token: "registry-test-token" });
        const image = route.endsWith(dated.split(":").at(-1)) ? pinned : currentAlias;
        return new Response(null, { headers: { "Docker-Content-Digest": image.split("@")[1] } });
      });
      const docker = (args) => {
        switch (args[0]) {
          case "image": return "present";
          case "create": assert.ok(args.includes(pinned)); return;
          case "container": return JSON.stringify([{ Id: "owned", Config: { Labels: { "io.dense-mem.eval.run": runID } } }]);
          case "cp":
            for (const file of files) fs.copyFileSync(path.join(directory, file), path.join(args.at(-1), file));
            if (scenario.changed) fs.writeFileSync(path.join(args.at(-1), "mapping.json"), "different");
            return;
          case "tag": mutations.push(args); return;
          case "push": assert(gated); mutations.push(args); currentAlias = pinned; return;
          default: assert.fail(`unexpected Docker operation ${args[0]}`);
        }
      };
      const result = publishSeed(docker, directory, "hotpotqa", version, {}, async () => { gated = true; }, { runID, previousLatest });
      if (scenario.error) await assert.rejects(result, scenario.error);
      else assert.equal(await result, pinned);
      assert.deepEqual(mutations, scenario.push ? [["tag", pinned, latest], ["push", "--quiet", latest]] : []);
    });
  }
});

test("public seed preparation and scored runs have separate runtime entry points", async () => {
  const hosted = { GITHUB_ACTIONS: "true", RUNNER_ENVIRONMENT: "github-hosted", GITHUB_RUN_ID: "42", GITHUB_RUN_ATTEMPT: "1", EVAL_MODE: "live", EVAL_DATASET: "hotpotqa", RUNNER_TEMP: "/tmp" };
  const result = spawnSync(process.execPath, ["tests/eval/scripts/local-seeds.mjs"], { env: { ...process.env, ...hosted }, encoding: "utf8", timeout: 10_000 });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /requires a local Linux amd64 Docker host/);
  assert.equal(fs.existsSync(".github/workflows/build-evaluation-seeds.yml"), false);
  for (const operation of ["prepare", "bootstrap", "verify-seed"]) {
    await assert.rejects(setup(hosted, { operation }), /only scoring and synthetic fixture preparation/);
  }
  const local = { EVAL_MODE: "live", EVAL_DATASET: "hotpotqa", EVAL_LOCAL_SEED_ID: `local-${"a".repeat(32)}`, EVAL_LOCAL_SEED_ROOT: "/tmp" };
  assert.equal(location(local).runID, `local-${"a".repeat(32)}-hotpotqa`);
  await assert.rejects(setup(local), /only public seed preparation and restore verification/);
  await assert.rejects(setup({ ...local, GITHUB_ACTIONS: "true" }, { operation: "prepare" }), /Invalid local seed runtime identity/);
});

function seedGitHubFixture() {
  const repository = "markhuangai/dense-mem", source = "b".repeat(40), main = "c".repeat(40);
  const target = `https://github.com/${repository}/actions/runs/100`;
  const digest = `sha256:${"a".repeat(64)}`;
  const pull = { number: 42, state: "open", draft: false, base: { ref: "main", sha: main }, user: { login: "Z-M-Huang" }, head: { sha: source, repo: { full_name: repository } } };
  const status = (id, context) => ({ id, context, state: "success", creator: { login: "github-actions[bot]" }, target_url: target });
  const statuses = [
    { ...status(2, "PR test image policy"), description: "Published test-42 (run 100, attempt 1)." },
    status(3, "Production image E2E"),
    status(4, "Repository CI"),
  ];
  const latest = { id: 100, head_sha: source, run_attempt: 2, event: "pull_request_target", path: ".github/workflows/pr-test-image.yml", display_title: "PR test image: PR #42", status: "completed", conclusion: "success" };
  const published = { ...latest, run_attempt: 1, conclusion: "failure" };
  const jobs = { jobs: [{ name: "Publish trusted preview", conclusion: "success" }] };
  const receipt = { version: 1, pr_number: 42, source_sha: source, image: `ghcr.io/${repository}:test-42@${digest}`, run_id: "100", run_attempt: "1", trusted_revision: main };
  const comments = [{ user: { login: "github-actions[bot]" }, body: imagePolicy.formatPreviewReceipt(receipt, target) }];
  const checks = { check_runs: ["CodeQL", "CodeQL analysis"].map((name, id) => ({ id, name, head_sha: source, status: "completed", conclusion: "success", app: { slug: name === "CodeQL" ? "github-advanced-security" : "github-actions" } })) };
  const permission = { permission: "admin" };
  const mainCI = { id: 300, run_attempt: 1, head_sha: main, path: ".github/workflows/ci-push.yml", event: "push", status: "completed", conclusion: "success" };
  const routes = new Map([
    ["user", pull.user],
    ["collaborators/Z-M-Huang/permission", permission],
    ["branches/main", { commit: { sha: main } }],
    ["pulls/42", pull],
    ["releases", [{ tag_name: "v2.8.1-rc.5", draft: false }]],
    ["git/ref/tags/v2.8.1-rc.5", { object: { type: "commit", sha: main } }],
    [`compare/${main}...${main}`, { status: "identical" }],
    [`commits/${main}/pulls`, [pull]],
    [`commits/${source}/statuses`, statuses],
    [`commits/${source}/check-runs`, checks],
    ["actions/runs/100", latest],
    ["actions/runs/100/attempts/1", published],
    ["actions/runs/100/attempts/1/jobs", jobs],
    ["actions/runs/100/attempts/2/jobs", { jobs: [] }],
    ["issues/42/comments", comments],
    ["actions/workflows/ci-push.yml/runs", { workflow_runs: [mainCI] }],
    ["actions/runs/300/attempts/1/jobs", { jobs: [] }],
  ]);
  const github = { request: async (route) => {
    const endpoint = route.replace(new RegExp(`^GET /(?:repos/${repository}/)?`), "");
    assert.ok(routes.has(endpoint), `Unexpected API route ${route}`);
    return { data: routes.get(endpoint) };
  } };
  return { github, repository, source, main, digest, pull, statuses, latest, published, checks, permission, mainCI };
}

test("local seed builds require current-head checks and a verified publisher attempt", async () => {
  const f = seedGitHubFixture();
  const resolve = () => resolveSeedBuildSource(f.github, 42, f.source);
  const source = await resolve();
  assert.equal(source.image, `ghcr.io/${f.repository}@${f.digest}`);
  assert.equal(source.sourceRevision, f.source);
  assert.equal(source.baseRelease, "v2.8.1-rc.5");
  assert.equal(source.validation.publisher_attempt, 1);
  assert.equal(source.validation.validation_attempt, 2);

  f.permission.permission = "write";
  await assert.rejects(resolve(), /repository admin/);
  f.permission.permission = "admin";
  await assert.rejects(resolveSeedBuildSource(f.github, 42, f.main), /own current open/);

  f.latest.status = "in_progress";
  f.latest.conclusion = null;
  f.statuses[1].state = "pending";
  await assert.rejects(resolve(), /passing current-head CI\/E2E/);
  await assert.rejects(requireSeedBuildValidation(f.github, source.validation), /passing current-head CI\/E2E/);
  f.latest.status = "completed";
  f.latest.conclusion = "success";
  f.statuses[1].state = "success";

  f.checks.check_runs[0].conclusion = "failure";
  await assert.rejects(resolve(), /current-head CodeQL/);
  f.checks.check_runs[0].conclusion = "success";
  f.published.head_sha = f.main;
  await assert.rejects(resolve(), /publisher workflow receipt/);
  f.published.head_sha = f.source;
  f.pull.head.sha = "d".repeat(40);
  await assert.rejects(requireSeedBuildValidation(f.github, source.validation), /PR head or ownership changed/);
});

test("main seed checkout selects a published release after main and merge-source validation", async () => {
  const f = seedGitHubFixture();
  Object.assign(f.pull, { state: "closed", merged: true, merged_at: "2026-11-17T12:00:00Z", merge_commit_sha: f.main });
  const source = await resolveSeedBuildSource(f.github, undefined, f.main);
  assert.equal(source.image, `ghcr.io/${f.repository}:v2.8.1-rc.5`);
  assert.equal(source.sourceRevision, f.main);
  assert.equal(source.validation.source_revision, f.source);
  await assert.rejects(resolveSeedBuildSource(f.github, undefined, f.source), /must match current main/);
  f.mainCI.conclusion = "failure";
  await assert.rejects(requireSeedBuildValidation(f.github, source.validation), /validation failed/);
  f.mainCI.conclusion = "success";
  f.pull.merge_commit_sha = "d".repeat(40);
  await assert.rejects(requireSeedBuildValidation(f.github, source.validation), /PR head or ownership changed/);
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
  const proof = { ...sealed, intake_proof: true, ai_security_review: "skipped", corpus_rows: 20 };
  fs.writeFileSync(path.join(directory, "seed.json"), JSON.stringify(proof));
  assert.deepEqual(await verifySeedFiles(directory, "hotpotqa", registryHash, 20, { intakeProof: true }), proof);
  await assert.rejects(verifySeedFiles(directory, "hotpotqa", registryHash, 20), /export kind/);
  await assert.rejects(publishSeed(() => assert.fail("Docker spent work on a private proof"), directory, "hotpotqa", "latest", {}, async () => {}), /proof exports cannot be published/);
  fs.writeFileSync(path.join(directory, "seed.json"), JSON.stringify(sealed));
  for (const changed of [{ ...sealed, dataset: "musique" }, { ...sealed, corpus_rows: 9810 }, { ...sealed, embedding_dimensions: 1024 }, { ...sealed, dump_sha256: "" }]) assert.throws(() => seedMetadata(changed, "hotpotqa", registryHash, 9811));
  fs.appendFileSync(path.join(directory, "database.dump"), "corruption");
  await assert.rejects(verifySeedFiles(directory, "hotpotqa", registryHash, 9811), /checksum or size/);
});

test("live evaluation pins every seed before allocating provider-backed jobs", async () => {
  const workflow = await readWorkflow("evaluation.yml");
  const registryJob = workflowJob(workflow, "registry");
  assert.ok(registryJob.steps.some((step) => step.run === "node tests/eval/scripts/workflow.mjs pin-seeds"));
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
