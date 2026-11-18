import fs from "node:fs";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { extractSeed, verifySeedFiles, restoreSeed, dumpSeed, sealSeed, verifySeedCredentialExclusions, seedReference, resolveSeedImage, postgresImage } from "../../../.github/scripts/evaluation-seeds.mjs";
import { requireRunner, configuration, resolveImportWorkers, requireEvaluationImportSupport, registryMatrix, sha256, aggregate, ownedResource } from "../../../.github/scripts/evaluation-policy.mjs";

const label = "io.dense-mem.eval.run";
const secret = () => randomBytes(32).toString("base64url");
const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
const save = (file, value) => fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });

export function docker(args, options = {}) {
  const result = spawnSync("docker", args, { encoding: "utf8", timeout: 300_000, maxBuffer: 64 << 20, ...options });
  if (result.status !== 0) {
    const error = new Error(`Docker operation ${args[0]} failed`);
    Object.defineProperty(error, "diagnostic", { value: result.stderr || result.error?.message || `exit=${result.status} signal=${result.signal}` });
    throw error;
  }
  return (result.stdout || "").trim();
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
  const runID = `${seedBuildID || env.GITHUB_RUN_ID}-${dataset}`;
  const directory = path.join(seedBuildID ? env.EVAL_LOCAL_SEED_ROOT : env.RUNNER_TEMP, `dense-mem-eval-${runID}`);
  return { dataset, runID, directory, seedBuildID, stateFile: path.join(directory, "runtime.json"), manifestFile: path.join(directory, "manifest.json") };
}

export function privateEnvironment(state, env) {
  const environment = { ...env, ...state.environment };
  delete environment.GH_TOKEN;
  delete environment.GITHUB_TOKEN;
  return environment;
}

