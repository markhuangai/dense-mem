import fs from "node:fs";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { spawnSync } from "node:child_process";
import { isDeepStrictEqual, parseArgs } from "node:util";
import { fileURLToPath } from "node:url";
import { configuration, resolveEvaluationImage, requireEvaluationImportSupport, sha256 } from "../../../.github/scripts/evaluation-policy.mjs";
import { seedBuildRequest, seedBuildVersion, resolveSeedBuildSource, requireSeedBuildValidation, seedReference, resolveSeedImage, dumpSeed, sealSeed, buildBootableSeed, publishSeed, lockLocalSeedFile, verifySeedCredentialExclusions, verifySeedFiles, hashFile, postgresImage } from "../../../.github/scripts/evaluation-seeds.mjs";
import { setup, cleanup, diagnostics, docker, location, runBinary, seedSecrets, saveRuntimeJSON as save } from "./workflow.mjs";

const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
const notice = () => `# Public evaluation seed\n\nSource: osunlp/HippoRAG_2 at 5ec05b38deecc3318bb432c69865959c56058990.\n\n${fs.readFileSync("tests/eval/source_locks/hipporag_2.json", "utf8")}\n\nHotpotQA attribution: https://github.com/hotpotqa/hotpot (CC BY-SA 4.0).\nMuSiQue attribution: https://github.com/StonyBrookNLP/musique (CC BY 4.0).\nOther dataset-specific upstream attribution and licensing limitations are recorded above.\nThe application Apache-2.0 license is not asserted for these corpora.\n`;
const localSettings = ["DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY", "DENSE_MEM_EVAL_VERIFIER_URL", "DENSE_MEM_EVAL_TEAM_API_KEY", "DENSE_MEM_EVAL_DATABASE_PASSWORD"];

export function localSeedRequest(args, registry) {
  const { values } = parseArgs({ args, options: { pr: { type: "string" }, dataset: { type: "string" }, workers: { type: "string" }, "skip-ai-security-check": { type: "boolean" }, resume: { type: "string" } } });
  if (values.resume !== undefined) {
    if (!/^local-[a-f0-9]{32}$/.test(values.resume) || Object.keys(values).length !== 1) throw new Error("Seed build --resume requires only the saved local build ID");
    return { resume: values.resume };
  }
  const pr = values.pr === undefined ? undefined : Number(values.pr);
  if (pr !== undefined && (!/^[1-9][0-9]*$/.test(values.pr) || !Number.isSafeInteger(pr))) throw new Error("Seed build --pr must be a valid pull request number");
  return { pr, ...seedBuildRequest(registry, values.dataset ?? "all", values.workers ?? 20), skipAISecurityReview: values["skip-ai-security-check"] ?? false };
}

export function lockSeedBuild(directory) {
  return lockLocalSeedFile(path.join(directory, "build.lock"), "Seed build is already running");
}

export function savedSeedRequest(receipt, id, registry, registryHash) {
  if (receipt?.resume_version !== 1 || receipt.build_id !== id || receipt.registry_sha256 !== registryHash ||
      (receipt.pr !== null && (!Number.isSafeInteger(receipt.pr) || receipt.pr < 1)) ||
      !Array.isArray(receipt.datasets) || typeof receipt.skip_ai_security_review !== "boolean") {
    throw new Error("Seed build checkpoint is missing or incompatible with the current registry");
  }
  seedReference("bootstrap", receipt.version);
  const { dataset, workers } = seedBuildRequest(registry, receipt.datasets.length === 1 ? receipt.datasets[0] : "all", receipt.workers);
  if (!isDeepStrictEqual(dataset, receipt.datasets)) throw new Error("Seed build checkpoint dataset selection is invalid");
  return { pr: receipt.pr ?? undefined, dataset, workers, skipAISecurityReview: receipt.skip_ai_security_review };
}

function command(binary, args) {
  const result = spawnSync(binary, args, { encoding: "utf8", timeout: 60_000, maxBuffer: 16 << 20 });
  if (result.status !== 0) throw new Error(`${binary} seed preflight failed`);
  return result.stdout.trim();
}

const github = { request: async (route, query = {}) => {
  return { data: JSON.parse(command("gh", ["api", "--hostname", "github.com", "--method", "GET", route.replace(/^GET /, ""), ...Object.entries(query).flatMap(([k, v]) => ["-f", `${k}=${v}`])])) };
} };

