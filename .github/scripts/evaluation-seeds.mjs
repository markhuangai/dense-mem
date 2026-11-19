import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { ownedResource, registryMatrix, resolveImportWorkers } from "./evaluation-policy.mjs";
import imagePolicy from "./image-release-policy.cjs";
import { seedGitHubPages, seedValidationState } from "./seed-validation.mjs";

export const seedRepository = "ghcr.io/markhuangai/dense-mem-eval-postgres";
export const postgresImage = "pgvector/pgvector:0.8.2-pg18-trixie";
export const excludedSeedData = ["operation_logs", "remember_attempt_diagnostics", "remember_invocation_diagnostics", "remember_failure_artifacts"];
const digest = /^[0-9a-f]{64}$/;
const release = /^v\d+\.\d+\.\d+(?:-rc\.\d+)?$/;
const version = /^\d{8}T\d{9}Z-v\d+\.\d+\.\d+(?:-rc\.\d+)?$/;
const read = (file) => JSON.parse(fs.readFileSync(file, "utf8"));
const save = (file, value) => fs.writeFileSync(file, JSON.stringify(value, null, 2) + "\n", { mode: 0o600 });

export function seedBuildRequest(registry, dataset, workers) {
  const allowed = registryMatrix(registry).dataset;
  if (dataset !== "all" && !allowed.includes(dataset)) throw new Error("Invalid public seed dataset");
  return { dataset: dataset === "all" ? allowed : [dataset], workers: resolveImportWorkers(String(workers)) };
}

export async function resolveSeedBuildSource(github, pullNumber, checkoutRevision) {
  if ((pullNumber !== undefined && (!Number.isSafeInteger(pullNumber) || pullNumber < 1)) || !/^[0-9a-f]{40}$/.test(checkoutRevision)) throw new Error("Seed source requires an exact checkout revision and a valid PR number when supplied");
  const repository = "markhuangai/dense-mem", base = `/repos/${repository}`;
  const get = async (endpoint) => (await github.request(`GET ${base}/${endpoint}`)).data;
  const { data: actor } = await github.request("GET /user");
  if ((await get(`collaborators/${encodeURIComponent(actor.login)}/permission`)).permission !== "admin") throw new Error("Seed building requires a repository admin");
  const main = (await get("branches/main")).commit.sha;
  const released = pullNumber === undefined;
  let pull, baseRelease, releaseRevision;
  if (released) {
    if (checkoutRevision !== main) throw new Error("Seed checkout without --pr must match current main");
  } else {
    pull = await get(`pulls/${pullNumber}`);
    if (pull.state !== "open" || pull.draft || pull.base.ref !== "main" || pull.head.sha !== checkoutRevision || pull.head.repo?.full_name !== repository || pull.user.login.toLowerCase() !== actor.login.toLowerCase()) throw new Error("Seed checkout must match the actor's own current open same-repository PR head");
  }
  for (const candidate of (await seedGitHubPages(github, `${base}/releases`)).filter((item) => !item.draft && release.test(item.tag_name))) {
    let object = (await get(`git/ref/tags/${candidate.tag_name}`)).object;
    while (object.type === "tag") object = (await get(`git/tags/${object.sha}`)).object;
    const comparison = await get(`compare/${object.sha}...${main}`);
    if (["identical", "ahead"].includes(comparison.status)) {
      baseRelease = candidate.tag_name;
      releaseRevision = object.sha;
      break;
    }
  }
  if (!baseRelease) throw new Error("Current main has no reachable published release");
  if (released) {
    const associated = await seedGitHubPages(github, `${base}/commits/${releaseRevision}/pulls`);
    const selected = imagePolicy.selectMergedPull(associated, releaseRevision);
    if (!selected.pull) throw new Error("Seed release has no unique tested merged PR");
    pull = await get(`pulls/${selected.pull.number}`);
  }
  const sourceRevision = pull.head.sha;
  const statuses = await seedGitHubPages(github, `${base}/commits/${sourceRevision}/statuses`);
  const receipt = imagePolicy.parseSuccessfulPolicyStatus({ statuses, pullNumber: pull.number, serverUrl: "https://github.com", repository });
  if (!receipt.status) throw new Error("Seed building requires a successful trusted preview receipt");
  const run = await get(`actions/runs/${receipt.status.runId}`);
  const published = await get(`actions/runs/${receipt.status.runId}/attempts/${receipt.status.runAttempt}`);
  if (run.head_sha !== sourceRevision || published.head_sha !== sourceRevision || published.id !== run.id || published.run_attempt !== Number(receipt.status.runAttempt) || run.run_attempt < published.run_attempt || run.event !== "pull_request_target" || run.path !== ".github/workflows/pr-test-image.yml") throw new Error("Seed building requires a valid trusted publisher workflow receipt");
  const jobs = await seedGitHubPages(github, `${base}/actions/runs/${run.id}/attempts/${published.run_attempt}/jobs`, "jobs");
  if (!jobs.some((job) => job.name === "Publish trusted preview" && job.conclusion === "success")) throw new Error("Seed building requires successful trusted preview publication");
  const comments = await seedGitHubPages(github, `${base}/issues/${pull.number}/comments`);
  const preview = imagePolicy.selectPreviewReceipt({ comments, pull, status: receipt.status, repository });
  if (!released && (preview.trusted_revision !== main || pull.base.sha !== main)) throw new Error("Seed PR image must be validated against current main");
  const validation = { source_revision: sourceRevision, pull_number: pull.number, actor: pull.user.login, run_id: run.id, publisher_attempt: published.run_attempt, validation_attempt: run.run_attempt, ...(released ? { main_revision: main, release_revision: releaseRevision } : {}) };
  await requireSeedBuildValidation(github, validation);
  const image = released ? `ghcr.io/${repository}:${baseRelease}` : `ghcr.io/${repository}@${preview.image.split("@")[1]}`;
  return { image, sourceRevision: released ? releaseRevision : sourceRevision, checkoutRevision, baseRelease, validation };
}

