import assert from "node:assert/strict";
import { mkdtemp, rm, stat } from "node:fs/promises";
import { createServer, request as httpRequest } from "node:http";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import { tmpdir } from "node:os";
import { test } from "node:test";

const proxyScript = fileURLToPath(new URL("../../scripts/e2e-docker-proxy.mjs", import.meta.url));
const contract = "dense-mem-ci-e2e.v1";
const repository = "markhuangai/dense-mem";
const project = "densemem-ci-123-1-exclusive-mcp-boundaries";
const digest = `sha256:${"a".repeat(64)}`;

test("restricted Docker proxy permits scoped lifecycle and exec upgrade only", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-proxy-"));
  const targetSocket = join(directory, "target.sock");
  const proxySocket = join(directory, "proxy.sock");
  const requests = [];
  const labels = {
    "io.dense-mem.ci.contract": contract,
    "io.dense-mem.ci.repository": repository,
    "io.dense-mem.ci.run-id": "123",
    "io.dense-mem.ci.run-attempt": "1",
    "io.dense-mem.ci.phase": "exclusive",
    "io.dense-mem.ci.scenario": "mcp_boundaries",
    "io.dense-mem.ci.image-digest": digest,
    "io.dense-mem.ci.created-at": new Date().toISOString(),
    "com.docker.compose.project": project,
  };
  const target = createServer((request, response) => {
    requests.push(`${request.method} ${request.url}`);
    const path = new URL(request.url || "/", "http://docker").pathname;
    if (path === "/v1.45/containers/demo/json") {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ Id: "demo", Config: { Labels: labels } }));
      return;
    }
    if (path === "/v1.45/containers/foreign/json") {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ Id: "foreign", Config: { Labels: { ...labels, "com.docker.compose.project": "other" } } }));
      return;
    }
    if (path === "/v1.45/containers/demo/logs") {
      response.writeHead(200, { "content-type": "text/plain" });
      response.end("scoped logs\n");
      return;
    }
    if (path === "/v1.45/containers/demo/restart") {
      response.writeHead(204);
      response.end();
      return;
    }
    if (path === "/v1.45/containers/demo/exec" && request.method === "POST") {
      response.writeHead(201, { "content-type": "application/json" });
      response.end(JSON.stringify({ Id: "exec-1" }));
      return;
    }
    response.writeHead(404);
    response.end();
  });
  target.on("upgrade", (_request, socket) => {
    socket.write("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n");
    socket.write("exec-ready");
    setTimeout(() => socket.destroy(), 20);
  });
  await listen(target, targetSocket);
  const proxy = spawn(process.execPath, [
    proxyScript, "--listen", proxySocket, "--target", targetSocket, "--project", project,
    "--contract", contract, "--repository", repository, "--run-id", "123", "--attempt", "1",
    "--phase", "exclusive", "--scenario", "mcp_boundaries", "--image-digest", digest,
  ], { stdio: ["ignore", "pipe", "pipe"] });
  t.after(async () => {
    proxy.kill("SIGTERM");
    await onceExit(proxy);
    await close(target);
    await rm(directory, { recursive: true, force: true });
  });
  await waitForSocket(proxySocket);

  assert.equal((await requestProxy(proxySocket, "GET", "/v1.45/containers/demo/logs")).status, 200);
  assert.equal((await requestProxy(proxySocket, "POST", "/v1.45/containers/demo/restart")).status, 204);
  assert.equal((await requestProxy(proxySocket, "POST", "/v1.45/containers/demo/exec", JSON.stringify({ Cmd: ["true"] }))).status, 201);
  const upgraded = await requestUpgrade(proxySocket, "/v1.45/exec/exec-1/start");
  assert.equal(upgraded.status, 101);
  assert.match(upgraded.body, /exec-ready/);

  const foreign = await requestProxy(proxySocket, "GET", "/v1.45/containers/foreign/logs");
  assert.equal(foreign.status, 403);
  assert.equal((await requestProxy(proxySocket, "GET", "/v1.45/images/json")).status, 403);
  assert.ok(requests.includes("POST /v1.45/containers/demo/exec"));
});

