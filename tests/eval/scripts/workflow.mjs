import fs from "node:fs";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { execFile, spawn, spawnSync } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";
import { buildBootableSeed, extractSeed, verifySeedFiles, restoreSeed, dumpSeed, sealSeed, verifySeedCredentialExclusions, seedReference, resolveSeedImage, verifySeedPair, postgresImage } from "../../../.github/scripts/evaluation-seeds.mjs";
import { requireRunner, configuration, resolveAnswerWorkers, resolveImportWorkers, requireEvaluationImportSupport, registryMatrix, evaluationDatasets, sha256, aggregateConditions, ownedResource } from "../../../.github/scripts/evaluation-policy.mjs";

const label = "io.dense-mem.eval.run";
const secret = () => randomBytes(32).toString("base64url");
const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
export function saveRuntimeJSON(file, value) {
  const temporary = `${file}.${randomBytes(8).toString("hex")}.tmp`;
  try {
    fs.writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: "wx", flush: true });
    fs.renameSync(temporary, file);
    const directory = fs.openSync(path.dirname(file), "r");
    try { fs.fsyncSync(directory); } finally { fs.closeSync(directory); }
  } finally { fs.rmSync(temporary, { force: true }); }
}
const save = saveRuntimeJSON;

export function docker(args, options = {}) {
  const result = spawnSync("docker", args, { encoding: "utf8", timeout: 300_000, maxBuffer: 64 << 20, ...options });
  if (result.status !== 0) {
    const error = new Error(`Docker operation ${args[0]} failed`);
    Object.defineProperty(error, "diagnostic", { value: result.stderr || result.error?.message || `exit=${result.status} signal=${result.signal}` });
    throw error;
  }
  return (result.stdout || "").trim();
}

function runCommand(file, args, { input, forwardOutput, ...options } = {}) {
  return new Promise((resolve, reject) => {
    let inputFailure;
    const child = execFile(file, args, { encoding: "utf8", maxBuffer: 64 << 20, ...options }, (error, stdout, stderr) => {
      if (forwardOutput) {
        if (stdout) process.stdout.write(stdout);
        if (stderr) process.stderr.write(stderr);
      }
      if (error || inputFailure) reject(error || inputFailure);
      else resolve({ stdout, stderr });
    });
    child.stdin.on("error", (error) => {
      inputFailure = error;
      child.kill();
    });
    child.stdin.end(input);
  });
}

async function dockerAsync(args, options) {
  const output = await runCommand("docker", args, options);
  return output.stdout.trim();
}

export function startupTimings(logs, readyAt) {
  let start, end;
  for (const line of logs.split("\n")) {
    const index = line.indexOf("{");
    if (index < 0) continue;
    let event;
    try { event = JSON.parse(line.slice(index)); } catch { continue; }
    const observed = Date.parse(event.time);
    if (!Number.isFinite(observed)) continue;
    if (event.msg === "running postgres migrations") start = observed;
    if (event.msg === "postgres migrations completed") end = observed;
  }
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start || readyAt < end) throw new Error("Runner migration or readiness timing is unavailable");
  return { migration: end - start, readiness: readyAt - end };
}

export function mask(value, env = process.env) {
  if (!value || /[\r\n]/.test(value)) throw new Error("Invalid private runtime configuration");
  if (env.GITHUB_ACTIONS === "true" && !env.EVAL_LOCAL_SEED_ID) process.stdout.write(`::add-mask::${value}\n`);
}

export function location(env) {
  const dataset = env.EVAL_DATASET;
  if (!/^[a-z0-9_]+$/.test(dataset || "")) throw new Error("Invalid dataset job identity");
  const seedBuildID = env.EVAL_LOCAL_SEED_ID;
  if (seedBuildID && (env.GITHUB_ACTIONS === "true" || !/^local-[a-f0-9]{32}$/.test(seedBuildID) || !path.isAbsolute(env.EVAL_LOCAL_SEED_ROOT || ""))) throw new Error("Invalid local seed runtime identity");
  const condition = env.EVAL_CONDITION || "";
  if (condition && !["off", "on"].includes(condition)) throw new Error("Invalid evaluation condition");
  const runID = `${seedBuildID || env.GITHUB_RUN_ID}-${dataset}${condition ? "-" + condition : ""}`;
  const directory = path.join(seedBuildID ? env.EVAL_LOCAL_SEED_ROOT : env.RUNNER_TEMP, `dense-mem-eval-${runID}`);
  return { dataset, condition, runID, directory, seedBuildID, stateFile: path.join(directory, "runtime.json"), manifestFile: path.join(directory, "manifest.json"),
    checkpointFile: path.join(directory, "import-checkpoint.json") };
}

export function privateEnvironment(state, env) {
  const environment = { ...env, ...state.environment };
  delete environment.GH_TOKEN;
  delete environment.GITHUB_TOKEN;
  return environment;
}

export function seedSecrets(state) {
  return ["EVAL_DATABASE_PASSWORD", "EVAL_API_KEY", "EVAL_CONTROL_TOKEN", "DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY", "DENSE_MEM_EVAL_VERIFIER_URL", "DENSE_MEM_EVAL_EMBED_URL"].map((name) => state.environment[name]).filter(Boolean);
}

