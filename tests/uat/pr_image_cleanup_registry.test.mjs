import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { createServer } from "node:net";
import { createServer as createHTTPServer } from "node:http";
import { mkdtemp, writeFile, chmod, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { test } from "node:test";
import { GitHubApi, RegistryClient } from "../../.github/scripts/pr-image-cleanup.cjs";
import { cleanupDevelopmentImages } from "../../.github/scripts/development-e2e-cleanup.cjs";

function runDocker(args) {
  return execFileSync("docker", args, { encoding: "utf8" }).trim();
}

async function freePort() {
  const server = createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}

function digest(body) {
  return `sha256:${createHash("sha256").update(body).digest("hex")}`;
}

async function request(url, options = {}) {
  const response = await fetch(url, options);
  if (!response.ok) {
    throw new Error(`${options.method || "GET"} ${url} returned ${response.status}: ${(await response.text()).slice(0, 200)}`);
  }
  return response;
}

async function pushBlob(base, repository, body) {
  const blob = Buffer.from(body);
  const blobDigest = digest(blob);
  const start = await request(`${base}/v2/${repository}/blobs/uploads/`, { method: "POST" });
  const location = new URL(start.headers.get("location"), base);
  location.searchParams.set("digest", blobDigest);
  await request(location, {
    method: "PUT",
    headers: { "Content-Type": "application/octet-stream", "Content-Length": String(blob.length) },
    body: blob,
  });
  return { digest: blobDigest, size: blob.length };
}

async function pushManifest(base, repository, reference, manifest) {
  const body = Buffer.from(JSON.stringify(manifest));
  await request(`${base}/v2/${repository}/manifests/${reference}`, {
    method: "PUT",
    headers: { "Content-Type": manifest.mediaType, "Content-Length": String(body.length) },
    body,
  });
}

async function readManifest(base, repository, reference) {
  const response = await request(`${base}/v2/${repository}/manifests/${reference}`, {
    headers: { Accept: "application/vnd.oci.image.manifest.v1+json" },
  });
  return response.json();
}

test("real OCI registry keeps a prerelease tag when its shared test alias is replaced", { timeout: 120000 }, async () => {
  runDocker(["info"]);
  const port = await freePort();
  const container = runDocker(["run", "--rm", "-d", "-p", `127.0.0.1:${port}:5000`, "registry:2"]);
  try {
    const base = `http://127.0.0.1:${port}`;
    let ready = false;
    for (let attempt = 0; attempt < 120; attempt += 1) {
      try {
        await request(`${base}/v2/`);
        ready = true;
        break;
      } catch {
        await new Promise((resolve) => setTimeout(resolve, 250));
      }
    }
    assert.equal(ready, true, "registry did not become ready");

    const repository = "cleanup-test";
    const labels = {
      "io.dense-mem.preview.pr": "42",
      "io.dense-mem.preview.head": "a".repeat(40),
      "io.dense-mem.preview.main": "b".repeat(40),
      "io.dense-mem.preview.run-id": "123",
      "io.dense-mem.preview.run-attempt": "1",
      "org.opencontainers.image.version": "test-42",
    };
    const config = Buffer.from(JSON.stringify({ architecture: "amd64", os: "linux", config: { Labels: labels }, rootfs: { type: "layers", diff_ids: [] } }));
    const layer = await pushBlob(base, repository, "");
    const originalConfig = await pushBlob(base, repository, config);
    const original = {
      schemaVersion: 2,
      mediaType: "application/vnd.oci.image.manifest.v1+json",
      config: { mediaType: "application/vnd.oci.image.config.v1+json", digest: originalConfig.digest, size: originalConfig.size },
      layers: [{ mediaType: "application/vnd.oci.image.layer.v1.tar", digest: layer.digest, size: layer.size }],
    };
    await pushManifest(base, repository, "test-42", original);
    await pushManifest(base, repository, "v2.6.4-rc.9", original);
    const retainedBefore = await readManifest(base, repository, "v2.6.4-rc.9");

    const detachedLabels = { "org.opencontainers.image.version": "cleanup-42" };
    const detachedConfig = await pushBlob(base, repository, Buffer.from(JSON.stringify({ architecture: "amd64", os: "linux", config: { Labels: detachedLabels }, rootfs: { type: "layers", diff_ids: [] } })));
    const detached = { ...original, config: { ...original.config, digest: detachedConfig.digest, size: detachedConfig.size } };
    await pushManifest(base, repository, "test-42", detached);

    const retainedAfter = await readManifest(base, repository, "v2.6.4-rc.9");
    const testAfter = await readManifest(base, repository, "test-42");
    assert.deepEqual(retainedAfter, retainedBefore);
    assert.notDeepEqual(testAfter.config.digest, original.config.digest);
    assert.equal(testAfter.layers[0].digest, original.layers[0].digest);
  } finally {
    runDocker(["rm", "-f", container]);
  }
});

test("development cleanup verifies real image ownership and preserves active or changed versions", { timeout: 180000 }, async (t) => {
  const temporary = await mkdtemp(join(tmpdir(), "dense-mem-development-cleanup-"));
  const port = await freePort();
  const container = runDocker(["run", "--rm", "-d", "-e", "REGISTRY_STORAGE_DELETE_ENABLED=true", "-p", `127.0.0.1:${port}:5000`, "registry:2"]);
  const base = `http://127.0.0.1:${port}`;
  const repository = "markhuangai/dense-mem";
  const imageRepository = `${repository}-e2e`;
  const image = `127.0.0.1:${port}/${imageRepository}`;
  const binary = join(temporary, "regctl");
  const previousConfig = process.env.REGCTL_CONFIG;
  const revision = "a".repeat(40);
  let versions = [];
  let runs = new Map();
  let deletions = [];
  let loseDeleteResponse = false;
  let beforeRefresh = null;
  let runReads = 0;
  let restartRun = false;
  let nextVersion = 1;
  const missingAttempts = new Set();
  const runFailures = new Map();
  const runRequests = [];
  const workflowRun = (id, attempt = 1, status = "completed") => ({
    id, run_attempt: attempt, status, event: "workflow_dispatch", head_branch: "main",
    head_sha: "b".repeat(40), repository: { full_name: repository }, path: ".github/workflows/development-e2e.yml",
  });
  const apiServer = createHTTPServer(async (incoming, response) => {
    try {
      const url = new URL(incoming.url, "http://fixture");
      let status = 200;
      let body;
      if (url.pathname.includes("/actions/runs/")) {
        const match = /\/runs\/(\d+)(?:\/attempts\/(\d+))?$/.exec(url.pathname);
        const run = runs.get(Number(match[1]));
        runReads += 1;
        runRequests.push(url.pathname);
        status = runFailures.get(Number(match[1])) || (!run || missingAttempts.has(`${match[1]}/${match[2]}`) ? 404 : 200);
        if (status !== 200) body = { message: "fixture failure" };
        else {
          body = { ...run, ...(match[2] ? { run_attempt: Number(match[2]) } : {}) };
          if (restartRun && runReads >= 2) body.status = "in_progress";
        }
      } else {
        assert.match(url.pathname, /^\/orgs\/markhuangai\/packages\/container\/dense-mem-e2e\/versions/);
        const id = Number(url.pathname.split("/").at(-1));
        const found = versions.find((version) => version.id === id);
        if (incoming.method === "DELETE") {
          deletions.push(id);
          if (!found) { status = 404; body = {}; }
          else {
            await request(`${base}/v2/${imageRepository}/manifests/${found.name}`, { method: "DELETE" });
            versions = versions.filter((version) => version.id !== id);
            if (loseDeleteResponse) { loseDeleteResponse = false; return incoming.socket.destroy(); }
            status = 204;
          }
        } else if (Number.isNaN(id)) body = versions;
        else if (!found) { status = 404; body = {}; }
        else {
          if (beforeRefresh) { const change = beforeRefresh; beforeRefresh = null; await change(found); }
          body = found;
        }
      }
      response.writeHead(status, { "Content-Type": "application/json" });
      response.end(status === 204 ? undefined : JSON.stringify(body));
    } catch (error) {
      response.writeHead(500, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ message: error.message }));
    }
  });
  const addImage = async (id, attempt = 1, extraTags = [], extraLabels = {}) => {
    const tag = `run-${id}-${attempt}`;
    const labels = {
      "org.opencontainers.image.variant": "production", "org.opencontainers.image.source": `https://github.com/${repository}`,
      "org.opencontainers.image.revision": revision, "org.opencontainers.image.version": tag,
      "io.dense-mem.e2e.repository": repository, "io.dense-mem.e2e.run-id": String(id), "io.dense-mem.e2e.run-attempt": String(attempt),
      ...extraLabels,
    };
    const config = await pushBlob(base, imageRepository, JSON.stringify({ architecture: "amd64", os: "linux", config: { Labels: labels }, rootfs: { type: "layers", diff_ids: [] } }));
    const layer = await pushBlob(base, imageRepository, "");
    const manifest = { schemaVersion: 2, mediaType: "application/vnd.oci.image.manifest.v1+json",
      config: { mediaType: "application/vnd.oci.image.config.v1+json", digest: config.digest, size: config.size },
      layers: [{ mediaType: "application/vnd.oci.image.layer.v1.tar", digest: layer.digest, size: layer.size }] };
    const manifestDigest = digest(JSON.stringify(manifest));
    for (const value of [tag, ...extraTags]) await pushManifest(base, imageRepository, value, manifest);
    const version = { id: nextVersion++, name: manifestDigest, metadata: { container: { tags: [tag, ...extraTags] } } };
    versions.push(version);
    runs.set(id, workflowRun(id, attempt));
    return { version, manifest };
  };
  try {
    const response = await request("https://github.com/regclient/regclient/releases/download/v0.11.5/regctl-linux-amd64", { signal: AbortSignal.timeout(30000) });
    const bytes = Buffer.from(await response.arrayBuffer());
    assert.equal(createHash("sha256").update(bytes).digest("hex"), "c93aa7638749f5aaac1a8e01787321889c78f0101809bb2880343478d0ba0467");
    await writeFile(binary, bytes);
    await chmod(binary, 0o700);
    process.env.REGCTL_CONFIG = join(temporary, "regctl.json");
    execFileSync(binary, ["registry", "set", `127.0.0.1:${port}`, "--tls", "disabled"], { stdio: "pipe" });
    let ready = false;
    for (let attempt = 0; attempt < 120; attempt += 1) {
      try { await request(`${base}/v2/`); ready = true; break; }
      catch { await new Promise((resolve) => setTimeout(resolve, 250)); }
    }
    assert.equal(ready, true);
    await new Promise((resolve) => apiServer.listen(0, "127.0.0.1", resolve));
    const api = new GitHubApi({ apiUrl: `http://127.0.0.1:${apiServer.address().port}`, token: "fixture", repository });
    const cleanup = (run = null, dryRun = false) => cleanupDevelopmentImages({ api, repository, run, dryRun, registry: new RegistryClient({ image, binary }) });

    await t.test("completed image is removed while active and foreign-labelled images remain", async () => {
      const completed = await addImage(42);
      const active = await addImage(43);
      const retained = await addImage(44, 1, ["keep"]);
      const foreign = await addImage(45, 1, [], { "io.dense-mem.e2e.repository": "other/repository" });
      runs.set(43, workflowRun(43, 1, "in_progress"));
      const result = await cleanup();
      assert.deepEqual(result.deleted.map(({ version_id }) => version_id), [completed.version.id]);
      assert.equal(result.retained.length, 3);
      await assert.rejects(readManifest(base, imageRepository, completed.version.name), /404/);
      for (const value of [active, retained, foreign]) await readManifest(base, imageRepository, value.version.name);
      assert.deepEqual(deletions, [completed.version.id]);
    });

    await t.test("lost DELETE response retries the same version and accepts its disappearance", async () => {
      versions = []; deletions = [];
      const owned = await addImage(46);
      loseDeleteResponse = true;
      const result = await cleanup(workflowRun(46));
      assert.deepEqual(result.already_deleted, [owned.version.id]);
      assert.deepEqual(deletions, [owned.version.id, owned.version.id]);
      await assert.rejects(readManifest(base, imageRepository, owned.version.name), /404/);
      assert.deepEqual((await cleanup(workflowRun(46))).deleted, []);
    });

    await t.test("a retained tag added during revalidation prevents deletion", async () => {
      versions = []; deletions = [];
      const owned = await addImage(47);
      beforeRefresh = async (version) => {
        await pushManifest(base, imageRepository, "retained", owned.manifest);
        version.metadata.container.tags.push("retained");
      };
      assert.match((await cleanup()).retained[0].reason, /changed before deletion/);
      assert.deepEqual(deletions, []);
      await readManifest(base, imageRepository, "retained");
    });

    await t.test("a newly active run prevents deletion immediately before mutation", async () => {
      versions = []; deletions = []; runReads = 0;
      const owned = await addImage(48);
      restartRun = true;
      const result = await cleanup();
      assert.match(result.retained[0].reason, /still active/);
      assert.deepEqual(deletions, []);
      await readManifest(base, imageRepository, owned.version.name);
      restartRun = false;
    });

    await t.test("completed reruns clean older owned attempts through the recovery sweep", async () => {
      versions = []; deletions = [];
      const older = await addImage(49, 1);
      const newer = await addImage(49, 2);
      const result = await cleanup();
      assert.deepEqual(result.deleted.map(({ version_id }) => version_id), [older.version.id, newer.version.id]);
    });

    for (const lookup of ["run", "attempt"]) {
      for (const stage of ["initial verification", "pre-delete revalidation"]) {
        await t.test(`missing ${lookup} during ${stage} retains its image and permits later cleanup`, async () => {
          versions = []; deletions = []; runRequests.length = 0;
          missingAttempts.clear();
          const stale = await addImage(60);
          const completed = await addImage(61);
          if (lookup === "attempt") runs.set(60, workflowRun(60, 2));
          const removeRecord = () => {
            if (lookup === "run") runs.delete(60);
            else missingAttempts.add("60/1");
          };
          if (stage === "initial verification") removeRecord();
          else beforeRefresh = removeRecord;
          const result = await cleanup();
          assert.deepEqual(result.retained, [{ version_id: stale.version.id, reason: "development run or attempt was not found" }]);
          assert.deepEqual(result.deleted.map(({ version_id }) => version_id), [completed.version.id]);
          assert.deepEqual(result.already_deleted, []);
          assert.deepEqual(deletions, [completed.version.id]);
          await readManifest(base, imageRepository, stale.version.name);
          await assert.rejects(readManifest(base, imageRepository, completed.version.name), /404/);
          assert.equal(runRequests.filter((path) => path.endsWith("/runs/60")).length,
            stage === "initial verification" ? 1 : 2);
          if (lookup === "attempt") assert.equal(runRequests.filter((path) => path.endsWith("/runs/60/attempts/1")).length,
            stage === "initial verification" ? 1 : 2);
        });
      }
    }

    for (const status of [401, 403, 503]) {
      await t.test(`run lookup HTTP ${status} remains a required failure`, async () => {
        versions = []; deletions = []; runRequests.length = 0;
        missingAttempts.clear();
        const blocked = await addImage(62);
        const completed = await addImage(63);
        runFailures.set(62, status);
        try {
          await assert.rejects(cleanup(), (error) => error.status === status && error.attempts === (status === 503 ? 3 : 1));
          assert.deepEqual(deletions, []);
          assert.equal(runRequests.length, status === 503 ? 3 : 1);
          for (const value of [blocked, completed]) await readManifest(base, imageRepository, value.version.name);
        } finally {
          runFailures.clear();
        }
      });
    }

    await t.test("untrusted run validation remains a required failure", async () => {
      versions = []; deletions = [];
      const untrusted = await addImage(64);
      const completed = await addImage(65);
      runs.set(64, { ...workflowRun(64), head_branch: "topic" });
      await assert.rejects(cleanup(), /trusted main/);
      assert.deepEqual(deletions, []);
      for (const value of [untrusted, completed]) await readManifest(base, imageRepository, value.version.name);
    });

    await t.test("registry inspection failure remains a required failure", async () => {
      versions = []; deletions = [];
      const missing = await addImage(66);
      const completed = await addImage(67);
      await request(`${base}/v2/${imageRepository}/manifests/${missing.version.name}`, { method: "DELETE" });
      await assert.rejects(cleanup());
      assert.deepEqual(deletions, []);
      await readManifest(base, imageRepository, completed.version.name);
    });

    await t.test("completion filtering skips unrelated missing runs and retains a missing event owner", async () => {
      versions = []; deletions = []; runRequests.length = 0;
      const stale = await addImage(68);
      const completed = await addImage(69);
      runs.delete(68);
      const result = await cleanup(workflowRun(69));
      assert.deepEqual(result.deleted.map(({ version_id }) => version_id), [completed.version.id]);
      assert.deepEqual(result.retained, []);
      assert.equal(runRequests.some((path) => path.includes("/runs/68")), false);
      assert.deepEqual((await cleanup(workflowRun(68))).retained,
        [{ version_id: stale.version.id, reason: "development run or attempt was not found" }]);
      assert.deepEqual(deletions, [completed.version.id]);
      await readManifest(base, imageRepository, stale.version.name);
    });

    await t.test("cancelled completions clean only their own event image", async () => {
      versions = []; deletions = [];
      const cancelled = await addImage(50);
      const other = await addImage(51);
      const event = { ...workflowRun(50), conclusion: "cancelled" };
      const result = await cleanup(event);
      assert.deepEqual(result.deleted.map(({ version_id }) => version_id), [cancelled.version.id]);
      await readManifest(base, imageRepository, other.version.name);
      await assert.rejects(cleanup({ ...event, head_branch: "topic" }), /trusted main/);
      await assert.rejects(cleanup({ ...event, path: ".github/workflows/untrusted.yml" }), /trusted main/);
    });

    await t.test("dry run retains a completed owned image", async () => {
      versions = []; deletions = [];
      const owned = await addImage(52);
      assert.match((await cleanup(null, true)).retained[0].reason, /dry run/);
      assert.deepEqual(deletions, []);
      await readManifest(base, imageRepository, owned.version.name);
    });

    await t.test("dry run retains missing-run and completed images without deletion", async () => {
      versions = []; deletions = [];
      const stale = await addImage(70);
      const completed = await addImage(71);
      runs.delete(70);
      const result = await cleanup(null, true);
      assert.deepEqual(result.deleted, []);
      assert.deepEqual(result.retained.map(({ version_id, reason }) => ({ version_id, reason })), [
        { version_id: stale.version.id, reason: "development run or attempt was not found" },
        { version_id: completed.version.id, reason: "dry run" },
      ]);
      assert.deepEqual(deletions, []);
      for (const value of [stale, completed]) await readManifest(base, imageRepository, value.version.name);
    });
  } finally {
    apiServer.closeAllConnections();
    await new Promise((resolve) => apiServer.close(resolve));
    if (previousConfig === undefined) delete process.env.REGCTL_CONFIG;
    else process.env.REGCTL_CONFIG = previousConfig;
    runDocker(["rm", "-f", container]);
    await rm(temporary, { recursive: true });
  }
});