function buildRunner(directory, sourceRevision) {
  const version = /^toolchain go(\d+\.\d+\.\d+)$/m.exec(fs.readFileSync("go.mod", "utf8"))?.[1];
  if (!version) throw new Error("Go toolchain pin is unavailable");
  const archive = path.join(directory, "source.tar");
  command("git", ["archive", "--format=tar", `--output=${archive}`, sourceRevision]);
  const uid = `${process.getuid()}:${process.getgid()}`;
  docker(["run", "--rm", "--user", uid, "--platform=linux/amd64", "--env", "GOTOOLCHAIN=local", "--env", "GOCACHE=/tmp/build-cache", "--env", "GOPATH=/tmp/go", "--env", "CGO_ENABLED=0",
    "--mount", `type=bind,source=${archive},target=/source.tar,readonly`, "--mount", `type=bind,source=${directory},target=/build`, `golang:${version}-bookworm`, "sh", "-ec",
    "mkdir /tmp/source; tar -xf /source.tar -C /tmp/source; cd /tmp/source; go build -trimpath -o /build/eval-runner ./cmd/eval-runner"], { timeout: 20 * 60_000 });
  fs.unlinkSync(archive);
  return path.join(directory, "eval-runner");
}

async function roundTrip(runtime, env, operation, signal, { intakeProof, dataDirectory, resumable = false, bootableImage = "" } = {}) {
  const key = runtime.state.environment.EVAL_API_KEY;
  const secrets = seedSecrets(runtime.state);
  const verificationEnv = resumable ? { ...env, EVAL_LOCAL_SEED_ID: `local-${sha256(env.EVAL_LOCAL_SEED_ID + ":verify").slice(0, 32)}` } : env;
  await cleanup(env, { retainVolume: resumable });
  signal.throwIfAborted();
  const verify = async () => {
    const restored = await setup(verificationEnv, { operation, seedDirectory: bootableImage ? undefined : runtime.seedDir, seedImage: bootableImage, apiKey: key, signal, intakeProof, dataDirectory });
    if (!bootableImage) await verifySeedCredentialExclusions(docker, `densemem-eval-${restored.paths.runID}-postgres`, runtime.seedDir, secrets);
    await runBinary(operation, restored, verificationEnv, signal);
  };
  if (resumable) {
    await cleanup(verificationEnv);
    await withOwnedRuntime(verificationEnv, verify);
  } else await verify();
}

export async function withOwnedRuntime(env, action, { resumable = false } = {}) {
  let failed = false;
  try {
    return await action();
  } catch (error) {
    failed = true;
    try {
      const paths = location(env);
      if (fs.existsSync(paths.stateFile) && read(paths.stateFile).runID === paths.runID) {
        save(path.join(paths.directory, "seed-failure.log"), { message: error.message, diagnostic: error.diagnostic });
        await diagnostics(env);
      }
    } catch { process.stderr.write("Protected failure capture is incomplete; private evidence was retained where available.\n"); }
    throw error;
  } finally {
    try {
      const paths = location(env);
      const retainVolume = failed && resumable && fs.existsSync(paths.checkpointFile);
      await cleanup(env, { retainVolume });
      if (retainVolume) process.stdout.write(`Resume the saved seed build with --resume ${paths.seedBuildID}.\n`);
    } catch (error) {
      if (!failed) throw error;
      process.stderr.write("Owned seed runtime cleanup was incomplete. Private runtime receipt retained.\n");
    }
  }
}

const verifiedExportFile = (paths) => path.join(paths.directory, "verified-export.json");

