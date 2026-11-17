import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { requireRunner, registryMatrix, sha256 } from "../../../.github/scripts/evaluation-policy.mjs";
import { seedBuildRequest, seedReference, resolveSeedImage, extractSeed, verifySeedFiles, dumpSeed, sealSeed, publishSeed, verifySeedCredentialExclusions, postgresImage } from "../../../.github/scripts/evaluation-seeds.mjs";
import { setup, cleanup, docker, location, runBinary } from "./workflow.mjs";
import { seedValidationClient, seedValidationState } from "../../../.github/scripts/seed-validation.mjs";
import { setTimeout as delay } from "node:timers/promises";

const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
const save = (file, value) => fs.writeFileSync(file, JSON.stringify(value, null, 2) + "\n", { mode: 0o600 });
const notice = () => `# Public evaluation seed\n\nSource: osunlp/HippoRAG_2 at 5ec05b38deecc3318bb432c69865959c56058990.\n\n${fs.readFileSync("tests/eval/source_locks/hipporag_2.json", "utf8")}\n\nHotpotQA attribution: https://github.com/hotpotqa/hotpot (CC BY-SA 4.0).\nMuSiQue attribution: https://github.com/StonyBrookNLP/musique (CC BY 4.0).\nOther dataset-specific upstream attribution and licensing limitations are recorded above.\nThe application Apache-2.0 license is not asserted for these corpora.\n`;

function protectedValues(state) {
  return ["EVAL_DATABASE_PASSWORD", "EVAL_API_KEY", "EVAL_CONTROL_TOKEN", "DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY", "DENSE_MEM_EVAL_VERIFIER_URL", "DENSE_MEM_EVAL_EMBED_URL"].map((name) => state.environment[name]).filter(Boolean);
}

async function roundTrip(runtime, env, operation, signal) {
  const key = runtime.state.environment.EVAL_API_KEY;
  const secrets = protectedValues(runtime.state);
  await cleanup(env);
  const restored = await setup(env, { operation, seedDirectory: runtime.seedDir, seedImage: "", apiKey: key });
  await verifySeedCredentialExclusions(docker, `densemem-eval-${restored.paths.runID}-postgres`, runtime.seedDir, secrets);
  await runBinary(operation, restored, env, signal);
  return restored;
}

export async function startSeedValidation(env, github = seedValidationClient(env)) {
  const proof = JSON.parse(env.EVAL_VALIDATION);
  if (proof.source_revision !== env.EVAL_SOURCE_REVISION || (!proof.main_revision && proof.image !== env.EVAL_IMAGE)) throw new Error("Seed runtime differs from validation identity");
  const controller = new AbortController();
  let failure, pendingCheck;
  const check = () => {
    pendingCheck ||= seedValidationState(github, proof).catch((error) => { failure = error; controller.abort(); throw error; }).finally(() => { pendingCheck = undefined; });
    return pendingCheck;
  };
  await check();
  const timer = setInterval(() => {
    check().catch(() => {});
  }, 60_000);
  return {
    signal: controller.signal,
    beforePublication: async () => {
      for (;;) {
        if (failure) throw failure;
        if (await check() === "success") return;
        if (Date.now() >= Number(env.EVAL_MODEL_DEADLINE_EPOCH) * 1000) throw new Error("Seed publication validation did not complete before the deadline");
        process.stdout.write('Waiting for successful current-head CI/E2E before seed publication.\n');
        await delay(60_000, undefined, { signal: controller.signal });
      }
    },
    finish: async () => {
      clearInterval(timer);
      await pendingCheck;
      if (failure) throw failure;
    },
  };
}