export async function requireSeedBuildValidation(github, validation) {
  if (await seedValidationState(github, validation) !== "success") throw new Error("Seed building requires passing current-head CI/E2E");
}

export function seedReference(dataset, selected = "latest") {
  if (!/^(bootstrap|hotpotqa|musique|2wikimultihopqa|nq_rear|popqa)$/.test(dataset) || (selected !== "latest" && !version.test(selected))) throw new Error("Invalid seed version or dataset");
  return `${seedRepository}:${dataset}-${selected}`;
}

export function seedBuildVersion(baseRelease, now = new Date()) {
  if (!release.test(baseRelease) || !Number.isFinite(now.getTime())) throw new Error("Invalid main release or seed creation date");
  return now.toISOString().replace(/[-:.]/g, "") + "-" + baseRelease;
}

export function seedMetadata(metadata, dataset, registryHash, corpusRows) {
  if (metadata.version !== 1 || metadata.dataset !== dataset || metadata.registry_sha256 !== registryHash || metadata.corpus_rows !== corpusRows || !release.test(metadata.base_release) || !/^[0-9a-f]{40}$/.test(metadata.source_revision || "") || metadata.postgres_image !== postgresImage || metadata.embedding_model !== "text-embedding-3-small" || metadata.embedding_dimensions !== 1536 || !digest.test(metadata.dump_sha256 || "") || !Number.isSafeInteger(metadata.dump_bytes) || metadata.dump_bytes < 1 || !Number.isSafeInteger(metadata.schema_version) || metadata.schema_version < 1) throw new Error("Seed metadata is incompatible with the public dataset or runtime");
  const scope = metadata.scope;
  for (const key of ["team_id", "credential_id", "owner_id", "space_id"]) if (!/^[0-9a-f-]{36}$/.test(scope?.[key] || "")) throw new Error("Seed scope is incomplete");
  if (!Number.isSafeInteger(scope.generation) || scope.generation < 1) throw new Error("Seed generation is invalid");
  if (dataset !== "bootstrap" && (!digest.test(metadata.canonical_sha256 || "") || !digest.test(metadata.mapping_sha256 || "") || !digest.test(metadata.search_sha256 || ""))) throw new Error("Seed canonical, search or mapping digest is missing");
  return metadata;
}

export async function hashFile(file) {
  const hash = createHash("sha256");
  for await (const chunk of fs.createReadStream(file)) hash.update(chunk);
  return hash.digest("hex");
}

export function extractSeed(docker, image, directory, container, runID) {
  if (!new RegExp(`^${seedRepository.replaceAll(".", "\\.")}@sha256:[0-9a-f]{64}$`).test(image)) throw new Error("Immutable seed image is required");
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  try {
    docker(["image", "inspect", "--format", "{{.Id}}", image]);
  } catch {
    docker(["pull", "--quiet", image]);
  }
  docker(["create", "--name", container, "--label", `io.dense-mem.eval.run=${runID}`, image, "/unused"]);
  try { docker(["cp", `${container}:/seed/.`, directory]); }
  finally {
    const actual = JSON.parse(docker(["container", "inspect", container]))[0];
    ownedResource({ Labels: actual.Config.Labels }, runID);
    docker(["container", "rm", actual.Id]);
  }
}

