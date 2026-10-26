import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFile, spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdir, readFile, writeFile, rm } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";

const exec = promisify(execFile);
const digest = (body) => `sha256:${createHash("sha256").update(body).digest("hex")}`;
const docker = async (...args) => (await exec("docker", args, { maxBuffer: 32 * 1024 * 1024 })).stdout.trim();
const delay = (ms) => new Promise(resolve => setTimeout(resolve, ms));

async function serve(directory) {
  const manifest = await readFile(join(directory, "manifest.json"));
  const server = createServer(async (request, response) => {
    try {
      if (request.url === "/v2/") {
        response.writeHead(200, { "Docker-Distribution-Api-Version": "registry/2.0" }).end("{}");
        return;
      }
      const state = JSON.parse(await readFile(join(directory, "state.json"), "utf8"));
      if (state.status !== 200 && request.url.includes("/manifests/")) {
        response.writeHead(state.status, { "Content-Type": "application/json" }).end(JSON.stringify({
          errors: [{ code: state.status === 401 ? "UNAUTHORIZED" : "UNKNOWN", message: "controlled registry failure" }],
        }));
        return;
      }
      let body, type;
      if (/^\/v2\/[a-z0-9-]+\/manifests\/(latest|sha256:[0-9a-f]{64})$/.test(request.url)) {
        body = manifest;
        type = "application/vnd.oci.image.manifest.v1+json";
      } else {
        const match = request.url.match(/^\/v2\/[a-z0-9-]+\/blobs\/sha256:([0-9a-f]{64})$/);
        if (!match) { response.writeHead(404).end(); return; }
        body = await readFile(join(directory, match[1]));
        type = "application/octet-stream";
      }
      response.writeHead(200, { "Content-Type": type, "Content-Length": body.length, "Docker-Content-Digest": digest(body) });
      response.end(request.method === "HEAD" ? undefined : body);
    } catch (error) {
      response.writeHead(500).end(String(error));
    }
  });
  server.listen(0, "127.0.0.1", () => process.stdout.write(`${JSON.stringify({ port: server.address().port })}\n`));
}

