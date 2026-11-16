import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { seedBuildRequest, seedReference, seedBuildVersion, seedMetadata, verifySeedFiles, sealSeed, hashFile, authorizeSeedBuild } from "../../../.github/scripts/evaluation-seeds.mjs";
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