test("shared Docker proxy exposes the shared stack and only the assigned row", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-proxy-shared-"));
  const targetSocket = join(directory, "target.sock");
  const proxySocket = join(directory, "proxy.sock");
  const sharedProject = "densemem-ci-123-1-shared-shared";
  const stackLabels = {
    "io.dense-mem.ci.contract": contract,
    "io.dense-mem.ci.repository": repository,
    "io.dense-mem.ci.run-id": "123",
    "io.dense-mem.ci.run-attempt": "1",
    "io.dense-mem.ci.phase": "shared",
    "io.dense-mem.ci.scenario": "shared",
    "io.dense-mem.ci.image-digest": digest,
    "io.dense-mem.ci.created-at": new Date().toISOString(),
    "com.docker.compose.project": sharedProject,
  };
  const ownLabels = { ...stackLabels, "io.dense-mem.ci.scenario": "mcp_sdk_parity" };
  const otherLabels = { ...stackLabels, "io.dense-mem.ci.scenario": "mcp_sdk_transport" };
  const target = createServer((request, response) => {
    const path = new URL(request.url || "/", "http://docker").pathname;
    const match = path.match(/^\/v1\.45\/containers\/([^/]+)\/json$/);
    if (match) {
      const labelsByID = { shared: stackLabels, own: ownLabels, other: otherLabels };
      const labels = labelsByID[match[1]];
      if (!labels) {
        response.writeHead(404);
        response.end();
        return;
      }
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ Id: match[1], Config: { Labels: labels } }));
      return;
    }
    if (path === "/v1.45/containers/json") {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify([
        { Id: "shared", Labels: stackLabels },
        { Id: "own", Labels: ownLabels },
        { Id: "other", Labels: otherLabels },
      ]));
      return;
    }
    response.writeHead(404);
    response.end();
  });
  await listen(target, targetSocket);
  const proxy = spawn(process.execPath, [
    proxyScript, "--listen", proxySocket, "--target", targetSocket, "--project", sharedProject,
    "--contract", contract, "--repository", repository, "--run-id", "123", "--attempt", "1",
    "--phase", "shared", "--scenario", "mcp_sdk_parity", "--image-digest", digest,
  ], { stdio: ["ignore", "pipe", "pipe"] });
  t.after(async () => {
    proxy.kill("SIGTERM");
    await onceExit(proxy);
    await close(target);
    await rm(directory, { recursive: true, force: true });
  });
  await waitForSocket(proxySocket);

  assert.equal((await requestProxy(proxySocket, "GET", "/v1.45/containers/shared/json")).status, 200);
  const listing = await requestProxy(proxySocket, "GET", `/v1.45/containers/json?all=1&filters=%7B%22label%22:%5B%22com.docker.compose.project%3D${sharedProject}%22%5D%7D`);
  assert.deepEqual(JSON.parse(listing.body).map(({ Id }) => Id), ["shared", "own"]);
  assert.equal((await requestProxy(proxySocket, "GET", "/v1.45/containers/other/json")).status, 403);
});