export async function verifiedSeedExport(paths, build, prepare) {
  const verified = verifiedExportFile(paths);
  const { dataset } = paths;
  if (fs.existsSync(verified)) {
    const saved = read(verified);
    if (!/^seed-export-[A-Za-z0-9]{6}$/.test(saved.directory) || saved.version !== build.version) throw new Error("Seed build verified export receipt is invalid");
    const directory = path.join(paths.directory, saved.directory);
    const seed = await verifySeedFiles(directory, dataset, build.registry_sha256, dataset === "bootstrap" ? 0 : build.rows[dataset]);
    if (await hashFile(path.join(directory, "seed.json")) !== saved.metadata_sha256) throw new Error("Seed build verified export metadata changed");
    if (dataset !== "bootstrap" && !/^sha256:[a-f0-9]{64}$/.test(saved.bootable_image || "")) throw new Error("Seed build has no verified bootable image receipt");
    return { directory, seed, bootableImage: saved.bootable_image, previousLatest: saved.previous_latest, reused: true };
  }
  fs.mkdirSync(paths.directory, { recursive: true, mode: 0o700 });
  const directory = fs.mkdtempSync(path.join(paths.directory, "seed-export-"));
  const { seed, bootableImage } = await prepare(directory);
  if (dataset !== "bootstrap" && !/^sha256:[a-f0-9]{64}$/.test(bootableImage || "")) throw new Error("Seed build has no verified bootable image receipt");
  const previousLatest = dataset === "bootstrap" ? null : await resolveSeedImage(seedReference(dataset), {});
  save(verified, { directory: path.basename(directory), version: build.version, metadata_sha256: await hashFile(path.join(directory, "seed.json")), bootable_image: bootableImage, previous_latest: previousLatest });
  return { directory, seed, bootableImage, previousLatest, reused: false };
}

async function bootstrap(env, build, signal, beforePublication) {
  return withOwnedRuntime(env, async () => {
    await cleanup(env);
    const paths = location(env);
    const existing = build.bootstrap_image || (!fs.existsSync(verifiedExportFile(paths)) && await resolveSeedImage(seedReference("bootstrap"), {}));
    signal.throwIfAborted();
    if (existing) {
      const runtime = await setup(env, { operation: "bootstrap", seedImage: existing, signal });
      await runBinary("bootstrap", runtime, env, signal);
      return existing;
    }
    const { directory, previousLatest, reused } = await verifiedSeedExport(paths, build, async (exportDirectory) => {
      const runtime = await setup(env, { operation: "bootstrap", seedImage: "", signal, exportDirectory });
      await runBinary("bootstrap", runtime, env, signal);
      const scope = read(path.join(runtime.seedDir, "scope.json"));
      const container = `densemem-eval-${runtime.paths.runID}-postgres`;
      const schema = Number(docker(["exec", container, "psql", "--username=densemem", "--dbname=densemem", "--tuples-only", "--no-align", "--command", "SELECT COALESCE(max(version_id),0) FROM (SELECT DISTINCT ON (version_id) version_id,is_applied FROM goose_db_version ORDER BY version_id,id DESC) latest WHERE is_applied"]));
      save(path.join(runtime.seedDir, "seed.json"), { version: 1, dataset: "bootstrap", registry_sha256: runtime.manifest.registry_sha256, base_release: runtime.manifest.base_release, source_revision: env.EVAL_SOURCE_REVISION, scope, corpus_rows: 0, canonical_sha256: "", search_sha256: "", mapping_sha256: "", dump_sha256: "", dump_bytes: 0, schema_version: schema, postgres_image: postgresImage, model: runtime.manifest.model, context_window_tokens: runtime.manifest.context_window_tokens, embedding_model: "text-embedding-3-small", embedding_dimensions: 1536, import_workers: runtime.manifest.import_workers, elapsed_ms: 0 });
      dumpSeed(docker, container, runtime.seedDir);
      const seed = await sealSeed(runtime.seedDir);
      await roundTrip(runtime, env, "bootstrap", signal);
      fs.writeFileSync(path.join(runtime.seedDir, "NOTICE.md"), notice());
      return { seed };
    });
    if (reused) {
      const runtime = await setup(env, { operation: "bootstrap", seedImage: "", seedDirectory: directory, signal });
      await runBinary("bootstrap", runtime, env, signal);
    }
    return publishSeed(docker, directory, "bootstrap", build.version, {}, beforePublication, { runID: paths.runID, previousLatest }, { github });
  });
}