export function serverEnvironment(configured, environment) {
  return {
    LOG_LEVEL: "debug", HTTP_ADDR: ":8080", CONTROL_HTTP_ADDR: ":8090", CONTROL_PORTAL_TOKEN: environment.EVAL_CONTROL_TOKEN,
    POSTGRES_USER: "densemem", POSTGRES_DB: "densemem", POSTGRES_PASSWORD: environment.EVAL_DATABASE_PASSWORD, POSTGRES_HOST: "postgres", POSTGRES_PORT: "5432", POSTGRES_SSLMODE: "disable", POSTGRES_MAX_OPEN_CONNS: String(databaseCapacity(configured.importWorkers).application), POSTGRES_MAX_IDLE_CONNS: "10", ...(configured.importWorkers ? { POSTGRES_ADVISORY_LOCK_CONCURRENCY: String(configured.importWorkers) } : {}),
    RATE_LIMIT_PER_MINUTE: "200000",
    AI_API_URL: configured.embedding, AI_API_KEY: environment.DENSE_MEM_EVAL_EMBED_API_KEY, AI_API_EMBEDDING_MODEL: "text-embedding-3-small", AI_API_EMBEDDING_DIMENSIONS: "1536", AI_API_EMBEDDING_MAX_CONCURRENCY: "2", AI_API_EMBEDDING_MAX_BATCH_ITEMS: "100",
    AI_VERIFIER_API_URL: configured.verifier, AI_VERIFIER_API_KEY: environment.DENSE_MEM_EVAL_API_KEY, AI_VERIFIER_MODEL: configured.model, AI_REMEMBER_MODEL: configured.model, AI_VERIFIER_DISABLE_TEMPERATURE: "true", AI_VERIFIER_MAX_CONCURRENCY: String(configured.ontologyWorkers || Math.max(4, configured.importWorkers || 0)), AI_VERIFIER_MAX_INPUT_TOKENS: String(configured.context - 8192), AI_VERIFIER_MAX_OUTPUT_TOKENS: "8192", AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS: String(Math.min(50000, Math.floor((configured.context - 8192) / 2))), AI_VERIFIER_TIMEOUT_SECONDS: "300",
    REDIS_ADDR: "", DISTRIBUTED_COORDINATION_REQUIRED: "false", TELEMETRY_ENABLED: "false", OTLP_ENABLED: "false",
  };
}

export function databaseCapacity(workers = 0) {
  const application = Math.max(25, 2 * workers + 10);
  // Separate operation-log and observer pools use two connections each; reserve ten for administration.
  return { application, postgres: Math.max(100, application + 14) };
}

export async function downloadSources(registry, dataset, directory, signal) {
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  for (const source of [dataset.corpus, dataset.questions]) {
    signal?.throwIfAborted();
    const existing = path.join(directory, source.path);
    if (fs.existsSync(existing)) { const bytes = fs.readFileSync(existing); if (sha256(bytes) !== source.sha256 || JSON.parse(bytes).length !== source.rows) throw new Error("Public source cache differs from its lock"); continue; }
    const response = await fetch(`${registry.base_url}/${registry.revision}/${source.path}`, { signal: AbortSignal.any([AbortSignal.timeout(120_000), ...(signal ? [signal] : [])]) });
    if (!response.ok) throw new Error(`Public source download failed with HTTP ${response.status}`);
    const bytes = Buffer.from(await response.arrayBuffer());
    if (sha256(bytes) !== source.sha256 || JSON.parse(bytes).length !== source.rows) throw new Error("Public source count or digest mismatch");
    fs.writeFileSync(path.join(directory, source.path), bytes, { flag: "wx", mode: 0o600 });
  }
}