test("precheck proxy scopes resource creation and filters listings", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-proxy-precheck-"));
  const targetSocket = join(directory, "target.sock");
  const proxySocket = join(directory, "proxy.sock");
  const precheckProject = "densemem-ci-123-1-precheck";
  const precheckNetwork = precheckProject;
  const precheckLabels = {
    "io.dense-mem.ci.contract": contract,
    "io.dense-mem.ci.repository": repository,
    "io.dense-mem.ci.run-id": "123",
    "io.dense-mem.ci.run-attempt": "1",
    "io.dense-mem.ci.phase": "precheck",
    "io.dense-mem.ci.scenario": "precheck",
    "io.dense-mem.ci.image-digest": digest,
    "io.dense-mem.ci.created-at": new Date().toISOString(),
    "com.docker.compose.project": precheckProject,
  };
  const forwardedBodies = [];
  const target = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    if (chunks.length > 0) forwardedBodies.push(Buffer.concat(chunks).toString("utf8"));
    const path = new URL(request.url || "/", "http://docker").pathname;
    if (path === "/v1.45/containers/create") {
      response.writeHead(201, { "content-type": "application/json" });
      response.end(JSON.stringify({ Id: "precheck-container" }));
      return;
    }
    if (path === "/v1.45/networks/create") {
      response.writeHead(201, { "content-type": "application/json" });
      response.end(JSON.stringify({ Id: "precheck-network" }));
      return;
    }
    if (path === "/v1.45/containers/json") {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify([
        { Id: "precheck-container", Labels: precheckLabels },
        { Id: "foreign", Labels: { "io.dense-mem.ci.contract": contract } },
      ]));
      return;
    }
    if (path === "/v1.45/containers/precheck-container/json") {
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ Id: "precheck-container", Config: { Labels: precheckLabels } }));
      return;
    }
    response.writeHead(204);
    response.end();
  });
  await listen(target, targetSocket);
  const proxy = spawn(process.execPath, [
    proxyScript, "--listen", proxySocket, "--target", targetSocket, "--project", precheckProject,
    "--contract", contract, "--repository", repository, "--mode", "precheck", "--run-id", "123", "--attempt", "1",
    "--phase", "precheck", "--scenario", "precheck", "--image-digest", digest, "--network", precheckNetwork,
  ], { stdio: ["ignore", "pipe", "pipe"] });
  t.after(async () => {
    proxy.kill("SIGTERM");
    await onceExit(proxy);
    await close(target);
    await rm(directory, { recursive: true, force: true });
  });
  await waitForSocket(proxySocket);

  const create = await requestProxy(proxySocket, "POST", "/v1.45/containers/create", JSON.stringify({
    Image: "pgvector/pgvector:0.8.2-pg18-trixie", Labels: precheckLabels,
    HostConfig: { PortBindings: { "5432/tcp": [{ HostIp: "127.0.0.1", HostPort: "0" }] } },
  }));
  assert.equal(create.status, 201);
  assert.deepEqual(JSON.parse(forwardedBodies[0]).HostConfig, {});
  const network = await requestProxy(proxySocket, "POST", "/v1.45/networks/create", JSON.stringify({
    Name: precheckNetwork, Driver: "bridge", Attachable: true,
  }));
  assert.equal(network.status, 201);
  const listing = await requestProxy(proxySocket, "GET", "/v1.45/containers/json");
  assert.deepEqual(JSON.parse(listing.body).map(({ Id }) => Id), ["precheck-container"]);
  const unsafe = await requestProxy(proxySocket, "POST", "/v1.45/containers/create", JSON.stringify({
    Image: "pgvector/pgvector:0.8.2-pg18-trixie", Labels: precheckLabels, HostConfig: { Binds: ["/etc:/host"] },
  }));
  assert.equal(unsafe.status, 403);
});

async function requestProxy(socketPath, method, path, body = "") {
  return new Promise((resolve, reject) => {
    const request = httpRequest({ socketPath, method, path, headers: body ? { "content-type": "application/json", "content-length": Buffer.byteLength(body) } : {} }, (response) => {
      let text = "";
      response.setEncoding("utf8");
      response.on("data", (chunk) => { text += chunk; });
      response.on("end", () => resolve({ status: response.statusCode, body: text }));
    });
    request.on("error", reject);
    request.end(body);
  });
}

async function requestUpgrade(socketPath, path) {
  return new Promise((resolve, reject) => {
    const request = httpRequest({ socketPath, method: "POST", path, headers: { Connection: "Upgrade", Upgrade: "tcp" } });
    request.on("upgrade", (response, socket, head) => {
      let body = head.toString();
      socket.on("data", (chunk) => { body += chunk.toString(); });
      setTimeout(() => { socket.destroy(); resolve({ status: response.statusCode, body }); }, 20);
    });
    request.on("error", reject);
    request.end();
  });
}

async function listen(server, path) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(path, resolve);
  });
}

async function waitForSocket(path) {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    try { await stat(path); return; } catch {}
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`timed out waiting for socket ${path}`);
}

async function onceExit(child) {
  if (child.exitCode !== null) return;
  await new Promise((resolve) => child.once("exit", resolve));
}

async function close(server) {
  await new Promise((resolve) => server.close(resolve));
}