export async function prepareSeedExport(env, signal, options) {
  const runtime = await setup(env, { ...options, operation: "prepare", signal });
  await runBinary("prepare", runtime, env, signal);
  const postgres = `densemem-eval-${runtime.paths.runID}-postgres`;
  dumpSeed(docker, postgres, runtime.seedDir);
  const seed = await sealSeed(runtime.seedDir);
  fs.writeFileSync(path.join(runtime.seedDir, "NOTICE.md"), notice());
  const bootableImage = options.intakeProof ? "" : await buildBootableSeed(docker, postgres, runtime.seedDir, seedSecrets(runtime.state));
  await roundTrip(runtime, env, "verify-seed", signal, { ...options, bootableImage });
  return { runtime, seed, bootableImage };
}

async function buildDataset(env, build, signal, beforePublication, skipAISecurityReview) {
  const paths = location(env);
  const version = build.version;
  const dataset = paths.dataset;
  const finished = path.join(paths.directory, "public/seed-build.json");
  if (fs.existsSync(finished)) {
    const receipt = read(finished);
    const published = await resolveSeedImage(seedReference(dataset, version), {});
    if (!published || receipt.image !== published || receipt.version !== version || receipt.dataset !== dataset || receipt.source_revision !== build.source_revision) {
      throw new Error("Seed build completed dataset receipt differs from its dated image");
    }
    await cleanup(env);
    process.stdout.write(`${JSON.stringify({ event: "seed_already_published", ...receipt })}\n`);
    return receipt;
  }
  return withOwnedRuntime(env, async () => {
    const checkpoint = fs.existsSync(paths.checkpointFile);
    await cleanup(env, { retainVolume: checkpoint });
    const { directory, seed, bootableImage, previousLatest } = await verifiedSeedExport(paths, build, async (exportDirectory) =>
      prepareSeedExport(env, signal, { skipAISecurityReview, resumable: true, exportDirectory, dataDirectory: path.join(paths.directory, "data") }));
    if (seed.source_revision !== build.source_revision || seed.base_release !== env.EVAL_BASE_RELEASE || seed.import_workers !== build.workers ||
        (seed.ai_security_review === "skipped") !== skipAISecurityReview) throw new Error("Seed build verified export differs from the pinned build settings");
    const image = await publishSeed(docker, directory, dataset, version, {}, beforePublication, { runID: paths.runID, previousLatest, bootableImage }, { github });
    const receipt = { dataset, image, version, corpus_rows: seed.corpus_rows, canonical_sha256: seed.canonical_sha256, workers: seed.import_workers, dump_bytes: seed.dump_bytes, preparation_ms: seed.elapsed_ms, base_release: seed.base_release, source_revision: seed.source_revision, ai_security_review: seed.ai_security_review || "ordinary" };
    save(finished, receipt);
    process.stdout.write(`${JSON.stringify({ event: "seed_published", ...receipt })}\n`);
    return receipt;
  }, { resumable: true });
}

export function localSeedEnvironment(settings, workers) {
  if (settings.GITHUB_ACTIONS === "true" || process.platform !== "linux" || process.arch !== "x64") throw new Error("Public seed creation requires a local Linux amd64 Docker host");
  const missing = localSettings.filter((name) => !settings[name]);
  if (missing.length) throw new Error(`Missing local seed settings: ${missing.join(", ")}`);
  if (localSettings.some((name) => /[\r\n]/.test(settings[name]))) throw new Error("Invalid private runtime configuration");
  const env = { ...settings, EVAL_MODE: "live", DENSE_MEM_EVAL_MODEL: "dense-mem-testing", DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS: "500000", DENSE_MEM_EVAL_EMBED_URL: "https://api.openai.com/v1", WORKFLOW_EMBEDDING_MODEL: "text-embedding-3-small", WORKFLOW_EMBEDDING_DIMENSIONS: "1536", DENSE_MEM_EVAL_IMPORT_WORKERS: String(workers) };
  configuration(env, { allowHTTPVerifier: true });
  return env;
}