async function runCases(args) {
  const [controller, root, testRoot, composePath, prefix, attempt, protectedProject, image] = args;
  const directory = join(testRoot, "registry");
  await mkdir(directory);
  const originalCompose = await readFile(composePath, "utf8");
  const archive = join(directory, "alpine.tar");
  await docker("image", "save", "-o", archive, "alpine:3.24");
  const saved = JSON.parse((await exec("tar", ["-xOf", archive, "manifest.json"])).stdout)[0];
  async function blob(path, type) {
    const { stdout } = await exec("tar", ["-xOf", archive, path], { encoding: "buffer", maxBuffer: 32 * 1024 * 1024 });
    const hash = digest(stdout);
    await writeFile(join(directory, hash.slice(7)), stdout);
    return { mediaType: type + (type.endsWith(".tar") && stdout[0] === 0x1f && stdout[1] === 0x8b ? "+gzip" : ""), digest: hash, size: stdout.length };
  }
  const config = await blob(saved.Config, "application/vnd.oci.image.config.v1+json");
  const layers = [];
  for (const path of saved.Layers) layers.push(await blob(path, "application/vnd.oci.image.layer.v1.tar"));
  await writeFile(join(directory, "manifest.json"), JSON.stringify({ schemaVersion: 2, mediaType: "application/vnd.oci.image.manifest.v1+json", config, layers }));
  await rm(archive);
  await writeFile(join(directory, "state.json"), JSON.stringify({ status: 200 }));
  await writeFile(join(directory, "server.mjs"), await readFile(new URL(import.meta.url)));
  let registry = "";
  const projects = new Set();
  const acquiredImages = new Set();
  const builtImage = `densemem-ci-${prefix}-${attempt}-local-helper:built`;
  let builtImageID;
  const repository = process.env.DENSE_MEM_CI_REPOSITORY;
  const protectedIDs = await docker("ps", "-q", "--filter", `label=com.docker.compose.project=${protectedProject}`);
  assert.ok(protectedIDs);
  const labels = ["--label", "io.dense-mem.ci.contract=dense-mem-ci-e2e.v1", "--label", `io.dense-mem.ci.repository=${repository}`];
  async function setStatus(status) {
    await docker("exec", registry, "node", "-e", "const fs=require('node:fs');fs.writeFileSync('/fixture/state.next',process.argv[1]);fs.renameSync('/fixture/state.next','/fixture/state.json')", JSON.stringify({ status }));
  }
  async function assertClean(runID) {
    for (const kind of ["container", "network", "volume"]) {
      const ids = await docker(kind, "ls", ...(kind === "container" ? ["-a"] : []), "-q",
        "--filter", `label=io.dense-mem.ci.repository=${repository}`, "--filter", `label=io.dense-mem.ci.run-id=${runID}`);
      assert.equal(ids, "", `${kind} leaked for run ${runID}`);
    }
  }
  async function start(runID, onOutput = () => {}) {
    const child = spawn(controller, ["start", runID, attempt, "shared", "shared", image, root], { detached: true });
    let stdout = "", stderr = "", callbackError;
    let updates = Promise.resolve();
    child.stdout.on("data", chunk => { stdout += chunk; });
    child.stderr.on("data", chunk => {
      stderr += chunk;
      for (const match of stderr.matchAll(/project=(densemem-ci-[a-z0-9-]+)/g)) projects.add(match[1]);
      updates = updates.then(() => onOutput(stderr, child)).catch(error => { callbackError = error; });
    });
    const result = await new Promise((resolve, reject) => {
      child.on("error", reject);
      child.on("close", (code, signal) => resolve({ code, signal }));
    });
    await updates;
    if (callbackError) throw callbackError;
    assert.doesNotMatch(stderr, /unbound variable/);
    process.stdout.write(stderr);
    return { ...result, stdout: stdout.trim(), stderr };
  }
  try {
    await docker("image", "tag", "alpine:3.24", builtImage);
    acquiredImages.add(builtImage);
    builtImageID = await docker("image", "inspect", "--format", "{{.Id}}", builtImage);
    registry = await docker("create", "--network", "host", ...labels, "node:22-alpine", "node", "/fixture/server.mjs", "serve", "/fixture");
    await docker("cp", `${directory}/.`, `${registry}:/fixture`);
    await docker("start", registry);
    let port;
    for (let i = 0; i < 100; i++) {
      const log = await docker("logs", registry);
      if (log.trim()) { port = JSON.parse(log.trim()).port; break; }
      await delay(100);
    }
    assert.ok(port, "fault registry did not become ready");
    let index = 20;
    for (const mode of ["recover", "exhaust", "denied", "partial", "signal"]) {
      const runID = `${prefix}${index++}`;
      const reference = `127.0.0.1:${port}/${mode}:latest`;
      acquiredImages.add(reference);
      let compose = originalCompose.replace("  redis:\n    image: alpine:3.24", `  redis:\n    image: ${reference}`);
      assert.notEqual(compose, originalCompose);
      compose = compose.replace("  prometheus:\n", `  local-built-helper:\n    image: ${builtImage}\n    command: ["sh", "-c", "sleep infinity"]\n    networks: [ci]\n    labels: *ci_labels\n  prometheus:\n`);
      if (mode === "partial") compose = compose.replace('  server:\n    image: alpine:3.24\n    command: ["sh","-c","while :; do sleep 3600; done"]', '  server:\n    image: alpine:3.24\n    command: ["sh","-c","exit 1"]');
      await writeFile(composePath, compose, { mode: 0o600 });
      await setStatus(mode === "denied" ? 401 : ["recover", "exhaust"].includes(mode) ? 500 : 200);
      let recovered = false, signalled = false;
      const started = Date.now();
      const since = Math.floor(started / 1000);
      const result = await start(runID, async (output, child) => {
        if (mode === "recover" && !recovered && output.includes("retrying once in 2 seconds")) {
          recovered = true;
          await setStatus(200);
        }
        if (mode === "signal" && !signalled && output.includes("phase=image_acquisition") && output.includes("status=0")) {
          signalled = true;
          for (let i = 0; i < 100; i++) {
            const ids = await docker("ps", "-aq", "--filter", `label=io.dense-mem.ci.run-id=${runID}`, "--filter", "label=com.docker.compose.service=postgres");
            if (ids) { process.kill(-child.pid, "SIGTERM"); return; }
            await delay(100);
          }
          throw new Error("signal case never reached partial stack creation");
        }
      });
      const acquisition = [...result.stderr.matchAll(/phase=image_acquisition[^\n]*status=(\d+)[^\n]*attempt=(\d+)/g)];
      if (mode === "recover") {
        assert.equal(result.code, 0, result.stderr);
        assert.equal(recovered, true);
        assert.match(result.stderr, /500 Internal Server Error/);
        assert.deepEqual(acquisition.map(match => match[2]), ["1", "2"]);
        assert.equal(acquisition[1][1], "0");
        projects.add(result.stdout);
        const events = await docker("events", "--since", String(since), "--until", String(Math.floor(Date.now() / 1000) + 1),
          "--filter", `label=com.docker.compose.project=${result.stdout}`, "--filter", "event=start", "--format", "{{json .}}");
        const fixtures = events.split("\n").filter(Boolean).map(line => JSON.parse(line))
          .filter(event => event.Actor?.Attributes?.["com.docker.compose.service"] === "postgres-init");
        assert.equal(fixtures.length, 1, "fixture was replayed during acquisition recovery");
        await docker("exec", (await docker("ps", "-q", "--filter", `label=com.docker.compose.project=${result.stdout}`, "--filter", "label=com.docker.compose.service=redis")), "sh", "-c", "test -f /etc/alpine-release");
        await exec(controller, ["stop", result.stdout]);
      } else {
        assert.ok(result.code !== 0 || result.signal, `${mode} unexpectedly succeeded`);
        assert.equal(result.stdout, "");
        assert.equal(acquisition.length, mode === "exhaust" ? 2 : 1);
        if (mode === "exhaust") assert.match(result.stderr, /500 Internal Server Error/);
        else assert.doesNotMatch(result.stderr, /retrying once/);
        if (mode === "partial") assert.match(result.stderr, /Compose stack failed to start/);
        if (mode === "signal") assert.equal(signalled, true);
      }
      await assertClean(runID);
      assert.equal(await docker("image", "inspect", "--format", "{{.Id}}", builtImage), builtImageID);
      assert.equal(await docker("ps", "-q", "--filter", `label=com.docker.compose.project=${protectedProject}`), protectedIDs);
      process.stdout.write(`${JSON.stringify({ case: mode, elapsed_ms: Date.now() - started, pull_attempts: acquisition.length, cleanup: "passed", protected_project: "unchanged" })}\n`);
    }
  } finally {
    await writeFile(composePath, originalCompose, { mode: 0o600 });
    for (const project of projects) await exec(controller, ["stop", project]);
    if (registry) await docker("rm", "-f", registry);
    for (const reference of acquiredImages) {
      try { await docker("image", "inspect", reference); }
      catch { continue; }
      await docker("image", "rm", reference);
    }
    await rm(directory, { recursive: true });
  }
}

if (process.argv[2] === "serve") await serve(process.argv[3]);
else await runCases(process.argv.slice(2));
