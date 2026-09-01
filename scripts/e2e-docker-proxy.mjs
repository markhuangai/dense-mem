#!/usr/bin/env node

import { createServer, request as httpRequest } from "node:http";
import { createConnection } from "node:net";
import { chmodSync, existsSync, unlinkSync } from "node:fs";
import { basename } from "node:path";

const options = parseArgs(process.argv.slice(2));
const execIDs = new Set();
const precheckResources = {
  containers: new Set(),
  networks: new Set(),
  volumes: new Set(),
};
const sockets = new Set();
const upstreamSockets = new Set();
let shuttingDown = false;

if (existsSync(options.listen)) unlinkSync(options.listen);

const server = createServer((request, response) => {
  handleRequest(request, response).catch((error) => {
    response.writeHead(error.statusCode || 502, { "content-type": "text/plain; charset=utf-8" });
    response.end(error.message || "Docker proxy request failed");
  });
});
server.on("connection", (socket) => {
  sockets.add(socket);
  socket.once("close", () => sockets.delete(socket));
});

server.on("upgrade", (request, socket, head) => {
  handleUpgrade(request, socket, head).catch(() => {
    socket.end("HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n");
  });
});

server.listen(options.listen, () => {
  chmodSync(options.listen, 0o660);
  process.stdout.write(`restricted Docker proxy listening on ${options.listen}\n`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, async () => {
    if (shuttingDown) return;
    shuttingDown = true;
    const shutdownTimer = setTimeout(() => process.exit(0), 5000);
    shutdownTimer.unref();
    if (options.mode === "precheck") await cleanupPrecheckResources();
    server.closeAllConnections?.();
    for (const socket of sockets) socket.destroy();
    for (const socket of upstreamSockets) socket.destroy();
    server.close(() => {
      clearTimeout(shutdownTimer);
      process.exit(0);
    });
  });
}

async function handleRequest(request, response) {
  const path = normalizedPath(request.url);
  const requestBody = request.method === "POST" ? await readRequestBody(request) : null;
  await authorize(request.method, path, request.url, requestBody);
  const forwardedBody = augmentPrecheckRequest(path, requestBody);
  const forwardedHeaders = { ...request.headers };
  if (forwardedBody !== null) {
    delete forwardedHeaders["transfer-encoding"];
    forwardedHeaders["content-length"] = Buffer.byteLength(forwardedBody);
  }
  const upstream = requestTarget(request.method, request.url, forwardedHeaders);
  if (forwardedBody === null) request.pipe(upstream.request);
  else upstream.request.end(forwardedBody);
  try {
    const { statusCode, headers, body } = await upstream.response;
    trackCreatedResource(path, requestBody, statusCode, body);
    const responseBody = path === "/containers/json" && options.mode === "precheck"
      ? filterPrecheckContainerList(body)
      : path === "/containers/json"
        ? filterScenarioContainerList(body)
      : /^\/containers\/[^/]+\/json$/.test(path)
        ? sanitizeContainerInspect(body)
      : options.mode === "precheck" && path === "/networks"
        ? filterPrecheckNetworkList(body)
        : body;
    const responseHeaders = { ...headers };
    if (responseBody !== body) {
      delete responseHeaders["transfer-encoding"];
      responseHeaders["content-length"] = Buffer.byteLength(responseBody);
    }
    response.writeHead(statusCode, responseHeaders);
    response.end(responseBody);
  } catch (error) {
    response.writeHead(error.statusCode || 502, { "content-type": "text/plain; charset=utf-8" });
    response.end(error.message || "Docker proxy request failed");
  }
}

async function handleUpgrade(request, socket, head) {
  const path = normalizedPath(request.url);
  await authorize(request.method, path, request.url, null);
  const target = createConnection({ path: options.target });
  upstreamSockets.add(target);
  target.once("close", () => upstreamSockets.delete(target));
  target.once("connect", () => {
    const headers = Object.entries(request.headers)
      .filter(([name]) => name !== "host")
      .map(([name, value]) => `${name}: ${Array.isArray(value) ? value.join(", ") : value}`)
      .join("\r\n");
    target.write(`${request.method} ${request.url} HTTP/1.1\r\n${headers}\r\n\r\n`);
    if (head.length > 0) target.write(head);
    request.socket.pipe(target).pipe(request.socket);
  });
  target.on("error", () => socket.destroy());
}

async function authorize(method, path, rawURL, requestBody) {
  if (["/_ping", "/version", "/info"].includes(path)) return;
  if (options.mode === "precheck") {
    await authorizePrecheck(method, path, rawURL, requestBody);
    return;
  }
  if (path === "/containers/json" && method === "GET") {
    const decoded = decodeURIComponent(rawURL || "");
    if (!decoded.includes(options.project)) deny("container listing must be scoped to the assigned project");
    return;
  }

  const containerMatch = path.match(/^\/containers\/([^/]+)(?:\/([^/]+))?$/);
  if (containerMatch && ["GET", "POST"].includes(method)) {
    const action = containerMatch[2] || "";
    if (!["json", "logs", "start", "stop", "restart", "wait", "stats", "top", "changes", "archive", "exec"].includes(action)) {
      deny("Docker container operation is not permitted");
    }
    await authorizeResource("containers", containerMatch[1]);
    return;
  }

  const execMatch = path.match(/^\/exec\/([^/]+)(?:\/([^/]+))?$/);
  if (execMatch && execIDs.has(execMatch[1]) && ["GET", "POST"].includes(method)) return;

  const networkMatch = path.match(/^\/networks\/([^/]+)$/);
  if (networkMatch && method === "GET") {
    await authorizeResource("networks", networkMatch[1]);
    return;
  }
  const volumeMatch = path.match(/^\/volumes\/([^/]+)$/);
  if (volumeMatch && method === "GET") {
    await authorizeResource("volumes", volumeMatch[1]);
    return;
  }
  deny("Docker API operation is not permitted by the CI scenario contract");
}

async function authorizePrecheck(method, path, rawURL, requestBody) {
  if (path === "/containers/json" && method === "GET") {
    return;
  }
  if (path === "/networks" && method === "GET") return;
  if (path === "/networks/bridge" && method === "GET") return;
  if (path.match(/^\/images\/.+\/json$/) && method === "GET") {
    const image = decodeURIComponent(path.slice("/images/".length, -"/json".length));
    if (!image.startsWith("pgvector/pgvector") && !image.startsWith("sha256:")) deny("precheck image inspection is not permitted");
    return;
  }
  if (path === "/images/create" && method === "POST") {
    const query = new URL(rawURL || "/", "http://docker").searchParams;
    const image = query.get("fromImage") || "";
    const tag = query.get("tag") || "";
    if (image !== "pgvector/pgvector" && image !== "pgvector/pgvector:0.8.2-pg18-trixie") {
      deny("precheck image pull is not permitted");
    }
    if (image === "pgvector/pgvector" && tag !== "0.8.2-pg18-trixie") deny("precheck image tag is not permitted");
    return;
  }
  if (path === "/containers/create" && method === "POST") {
    const payload = parseJSONBody(requestBody, "container create");
    assertPrecheckLabels(payload.Labels);
    const hostConfig = payload.HostConfig || {};
    if (
      hostConfig.Privileged === true ||
      hostConfig.NetworkMode === "host" ||
      hostConfig.PidMode === "host" ||
      hostConfig.IpcMode === "host" ||
      hostConfig.UTSMode === "host" ||
      hostConfig.UsernsMode === "host" ||
      hasConfiguredEntries(hostConfig.Binds) ||
      hasConfiguredEntries(hostConfig.Mounts) ||
      hasConfiguredEntries(hostConfig.VolumesFrom) ||
      hasConfiguredEntries(hostConfig.Tmpfs) ||
      hasConfiguredEntries(payload.Mounts)
    ) deny("precheck containers cannot use host privileges or mounts");
    assertSafePortBindings(hostConfig.PortBindings);
    if (typeof payload.Image !== "string" || payload.Image !== "pgvector/pgvector:0.8.2-pg18-trixie") {
      deny("precheck container image is not permitted");
    }
    return;
  }
  if (path === "/networks/create" || path === "/volumes/create") {
    if (method !== "POST") deny("precheck resource creation method is not permitted");
    const payload = parseJSONBody(requestBody, "resource create");
    if (path === "/networks/create") {
      if (payload.Name !== options.network || (payload.Driver && payload.Driver !== "bridge")) deny("precheck network is not permitted");
    } else {
      assertPrecheckLabels(payload.Labels);
    }
    return;
  }

  const containerMatch = path.match(/^\/containers\/([^/]+)(?:\/([^/]+))?$/);
  if (containerMatch && ["GET", "POST", "DELETE"].includes(method)) {
    const action = containerMatch[2] || "";
    if (!["json", "logs", "start", "stop", "restart", "wait", "stats", "top", "changes", "archive", "exec", "kill"].includes(action) && method !== "DELETE") {
      deny("precheck container operation is not permitted");
    }
    await authorizeResource("containers", containerMatch[1]);
    return;
  }
  const execMatch = path.match(/^\/exec\/([^/]+)(?:\/([^/]+))?$/);
  if (execMatch && execIDs.has(execMatch[1]) && ["GET", "POST"].includes(method)) return;
  const networkMatch = path.match(/^\/networks\/([^/]+)$/);
  if (networkMatch && ["GET", "DELETE"].includes(method)) {
    await authorizeResource("networks", networkMatch[1]);
    return;
  }
  const volumeMatch = path.match(/^\/volumes\/([^/]+)$/);
  if (volumeMatch && ["GET", "DELETE"].includes(method)) {
    await authorizeResource("volumes", volumeMatch[1]);
    return;
  }
  deny("Docker API operation is not permitted by the precheck contract");
}

function assertPrecheckLabels(labels) {
  if (!hasManagedLabels(labels) || labels["io.dense-mem.ci.phase"] !== "precheck" || labels["io.dense-mem.ci.scenario"] !== options.scenario) {
    deny("precheck resource labels do not match the assigned run");
  }
}

function hasManagedLabels(labels) {
  return labels &&
    labels["io.dense-mem.ci.contract"] === options.contract &&
    labels["io.dense-mem.ci.repository"] === options.repository &&
    labels["io.dense-mem.ci.run-id"] === options.runId &&
    labels["io.dense-mem.ci.run-attempt"] === options.attempt &&
    labels["io.dense-mem.ci.phase"] === options.phase &&
    scenarioLabelAllowed(labels) &&
    labels["io.dense-mem.ci.image-digest"] === options.imageDigest &&
    typeof labels["io.dense-mem.ci.created-at"] === "string" &&
    labels["io.dense-mem.ci.created-at"].length > 0 &&
    labels["com.docker.compose.project"] === options.project;
}

function scenarioLabelAllowed(labels) {
  const scenario = labels?.["io.dense-mem.ci.scenario"];
  return scenario === options.scenario ||
    (options.mode === "scenario" && options.phase === "shared" && scenario === "shared");
}

function assertSafePortBindings(bindings) {
  if (!bindings || typeof bindings !== "object") return;
  for (const values of Object.values(bindings)) {
    if (!Array.isArray(values)) deny("precheck port bindings are invalid");
    for (const value of values) {
      const hostIP = value?.HostIp || value?.HostIP || "";
      const hostPort = value?.HostPort || "";
      if (hostIP && hostIP !== "127.0.0.1" && hostIP !== "::1") deny("precheck port bindings must stay on loopback");
      if (hostPort && !/^0$|^[1-9][0-9]{0,4}$/.test(hostPort)) deny("precheck port binding is invalid");
    }
  }
}

function hasConfiguredEntries(value) {
  if (value === undefined || value === null) return false;
  if (Array.isArray(value)) return value.length > 0;
  if (typeof value === "object") return Object.keys(value).length > 0;
  return String(value).length > 0;
}

function sanitizeContainerInspect(body) {
  try {
    const inspected = JSON.parse(body || "{}");
    if (inspected && typeof inspected === "object" && inspected.Config && typeof inspected.Config === "object") {
      delete inspected.Config.Env;
    }
    return JSON.stringify(inspected);
  } catch {
    const error = new Error("Docker container inspection response was invalid");
    error.statusCode = 502;
    throw error;
  }
}

async function authorizeResource(kind, id) {
  const inspected = await inspectResource(kind, id);
  const labels = inspected?.Config?.Labels || inspected?.Labels || {};
  if (options.mode === "precheck") {
    assertPrecheckLabels(labels);
    precheckResources[kind].add(id);
    return;
  }
  if (!hasManagedLabels(labels)) {
    deny(`Docker ${kind} resource ${basename(id)} is outside the assigned CI project`);
  }
}

function parseJSONBody(body, label) {
  try {
    return JSON.parse(body || "{}");
  } catch {
    deny(`invalid ${label} request`);
  }
}

function readRequestBody(request) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    request.on("data", (chunk) => {
      size += chunk.length;
      if (size > 2 * 1024 * 1024) {
        const error = new Error("Docker proxy request body is too large");
        error.statusCode = 413;
        reject(error);
        request.destroy();
        return;
      }
      chunks.push(chunk);
    });
    request.on("end", () => resolve(Buffer.concat(chunks)));
    request.on("error", reject);
  });
}

