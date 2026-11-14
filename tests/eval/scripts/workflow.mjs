import fs from "node:fs";
import path from "node:path";
import { randomBytes } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { requireRunner, configuration, registryMatrix, sha256, aggregate, ownedResource } from "../../../.github/scripts/evaluation-policy.mjs";
import { replayRegistry } from "./replay.mjs";

const label = "io.dense-mem.eval.run";
const secret = () => randomBytes(32).toString("base64url");
const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
const save = (file, value) => fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });

function docker(args, options = {}) {
  const result = spawnSync("docker", args, { encoding: "utf8", timeout: 300_000, maxBuffer: 64 << 20, ...options });
  if (result.status !== 0) throw new Error(`Docker operation ${args[0]} failed`);
  return result.stdout.trim();
}

function mask(value) {
  if (!value || /[\r\n]/.test(value)) throw new Error("Invalid private runtime configuration");
  process.stdout.write(`::add-mask::${value}\n`);
}

function location(env) {
  const dataset = env.EVAL_DATASET;
  if (!/^[a-z0-9_]+$/.test(dataset || "")) throw new Error("Invalid dataset job identity");
  const runID = `${env.GITHUB_RUN_ID}-${dataset}`;
  const directory = path.join(env.RUNNER_TEMP, `dense-mem-eval-${runID}`);
  return { dataset, runID, directory, stateFile: path.join(directory, "runtime.json"), manifestFile: path.join(directory, "manifest.json") };
}

function privateEnvironment(state, env) {
  const environment = { ...env, ...state.environment };
  delete environment.GH_TOKEN;
  delete environment.GITHUB_TOKEN;
  return environment;
}

export function serverEnvironment(configured, environment) {
  return {
    LOG_LEVEL: "debug", HTTP_ADDR: ":8080", CONTROL_HTTP_ADDR: ":8090", CONTROL_PORTAL_TOKEN: environment.EVAL_CONTROL_TOKEN,
    POSTGRES_USER: "densemem", POSTGRES_DB: "densemem", POSTGRES_PASSWORD: environment.EVAL_DATABASE_PASSWORD, POSTGRES_HOST: "postgres", POSTGRES_PORT: "5432", POSTGRES_SSLMODE: "disable", POSTGRES_MAX_OPEN_CONNS: "25", POSTGRES_MAX_IDLE_CONNS: "10",
    RATE_LIMIT_PER_MINUTE: "200000",
    AI_API_URL: configured.embedding, AI_API_KEY: environment.DENSE_MEM_EVAL_EMBED_API_KEY, AI_API_EMBEDDING_MODEL: "@cf/baai/bge-m3", AI_API_EMBEDDING_DIMENSIONS: "1024", AI_API_EMBEDDING_MAX_CONCURRENCY: "2", AI_API_EMBEDDING_MAX_BATCH_ITEMS: "100",
    AI_VERIFIER_API_URL: configured.verifier, AI_VERIFIER_API_KEY: environment.DENSE_MEM_EVAL_API_KEY, AI_VERIFIER_MODEL: configured.model, AI_REMEMBER_MODEL: configured.model, AI_VERIFIER_DISABLE_TEMPERATURE: "true", AI_VERIFIER_MAX_CONCURRENCY: "4", AI_VERIFIER_MAX_INPUT_TOKENS: String(configured.context - 8192), AI_VERIFIER_MAX_OUTPUT_TOKENS: "8192", AI_VERIFIER_MAX_CANDIDATE_CONTEXT_TOKENS: String(Math.min(50000, Math.floor((configured.context - 8192) / 2))), AI_VERIFIER_TIMEOUT_SECONDS: "300",
    REDIS_ADDR: "", DISTRIBUTED_COORDINATION_REQUIRED: "false", TELEMETRY_ENABLED: "false", OTLP_ENABLED: "false",
  };
}

async function downloadSources(registry, dataset, directory) {
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  for (const source of [dataset.corpus, dataset.questions]) {
    const response = await fetch(`${registry.base_url}/${registry.revision}/${source.path}`, { signal: AbortSignal.timeout(120_000) });
    if (!response.ok) throw new Error(`Public source download failed with HTTP ${response.status}`);
    const bytes = Buffer.from(await response.arrayBuffer());
    if (sha256(bytes) !== source.sha256 || JSON.parse(bytes).length !== source.rows) throw new Error("Public source count or digest mismatch");
    fs.writeFileSync(path.join(directory, source.path), bytes, { flag: "wx", mode: 0o600 });
  }
}

