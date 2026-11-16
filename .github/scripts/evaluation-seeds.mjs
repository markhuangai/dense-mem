import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { ownedResource, registryMatrix, resolveImportWorkers } from "./evaluation-policy.mjs";
import imagePolicy from "./image-release-policy.cjs";

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

export async function authorizeSeedBuild(github, context, triggeringActor) {
  const { owner, repo } = context.repo;
  const repository = `${owner}/${repo}`;
  if (repository !== "markhuangai/dense-mem" || Number(context.runAttempt) !== 1 || !context.ref.startsWith("refs/heads/")) throw new Error("Seed building requires a fresh same-repository branch dispatch");
  for (const username of [context.actor, triggeringActor]) {
    const response = await github.rest.repos.getCollaboratorPermissionLevel({ owner, repo, username });
    if (response.data.permission !== "admin") throw new Error("Seed building requires initiating and triggering repository admins");
  }
  const main = (await github.rest.repos.getBranch({ owner, repo, branch: "main" })).data.commit.sha;
  const releases = await github.paginate(github.rest.repos.listReleases, { owner, repo, per_page: 100 });
  let baseRelease;
  for (const candidate of releases.filter((item) => !item.draft && release.test(item.tag_name))) {
    let object = (await github.rest.git.getRef({ owner, repo, ref: `tags/${candidate.tag_name}` })).data.object;
    while (object.type === "tag") object = (await github.rest.git.getTag({ owner, repo, tag_sha: object.sha })).data.object;
    const comparison = await github.request("GET /repos/{owner}/{repo}/compare/{basehead}", { owner, repo, basehead: `${object.sha}...${main}` });
    if (["identical", "ahead"].includes(comparison.data.status)) { baseRelease = candidate.tag_name; break; }
  }
  if (!baseRelease) throw new Error("Current main has no reachable published release");
  let image;
  if (context.ref === "refs/heads/main") {
    if (context.sha !== main) throw new Error("Seed workflow source no longer matches current main");
    image = `ghcr.io/${repository}:${baseRelease}`;
  } else {
    const pulls = await github.paginate(github.rest.pulls.list, { owner, repo, state: "open", base: "main", head: `${owner}:${context.ref.slice(11)}`, per_page: 100 });
    const matches = pulls.filter((pull) => !pull.draft && pull.head.sha === context.sha && pull.head.repo?.full_name === repository && pull.user.login.toLowerCase() === context.actor.toLowerCase());
    if (matches.length !== 1) throw new Error("Seed branch must be the actor's own current open same-repository PR head");
    const pull = matches[0];
    const statuses = await github.paginate(github.rest.repos.listCommitStatusesForRef, { owner, repo, ref: pull.head.sha, per_page: 100 });
    const receipt = imagePolicy.parseSuccessfulPolicyStatus({ statuses, pullNumber: pull.number, serverUrl: context.serverUrl, repository });
    if (!receipt.status) throw new Error("Seed building requires a successful trusted preview receipt");
    const run = (await github.rest.actions.getWorkflowRun({ owner, repo, run_id: Number(receipt.status.runId) })).data;
    const full = statuses.find((status) => status.context === "Production image E2E");
    if (run.run_attempt !== Number(receipt.status.runAttempt) || run.event !== "pull_request_target" || run.path !== ".github/workflows/pr-test-image.yml" || run.conclusion !== "success" || full?.state !== "success" || full.creator?.login !== "github-actions[bot]" || full.target_url !== `${context.serverUrl}/${repository}/actions/runs/${run.id}`) throw new Error("Seed building requires the current-head full production-image E2E receipt");
    const comments = await github.paginate(github.rest.issues.listComments, { owner, repo, issue_number: pull.number, per_page: 100 });
    image = imagePolicy.selectPreviewReceipt({ comments, pull, status: receipt.status, repository }).image;
  }
  return { image, source_revision: context.sha, source_repository: repository, base_release: baseRelease, seed_version: seedBuildVersion(baseRelease) };
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
  docker(["pull", "--quiet", image]);
  docker(["create", "--name", container, "--label", `io.dense-mem.eval.run=${runID}`, image, "/unused"]);
  try { docker(["cp", `${container}:/seed/.`, directory]); }
  finally {
    const actual = JSON.parse(docker(["container", "inspect", container]))[0];
    ownedResource({ Labels: actual.Config.Labels }, runID);
    docker(["container", "rm", actual.Id]);
  }
}

export async function verifySeedFiles(directory, dataset, registryHash, corpusRows) {
  const metadata = seedMetadata(read(path.join(directory, "seed.json")), dataset, registryHash, corpusRows);
  if (await hashFile(path.join(directory, "database.dump")) !== metadata.dump_sha256 || fs.statSync(path.join(directory, "database.dump")).size !== metadata.dump_bytes) throw new Error("Seed dump checksum or size differs");
  if (dataset !== "bootstrap" && await hashFile(path.join(directory, "mapping.json")) !== metadata.mapping_sha256) throw new Error("Seed mapping checksum differs");
  return metadata;
}

export function restoreSeed(docker, postgres, directory) {
  try {
    docker(["cp", path.join(directory, "database.dump"), `${postgres}:/tmp/evaluation-seed.dump`]);
    docker(["exec", postgres, "pg_restore", "--username=densemem", "--dbname=densemem", "--no-owner", "--no-privileges", "--clean", "--if-exists", "--exit-on-error", "--jobs=2", "/tmp/evaluation-seed.dump"], { timeout: 20 * 60_000 });
  } catch (error) { preservePostgresFailure(directory, "restore", error); throw error; }
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
  const headers = { Accept: "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" };
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

export async function publishSeed(docker, directory, dataset, selected, env = process.env) {
  const tagged = seedReference(dataset, selected);
  if (await resolveSeedImage(tagged, env)) throw new Error("Dated seed image already exists and cannot be overwritten");
  const files = ["database.dump", "seed.json", "NOTICE.md", ...(dataset === "bootstrap" ? [] : ["mapping.json"])];
  const context = path.join(directory, "image");
  fs.mkdirSync(context, { mode: 0o700 });
  for (const file of files) fs.copyFileSync(path.join(directory, file), path.join(context, file));
  fs.writeFileSync(path.join(context, "Dockerfile"), `FROM scratch\nLABEL org.opencontainers.image.source="https://github.com/markhuangai/dense-mem"\nCOPY ${files.join(" ")} /seed/\n`);
  docker(["build", "--network=none", "--platform=linux/amd64", "--tag", tagged, context], { timeout: 20 * 60_000 });
  docker(["push", "--quiet", tagged], { timeout: 20 * 60_000 });
  const pinned = await resolveSeedImage(tagged, env);
  if (!pinned) throw new Error("Published seed has no immutable registry digest");
  docker(["tag", tagged, seedReference(dataset)]);
  docker(["push", "--quiet", seedReference(dataset)], { timeout: 20 * 60_000 });
  if (await resolveSeedImage(seedReference(dataset), env) !== pinned) throw new Error("Seed latest alias differs from the verified dated image");
  return pinned;
}