export function serverEnvironment(configured, environment) {
  return {
    LOG_LEVEL: "debug", HTTP_ADDR: ":8080", CONTROL_HTTP_ADDR: ":8090", CONTROL_PORTAL_TOKEN: environment.EVAL_CONTROL_TOKEN,
    POSTGRES_USER: "densemem", POSTGRES_DB: "densemem", POSTGRES_PASSWORD: environment.EVAL_DATABASE_PASSWORD, POSTGRES_HOST: "postgres", POSTGRES_PORT: "5432", POSTGRES_SSLMODE: "disable", POSTGRES_MAX_OPEN_CONNS: String(databaseCapacity(configured.importWorkers).application), POSTGRES_MAX_IDLE_CONNS: "10", ...(configured.importWorkers ? { POSTGRES_ADVISORY_LOCK_CONCURRENCY: String(configured.importWorkers) } : {}),
    RATE_LIMIT_PER_MINUTE: "200000",
    AI_API_URL: configured.embedding, AI_API_KEY: environment.DENSE_MEM_EVAL_EMBED_API_KEY, AI_API_EMBEDDING_MODEL: "text-embedding-3-small", AI_API_EMBEDDING_DIMENSIONS: "1536", AI_API_EMBEDDING_MAX_CONCURRENCY: "2", AI_API_EMBEDDING_MAX_BATCH_ITEMS: "100",
    AI_VERIFIER_API_URL: configured.verifier, AI_VERIFIER_API_KEY: environment.DENSE_MEM_EVAL_API_KEY, AI_VERIFIER_MODEL: configured.model, AI_REMEMBER_MODEL: configured.model, AI_VERIFIER_DISABLE_TEMPERATURE: "true", AI_VERIFIER_MAX_CONCURRENCY: String(Math.max(4, configured.importWorkers || 0)), AI_VERIFIER_MAX_INPUT_TOKENS: String(configured.context - 8192), AI_VERIFIER_MAX_OUTPUT_TOKENS: "8192", AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS: String(Math.min(50000, Math.floor((configured.context - 8192) / 2))), AI_VERIFIER_TIMEOUT_SECONDS: "300",
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

export async function control(state, method, endpoint, body) {
  const response = await fetch(`${state.environment.EVAL_CONTROL_URL}/control/api${endpoint}`, {
    method, headers: { Authorization: `Bearer ${state.environment.EVAL_CONTROL_TOKEN}`, "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(30_000),
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
  const { signal } = options;
  signal?.throwIfAborted();
  if (paths.seedBuildID && (env.EVAL_MODE !== "live" || (!preparing && operation !== "verify-seed"))) throw new Error("Local runtime permits only public seed preparation and restore verification");
  if (!paths.seedBuildID) {
    requireRunner(env);
    if (operation !== "run" && !(fixture && operation === "prepare")) throw new Error("Hosted runtime permits only scoring and synthetic fixture preparation");
  }
  const configured = fixture ? { model: "fixture", context: 300_000, verifier: "", embedding: "" } : configuration(env, { allowHTTPVerifier: Boolean(paths.seedBuildID) });
  configured.importWorkers = preparing ? resolveImportWorkers(env.DENSE_MEM_EVAL_IMPORT_WORKERS) : 0;
  if (!/^[0-9a-f]{40}$/.test(env.EVAL_SOURCE_REVISION || "") || !/^ghcr\.io\/markhuangai\/dense-mem(?::[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$/.test(env.EVAL_IMAGE || "")) throw new Error("Immutable runner source and image are required");
  docker(["pull", "--quiet", env.EVAL_IMAGE]);
  if (preparing && !fixture) requireEvaluationImportSupport(JSON.parse(docker(["image", "inspect", env.EVAL_IMAGE]))[0]);
  fs.mkdirSync(paths.directory, { recursive: true, mode: 0o700 });
  const registryPath = fixture ? "tests/eval/fixtures/datasets.json" : "tests/eval/datasets.json";
  const registryBytes = fs.readFileSync(registryPath);
  const registry = JSON.parse(registryBytes);
  registryMatrix(registry);
  const dataset = registry.datasets.find((row) => row.id === paths.dataset);
  if (!dataset && operation !== "bootstrap") throw new Error("Dataset is not registered");
  const dataDir = path.join(paths.directory, "data");
  const outputDir = path.join(paths.directory, "public");
  const seedDir = options.seedDirectory || path.join(paths.directory, "seed");
  fs.mkdirSync(outputDir, { mode: 0o700, recursive: true });
  fs.mkdirSync(seedDir, { mode: 0o700, recursive: true });
  const deadline = new Date(Number(env.EVAL_MODEL_DEADLINE_EPOCH) * 1000);
  if (!Number.isFinite(deadline.getTime()) || deadline <= new Date() || deadline > Date.now() + 330 * 60_000) throw new Error("Runner model deadline is invalid");
  const manifest = { version: 1, run_id: paths.runID, workflow_run_id: paths.seedBuildID ? "" : env.GITHUB_RUN_ID, seed_build_id: paths.seedBuildID, source_revision: env.EVAL_SOURCE_REVISION, image: env.EVAL_IMAGE, registry_sha256: sha256(registryBytes), dataset: paths.dataset, mode: fixture ? "fixtures" : "live", operation, model: configured.model, embedding_model: "text-embedding-3-small", embedding_dimensions: 1536, context_window_tokens: configured.context, import_workers: configured.importWorkers, base_release: env.EVAL_BASE_RELEASE || (fixture ? "v0.0.0" : ""), seed_image: options.seedImage ?? env.EVAL_SEED_IMAGE ?? "", seed_dir: seedDir, model_deadline: deadline.toISOString(), data_dir: dataDir, output_dir: outputDir, timings_ms: {} };
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
    extractSeed(docker, manifest.seed_image, inputSeedDir, `${prefix}-seed`, paths.runID);
    manifest.timings_ms.seed_pull = Date.now() - started;
  }
  if (manifest.seed_image || options.seedDirectory) seed = await verifySeedFiles(inputSeedDir, preparing ? "bootstrap" : paths.dataset, manifest.registry_sha256, preparing ? 0 : dataset.corpus.rows);
  if (!preparing && !seed) throw new Error("Runner evaluation requires a verified seed");
  if (operation !== "bootstrap") {
    if (fixture) { fs.mkdirSync(dataDir, { mode: 0o700, recursive: true }); for (const source of [dataset.corpus, dataset.questions]) fs.copyFileSync(path.join("tests/eval/fixtures", source.path), path.join(dataDir, source.path)); }
    else await downloadSources(registry, dataset, dataDir, signal);
    save(paths.manifestFile, manifest);
    const validation = spawnSync(env.EVAL_RUNNER_BINARY, ["validate", paths.manifestFile], { stdio: "inherit", env: privateEnvironment(state, env) });
    if (validation.status !== 0) throw new Error("Source validation failed before runtime allocation");
  }
  signal?.throwIfAborted();
  docker(["network", "create", "--label", `${label}=${paths.runID}`, state.network]);
  docker(["volume", "create", "--label", `${label}=${paths.runID}`, state.volume]);
  const pgEnv = path.join(paths.directory, "postgres.env");
  fs.writeFileSync(pgEnv, `POSTGRES_USER=densemem\nPOSTGRES_DB=densemem\nPOSTGRES_PASSWORD=${password}\n`, { mode: 0o600 });
  docker(["run", "--detach", "--name", `${prefix}-postgres`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--network-alias", "postgres", "--publish", "127.0.0.1::5432", "--env-file", pgEnv, "--mount", `type=volume,source=${state.volume},target=/var/lib/postgresql`, postgresImage, "postgres", "-c", `max_connections=${databaseCapacity(configured.importWorkers).postgres}`]);
  const pgDeadline = Date.now() + 120_000;
  while (spawnSync("docker", ["exec", `${prefix}-postgres`, "pg_isready", "--host=127.0.0.1", "-U", "densemem"], { stdio: "ignore", timeout: 5000 }).status !== 0) { if (Date.now() >= pgDeadline) throw new Error("Runner PostgreSQL readiness timed out"); await delay(1000, undefined, { signal }); }
  if (seed) {
    const started = Date.now(); restoreSeed(docker, `${prefix}-postgres`, inputSeedDir); manifest.timings_ms.restore = Date.now() - started;
    await verifySeedCredentialExclusions(docker, `${prefix}-postgres`, inputSeedDir, [password, controlToken, apiKey, state.environment.DENSE_MEM_EVAL_API_KEY, state.environment.DENSE_MEM_EVAL_EMBED_API_KEY]);
    Object.assign(state.environment, { EVAL_TEAM_ID: seed.scope.team_id, EVAL_CREDENTIAL_ID: seed.scope.credential_id });
  }
  let serverVerifier = configured.verifier; let serverEmbedding = configured.embedding;
  if (fixture) {
    docker(["run", "--detach", "--name", `${prefix}-provider`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--network-alias", "fixture-provider", "--publish", "127.0.0.1::8787", "--mount", `type=bind,source=${env.GITHUB_WORKSPACE},target=/workspace,readonly`, "--workdir", "/workspace", "node:24-bookworm-slim", "node", "tests/eval/scripts/fixture-provider.mjs"]);
    serverVerifier = serverEmbedding = "http://fixture-provider:8787/v1";
    configured.verifier = configured.embedding = `http://127.0.0.1:${port(`${prefix}-provider`, 8787)}/v1`;
  }
  const serverEnv = serverEnvironment({ ...configured, verifier: serverVerifier, embedding: serverEmbedding }, state.environment);
  for (const value of [serverVerifier, serverEmbedding, configured.verifier, configured.embedding]) mask(value, env);
  const serverFile = path.join(paths.directory, "server.env");
  fs.writeFileSync(serverFile, Object.entries(serverEnv).map(([key, value]) => `${key}=${value}`).join("\n") + "\n", { mode: 0o600 });
  Object.assign(state.environment, { DENSE_MEM_EVAL_VERIFIER_URL: configured.verifier, DENSE_MEM_EVAL_EMBED_URL: configured.embedding, AI_VERIFIER_API_URL: serverVerifier, AI_API_URL: serverEmbedding });
  save(paths.stateFile, state);
  signal?.throwIfAborted();
  docker(["run", "--detach", "--name", `${prefix}-server`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--publish", "127.0.0.1::8080", "--publish", "127.0.0.1::8090", "--env-file", serverFile, env.EVAL_IMAGE]);
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
  return { paths, state, manifest, seedDir };
}

export async function cleanup(env) {
  const paths = location(env);
  if (!fs.existsSync(paths.stateFile)) { process.stdout.write("No evaluation runtime was allocated.\n"); return; }
  const state = read(paths.stateFile);
  if (state.runID !== paths.runID) throw new Error("Runtime cleanup receipt does not belong to this job");
  for (const name of state.containers) {
    const listed = docker(["container", "ls", "--all", "--filter", `name=^${name}$`, "--format", "{{.ID}}"]);
    if (!listed) continue;
    const actual = JSON.parse(docker(["container", "inspect", name]))[0];
    ownedResource({ Labels: actual.Config.Labels }, paths.runID);
    if (actual.State.Running) docker(["container", "stop", "--signal=SIGTERM", "--timeout=60", actual.Id], { timeout: 120_000 });
    docker(["container", "rm", "--volumes", actual.Id]);
  }
  for (const [kind, name] of [["volume", state.volume], ["network", state.network]]) {
    if (!docker([kind, "ls", "--filter", `label=${label}=${paths.runID}`, "--format", "{{.Name}}"]).split("\n").includes(name)) {
      if (docker([kind, "ls", "--format", "{{.Name}}"]).split("\n").includes(name)) throw new Error("Recorded cleanup resource changed ownership");
      continue;
    }
    ownedResource(JSON.parse(docker([kind, "inspect", name]))[0], paths.runID);
    docker([kind, "rm", name]);
  }
  if (docker(["container", "ls", "--all", "--filter", `label=${label}=${paths.runID}`, "--quiet"])) throw new Error("Owned runtime cleanup is incomplete");
  for (const kind of ["volume", "network"]) if (docker([kind, "ls", "--filter", `label=${label}=${paths.runID}`, "--quiet"])) throw new Error("Owned storage or network cleanup is incomplete");
  for (const file of ["server.env", "postgres.env"]) { const target = path.join(paths.directory, file); if (fs.existsSync(target)) fs.unlinkSync(target); }
  delete state.environment; save(paths.stateFile, state);
  process.stdout.write(`Evaluation runtime ${paths.runID} removed; borrowed images retained.\n`);
}

export function diagnostics(env) {
  const paths = location(env);
  if (!fs.existsSync(paths.stateFile) || fs.existsSync(path.join(paths.directory, "public/scores.json"))) return;
  const state = read(paths.stateFile);
  if (state.runID !== paths.runID) throw new Error("Startup log ownership mismatch");
  const files = fs.readdirSync(paths.directory, { recursive: true }).filter((file) => ["postgres-failure.log", "seed-failure.log"].includes(path.basename(file))).map((file) => path.join(paths.directory, file));
  const name = `densemem-eval-${paths.runID}-server`;
  if (docker(["container", "ls", "--all", "--filter", `name=^${name}$`, "--quiet"])) {
    const actual = JSON.parse(docker(["container", "inspect", name]))[0]; ownedResource({ Labels: actual.Config.Labels }, paths.runID);
    const result = spawnSync("docker", ["logs", name], { encoding: "utf8", maxBuffer: 64 << 20 });
    if (result.status !== 0) throw new Error("Startup log collection failed");
    const file = path.join(paths.directory, "server.log"); fs.writeFileSync(file, result.stdout + result.stderr, { mode: 0o600 }); files.push(file);
  }
  for (const file of files) {
    const printed = spawnSync(env.EVAL_RUNNER_BINARY, ["validate", paths.manifestFile], { stdio: "inherit", env: { ...privateEnvironment(state, env), EVAL_SERVER_LOG_FILE: file } });
    if (printed.status !== 0) throw new Error("Startup log protection failed");
  }
}

export async function main(mode, env = process.env) {
  requireRunner(env);
  if (!["live", "fixtures"].includes(env.EVAL_MODE)) throw new Error("Invalid evaluation mode");
  if (mode === "matrix") {
    const matrix = registryMatrix(read(env.EVAL_MODE === "fixtures" ? "tests/eval/fixtures/datasets.json" : "tests/eval/datasets.json"));
    fs.appendFileSync(env.GITHUB_OUTPUT, `matrix=${JSON.stringify(matrix)}\n`); return;
  }
  if (mode === "pin-seeds" && env.EVAL_MODE === "live") {
    const bytes = fs.readFileSync("tests/eval/datasets.json");
    const registry = JSON.parse(bytes);
    const images = {};
    for (const dataset of registryMatrix(registry).dataset) {
      const image = await resolveSeedImage(seedReference(dataset, env.EVAL_SEED_VERSION || "latest"), env);
      if (!image) throw new Error(`Public seed is unavailable for ${dataset}`);
      const directory = path.join(env.RUNNER_TEMP, `seed-pin-${env.GITHUB_RUN_ID}-${dataset}`);
      extractSeed(docker, image, directory, `seed-pin-${env.GITHUB_RUN_ID}-${dataset}`, `${env.GITHUB_RUN_ID}-${dataset}`);
      await verifySeedFiles(directory, dataset, sha256(bytes), registry.datasets.find((row) => row.id === dataset).corpus.rows);
      images[dataset] = image;
    }
    fs.appendFileSync(env.GITHUB_OUTPUT, `images=${JSON.stringify(images)}\n`);
    return;
  }
  if (mode === "cleanup") return cleanup(env);
  if (mode === "diagnostics") return diagnostics(env);
  if (mode === "aggregate") {
    const registry = read(env.EVAL_MODE === "fixtures" ? "tests/eval/fixtures/datasets.json" : "tests/eval/datasets.json");
    const root = path.join(env.RUNNER_TEMP, "evaluation-reports");
    const reports = fs.existsSync(root) ? fs.readdirSync(root, { recursive: true }).filter((file) => path.basename(file) === "scores.json").map((file) => read(path.join(root, file))) : [];
    const report = aggregate(registry, reports);
    fs.mkdirSync(path.join(root, "aggregate"), { recursive: true }); save(path.join(root, "aggregate/comparison.json"), report);
    fs.appendFileSync(env.GITHUB_STEP_SUMMARY, `Evaluation: ${report.completed_answers}/${report.expected_answers} answers complete. Overall complete: ${report.complete}.\n`);
    if (!report.complete) process.exitCode = 1; return;
  }
  if (mode !== "run") throw new Error("Unsupported workflow operation");
  let runtime;
  if (env.EVAL_MODE === "fixtures") {
    const prepared = await setup({ ...env, DENSE_MEM_EVAL_IMPORT_WORKERS: "4" }, { operation: "prepare" });
    await runBinary("prepare", prepared, env);
    dumpSeed(docker, `densemem-eval-${prepared.paths.runID}-postgres`, prepared.seedDir);
    await sealSeed(prepared.seedDir);
    const apiKey = prepared.state.environment.EVAL_API_KEY;
    await cleanup(env);
    runtime = await setup(env, { seedDirectory: prepared.seedDir, apiKey });
  } else runtime = await setup(env);
  await runBinary("run", runtime, env);
}

export async function runBinary(operation, { paths, state }, env = process.env, signal) {
  signal?.throwIfAborted();
  const child = spawn(env.EVAL_RUNNER_BINARY, [operation, paths.manifestFile], { stdio: "inherit", env: privateEnvironment(state, env) });
  const stop = () => {
    child.kill("SIGTERM");
    try {
      const name = `densemem-eval-${paths.runID}-server`;
      const actual = JSON.parse(docker(["container", "inspect", name]))[0];
      ownedResource({ Labels: actual.Config.Labels }, paths.runID);
      docker(["container", "stop", "--signal=SIGTERM", "--timeout=-1", actual.Id], { timeout: 120_000 });
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
    const message = /^(Missing required Actions setting:|Invalid Actions setting:|Public source|Runner |Immutable |Dataset |Source validation|Production instance)/.test(error.message) ? error.message : "Evaluation workflow operation failed; inspect protected run logs.";
    process.stderr.write(`${message}\n`); process.exitCode = 1;
  });
}