function trackCreatedResource(path, requestBody, statusCode, responseBody) {
  if (statusCode < 200 || statusCode >= 300) return;
  let payload;
  try { payload = JSON.parse(responseBody || "{}"); } catch { payload = {}; }
  if (path.endsWith("/exec") && typeof payload.Id === "string" && payload.Id.length > 0) {
    execIDs.add(payload.Id);
  }
  if (options.mode !== "precheck") return;
  if (path === "/containers/create" && typeof payload.Id === "string") precheckResources.containers.add(payload.Id);
  if (path === "/networks/create" && typeof payload.Id === "string") precheckResources.networks.add(payload.Id);
  if (path === "/volumes/create" && typeof payload.Name === "string") precheckResources.volumes.add(payload.Name);
}

function filterPrecheckContainerList(body) {
  try {
    const containers = JSON.parse(body || "[]");
    if (!Array.isArray(containers)) return "[]";
    return JSON.stringify(containers.filter((container) => {
      const labels = container?.Labels || {};
      try {
        assertPrecheckLabels(labels);
        return true;
      } catch {
        return false;
      }
    }));
  } catch {
    return "[]";
  }
}

function filterScenarioContainerList(body) {
  try {
    const containers = JSON.parse(body || "[]");
    if (!Array.isArray(containers)) return "[]";
    return JSON.stringify(containers.filter((container) => hasManagedLabels(container?.Labels || {})));
  } catch {
    return "[]";
  }
}