export async function control(state, method, endpoint, body, signal) {
  const timeout = AbortSignal.timeout(30_000);
  const response = await fetch(`${state.environment.EVAL_CONTROL_URL}/control/api${endpoint}`, {
    method, headers: { Authorization: `Bearer ${state.environment.EVAL_CONTROL_TOKEN}`, "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined,
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  if (!response.ok) throw new Error(`Control operation failed with HTTP ${response.status}`);
  return (await response.json()).data;
}

function port(container, internal) {
  const address = docker(["port", container, `${internal}/tcp`]);
  const match = address.match(/^127\.0\.0\.1:(\d+)$/);
  if (!match) throw new Error("Runner port binding is invalid");
  return Number(match[1]);
}

export async function setup(env, options = {}) {
  const paths = location(env);
  const fixture = env.EVAL_MODE === "fixtures";
  const operation = options.operation || "run";
  const preparing = operation === "prepare" || operation === "bootstrap";
  const baking = operation === "bake-ontology";
  const { signal, intakeProof = false, skipAISecurityReview = false, resumable = false } = options;
  const seedImage = options.seedImage ?? env.EVAL_SEED_IMAGE ?? "";
  const bootable = !preparing && !intakeProof;
  const seedOptions = { intakeProof: intakeProof && !preparing, bootable, allowLocal: fixture || operation === "verify-seed", condition: paths.condition || "off" };
  signal?.throwIfAborted();
  if (paths.seedBuildID && (env.EVAL_MODE !== "live" || (!preparing && !baking && operation !== "verify-seed"))) throw new Error("Local runtime permits only public seed preparation and restore verification");
  if (!paths.seedBuildID) {
    requireRunner(env);
    if (operation !== "run" && !(fixture && ["prepare", "bake-ontology"].includes(operation))) throw new Error("Hosted runtime permits only scoring and synthetic fixture preparation");
  }
  if (intakeProof && (!paths.seedBuildID || !options.dataDirectory || (operation !== "prepare" && operation !== "verify-seed"))) throw new Error("Intake proof requires a local seed runtime and frozen public inputs");
  if (skipAISecurityReview && (!paths.seedBuildID || operation !== "prepare" || !seedImage)) throw new Error("AI security review can only be skipped during local preparation from a public bootstrap");
  if (resumable && (!paths.seedBuildID || (!baking && operation !== "prepare") || intakeProof || !seedImage)) throw new Error("Only local public seed preparation supports checkpoints");
  if (bootable && !seedImage) throw new Error("Evaluation requires a bootable PostgreSQL seed image");
  const checkpoint = resumable && fs.existsSync(paths.checkpointFile) ? read(paths.checkpointFile) : null;
  const configured = fixture ? { model: "fixture", context: 300_000, verifier: "", embedding: "" } : configuration(env, { allowHTTPVerifier: Boolean(paths.seedBuildID) });
  configured.importWorkers = preparing ? resolveImportWorkers(env.DENSE_MEM_EVAL_IMPORT_WORKERS) : 0;
  if (baking) configured.ontologyWorkers = 8;
  const imagePattern = intakeProof ? /^sha256:[a-f0-9]{64}$/ : /^ghcr\.io\/markhuangai\/dense-mem(?::[A-Za-z0-9._-]+)?@sha256:[a-f0-9]{64}$/;
  if (!/^[a-f0-9]{40}$/.test(env.EVAL_SOURCE_REVISION || "") || !imagePattern.test(env.EVAL_IMAGE || "")) throw new Error("Immutable runner source and image are required");
  if (!intakeProof && !paths.seedBuildID) docker(["pull", "--quiet", env.EVAL_IMAGE]);
  if (preparing && !fixture) requireEvaluationImportSupport(JSON.parse(docker(["image", "inspect", env.EVAL_IMAGE]))[0], { skipAISecurityReview });
  fs.mkdirSync(paths.directory, { recursive: true, mode: 0o700 });
  const registryPath = fixture ? "tests/eval/fixtures/datasets.json" : "tests/eval/datasets.json";
  const registryBytes = fs.readFileSync(registryPath);
  const registry = JSON.parse(registryBytes);
  registryMatrix(registry);
  const dataset = registry.datasets.find((row) => row.id === paths.dataset);
  if (!dataset && operation !== "bootstrap") throw new Error("Dataset is not registered");
  const dataDir = options.dataDirectory || path.join(paths.directory, "data");
  const outputDir = path.join(paths.directory, "public");
  const seedDir = options.seedDirectory || options.exportDirectory || path.join(paths.directory, "seed");
  fs.mkdirSync(outputDir, { mode: 0o700, recursive: true });
  fs.mkdirSync(seedDir, { mode: 0o700, recursive: true });
  const deadline = new Date(Number(env.EVAL_MODEL_DEADLINE_EPOCH) * 1000);
  const limit = baking ? 24 * 60 : 330;
  if (!Number.isFinite(deadline.getTime()) || deadline <= new Date() || deadline > Date.now() + limit * 60_000) throw new Error("Runner model deadline is invalid");
  const manifest = { version: 1, run_id: paths.runID, workflow_run_id: paths.seedBuildID ? "" : env.GITHUB_RUN_ID, seed_build_id: paths.seedBuildID, source_revision: env.EVAL_SOURCE_REVISION, image: env.EVAL_IMAGE, registry_sha256: sha256(registryBytes), dataset: paths.dataset, mode: env.EVAL_MODE, operation, model: configured.model, embedding_model: "text-embedding-3-small", embedding_dimensions: 1536, context_window_tokens: configured.context, import_workers: configured.importWorkers, base_release: env.EVAL_BASE_RELEASE || (fixture ? "v0.0.0" : ""), seed_image: seedImage, seed_dir: seedDir, model_deadline: deadline.toISOString(), data_dir: dataDir, output_dir: outputDir, timings_ms: {}, intake_proof: intakeProof, skip_ai_security_review: skipAISecurityReview };
  if (checkpoint) manifest.resume_scope = checkpoint.scope;
  if (paths.condition) manifest.condition = paths.condition;
  if (paths.condition === "on") manifest.parent_seed_image = env.EVAL_PARENT_SEED_IMAGE;
  if (baking) manifest.bake_state_dir = paths.directory;
  if (operation === "run") manifest.answer_workers = resolveAnswerWorkers(env.EVAL_ANSWER_WORKERS);
  save(paths.manifestFile, manifest);
  const prefix = `densemem-eval-${paths.runID}`;
  const state = { runID: paths.runID, source: env.EVAL_SOURCE_REVISION, image: env.EVAL_IMAGE, network: `${prefix}-network`, volume: `${prefix}-postgres`, containers: [`${prefix}-server`, `${prefix}-postgres`, `${prefix}-seed`, ...(fixture ? [`${prefix}-provider`] : [])], environment: {} };
  const password = fixture ? secret() : env.DENSE_MEM_EVAL_DATABASE_PASSWORD;
  const apiKey = options.apiKey || (fixture ? secret() : env.DENSE_MEM_EVAL_TEAM_API_KEY);
  const controlToken = secret();
  for (const value of [password, apiKey, controlToken]) mask(value, env);
  state.environment = { EVAL_DATABASE_PASSWORD: password, EVAL_CONTROL_TOKEN: controlToken, EVAL_API_KEY: apiKey, EVAL_SERVER_CONTAINER: `${prefix}-server`, DENSE_MEM_EVAL_API_KEY: fixture ? secret() : env.DENSE_MEM_EVAL_API_KEY, DENSE_MEM_EVAL_EMBED_API_KEY: fixture ? secret() : env.DENSE_MEM_EVAL_EMBED_API_KEY };
  for (const key of ["DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY"]) mask(state.environment[key], env);
  save(paths.stateFile, state);
  let seed;
  const inputSeedDir = preparing && !options.seedDirectory ? path.join(paths.directory, "bootstrap") : seedDir;
  if (manifest.seed_image) {
    const started = Date.now();
    extractSeed(docker, manifest.seed_image, inputSeedDir, `${prefix}-seed`, paths.runID, seedOptions);
    manifest.timings_ms.seed_pull = Date.now() - started;
  }
  if (manifest.seed_image || options.seedDirectory) {
    seed = await verifySeedFiles(inputSeedDir, preparing ? "bootstrap" : paths.dataset, manifest.registry_sha256, preparing ? 0 : seedOptions.intakeProof ? 20 : dataset.corpus.rows, seedOptions);
  }
  if ((!preparing || intakeProof || skipAISecurityReview) && !seed) throw new Error("Runner evaluation requires a verified seed");
  const checkpointIdentity = { version: 1, run_id: paths.runID, volume: state.volume, source_revision: manifest.source_revision, image: manifest.image, registry_sha256: manifest.registry_sha256,
    bootstrap_image: seedImage, scope: seed?.scope, skip_ai_security_review: skipAISecurityReview };
  if (baking) Object.assign(checkpointIdentity, { operation, model_deadline: manifest.model_deadline });
  if (checkpoint && !isDeepStrictEqual(checkpoint, checkpointIdentity)) throw new Error("Seed checkpoint identity differs from the pinned build and bootstrap");
  if (intakeProof) manifest.base_release = seed.base_release;
  if (skipAISecurityReview) save(path.join(paths.directory, "seed-admission-scope.json"), seed.scope);
  if (operation !== "bootstrap") {
    if (fixture) { fs.mkdirSync(dataDir, { mode: 0o700, recursive: true }); for (const source of [dataset.corpus, dataset.questions]) fs.copyFileSync(path.join("tests/eval/fixtures", source.path), path.join(dataDir, source.path)); }
    else await downloadSources(registry, dataset, dataDir, signal);
    save(paths.manifestFile, manifest);
    const validation = spawnSync(env.EVAL_RUNNER_BINARY, ["validate", paths.manifestFile], { stdio: "inherit", env: privateEnvironment(state, env) });
    if (validation.status !== 0) throw new Error("Source validation failed before runtime allocation");
  }
  signal?.throwIfAborted();
  if (checkpoint) ownedResource(JSON.parse(docker(["volume", "inspect", state.volume]))[0], paths.runID);
  docker(["network", "create", "--label", `${label}=${paths.runID}`, state.network]);
  if (!checkpoint) docker(["volume", "create", "--label", `${label}=${paths.runID}`, state.volume]);
  const pgEnv = path.join(paths.directory, "postgres.env");
  fs.writeFileSync(pgEnv, `POSTGRES_USER=densemem\nPOSTGRES_DB=densemem\nPOSTGRES_PASSWORD=${password}\n`, { mode: 0o600 });
  const pgStarted = Date.now();
  docker(["run", "--detach", "--name", `${prefix}-postgres`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--network-alias", "postgres", "--publish", "127.0.0.1::5432", "--env-file", pgEnv, "--mount", `type=volume,source=${state.volume},target=/var/lib/postgresql`, bootable ? seedImage : postgresImage, "postgres", "-c", `max_connections=${databaseCapacity(configured.importWorkers).postgres}`]);
  const pgDeadline = Date.now() + 120_000;
  while (spawnSync("docker", ["exec", `${prefix}-postgres`, "pg_isready", "--host=127.0.0.1", "-U", "densemem"], { stdio: "ignore", timeout: 5000 }).status !== 0) { if (Date.now() >= pgDeadline) throw new Error("Runner PostgreSQL readiness timed out"); await delay(1000, undefined, { signal }); }
  manifest.timings_ms.postgres_startup = Date.now() - pgStarted;
  if (seed && !bootable && !checkpoint) {
    const started = Date.now(); restoreSeed(docker, `${prefix}-postgres`, inputSeedDir); manifest.timings_ms.restore = Date.now() - started;
    await verifySeedCredentialExclusions(docker, `${prefix}-postgres`, inputSeedDir, [password, controlToken, apiKey, state.environment.DENSE_MEM_EVAL_API_KEY, state.environment.DENSE_MEM_EVAL_EMBED_API_KEY]);
  }
  if (seed) Object.assign(state.environment, { EVAL_TEAM_ID: seed.scope.team_id, EVAL_CREDENTIAL_ID: seed.scope.credential_id });
  let serverVerifier = configured.verifier; let serverEmbedding = configured.embedding;
  if (fixture) {
    docker(["run", "--detach", "--name", `${prefix}-provider`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--network-alias", "fixture-provider", "--publish", "127.0.0.1::8787", "--mount", `type=bind,source=${env.GITHUB_WORKSPACE},target=/workspace,readonly`, "--workdir", "/workspace", "node:24-bookworm-slim", "node", "tests/eval/scripts/fixture-provider.mjs"]);
    serverVerifier = serverEmbedding = "http://fixture-provider:8787/v1";
    configured.verifier = configured.embedding = `http://127.0.0.1:${port(`${prefix}-provider`, 8787)}/v1`;
  }
  const serverEnv = serverEnvironment({ ...configured, verifier: serverVerifier, embedding: serverEmbedding }, state.environment);
  const admissionMount = [];
  if (skipAISecurityReview) {
    serverEnv.DANGEROUSLY_SKIP_AI_SECURITY_CHECK = "true";
    admissionMount.push("--mount", "type=bind,source=" + path.join(paths.directory, "seed-admission.json") + ",target=/run/dense-mem-seed-admission.json,readonly");
  }
  for (const value of [serverVerifier, serverEmbedding, configured.verifier, configured.embedding]) mask(value, env);
  const serverFile = path.join(paths.directory, "server.env");
  if (baking) {
    const cooldown = path.join(paths.directory, "cooldown");
    // The enclosing host directory is private. The container's runtime user owns writes to this mount.
    fs.mkdirSync(cooldown, { recursive: true, mode: 0o777 });
    fs.chmodSync(cooldown, 0o777);
    admissionMount.push("--mount", `type=bind,source=${cooldown},target=/tmp/ontology-preparation`);
    serverEnv.ONTOLOGY_PREPARATION_COOLDOWN_FILE = "/tmp/ontology-preparation/until";
  }
  fs.writeFileSync(serverFile, Object.entries(serverEnv).map(([key, value]) => `${key}=${value}`).join("\n") + "\n", { mode: 0o600 });
  Object.assign(state.environment, { DENSE_MEM_EVAL_VERIFIER_URL: configured.verifier, DENSE_MEM_EVAL_EMBED_URL: configured.embedding, AI_VERIFIER_API_URL: serverVerifier, AI_API_URL: serverEmbedding });
  save(paths.stateFile, state);
  signal?.throwIfAborted();
  docker(["run", "--detach", "--name", `${prefix}-server`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--publish", "127.0.0.1::8080", "--publish", "127.0.0.1::8090", "--env-file", serverFile, ...admissionMount, env.EVAL_IMAGE]);
  Object.assign(state.environment, { EVAL_MCP_URL: `http://127.0.0.1:${port(`${prefix}-server`, 8080)}`, EVAL_CONTROL_URL: `http://127.0.0.1:${port(`${prefix}-server`, 8090)}`, EVAL_DATABASE_URL: `postgres://densemem:${encodeURIComponent(password)}@127.0.0.1:${port(`${prefix}-postgres`, 5432)}/densemem?sslmode=disable` });
  mask(state.environment.EVAL_DATABASE_URL, env); save(paths.stateFile, state);
  const startupDeadline = Date.now() + 300_000;
  for (;;) { try { const response = await fetch(`${state.environment.EVAL_MCP_URL}/health`, { signal: AbortSignal.timeout(5000) }); if (response.ok) break; } catch { /* Readiness is bounded by the startup deadline. */ } if (Date.now() >= startupDeadline) throw new Error("Production instance readiness timed out"); await delay(1000, undefined, { signal }); }
  signal?.throwIfAborted();
  const startupLogs = spawnSync("docker", ["logs", `${prefix}-server`], { encoding: "utf8", maxBuffer: 64 << 20 });
  if (startupLogs.status !== 0) throw new Error("Runner startup timing capture failed");
  Object.assign(manifest.timings_ms, startupTimings(startupLogs.stdout + startupLogs.stderr, Date.now()));
  if (!seed) {
    const team = await control(state, "POST", "/teams", { name: "public-rag-evaluation" });
    state.environment.EVAL_TEAM_ID = team.id;
    save(paths.stateFile, state);
    if (operation !== "bootstrap") {
      const credential = await control(state, "POST", `/teams/${team.id}/credentials`, { name: "evaluation runner", role: "manager", scopes: ["read", "write"] });
      mask(credential.api_key, env);
      Object.assign(state.environment, { EVAL_CREDENTIAL_ID: credential.credential.id, EVAL_API_KEY: credential.api_key });
    }
  }
  if (operation === "bootstrap" || (preparing && !seed)) {
    const values = { ONTOLOGY_MAINTENANCE_ENABLED: "false", ONTOLOGY_MAINTENANCE_MODEL: configured.model, ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY: "4", ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY: "4", ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS: String(configured.context) };
    await control(state, "PATCH", "/config/ontology-maintenance", { items: Object.entries(values).map(([key, value]) => ({ key, value })) });
    await control(state, "POST", "/ontology/pause", { operation_key: `pause-${paths.runID}` });
  }
  save(paths.stateFile, state); save(paths.manifestFile, manifest);
  if (resumable && !checkpoint) save(paths.checkpointFile, checkpointIdentity);
  return { paths, state, manifest, seedDir };
}

export async function cleanup(env, { retainVolume = false } = {}) {
  const paths = location(env);
  if (retainVolume && (!paths.seedBuildID || !fs.existsSync(paths.checkpointFile))) throw new Error("Retaining a volume requires its local seed checkpoint");
  if (!fs.existsSync(paths.stateFile)) { process.stdout.write("No evaluation runtime was allocated.\n"); return; }
  const state = read(paths.stateFile);
  if (state.runID !== paths.runID) throw new Error("Runtime cleanup receipt does not belong to this job");
  if (retainVolume) {
    const checkpoint = read(paths.checkpointFile);
    if (checkpoint.run_id !== paths.runID || checkpoint.volume !== state.volume || state.volume !== `densemem-eval-${paths.runID}-postgres`) throw new Error("Checkpoint volume ownership differs from this dataset job");
    ownedResource(JSON.parse(docker(["volume", "inspect", state.volume]))[0], paths.runID);
  }
  for (const name of state.containers) {
    const listed = docker(["container", "ls", "--all", "--filter", `name=^${name}$`, "--format", "{{.ID}}"]);
    if (!listed) continue;
    const actual = JSON.parse(docker(["container", "inspect", name]))[0];
    ownedResource({ Labels: actual.Config.Labels }, paths.runID);
    if (actual.State.Running) docker(["container", "stop", "--signal=SIGTERM", "--timeout=60", actual.Id], { timeout: 120_000 });
    docker(["container", "rm", "--volumes", actual.Id]);
  }
  for (const [kind, name] of [["volume", state.volume], ["network", state.network]]) {
    if (kind === "volume" && retainVolume) continue;
    if (!docker([kind, "ls", "--filter", `label=${label}=${paths.runID}`, "--format", "{{.Name}}"]).split("\n").includes(name)) {
      if (docker([kind, "ls", "--format", "{{.Name}}"]).split("\n").includes(name)) throw new Error("Recorded cleanup resource changed ownership");
      continue;
    }
    ownedResource(JSON.parse(docker([kind, "inspect", name]))[0], paths.runID);
    docker([kind, "rm", name]);
  }
  if (docker(["container", "ls", "--all", "--filter", `label=${label}=${paths.runID}`, "--quiet"])) throw new Error("Owned runtime cleanup is incomplete");
  for (const kind of ["volume", "network"]) {
    const owned = docker([kind, "ls", "--filter", `label=${label}=${paths.runID}`, "--format", "{{.Name}}"]).split("\n").filter(Boolean);
    if (owned.some((name) => kind !== "volume" || !retainVolume || name !== state.volume)) throw new Error("Owned storage or network cleanup is incomplete");
  }
  for (const file of ["server.env", "postgres.env"]) { const target = path.join(paths.directory, file); if (fs.existsSync(target)) fs.unlinkSync(target); }
  if (paths.condition && fs.existsSync(path.join(paths.directory, "public", "scores.json"))) {
    save(path.join(paths.directory, "public", "cleanup.json"), { run_id: paths.runID, complete: true });
  }
  delete state.environment; save(paths.stateFile, state);
  const storage = retainVolume ? "PostgreSQL checkpoint retained" : "borrowed images retained";
  process.stdout.write(`Evaluation runtime ${paths.runID} removed; ${storage}.\n`);
}

export async function diagnostics(env) {
  const paths = location(env);
  if (!fs.existsSync(paths.stateFile) || fs.existsSync(path.join(paths.directory, "public/scores.json"))) return;
  const state = read(paths.stateFile);
  if (state.runID !== paths.runID) throw new Error("Startup log ownership mismatch");
  const files = fs.readdirSync(paths.directory, { recursive: true }).filter((file) => ["postgres-failure.log", "seed-failure.log"].includes(path.basename(file))).map((file) => path.join(paths.directory, file));
  const name = "densemem-eval-" + paths.runID + "-server";
  if (await dockerAsync(["container", "ls", "--all", "--filter", "name=^" + name + "$", "--quiet"])) {
    const actual = JSON.parse(await dockerAsync(["container", "inspect", name]))[0];
    ownedResource({ Labels: actual.Config.Labels }, paths.runID);
    const result = await runCommand("docker", ["logs", name]);
    const file = path.join(paths.directory, "server.log");
    fs.writeFileSync(file, result.stdout + result.stderr, { mode: 0o600 });
    files.push(file);
  }
  for (const file of files) {
    const environment = { ...privateEnvironment(state, env), EVAL_SERVER_LOG_FILE: file };
    await runCommand(env.EVAL_RUNNER_BINARY, ["validate", paths.manifestFile], { env: environment, forwardOutput: true });
  }
  return files.length !== 0;
}

export async function main(mode, env = process.env) {
  requireRunner(env);
  if (!["live", "fixtures"].includes(env.EVAL_MODE)) throw new Error("Invalid evaluation mode");
  const live = env.EVAL_MODE === "live";
  const registryPath = live ? "tests/eval/datasets.json" : "tests/eval/fixtures/datasets.json";
  const selection = live ? env.EVAL_DATASET_SELECTION : undefined;
  if (mode === "matrix") {
    const workers = resolveAnswerWorkers(env.EVAL_ANSWER_WORKERS);
    const datasets = evaluationDatasets(read(registryPath), selection);
    const matrix = { include: datasets.flatMap(({ id }) => (live ? ["off", "on"] : [""]).map((condition) => ({ dataset: id, condition }))) };
    const maxParallel = Math.floor(20 / workers);
    fs.appendFileSync(env.GITHUB_OUTPUT, `matrix=${JSON.stringify(matrix)}\nanswer_workers=${workers}\nmax_parallel=${maxParallel}\n`);
    if (env.GITHUB_STEP_SUMMARY) fs.appendFileSync(env.GITHUB_STEP_SUMMARY, `Answer workers per job: ${workers}. Concurrent jobs: up to ${maxParallel}.\n`);
    return;
  }
  if (mode === "pin-seeds" && live) {
    const bytes = fs.readFileSync(registryPath);
    const registry = JSON.parse(bytes);
    const registryHash = sha256(bytes);
    const images = {};
    for (const { id, corpus } of evaluationDatasets(registry, selection)) {
      const pins = {};
      images[id] = pins;
      let parent;
      for (const condition of ["off", "on"]) {
        const selected = condition === "off" ? env.EVAL_SEED_VERSION : env.EVAL_ONTOLOGY_SEED_VERSION;
        const reference = seedReference(id, selected || "latest", condition);
        const name = `seed-pin-${env.GITHUB_RUN_ID}-${id}-${condition}`;
        const directory = path.join(env.RUNNER_TEMP, name);
        try {
          const image = await resolveSeedImage(reference, env);
          if (!image) throw new Error("published seed is unavailable");
          const bootable = { bootable: true, condition };
          extractSeed(docker, image, directory, name, name, bootable);
          const metadata = await verifySeedFiles(directory, id, registryHash, corpus.rows, bootable);
          if (condition === "on") verifySeedPair(parent || {}, metadata, pins.off);
          else parent = metadata;
          pins[condition] = image;
        } catch {
          pins[condition + "_failure"] = `Unavailable or incompatible ${reference}`;
          process.stdout.write(`${pins[condition + "_failure"]}\n`);
        } finally {
          fs.rmSync(directory, { recursive: true, force: true });
        }
      }
    }
    fs.appendFileSync(env.GITHUB_OUTPUT, `images=${JSON.stringify(images)}\n`);
    return;
  }
  if (mode === "cleanup") return cleanup(env);
  if (mode === "diagnostics") return diagnostics(env);
  if (mode === "aggregate") {
    const registry = read(registryPath);
    const datasets = evaluationDatasets(registry, selection);
    const root = path.join(env.RUNNER_TEMP, "evaluation-reports");
    const reports = [];
    for (const file of fs.existsSync(root) ? fs.readdirSync(root, { recursive: true }) : []) {
      if (path.basename(file) !== "scores.json") continue;
      try {
        const report = read(path.join(root, file));
        const receipt = path.join(root, path.dirname(file), "cleanup.json");
        const clean = fs.existsSync(receipt) && read(receipt);
        reports.push({ ...report, owned_cleanup: clean?.complete === true && clean.run_id === report.identity?.run_id });
      } catch {
        process.stdout.write("One evaluation artifact could not be read. Its condition remains incomplete.\n");
      }
    }
    const report = { ...aggregateConditions({ ...registry, datasets }, reports, {
      runID: env.GITHUB_RUN_ID, source: env.EVAL_SOURCE_REVISION, image: env.EVAL_IMAGE,
      registryHash: sha256(fs.readFileSync(registryPath)), answerWorkers: resolveAnswerWorkers(env.EVAL_ANSWER_WORKERS),
      pins: live ? JSON.parse(env.EVAL_PINNED_SEEDS) : undefined,
    }), full_public_campaign: live && datasets.length === registry.datasets.length };
    fs.mkdirSync(path.join(root, "aggregate"), { recursive: true }); save(path.join(root, "aggregate/comparison.json"), report);
    const label = live ? datasets.map(({ id }) => id).join(", ") : "Synthetic fixtures";
    fs.appendFileSync(env.GITHUB_STEP_SUMMARY, `**${label}** completed ${report.completed_answers}/${report.expected_answers} answers. The run ${report.complete ? "passed" : "is incomplete"}.\n`);
    if (!report.complete) process.exitCode = 1; return;
  }
  if (mode !== "run") throw new Error("Unsupported workflow operation");
  if (live) {
    const runtime = await setup(env);
    return runBinary("run", runtime, env);
  }
  const off = await prepareFixtureSeed(env);
  const on = await prepareOntologyExport(env, off.seedImage, path.join(location(env).directory, "ontology-export"), { apiKey: off.apiKey });
  for (const [condition, seedImage] of [["off", off.seedImage], ["on", on.bootableImage]]) {
    const arm = { ...env, EVAL_CONDITION: condition, EVAL_PARENT_SEED_IMAGE: off.seedImage };
    try {
      const runtime = await setup(arm, { seedImage, apiKey: off.apiKey });
      await runBinary("run", runtime, arm);
    } finally {
      await cleanup(arm);
    }
  }
}

export async function prepareFixtureSeed(env) {
  const prepared = await setup({ ...env, DENSE_MEM_EVAL_IMPORT_WORKERS: "4" }, { operation: "prepare" });
  await runBinary("prepare", prepared, env);
  const postgres = `densemem-eval-${prepared.paths.runID}-postgres`;
  dumpSeed(docker, postgres, prepared.seedDir);
  await sealSeed(prepared.seedDir);
  fs.writeFileSync(path.join(prepared.seedDir, "NOTICE.md"), "# Synthetic evaluation seed\n");
  const seedImage = await buildBootableSeed(docker, postgres, prepared.seedDir, seedSecrets(prepared.state));
  const apiKey = prepared.state.environment.EVAL_API_KEY;
  await cleanup(env);
  return { seedImage, apiKey };
}

export async function prepareOntologyExport(env, parent, directory, { apiKey, resumable, signal } = {}) {
  const runtime = await setup(env, { operation: "bake-ontology", seedImage: parent, apiKey, resumable, signal });
  await runBinary("bake-ontology", runtime, env, signal);
  const metadata = read(path.join(runtime.paths.directory, "ontology-prepared.json"));
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  save(path.join(directory, "seed.json"), metadata);
  fs.copyFileSync(path.join(runtime.seedDir, "mapping.json"), path.join(directory, "mapping.json"));
  fs.copyFileSync(path.join(runtime.seedDir, "NOTICE.md"), path.join(directory, "NOTICE.md"));
  const postgres = `densemem-eval-${runtime.paths.runID}-postgres`;
  dumpSeed(docker, postgres, directory);
  const seed = await sealSeed(directory);
  const bootableImage = await buildBootableSeed(docker, postgres, directory, seedSecrets(runtime.state));
  if (!resumable) await cleanup(env);
  return { runtime, seed, bootableImage };
}

export async function runBinary(operation, { paths, state }, env = process.env, signal) {
  signal?.throwIfAborted();
  const child = spawn(env.EVAL_RUNNER_BINARY, [operation, paths.manifestFile], { stdio: "inherit", env: privateEnvironment(state, env) });
  let stopping = false;
  const stop = () => {
    if (stopping) return;
    stopping = true;
    child.kill("SIGTERM");
    // Ontology preparation owns a pause and drain before local runtime cleanup.
    if (operation === "bake-ontology") return;
    try {
      const name = `densemem-eval-${paths.runID}-server`;
      const deadline = Date.now() + 120_000;
      const actual = JSON.parse(docker(["container", "inspect", name], operation === "run" ? { timeout: 120_000 } : undefined))[0];
      ownedResource({ Labels: actual.Config.Labels }, paths.runID);
      if (actual.State.Running) {
        const stopTimeout = operation === "run" ? "--timeout=0" : "--timeout=-1";
        const budget = operation === "run" ? Math.max(1, deadline - Date.now()) : 120_000;
        docker(["container", "stop", "--signal=SIGTERM", stopTimeout, actual.Id], { timeout: budget });
      }
    } catch { process.stderr.write("Runner deadline shutdown failed; owned cleanup remains required.\n"); process.exitCode = 1; }
  };
  signal?.addEventListener("abort", stop, { once: true });
  const cutoff = setTimeout(stop, Math.max(1, Number(env.EVAL_MODEL_DEADLINE_EPOCH) * 1000 - Date.now()));
  try {
    const code = await new Promise((resolve, reject) => { child.once("error", reject); child.once("exit", resolve); });
    if (code !== 0) throw new Error("Runner binary operation failed; inspect protected logs");
    signal?.throwIfAborted();
  } finally { clearTimeout(cutoff); signal?.removeEventListener("abort", stop); }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  main(process.argv[2]).catch((error) => {
    const message = /^(Missing required Actions setting:|Invalid Actions setting:|Public (?:source|seed)|Runner |Immutable |Dataset |Source validation|Production instance)/.test(error.message) ? error.message : "Evaluation workflow operation failed; inspect protected run logs.";
    process.stderr.write(`${message}\n`); process.exitCode = 1;
  });
}