export async function main(args, settings = process.env) {
  if (args[0] === "prepare-topics") return prepareTopicDerivatives(args.slice(1), settings);
  const registryBytes = fs.readFileSync("tests/eval/datasets.json");
  const registry = JSON.parse(registryBytes);
  const registryHash = sha256(registryBytes);
  const selected = localSeedRequest(args, registry);
  const id = selected.resume ?? `local-${randomBytes(16).toString("hex")}`;
  const root = path.resolve("tmp/eval-seeds");
  const directory = path.join(root, id);
  if (selected.resume) {
    const info = fs.lstatSync(directory, { throwIfNoEntry: false });
    if (!info?.isDirectory() || info.uid !== process.getuid() || (info.mode & 0o077) !== 0) throw new Error("Seed build checkpoint directory is missing or is not private");
  } else {
    fs.mkdirSync(root, { recursive: true, mode: 0o700 });
    fs.mkdirSync(directory, { mode: 0o700 });
  }
  const unlock = lockSeedBuild(directory);
  try {
    const receipt = path.join(directory, "source.json");
    if (selected.resume && !fs.existsSync(receipt)) throw new Error("Seed build checkpoint has no source receipt");
    const saved = selected.resume ? read(receipt) : null;
    const request = selected.resume ? savedSeedRequest(saved, id, registry, registryHash) : selected;
    const rows = Object.fromEntries(registry.datasets.map((dataset) => [dataset.id, dataset.corpus.rows]));
    return await runSeedBuild({ id, directory, request, registryHash, rows, saved, receipt }, settings);
  } finally { unlock(); }
}

async function runSeedBuild({ id, directory, request, registryHash, rows, saved, receipt }, settings) {
  const env = localSeedEnvironment(settings, request.workers);
  if (command("git", ["status", "--porcelain", "--untracked-files=no"])) throw new Error("Seed building requires a clean tracked checkout of the tested PR head");
  const source = await resolveSeedBuildSource(github, request.pr, command("git", ["rev-parse", "HEAD"]));
  const { data: pkg } = await github.request("GET /users/markhuangai/packages/container/dense-mem-eval-postgres");
  if (pkg.visibility !== "public") throw new Error("Seed publication requires a public dense-mem-eval-postgres package");
  try {
    command("docker", ["login", "ghcr.io"]);
  } catch {
    throw new Error("Seed publication requires a working Docker login to ghcr.io with write:packages access");
  }
  docker(["pull", "--quiet", "--platform=linux/amd64", source.image]);
  const inspected = JSON.parse(docker(["image", "inspect", source.image]))[0];
  requireEvaluationImportSupport(inspected, { skipAISecurityReview: request.skipAISecurityReview });
  const image = resolveEvaluationImage(source.image, "markhuangai/dense-mem", inspected);
  if (image.revision !== source.sourceRevision) throw new Error("Local seed runtime differs from the validated source revision");
  if (saved && (saved.image !== image.image || saved.source_revision !== source.sourceRevision || saved.checkout_revision !== source.checkoutRevision || !saved.version.endsWith("-" + source.baseRelease))) {
    throw new Error("Seed build checkpoint source or trusted image changed");
  }
  const version = saved?.version ?? seedBuildVersion(source.baseRelease);
  const build = { resume_version: 1, build_id: id, registry_sha256: registryHash, pr: request.pr ?? null, image: image.image,
    source_revision: source.sourceRevision, checkout_revision: source.checkoutRevision, version, validation: source.validation,
    workers: request.workers, datasets: request.dataset, skip_ai_security_review: request.skipAISecurityReview, bootstrap_image: saved?.bootstrap_image };
  save(receipt, build);
  process.stdout.write(`Seed build receipts: ${path.relative(process.cwd(), directory)}\n`);
  let binary;
  try {
    binary = buildRunner(directory, source.checkoutRevision);
  } catch (error) {
    save(path.join(directory, "build-failure.json"), { message: error.message, diagnostic: error.diagnostic });
    throw new Error("Could not build the seed helper in Docker. Inspect the private build-failure.json receipt");
  }
  Object.assign(env, { EVAL_LOCAL_SEED_ID: id, EVAL_LOCAL_SEED_ROOT: directory, EVAL_SOURCE_REVISION: source.sourceRevision, EVAL_IMAGE: image.image, EVAL_BASE_RELEASE: source.baseRelease, EVAL_RUNNER_BINARY: binary });
  const controller = new AbortController();
  const stop = () => controller.abort();
  for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, stop);
  const beforePublication = async () => {
    controller.signal.throwIfAborted();
    await requireSeedBuildValidation(github, source.validation);
    controller.signal.throwIfAborted();
  };
  const datasetEnv = (dataset, seedImage = "") => ({ ...env, EVAL_DATASET: dataset, EVAL_SEED_IMAGE: seedImage, EVAL_MODEL_DEADLINE_EPOCH: String(Math.floor(Date.now() / 1000) + 330 * 60) });
  try {
    await beforePublication();
    const bootstrapImage = await bootstrap(datasetEnv("bootstrap"), build, controller.signal, beforePublication);
    build.bootstrap_image = bootstrapImage;
    save(receipt, build);
    const receipts = [];
    for (const dataset of request.dataset) {
      await beforePublication();
      receipts.push(await buildDataset(datasetEnv(dataset, bootstrapImage), { ...build, rows }, controller.signal, beforePublication, request.skipAISecurityReview));
    }
    return receipts;
  } finally {
    for (const signal of ["SIGINT", "SIGTERM"]) process.removeListener(signal, stop);
  }
}