export async function verifySeedFiles(directory, dataset, registryHash, corpusRows, { intakeProof = false } = {}) {
  const metadata = seedMetadata(read(path.join(directory, "seed.json")), dataset, registryHash, corpusRows);
  if ((metadata.intake_proof ?? false) !== intakeProof || (intakeProof && metadata.ai_security_review !== "skipped")) throw new Error("Seed export kind or admission policy differs from the requested operation");
  if (await hashFile(path.join(directory, "database.dump")) !== metadata.dump_sha256 || fs.statSync(path.join(directory, "database.dump")).size !== metadata.dump_bytes) throw new Error("Seed dump checksum or size differs");
  if (dataset !== "bootstrap" && await hashFile(path.join(directory, "mapping.json")) !== metadata.mapping_sha256) throw new Error("Seed mapping checksum differs");
  return metadata;
}

export function remainingSeedRestoreEntries(toc) {
  let spaces = 0;
  const remaining = toc.split("\n").map((line) => {
    if (!/^\d+;\s+\d+\s+\d+\s+TABLE DATA public memory_spaces(?:\s|$)/.test(line)) return line;
    spaces++;
    return `;${line}`;
  });
  if (spaces !== 1) throw new Error("Seed archive must contain exactly one memory_spaces data entry");
  return remaining.join("\n");
}

export function restoreSeed(docker, postgres, directory) {
  let phase = "archive inspection";
  try {
    const dump = "/tmp/evaluation-seed.dump";
    docker(["cp", path.join(directory, "database.dump"), `${postgres}:${dump}`]);
    const remaining = remainingSeedRestoreEntries(docker(["exec", postgres, "pg_restore", "--list", dump]));
    const list = path.join(directory, "restore.list");
    fs.writeFileSync(list, remaining, { mode: 0o600 });
    docker(["cp", list, `${postgres}:/tmp/evaluation-seed-restore.list`]);
    const command = ["exec", postgres, "pg_restore", "--username=densemem", "--dbname=densemem", "--no-owner", "--no-privileges", "--exit-on-error", "--jobs=2"];
    phase = "schema restore";
    docker([...command, "--section=pre-data", "--clean", "--if-exists", dump], { timeout: 20 * 60_000 });
    phase = "memory space restore";
    docker([...command, "--data-only", "--schema=public", "--table=memory_spaces", dump], { timeout: 20 * 60_000 });
    phase = "remaining data and index restore";
    docker([...command, "--section=data", "--section=post-data", "--use-list=/tmp/evaluation-seed-restore.list", dump], { timeout: 20 * 60_000 });
  } catch (error) { preservePostgresFailure(directory, phase, error); throw error; }
}

export function dumpSeed(docker, postgres, directory) {
  fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  const fd = fs.openSync(path.join(directory, "database.dump"), "wx", 0o600);
  try { docker(["exec", postgres, "pg_dump", "--username=densemem", "--dbname=densemem", "--format=custom", "--no-owner", "--no-privileges", ...excludedSeedData.map((table) => `--exclude-table-data=public.${table}`)], { stdio: ["ignore", fd, "pipe"], timeout: 20 * 60_000 }); }
  catch (error) { preservePostgresFailure(directory, "dump", error); throw error; }
  finally { fs.closeSync(fd); }
}

function preservePostgresFailure(directory, operation, error) {
  fs.writeFileSync(path.join(directory, "postgres-failure.log"), `PostgreSQL ${operation} failed\n${error.diagnostic || error.message}\n`, { mode: 0o600 });
}

export async function sealSeed(directory) {
  const metadata = read(path.join(directory, "seed.json"));
  metadata.dump_sha256 = await hashFile(path.join(directory, "database.dump"));
  metadata.dump_bytes = fs.statSync(path.join(directory, "database.dump")).size;
  save(path.join(directory, "seed.json"), metadata);
  return metadata;
}

export async function verifySeedCredentialExclusions(docker, postgres, directory, secrets) {
  const sqlFile = path.join(directory, "verification.sql");
  const fd = fs.openSync(sqlFile, "wx", 0o600);
  try {
    try { docker(["exec", postgres, "pg_restore", "--file=-", "/tmp/evaluation-seed.dump"], { stdio: ["ignore", fd, "pipe"], timeout: 20 * 60_000 }); }
    catch (error) { preservePostgresFailure(directory, "verification", error); throw error; }
    finally { fs.closeSync(fd); }
    const patterns = [...new Set(secrets.filter(Boolean).flatMap((value) => {
      const base64 = Buffer.from(value).toString("base64");
      const base64url = Buffer.from(value).toString("base64url");
      return [value, base64, base64.replace(/=+$/, ""), base64url, base64url + "=".repeat((4 - base64url.length % 4) % 4), Buffer.from(value).toString("hex"), JSON.stringify(value).slice(1, -1), encodeURIComponent(value)];
    }))].map((value) => Buffer.from(value));
    const overlap = Math.max(1, ...patterns.map((pattern) => pattern.length));
    for (const file of [sqlFile, path.join(directory, "seed.json"), ...(fs.existsSync(path.join(directory, "mapping.json")) ? [path.join(directory, "mapping.json")] : [])]) {
      let tail = Buffer.alloc(0);
      for await (const chunk of fs.createReadStream(file)) {
        const bytes = Buffer.concat([tail, chunk]);
        if (patterns.some((pattern) => bytes.includes(pattern))) throw new Error("Seed export contains protected runtime configuration");
        tail = bytes.subarray(Math.max(0, bytes.length - overlap));
      }
    }
  } finally { fs.unlinkSync(sqlFile); }
}