export async function main(operation, env = process.env) {
  requireRunner(env);
  if (env.EVAL_MODE !== "live") throw new Error("Seed workflow builds only complete public datasets");
  const registryBytes = fs.readFileSync("tests/eval/datasets.json");
  const registry = JSON.parse(registryBytes);
  if (operation === "matrix") {
    const selected = seedBuildRequest(registry, env.INPUT_DATASET, env.INPUT_WORKERS);
    fs.appendFileSync(env.GITHUB_OUTPUT, `matrix=${JSON.stringify({ dataset: selected.dataset })}\nworkers=${selected.workers}\n`);
    return;
  }
  if (operation === "pin") {
    const images = {};
    for (const dataset of registryMatrix(registry).dataset) {
      const image = await resolveSeedImage(seedReference(dataset, env.EVAL_SEED_VERSION || "latest"), env);
      if (!image) throw new Error(`Public seed is unavailable for ${dataset}`);
      const directory = path.join(env.RUNNER_TEMP, `seed-pin-${env.GITHUB_RUN_ID}-${dataset}`);
      extractSeed(docker, image, directory, `seed-pin-${env.GITHUB_RUN_ID}-${dataset}`, `${env.GITHUB_RUN_ID}-${dataset}`);
      await verifySeedFiles(directory, dataset, sha256(registryBytes), registry.datasets.find((row) => row.id === dataset).corpus.rows);
      images[dataset] = image;
    }
    fs.appendFileSync(env.GITHUB_OUTPUT, `images=${JSON.stringify(images)}\n`);
    return;
  }
  if (operation === "cleanup") return cleanup(env);
  if (operation !== "bootstrap" && operation !== "build") throw new Error("Unsupported seed workflow operation");
  const validation = await startSeedValidation(env);
  try {
    if (operation === "bootstrap") {
      const existing = await resolveSeedImage(seedReference("bootstrap"), env);
      let runtime = await setup(env, { operation: "bootstrap", seedImage: existing || "" });
      await runBinary("bootstrap", runtime, env, validation.signal);
      if (existing) {
        fs.appendFileSync(env.GITHUB_OUTPUT, `image=${existing}\n`);
        return;
      }
      const scope = read(path.join(runtime.seedDir, "scope.json"));
      const schema = Number(docker(["exec", `densemem-eval-${runtime.paths.runID}-postgres`, "psql", "--username=densemem", "--dbname=densemem", "--tuples-only", "--no-align", "--command", "SELECT COALESCE(max(version_id),0) FROM (SELECT DISTINCT ON (version_id) version_id,is_applied FROM goose_db_version ORDER BY version_id,id DESC) latest WHERE is_applied"]));
      save(path.join(runtime.seedDir, "seed.json"), { version: 1, dataset: "bootstrap", registry_sha256: runtime.manifest.registry_sha256, base_release: runtime.manifest.base_release, source_revision: env.EVAL_SOURCE_REVISION, scope, corpus_rows: 0, canonical_sha256: "", search_sha256: "", mapping_sha256: "", dump_sha256: "", dump_bytes: 0, schema_version: schema, postgres_image: postgresImage, model: runtime.manifest.model, context_window_tokens: runtime.manifest.context_window_tokens, embedding_model: "text-embedding-3-small", embedding_dimensions: 1536, import_workers: runtime.manifest.import_workers, elapsed_ms: 0 });
      dumpSeed(docker, `densemem-eval-${runtime.paths.runID}-postgres`, runtime.seedDir);
      await sealSeed(runtime.seedDir);
      await roundTrip(runtime, env, "bootstrap", validation.signal);
      fs.writeFileSync(path.join(runtime.seedDir, "NOTICE.md"), notice());
      const image = await publishSeed(docker, runtime.seedDir, "bootstrap", env.EVAL_SEED_VERSION, env, validation.beforePublication);
      fs.appendFileSync(env.GITHUB_OUTPUT, `image=${image}\n`);
      return;
    }
    const runtime = await setup(env, { operation: "prepare" });
    await runBinary("prepare", runtime, env, validation.signal);
    dumpSeed(docker, `densemem-eval-${runtime.paths.runID}-postgres`, runtime.seedDir);
    const seed = await sealSeed(runtime.seedDir);
    fs.writeFileSync(path.join(runtime.seedDir, "NOTICE.md"), notice());
    await roundTrip(runtime, env, "verify-seed", validation.signal);
    const image = await publishSeed(docker, runtime.seedDir, env.EVAL_DATASET, env.EVAL_SEED_VERSION, env, validation.beforePublication);
    const receipt = { dataset: env.EVAL_DATASET, image, corpus_rows: seed.corpus_rows, canonical_sha256: seed.canonical_sha256, workers: seed.import_workers, dump_bytes: seed.dump_bytes, preparation_ms: seed.elapsed_ms, base_release: seed.base_release };
    save(path.join(location(env).directory, "public/seed-build.json"), receipt);
    fs.appendFileSync(env.GITHUB_STEP_SUMMARY, `Seed ${receipt.dataset}: ${receipt.corpus_rows} corpus rows, ${receipt.workers} workers, ${receipt.dump_bytes} compressed bytes.\n\nImage: ${image}\n`);
  } finally { await validation.finish(); }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  main(process.argv[2]).catch((error) => {
    process.exitCode = 1;
    try {
      requireRunner(process.env);
      const paths = location(process.env);
      if (fs.existsSync(paths.stateFile)) {
        if (read(paths.stateFile).runID !== paths.runID) throw new Error("Seed failure log ownership mismatch");
        save(path.join(paths.directory, "seed-failure.log"), { message: error.message, diagnostic: error.diagnostic });
      }
    } catch { process.stderr.write("Seed failure evidence could not be retained.\n"); }
    process.stderr.write("Seed workflow failed; inspect protected runner logs. No automatic rebuild was requested.\n");
  });
}