async function control(state, method, endpoint, body) {
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

async function setup(env) {
  const paths = location(env);
  const fixture = env.EVAL_MODE === "fixtures";
  const configured = fixture ? { model: "fixture", context: 300_000, verifier: "", embedding: "" } : configuration(env);
  if (!/^[0-9a-f]{40}$/.test(env.EVAL_SOURCE_REVISION || "") || !/^ghcr\.io\/markhuangai\/dense-mem(?::[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$/.test(env.EVAL_IMAGE || "")) throw new Error("Immutable runner source and image are required");
  fs.mkdirSync(paths.directory, { mode: 0o700 });
  const registryPath = fixture ? "tests/eval/fixtures/datasets.json" : "tests/eval/datasets.json";
  const registryBytes = fs.readFileSync(registryPath);
  const registry = JSON.parse(registryBytes);
  registryMatrix(registry);
  const dataset = registry.datasets.find((row) => row.id === paths.dataset);
  if (!dataset) throw new Error("Dataset is not registered");
  const dataDir = path.join(paths.directory, "data");
  const outputDir = path.join(paths.directory, "public");
  fs.mkdirSync(outputDir, { mode: 0o700 });
  const deadline = new Date(Number(env.EVAL_MODEL_DEADLINE_EPOCH) * 1000);
  if (!Number.isFinite(deadline.getTime()) || deadline <= new Date() || deadline > Date.now() + 330 * 60_000) throw new Error("Runner model deadline is invalid");
  const manifest = { version: 1, run_id: paths.runID, workflow_run_id: env.GITHUB_RUN_ID, source_revision: env.EVAL_SOURCE_REVISION, image: env.EVAL_IMAGE, registry_sha256: sha256(registryBytes), dataset: paths.dataset, mode: fixture ? "fixtures" : "live", model: configured.model, embedding_model: "@cf/baai/bge-m3", embedding_dimensions: 1024, context_window_tokens: configured.context, model_deadline: deadline.toISOString(), data_dir: dataDir, output_dir: outputDir };
  save(paths.manifestFile, manifest);
  const prefix = `densemem-eval-${paths.runID}`;
  const state = { runID: paths.runID, source: env.EVAL_SOURCE_REVISION, image: env.EVAL_IMAGE, network: `${prefix}-network`, volume: `${prefix}-postgres`, containers: [`${prefix}-server`, `${prefix}-postgres`, ...(fixture ? [`${prefix}-provider`] : [])], environment: {} };
  const password = secret(); const controlToken = secret();
  for (const value of [password, controlToken]) mask(value);
  state.environment = { EVAL_DATABASE_PASSWORD: password, EVAL_CONTROL_TOKEN: controlToken, EVAL_SERVER_CONTAINER: `${prefix}-server`, DENSE_MEM_EVAL_API_KEY: fixture ? secret() : env.DENSE_MEM_EVAL_API_KEY, DENSE_MEM_EVAL_EMBED_API_KEY: fixture ? secret() : env.DENSE_MEM_EVAL_EMBED_API_KEY };
  for (const key of ["DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY"]) mask(state.environment[key]);
  save(paths.stateFile, state);
  if (fixture) { fs.mkdirSync(dataDir, { mode: 0o700 }); for (const source of [dataset.corpus, dataset.questions]) fs.copyFileSync(path.join("tests/eval/fixtures", source.path), path.join(dataDir, source.path)); }
  else await downloadSources(registry, dataset, dataDir);
  let result = spawnSync(env.EVAL_RUNNER_BINARY, ["validate", paths.manifestFile], { stdio: "inherit", env: privateEnvironment(state, env) });
  if (result.status !== 0) throw new Error("Source validation failed before runtime allocation");
  docker(["network", "create", "--label", `${label}=${paths.runID}`, state.network]);
  docker(["volume", "create", "--label", `${label}=${paths.runID}`, state.volume]);
  const pgEnv = path.join(paths.directory, "postgres.env");
  fs.writeFileSync(pgEnv, `POSTGRES_USER=densemem\nPOSTGRES_DB=densemem\nPOSTGRES_PASSWORD=${password}\n`, { mode: 0o600 });
  docker(["run", "--detach", "--name", `${prefix}-postgres`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--network-alias", "postgres", "--publish", "127.0.0.1::5432", "--env-file", pgEnv, "--mount", `type=volume,source=${state.volume},target=/var/lib/postgresql`, "pgvector/pgvector:0.8.2-pg18-trixie"]);
  let serverVerifier = configured.verifier; let serverEmbedding = configured.embedding;
  if (fixture) {
    docker(["run", "--detach", "--name", `${prefix}-provider`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--network-alias", "fixture-provider", "--publish", "127.0.0.1::8787", "--mount", `type=bind,source=${env.GITHUB_WORKSPACE},target=/workspace,readonly`, "--workdir", "/workspace", "node:24-bookworm-slim", "node", "tests/eval/scripts/fixture-provider.mjs"]);
    serverVerifier = serverEmbedding = "http://fixture-provider:8787/v1";
    configured.verifier = configured.embedding = `http://127.0.0.1:${port(`${prefix}-provider`, 8787)}/v1`;
  }
  const serverEnv = serverEnvironment({ ...configured, verifier: serverVerifier, embedding: serverEmbedding }, state.environment);
  for (const value of [serverVerifier, serverEmbedding, configured.verifier, configured.embedding]) mask(value);
  const serverFile = path.join(paths.directory, "server.env");
  fs.writeFileSync(serverFile, Object.entries(serverEnv).map(([key, value]) => `${key}=${value}`).join("\n") + "\n", { mode: 0o600 });
  Object.assign(state.environment, { DENSE_MEM_EVAL_VERIFIER_URL: configured.verifier, DENSE_MEM_EVAL_EMBED_URL: configured.embedding, AI_VERIFIER_API_URL: serverVerifier, AI_API_URL: serverEmbedding });
  save(paths.stateFile, state);
  const pgDeadline = Date.now() + 120_000;
  while (spawnSync("docker", ["exec", `${prefix}-postgres`, "pg_isready", "-U", "densemem"], { stdio: "ignore" }).status !== 0) { if (Date.now() >= pgDeadline) throw new Error("Runner PostgreSQL readiness timed out"); await delay(1000); }
  docker(["run", "--detach", "--name", `${prefix}-server`, "--label", `${label}=${paths.runID}`, "--network", state.network, "--publish", "127.0.0.1::8080", "--publish", "127.0.0.1::8090", "--env-file", serverFile, env.EVAL_IMAGE]);
  Object.assign(state.environment, { EVAL_MCP_URL: `http://127.0.0.1:${port(`${prefix}-server`, 8080)}`, EVAL_CONTROL_URL: `http://127.0.0.1:${port(`${prefix}-server`, 8090)}`, EVAL_DATABASE_URL: `postgres://densemem:${password}@127.0.0.1:${port(`${prefix}-postgres`, 5432)}/densemem?sslmode=disable` });
  mask(state.environment.EVAL_DATABASE_URL); save(paths.stateFile, state);
  const startupDeadline = Date.now() + 300_000;
  for (;;) { try { const response = await fetch(`${state.environment.EVAL_MCP_URL}/health`, { signal: AbortSignal.timeout(5000) }); if (response.ok) break; } catch { /* Readiness is bounded by the startup deadline. */ } if (Date.now() >= startupDeadline) throw new Error("Production instance readiness timed out"); await delay(1000); }
  const team = await control(state, "POST", "/teams", { name: `public-rag-${paths.runID}` });
  const credential = await control(state, "POST", `/teams/${team.id}/credentials`, { name: "evaluation runner", role: "manager", scopes: ["read", "write"] });
  mask(credential.api_key);
  Object.assign(state.environment, { EVAL_TEAM_ID: team.id, EVAL_CREDENTIAL_ID: credential.credential.id, EVAL_API_KEY: credential.api_key });
  save(paths.stateFile, state);
  const values = { ONTOLOGY_MAINTENANCE_ENABLED: "false", ONTOLOGY_MAINTENANCE_MODEL: configured.model, ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY: "4", ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY: "4", ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS: String(configured.context) };
  await control(state, "PATCH", "/config/ontology-maintenance", { items: Object.entries(values).map(([key, value]) => ({ key, value })) });
  await control(state, "POST", "/ontology/pause", { operation_key: `pause-${paths.runID}` });
  return { paths, state };
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
    if (actual.State.Running) docker(["container", "stop", "--signal=SIGTERM", "--timeout=-1", actual.Id], { timeout: 120_000 });
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

export async function main(mode, env = process.env) {
  requireRunner(env);
  if (mode === "matrix") {
    const resolved = env.EVAL_MODE === "replay" ? await replayRegistry(env) : { mode: env.EVAL_MODE, matrix: registryMatrix(read(env.EVAL_MODE === "fixtures" ? "tests/eval/fixtures/datasets.json" : "tests/eval/datasets.json")) };
    fs.appendFileSync(env.GITHUB_OUTPUT, `matrix=${JSON.stringify(resolved.matrix)}\nregistry_mode=${resolved.mode}\n`); return;
  }
  if (mode === "cleanup") return cleanup(env);
  if (mode === "diagnostics") {
    const paths = location(env);
    if (!fs.existsSync(paths.stateFile) || fs.existsSync(path.join(paths.directory, "public/scores.json"))) return;
    const state = read(paths.stateFile);
    if (state.runID !== paths.runID) throw new Error("Startup log ownership mismatch");
    const name = `densemem-eval-${paths.runID}-server`;
    if (!docker(["container", "ls", "--all", "--filter", `name=^${name}$`, "--quiet"])) return;
    const actual = JSON.parse(docker(["container", "inspect", name]))[0]; ownedResource({ Labels: actual.Config.Labels }, paths.runID);
    const result = spawnSync("docker", ["logs", name], { encoding: "utf8", maxBuffer: 64 << 20 });
    if (result.status !== 0) throw new Error("Startup log collection failed");
    const file = path.join(paths.directory, "server.log"); fs.writeFileSync(file, result.stdout + result.stderr, { mode: 0o600 });
    const printed = spawnSync(env.EVAL_RUNNER_BINARY, ["validate", paths.manifestFile], { stdio: "inherit", env: { ...privateEnvironment(state, env), EVAL_SERVER_LOG_FILE: file } });
    if (printed.status !== 0) throw new Error("Startup log protection failed"); return;
  }
  if (mode === "aggregate") {
    const registry = read((env.EVAL_REGISTRY_MODE || env.EVAL_MODE) === "fixtures" ? "tests/eval/fixtures/datasets.json" : "tests/eval/datasets.json");
    const root = path.join(env.RUNNER_TEMP, "evaluation-reports");
    const reports = fs.existsSync(root) ? fs.readdirSync(root, { recursive: true }).filter((file) => path.basename(file) === "scores.json").map((file) => read(path.join(root, file))) : [];
    const report = aggregate(registry, reports);
    fs.mkdirSync(path.join(root, "aggregate"), { recursive: true }); save(path.join(root, "aggregate/comparison.json"), report);
    fs.appendFileSync(env.GITHUB_STEP_SUMMARY, `Evaluation: ${report.completed_answers}/${report.expected_answers} answers complete. Overall complete: ${report.complete}.\n`);
    if (!report.complete) process.exitCode = 1; return;
  }
  if (mode !== "run") throw new Error("Unsupported workflow operation");
  const { paths, state } = await setup(env);
  const child = spawn(env.EVAL_RUNNER_BINARY, ["run", paths.manifestFile], { stdio: "inherit", env: privateEnvironment(state, env) });
  const cutoff = setTimeout(() => {
    child.kill("SIGTERM");
    try {
      const name = `densemem-eval-${paths.runID}-server`;
      const actual = JSON.parse(docker(["container", "inspect", name]))[0];
      ownedResource({ Labels: actual.Config.Labels }, paths.runID);
      docker(["container", "stop", "--signal=SIGTERM", "--timeout=-1", actual.Id], { timeout: 120_000 });
    } catch { process.stderr.write("Runner deadline shutdown failed; owned cleanup remains required.\n"); process.exitCode = 1; }
  }, Math.max(1, Number(env.EVAL_MODEL_DEADLINE_EPOCH) * 1000 - Date.now()));
  try {
    const code = await new Promise((resolve, reject) => { child.once("error", reject); child.once("exit", resolve); });
    if (code !== 0) process.exitCode = 1;
  } finally { clearTimeout(cutoff); }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  main(process.argv[2]).catch((error) => {
    const message = /^(Missing required Actions setting:|Invalid Actions setting:|Public source|Runner |Immutable |Dataset |Source validation|Production instance)/.test(error.message) ? error.message : "Evaluation workflow operation failed; inspect protected run logs.";
    process.stderr.write(`${message}\n`); process.exitCode = 1;
  });
}