function filterPrecheckNetworkList(body) {
  try {
    const networks = JSON.parse(body || "[]");
    if (!Array.isArray(networks)) return "[]";
    return JSON.stringify(networks.filter((network) => network?.Name === "bridge" || network?.Name === options.network));
  } catch {
    return "[]";
  }
}

function augmentPrecheckRequest(path, body) {
  if (options.mode !== "precheck" || body === null) return body;
  if (path === "/networks/create") {
    const payload = parseJSONBody(body, "network create");
    payload.Labels = {
      ...(payload.Labels || {}),
      "io.dense-mem.ci.contract": options.contract,
      "io.dense-mem.ci.repository": options.repository,
      "io.dense-mem.ci.run-id": options.runId,
      "io.dense-mem.ci.run-attempt": options.attempt,
      "io.dense-mem.ci.phase": "precheck",
      "io.dense-mem.ci.scenario": options.scenario,
      "io.dense-mem.ci.image-digest": options.imageDigest,
      "com.docker.compose.project": options.project,
      "io.dense-mem.ci.created-at": new Date().toISOString(),
    };
    return Buffer.from(JSON.stringify(payload));
  }
  if (path === "/containers/create") {
    const payload = parseJSONBody(body, "container create");
    if (payload.HostConfig && Object.hasOwn(payload.HostConfig, "PortBindings")) {
      delete payload.HostConfig.PortBindings;
    }
    return Buffer.from(JSON.stringify(payload));
  }
  return body;
}