export async function prepareTopicDerivatives(args, settings) {
  const { values } = parseArgs({ args, options: {
    pr: { type: "string" }, lock: { type: "string" }, resume: { type: "string" },
  } });
  const registry = read("tests/eval/datasets.json");
  const pr = Number(values.pr);
  if (!Number.isSafeInteger(pr) || pr < 1 || !values.lock) throw new Error("Seed build requires --pr and a pinned source --lock");
  if (values.resume && !/^local-[a-f0-9]{32}$/.test(values.resume)) throw new Error("Seed build resume ID is invalid");
  const frozen = read(values.lock);
  const checkout = command("git", ["rev-parse", "HEAD"]);
  const source = await resolveSeedBuildSource(github, pr, checkout);
  const env = localSeedEnvironment(settings, 8);
  const imagePattern = /^ghcr\.io\/markhuangai\/dense-mem@sha256:[a-f0-9]{64}$/;
  const seedPattern = /^ghcr\.io\/markhuangai\/dense-mem-eval-postgres@sha256:[a-f0-9]{64}$/;
  if (!imagePattern.test(frozen.baseline_image) || !imagePattern.test(frozen.candidate_image) ||
      Object.keys(frozen.seeds ?? {}).length !== registry.datasets.length ||
      registry.datasets.some((dataset) => !seedPattern.test(frozen.seeds[dataset.id]))) {
    throw new Error("Seed build lock must pin both application images and all five parent seed digests");
  }
  const images = {};
  for (const arm of ["baseline", "candidate"]) {
    const ref = frozen[arm + "_image"];
    docker(["pull", "--quiet", "--platform=linux/amd64", ref]);
    images[arm] = resolveEvaluationImage(ref, "markhuangai/dense-mem", JSON.parse(docker(["image", "inspect", ref]))[0]);
  }
  if (images.candidate.revision !== source.sourceRevision || images.candidate.image !== source.image) {
    throw new Error("Seed build candidate does not match the approved PR image");
  }
  if (!/^[a-f0-9]{40}$/.test(frozen.baseline_revision ?? "") || images.baseline.revision !== frozen.baseline_revision) {
    throw new Error("Seed build baseline does not match the pinned revision");
  }
  const id = values.resume ?? `local-${randomBytes(16).toString("hex")}`;
  const directory = path.resolve("tmp/eval-seeds", id);
  if (!values.resume) fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  const stat = fs.lstatSync(directory);
  if (!stat.isDirectory() || stat.uid !== process.getuid() || (stat.mode & 0o077) !== 0) throw new Error("Seed build state must be private");
  const unlock = lockSeedBuild(directory);
  const controller = new AbortController();
  const stop = () => controller.abort();
  for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, stop);
  try {
    const lockFile = path.join(directory, "topic-source.json");
    if (values.resume ? !isDeepStrictEqual(read(lockFile), frozen) : fs.existsSync(lockFile)) throw new Error("Seed build immutable lock differs");
    if (!values.resume) save(lockFile, frozen);
    const binary = path.join(directory, "eval-runner");
    if (!fs.existsSync(binary)) buildRunner(directory, checkout);
    process.stdout.write(`Topic seed preparation ID: ${id}\n`);
    const completed = {};
    for (const dataset of registry.datasets) {
      const parent = frozen.seeds[dataset.id];
      completed[dataset.id] = { parent };
      for (const arm of ["baseline", "candidate"]) {
        controller.signal.throwIfAborted();
        await requireSeedBuildValidation(github, source.validation);
        const runID = `local-${sha256(id + ":" + dataset.id + ":" + arm).slice(0, 32)}`;
        const work = path.join(directory, dataset.id, arm);
        fs.mkdirSync(work, { recursive: true, mode: 0o700 });
        const worker = {
          ...env, EVAL_LOCAL_SEED_ID: runID, EVAL_LOCAL_SEED_ROOT: work,
          EVAL_SOURCE_REVISION: images[arm].revision, EVAL_IMAGE: images[arm].image,
          EVAL_BASE_RELEASE: source.baseRelease, EVAL_DATASET: dataset.id, EVAL_SEED_IMAGE: parent,
          EVAL_RUNNER_BINARY: binary, EVAL_PAIR_ARM: arm, EVAL_PARENT_SEED_IMAGE: parent,
          EVAL_MODEL_DEADLINE_EPOCH: String(Math.floor(Date.now() / 1000) + 330 * 60),
        };
        completed[dataset.id][arm] = await prepareTopicDerivative(worker, work, controller.signal, () => requireSeedBuildValidation(github, source.validation));
      }
      // A partial numeric receipt can be inspected without qualifying the paired run.
      save(path.join(directory, "paired-seed-lock.json"), completed);
    }
    return completed;
  } finally {
    for (const signal of ["SIGINT", "SIGTERM"]) process.removeListener(signal, stop);
    unlock();
  }
}