async function registryManifest(reference, method = "HEAD", env = process.env) {
  const selected = reference.slice(seedRepository.length + 1);
  const headers = { Accept: "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" };
  const tokenHeaders = {};
  if (env.GITHUB_TOKEN) tokenHeaders.Authorization = `Basic ${Buffer.from(`${env.GITHUB_ACTOR}:${env.GITHUB_TOKEN}`).toString("base64")}`;
  const authorization = await fetch("https://ghcr.io/token?service=ghcr.io&scope=repository:markhuangai/dense-mem-eval-postgres:pull", { headers: tokenHeaders, signal: AbortSignal.timeout(30_000) });
  if (!authorization.ok) throw new Error("Seed registry authorization failed");
  const body = await authorization.json();
  headers.Authorization = `Bearer ${body.token || body.access_token}`;
  return fetch(`https://ghcr.io/v2/markhuangai/dense-mem-eval-postgres/manifests/${selected}`, { method, headers, signal: AbortSignal.timeout(30_000) });
}

export async function resolveSeedImage(reference, env = process.env) {
  if (!reference.startsWith(seedRepository + ":")) throw new Error("Seed repository is invalid");
  const response = await registryManifest(reference, "HEAD", env);
  if (response.status === 404) return null;
  const hash = response.headers.get("docker-content-digest");
  if (!response.ok || !/^sha256:[0-9a-f]{64}$/.test(hash || "")) throw new Error("Seed registry image could not be resolved");
  return `${seedRepository}@${hash}`;
}

export async function publishSeed(docker, directory, dataset, selected, env, beforePublication, resume) {
  if (read(path.join(directory, "seed.json")).intake_proof) throw new Error("Intake proof exports cannot be published");
  if (typeof beforePublication !== "function") throw new Error("Seed publication requires a fresh validation gate");
  const tagged = seedReference(dataset, selected);
  const alias = seedReference(dataset);
  const files = ["database.dump", "seed.json", "NOTICE.md", ...(dataset === "bootstrap" ? [] : ["mapping.json"])];
  let pinned = await resolveSeedImage(tagged, env);
  let aliasSource = tagged;
  if (pinned) {
    if (!resume || !/^local-[a-f0-9]{32}-[a-z0-9_]+$/.test(resume.runID)) throw new Error("Dated seed image already exists and cannot be overwritten");
    const existing = fs.mkdtempSync(path.join(directory, "published-"));
    try {
      extractSeed(docker, pinned, existing, `densemem-eval-${resume.runID}-seed`, resume.runID);
      for (const file of files) {
        if (await hashFile(path.join(existing, file)) !== await hashFile(path.join(directory, file))) throw new Error("Dated seed image differs from the verified local export");
      }
    } finally { fs.rmSync(existing, { recursive: true, force: true }); }
    aliasSource = pinned;
  } else {
    const context = fs.mkdtempSync(path.join(directory, "image-"));
    try {
      for (const file of files) fs.copyFileSync(path.join(directory, file), path.join(context, file));
      fs.writeFileSync(path.join(context, "Dockerfile"), `FROM scratch\nLABEL org.opencontainers.image.source="https://github.com/markhuangai/dense-mem"\nCOPY ${files.join(" ")} /seed/\n`);
      docker(["build", "--network=none", "--platform=linux/amd64", "--tag", tagged, context], { timeout: 20 * 60_000 });
    } finally { fs.rmSync(context, { recursive: true, force: true }); }
    await beforePublication();
    docker(["push", "--quiet", tagged], { timeout: 20 * 60_000 });
    pinned = await resolveSeedImage(tagged, env);
    if (!pinned) throw new Error("Published seed has no immutable registry digest");
  }
  if (resume) {
    const latest = await resolveSeedImage(alias, env);
    if (latest === pinned) return pinned;
    if (latest !== resume.previousLatest) throw new Error("Seed latest alias changed after the local export was verified");
  }
  docker(["tag", aliasSource, alias]);
  await beforePublication();
  docker(["push", "--quiet", alias], { timeout: 20 * 60_000 });
  if (await resolveSeedImage(alias, env) !== pinned) throw new Error("Seed latest alias differs from the verified dated image");
  return pinned;
}
