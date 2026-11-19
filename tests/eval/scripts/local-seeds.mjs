import fs from "node:fs";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { spawnSync } from "node:child_process";
import { parseArgs } from "node:util";
import { fileURLToPath } from "node:url";
import { configuration, resolveEvaluationImage, requireEvaluationImportSupport } from "../../../.github/scripts/evaluation-policy.mjs";
import { seedBuildRequest, seedBuildVersion, resolveSeedBuildSource, requireSeedBuildValidation, seedReference, resolveSeedImage, dumpSeed, sealSeed, publishSeed, verifySeedCredentialExclusions, postgresImage } from "../../../.github/scripts/evaluation-seeds.mjs";
import { setup, cleanup, diagnostics, docker, location, runBinary } from "./workflow.mjs";

const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
const save = (file, value) => fs.writeFileSync(file, JSON.stringify(value, null, 2) + "\n", { mode: 0o600 });
const notice = () => `# Public evaluation seed\n\nSource: osunlp/HippoRAG_2 at 5ec05b38deecc3318bb432c69865959c56058990.\n\n${fs.readFileSync("tests/eval/source_locks/hipporag_2.json", "utf8")}\n\nHotpotQA attribution: https://github.com/hotpotqa/hotpot (CC BY-SA 4.0).\nMuSiQue attribution: https://github.com/StonyBrookNLP/musique (CC BY 4.0).\nOther dataset-specific upstream attribution and licensing limitations are recorded above.\nThe application Apache-2.0 license is not asserted for these corpora.\n`;
const localSettings = ["DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY", "DENSE_MEM_EVAL_VERIFIER_URL", "DENSE_MEM_EVAL_TEAM_API_KEY", "DENSE_MEM_EVAL_DATABASE_PASSWORD"];

export function localSeedRequest(args, registry) {
  const { values } = parseArgs({ args, options: { pr: { type: "string" }, dataset: { type: "string", default: "all" }, workers: { type: "string", default: "20" }, "skip-ai-security-check": { type: "boolean", default: false } } });
  const pr = values.pr === undefined ? undefined : Number(values.pr);
  if (pr !== undefined && (!/^[1-9][0-9]*$/.test(values.pr) || !Number.isSafeInteger(pr))) throw new Error("Seed build --pr must be a valid pull request number");
  return { pr, ...seedBuildRequest(registry, values.dataset, values.workers), skipAISecurityReview: values["skip-ai-security-check"] };
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

function protectedValues(state) {
  return ["EVAL_DATABASE_PASSWORD", "EVAL_API_KEY", "EVAL_CONTROL_TOKEN", "DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY", "DENSE_MEM_EVAL_VERIFIER_URL", "DENSE_MEM_EVAL_EMBED_URL"].map((name) => state.environment[name]).filter(Boolean);
}

async function roundTrip(runtime, env, operation, signal, { intakeProof, dataDirectory } = {}) {
  const key = runtime.state.environment.EVAL_API_KEY;
  const secrets = protectedValues(runtime.state);
  await cleanup(env);
  signal.throwIfAborted();
  const restored = await setup(env, { operation, seedDirectory: runtime.seedDir, seedImage: "", apiKey: key, signal, intakeProof, dataDirectory });
  await verifySeedCredentialExclusions(docker, `densemem-eval-${restored.paths.runID}-postgres`, runtime.seedDir, secrets);
  await runBinary(operation, restored, env, signal);
}

export async function withOwnedRuntime(env, action) {
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
      await cleanup(env);
    } catch (error) {
      if (!failed) throw error;
      process.stderr.write("Owned seed runtime cleanup was incomplete. Private runtime receipt retained.\n");
    }
  }
}

