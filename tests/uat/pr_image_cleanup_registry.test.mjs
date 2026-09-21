import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { createServer } from "node:net";
import { test } from "node:test";

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
