import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";
import { fileURLToPath } from "node:url";
import { requireRunner, sha256, registryMatrix } from "../../../.github/scripts/evaluation-policy.mjs";

function numericArchive(bytes, env) {
  const directory = fs.mkdtempSync(path.join(env.RUNNER_TEMP, "evaluation-replay-registry-"));
  const file = path.join(directory, "numeric.zip");
  try {
    fs.writeFileSync(file, bytes, { mode: 0o600 });
    const extracted = spawnSync("unzip", ["-p", file, "scores.json"], { encoding: "utf8", maxBuffer: 32 << 20 });
    if (extracted.status !== 0) throw new Error("Replay unavailable: numeric registry evidence is invalid");
    return JSON.parse(extracted.stdout);
  } finally { if (fs.existsSync(file)) fs.unlinkSync(file); fs.rmdirSync(directory); }
}

export async function replayRegistry(env = process.env) {
  requireRunner(env);
  if (!/^[1-9][0-9]*$/.test(env.EVAL_REPLAY_RUN_ID || "")) throw new Error("Replay unavailable: a retained run is required");
  const api = async (suffix) => {
    const response = await fetch(`https://api.github.com/repos/${env.GITHUB_REPOSITORY}${suffix}`, { headers: { Authorization: `Bearer ${env.GH_TOKEN}`, Accept: "application/vnd.github+json" }, signal: AbortSignal.timeout(120_000) });
    if (!response.ok) throw new Error("Replay unavailable: retained registry evidence is missing");
    return response;
  };
  const original = await (await api(`/actions/runs/${env.EVAL_REPLAY_RUN_ID}`)).json();
  if (original.path !== ".github/workflows/production-image-e2e.yml" || original.event !== "workflow_dispatch" || original.head_sha !== env.EVAL_SOURCE_REVISION || original.run_attempt !== 1 || original.status !== "completed") throw new Error("Replay unavailable: source run identity differs");
  const artifacts = [];
  for (let page = 1;; page++) { const batch = (await (await api(`/actions/runs/${original.id}/artifacts?per_page=100&page=${page}`)).json()).artifacts; artifacts.push(...batch); if (batch.length < 100) break; }
  const artifact = artifacts.find((row) => /^evaluation-(hotpotqa|musique|2wikimultihopqa|nq_rear|popqa)$/.test(row.name) && !row.expired);
  if (!artifact || !/^sha256:[0-9a-f]{64}$/.test(artifact.digest || "")) throw new Error("Replay unavailable: numeric registry evidence expired");
  const bytes = Buffer.from(await (await api(`/actions/artifacts/${artifact.id}/zip`)).arrayBuffer());
  if (`sha256:${sha256(bytes)}` !== artifact.digest) throw new Error("Replay unavailable: registry evidence digest mismatch");
  const measured = numericArchive(bytes, env);
  if (measured.identity?.source_revision !== env.EVAL_SOURCE_REVISION || measured.identity?.image !== env.EVAL_IMAGE || measured.identity?.workflow_run_id !== String(original.id)) throw new Error("Replay unavailable: measured registry identity differs");
  for (const mode of ["live", "fixtures"]) {
    const bytes = fs.readFileSync(mode === "fixtures" ? "tests/eval/fixtures/datasets.json" : "tests/eval/datasets.json");
    if (sha256(bytes) === measured.identity.registry_sha256) return { mode, matrix: registryMatrix(JSON.parse(bytes)) };
  }
  throw new Error("Replay unavailable: pinned registry is unknown");
}