async function bootstrap(env, version, signal, beforePublication) {
  return withOwnedRuntime(env, async () => {
    const existing = await resolveSeedImage(seedReference("bootstrap"), {});
    signal.throwIfAborted();
    const runtime = await setup(env, { operation: "bootstrap", seedImage: existing || "", signal });
    await runBinary("bootstrap", runtime, env, signal);
    if (existing) return existing;
    const scope = read(path.join(runtime.seedDir, "scope.json"));
    const container = `densemem-eval-${runtime.paths.runID}-postgres`;
    const schema = Number(docker(["exec", container, "psql", "--username=densemem", "--dbname=densemem", "--tuples-only", "--no-align", "--command", "SELECT COALESCE(max(version_id),0) FROM (SELECT DISTINCT ON (version_id) version_id,is_applied FROM goose_db_version ORDER BY version_id,id DESC) latest WHERE is_applied"]));
    save(path.join(runtime.seedDir, "seed.json"), { version: 1, dataset: "bootstrap", registry_sha256: runtime.manifest.registry_sha256, base_release: runtime.manifest.base_release, source_revision: env.EVAL_SOURCE_REVISION, scope, corpus_rows: 0, canonical_sha256: "", search_sha256: "", mapping_sha256: "", dump_sha256: "", dump_bytes: 0, schema_version: schema, postgres_image: postgresImage, model: runtime.manifest.model, context_window_tokens: runtime.manifest.context_window_tokens, embedding_model: "text-embedding-3-small", embedding_dimensions: 1536, import_workers: runtime.manifest.import_workers, elapsed_ms: 0 });
    dumpSeed(docker, container, runtime.seedDir);
    await sealSeed(runtime.seedDir);
    await roundTrip(runtime, env, "bootstrap", signal);
    fs.writeFileSync(path.join(runtime.seedDir, "NOTICE.md"), notice());
    return publishSeed(docker, runtime.seedDir, "bootstrap", version, {}, beforePublication);
  });
}

export async function prepareSeedExport(env, signal, options) {
  const runtime = await setup(env, { ...options, operation: "prepare", signal });
  await runBinary("prepare", runtime, env, signal);
  dumpSeed(docker, `densemem-eval-${runtime.paths.runID}-postgres`, runtime.seedDir);
  const seed = await sealSeed(runtime.seedDir);
  fs.writeFileSync(path.join(runtime.seedDir, "NOTICE.md"), notice());
  await roundTrip(runtime, env, "verify-seed", signal, options);
  return { runtime, seed };
}

async function buildDataset(env, version, signal, beforePublication, skipAISecurityReview) {
  return withOwnedRuntime(env, async () => {
    const { runtime, seed } = await prepareSeedExport(env, signal, { skipAISecurityReview });
    const image = await publishSeed(docker, runtime.seedDir, env.EVAL_DATASET, version, {}, beforePublication);
    const receipt = { dataset: env.EVAL_DATASET, image, version, corpus_rows: seed.corpus_rows, canonical_sha256: seed.canonical_sha256, workers: seed.import_workers, dump_bytes: seed.dump_bytes, preparation_ms: seed.elapsed_ms, base_release: seed.base_release, source_revision: seed.source_revision, ai_security_review: seed.ai_security_review || "ordinary" };
    save(path.join(runtime.paths.directory, "public/seed-build.json"), receipt);
    process.stdout.write(`${JSON.stringify({ event: "seed_published", ...receipt })}\n`);
    return receipt;
  });
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
  const request = localSeedRequest(args, read("tests/eval/datasets.json"));
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
  const version = seedBuildVersion(source.baseRelease);
  const id = `local-${randomBytes(16).toString("hex")}`;
  const directory = path.resolve("tmp/eval-seeds", id);
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  save(path.join(directory, "source.json"), { image: image.image, source_revision: source.sourceRevision, checkout_revision: source.checkoutRevision, version, validation: source.validation, workers: request.workers, datasets: request.dataset });
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
    const bootstrapImage = await bootstrap(datasetEnv("bootstrap"), version, controller.signal, beforePublication);
    const receipts = [];
    for (const dataset of request.dataset) {
      await beforePublication();
      receipts.push(await buildDataset(datasetEnv(dataset, bootstrapImage), version, controller.signal, beforePublication, request.skipAISecurityReview));
    }
    return receipts;
  } finally {
    for (const signal of ["SIGINT", "SIGTERM"]) process.removeListener(signal, stop);
  }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  main(process.argv.slice(2)).catch((error) => {
    process.exitCode = 1;
    const message = error.message;
    if (/^(Public seed creation requires|Missing local seed settings:|Seed build --pr|Seed building requires|Seed checkout |Seed PR image must be|Seed validation |Seed main CI |Seed publication requires|Invalid public seed dataset|Invalid Actions setting: DENSE_MEM_EVAL_|Invalid private runtime configuration|Local seed runtime differs|Evaluation requires a production image supporting locked public seed admission|Could not build the seed helper)/.test(message)) process.stderr.write(`${message}\n`);
    else process.stderr.write("Local seed build failed; inspect protected output or private evidence. No automatic rebuild was requested.\n");
  });
}