async function prepareTopicDerivative(env, work, signal, verifyPublication) {
  const identity = sha256(JSON.stringify([env.EVAL_DATASET, env.EVAL_PAIR_ARM, env.EVAL_IMAGE, env.EVAL_PARENT_SEED_IMAGE]));
  const repository = env.EVAL_PARENT_SEED_IMAGE.split("@")[0];
  const tag = `${repository}:${env.EVAL_DATASET}-prepared-${env.EVAL_PAIR_ARM}-${identity.slice(0, 24)}`;
  const receiptFile = path.join(work, "published.json");
  const published = await resolveSeedImage(tag, {});
  if (published) {
    if (!fs.existsSync(receiptFile) || read(receiptFile).digest !== published) throw new Error("Seed build derivative tag exists without a matching verified receipt");
    return published;
  }
  return withOwnedRuntime(env, async () => {
    const checkpoint = fs.existsSync(location(env).checkpointFile);
    await cleanup(env, { retainVolume: checkpoint });
    const exportDirectory = fs.mkdtempSync(path.join(work, "seed-export-"));
    const runtime = await setup(env, { operation: "prepare-topics", seedImage: env.EVAL_PARENT_SEED_IMAGE, resumable: true, exportDirectory, signal });
    await runBinary("prepare-topics", runtime, env, signal);
    const postgres = `densemem-eval-${runtime.paths.runID}-postgres`;
    dumpSeed(docker, postgres, runtime.seedDir);
    await sealSeed(runtime.seedDir);
    fs.writeFileSync(path.join(runtime.seedDir, "NOTICE.md"), notice());
    const image = await buildBootableSeed(docker, postgres, runtime.seedDir, seedSecrets(runtime.state));
    await roundTrip(runtime, env, "verify-seed", signal, { resumable: true, bootableImage: image });
    await verifyPublication();
    signal.throwIfAborted();
    docker(["image", "tag", image, tag]);
    docker(["image", "push", tag], { timeout: 20 * 60_000 });
    const digest = await resolveSeedImage(tag, {});
    if (!digest) throw new Error("Seed build derivative was not visible after publication");
    save(receiptFile, { digest, tag, identity });
    return digest;
  }, { resumable: true });
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  main(process.argv.slice(2)).catch((error) => {
    process.exitCode = 1;
    const message = error.message;
    if (/^(Public seed creation requires|Missing local seed settings:|Seed build |Seed building requires|Seed checkout |Seed PR image must be|Seed validation |Seed main CI |Seed publication requires|Invalid public seed dataset|Invalid Actions setting: DENSE_MEM_EVAL_|Invalid private runtime configuration|Local seed runtime differs|Evaluation requires a production image supporting locked public seed admission|Could not build the seed helper)/.test(message)) process.stderr.write(`${message}\n`);
    else process.stderr.write("Local seed build failed; inspect protected output or private evidence. No automatic rebuild was requested.\n");
  });
}