async function cleanupPrecheckResources() {
  for (const id of precheckResources.containers) {
    await directTarget("POST", `/v1.45/containers/${encodeURIComponent(id)}/stop?t=5`);
    await directTarget("DELETE", `/v1.45/containers/${encodeURIComponent(id)}`);
  }
  for (const id of precheckResources.networks) await directTarget("DELETE", `/v1.45/networks/${encodeURIComponent(id)}`);
  for (const id of precheckResources.volumes) await directTarget("DELETE", `/v1.45/volumes/${encodeURIComponent(id)}`);
}

async function directTarget(method, path) {
  const target = requestTarget(method, path, {});
  target.request.end();
  try { await target.response; } catch {}
}

function inspectResource(kind, id) {
  const suffix = kind === "containers" ? "/json" : "";
  const target = requestTarget("GET", `/v1.45/${kind}/${encodeURIComponent(id)}${suffix}`, {});
  target.request.end();
  return target.response.then(({ statusCode, body }) => {
    if (statusCode !== 200) {
      const error = new Error(`Docker ${kind} resource inspection failed`);
      error.statusCode = statusCode;
      throw error;
    }
    return JSON.parse(body);
  });
}

function requestTarget(method, path, headers = {}) {
  const request = createRequest(method, path, headers);
  const response = new Promise((resolve, reject) => {
    request.once("response", (incoming) => {
      let body = "";
      incoming.setEncoding("utf8");
      incoming.on("data", (chunk) => { body += chunk; });
      incoming.on("end", () => resolve({ statusCode: incoming.statusCode, headers: incoming.headers, body }));
      incoming.on("error", reject);
    });
    request.once("error", reject);
  });
  return { request, response };
}