async function main() {
  requireRunner();
  const env = process.env;
  if (!/^[1-9][0-9]*$/.test(env.EVAL_REPLAY_RUN_ID || "") || !/^[a-z0-9_]+$/.test(env.EVAL_DATASET || "")) throw new Error("Replay requires a retained run and registered dataset");
  const api = async (suffix) => {
    const response = await fetch(`https://api.github.com/repos/${env.GITHUB_REPOSITORY}${suffix}`, { headers: { Authorization: `Bearer ${env.GH_TOKEN}`, Accept: "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28" }, signal: AbortSignal.timeout(120_000) });
    if (!response.ok) throw new Error(`Replay unavailable: retained GitHub evidence returned HTTP ${response.status}`);
    return response;
  };
  const original = await (await api(`/actions/runs/${env.EVAL_REPLAY_RUN_ID}`)).json();
  if (original.path !== ".github/workflows/production-image-e2e.yml" || original.event !== "workflow_dispatch" || original.head_sha !== env.EVAL_SOURCE_REVISION || original.run_attempt !== 1 || original.status !== "completed") throw new Error("Replay source identity differs from this approved PR head");
  const artifacts = [];
  for (let page = 1;; page++) { const batch = (await (await api(`/actions/runs/${original.id}/artifacts?per_page=100&page=${page}`)).json()).artifacts; artifacts.push(...batch); if (batch.length < 100) break; }
  const matches = artifacts.filter((artifact) => artifact.name === `evaluation-${env.EVAL_DATASET}` && !artifact.expired);
  if (matches.length !== 1 || !/^sha256:[0-9a-f]{64}$/.test(matches[0].digest || "")) throw new Error("Replay unavailable: missing, expired or unverifiable numeric artifact");
  const artifact = matches[0];
  const archive = Buffer.from(await (await api(`/actions/artifacts/${artifact.id}/zip`)).arrayBuffer());
  if (`sha256:${sha256(archive)}` !== artifact.digest) throw new Error("Replay artifact digest mismatch");
  const runID = `${env.GITHUB_RUN_ID}-${env.EVAL_DATASET}`;
  const directory = path.join(env.RUNNER_TEMP, `dense-mem-eval-${runID}`);
  fs.mkdirSync(directory, { mode: 0o700 });
  const zip = path.join(directory, "numeric.zip"); fs.writeFileSync(zip, archive, { mode: 0o600 });
  const extracted = spawnSync("unzip", ["-p", zip, "scores.json"], { encoding: "utf8", maxBuffer: 32 << 20 });
  if (extracted.status !== 0) throw new Error("Replay numeric artifact is invalid");
  const measured = JSON.parse(extracted.stdout);
  if (measured.identity.source_revision !== env.EVAL_SOURCE_REVISION || measured.identity.image !== env.EVAL_IMAGE || measured.identity.workflow_run_id !== String(original.id) || measured.identity.dataset !== env.EVAL_DATASET) throw new Error("Replay measured identity mismatch");
  const jobs = [];
  for (let page = 1;; page++) { const batch = (await (await api(`/actions/runs/${original.id}/jobs?filter=latest&per_page=100&page=${page}`)).json()).jobs; jobs.push(...batch); if (batch.length < 100) break; }
  const selected = jobs.filter((job) => job.name === `Evaluate ${env.EVAL_DATASET}` || job.name.endsWith(` / Evaluate ${env.EVAL_DATASET}`));
  if (selected.length !== 1) throw new Error("Replay unavailable: dataset job logs cannot be identified");
  const logs = await api(`/actions/jobs/${selected[0].id}/logs`);
  const logsDir = path.join(directory, "logs"); fs.mkdirSync(logsDir, { mode: 0o700 });
  await pipeline(Readable.fromWeb(logs.body), fs.createWriteStream(path.join(logsDir, "dataset.txt"), { flags: "wx", mode: 0o600 }));
  const publicRegistry = fs.readFileSync("tests/eval/datasets.json"); const fixtureRegistry = fs.readFileSync("tests/eval/fixtures/datasets.json");
  const fixture = measured.identity.registry_sha256 === sha256(fixtureRegistry);
  const registryBytes = fixture ? fixtureRegistry : publicRegistry;
  if (sha256(registryBytes) !== measured.identity.registry_sha256) throw new Error("Replay registry differs from pinned campaign");
  const registry = JSON.parse(registryBytes); const dataset = registry.datasets.find((row) => row.id === env.EVAL_DATASET);
  if (!dataset) throw new Error("Replay dataset registration is missing");
  const dataDir = path.join(directory, "data"); fs.mkdirSync(dataDir, { mode: 0o700 });
  for (const source of [dataset.corpus, dataset.questions]) {
    const bytes = fixture ? fs.readFileSync(path.join("tests/eval/fixtures", source.path)) : Buffer.from(await (await fetch(`${registry.base_url}/${registry.revision}/${source.path}`, { signal: AbortSignal.timeout(120_000) })).arrayBuffer());
    if (sha256(bytes) !== source.sha256) throw new Error("Replay public source digest mismatch");
    fs.writeFileSync(path.join(dataDir, source.path), bytes, { flag: "wx", mode: 0o600 });
  }
  const outputDir = path.join(directory, "public");
  const identity = measured.identity;
  const manifest = { version: 1, run_id: identity.run_id, workflow_run_id: env.GITHUB_RUN_ID, source_revision: identity.source_revision, image: identity.image, registry_sha256: identity.registry_sha256, dataset: identity.dataset, mode: "replay", model: identity.model, embedding_model: identity.embedding_model, embedding_dimensions: identity.embedding_dimensions, context_window_tokens: identity.context_window_tokens, model_deadline: new Date(0).toISOString(), data_dir: dataDir, output_dir: outputDir };
  const manifestPath = path.join(directory, "manifest.json"); fs.writeFileSync(manifestPath, JSON.stringify(manifest), { mode: 0o600 });
  const result = spawnSync(env.EVAL_RUNNER_BINARY, ["report", manifestPath], { env: { ...env, EVAL_LOGS_DIR: logsDir }, stdio: "inherit" });
  if (result.status !== 0) throw new Error("Replay unavailable: log reconstruction failed");
  const replayed = JSON.parse(fs.readFileSync(path.join(outputDir, "scores.json")));
  if (JSON.stringify(replayed) !== JSON.stringify(measured)) throw new Error("Runtime-free replay differs from measured scores");
  process.stdout.write(`Replay verified ${identity.dataset}: ${replayed.completed_answers}/${replayed.expected_answers} answers; no runtime or provider calls.\n`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  main().catch(() => { process.stderr.write("Replay unavailable or unverifiable; retained logs, source identity, and numeric artifact are required.\n"); process.exitCode = 1; });
}