function createRequest(method, path, headers) {
  const requestOptions = { socketPath: options.target, path, method, headers: { ...headers } };
  delete requestOptions.headers.host;
  return httpRequest(requestOptions);
}

function normalizedPath(rawURL) {
  const path = new URL(rawURL || "/", "http://docker").pathname;
  return path.replace(/^\/v[0-9]+(?:\.[0-9]+)?/, "");
}

function deny(message) {
  const error = new Error(message);
  error.statusCode = 403;
  throw error;
}

function parseArgs(args) {
  const values = {};
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    const value = args[index + 1];
    if (!key?.startsWith("--") || value === undefined) usage();
    values[key.slice(2)] = value;
  }
  for (const key of ["listen", "target", "project", "contract", "repository", "run-id", "attempt", "phase", "scenario", "image-digest"]) {
    if (!values[key]) usage();
  }
  values.mode = values.mode || "scenario";
  if (values.mode !== "scenario" && values.mode !== "precheck") usage();
  if (values.mode === "precheck" && !values.network) usage();
  values.runId = values["run-id"];
  values.network = values.network || "";
  values.phase = values.phase;
  values.scenario = values.scenario;
  values.imageDigest = values["image-digest"];
  return values;
}

function usage() {
  process.stderr.write("usage: e2e-docker-proxy.mjs --listen SOCKET --target SOCKET --project PROJECT --contract CONTRACT --repository REPOSITORY --run-id RUN_ID --attempt ATTEMPT --phase PHASE --scenario SCENARIO --image-digest DIGEST [--mode scenario|precheck --network NETWORK]\n");
  process.exit(2);
}
