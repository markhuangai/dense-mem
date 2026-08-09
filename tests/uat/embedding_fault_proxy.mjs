#!/usr/bin/env node

import http from "node:http";
import https from "node:https";

const port = Number(process.env.EMBEDDING_PROXY_PORT ?? 8081);
const upstreamURL = String(process.env.EMBEDDING_PROXY_UPSTREAM_URL ?? "").replace(/\/$/, "");
const upstreamKey = String(process.env.EMBEDDING_PROXY_UPSTREAM_KEY ?? "");
const maxBodyBytes = 4 * 1024 * 1024;
const upstreamTimeoutMilliseconds = 110_000;
let mode = process.env.EMBEDDING_PROXY_MODE === "forward" ? "forward" : "quota";
const stats = { requests: 0, quota_failures: 0, forwarded: 0, upstream_failures: 0, request_item_counts: [] };

const server = http.createServer(async (request, response) => {
  if (request.method === "GET" && request.url === "/health") {
    return sendJSON(response, 200, { status: "ok" });
  }
  if (request.method === "GET" && request.url === "/stats") {
    return sendJSON(response, 200, { mode, ...stats });
  }
  if (request.url === "/control/mode" && (request.method === "GET" || request.method === "POST")) {
    if (request.method === "POST") {
      const body = await readRequestBody(request, response);
      if (body === null) return;
      try {
        const requested = JSON.parse(body.toString("utf8")).mode;
        if (requested !== "quota" && requested !== "forward") {
          return sendJSON(response, 400, { error: "mode must be quota or forward" });
        }
        mode = requested;
      } catch {
        return sendJSON(response, 400, { error: "mode must be quota or forward" });
      }
    }
    return sendJSON(response, 200, { mode });
  }
  if (request.method !== "POST" || request.url !== "/v1/embeddings") {
    return sendJSON(response, 404, { error: "not found" });
  }

  stats.requests += 1;
  const body = await readRequestBody(request, response);
  if (body === null) return;
  stats.request_item_counts.push(requestItemCount(body));
  if (mode === "quota") {
    stats.quota_failures += 1;
    return sendJSON(response, 429, { error: { code: "insufficient_quota", type: "insufficient_quota" } }, { "retry-after": "1" });
  }
  if (!upstreamURL || !upstreamKey) {
    stats.upstream_failures += 1;
    return sendJSON(response, 502, { error: { code: "upstream_unavailable", type: "upstream_unavailable" } });
  }
  try {
    const upstream = await forward(body);
    stats.forwarded += 1;
    response.writeHead(upstream.status, filterResponseHeaders(upstream.headers));
    response.end(upstream.body);
  } catch {
    stats.upstream_failures += 1;
    sendJSON(response, 502, { error: { code: "upstream_unavailable", type: "upstream_unavailable" } });
  }
});

server.listen(port, "0.0.0.0");

function sendJSON(response, status, value, headers = {}) {
  const body = Buffer.from(JSON.stringify(value));
  response.writeHead(status, { "content-type": "application/json", "content-length": body.length, ...headers });
  response.end(body);
}

function readBody(request) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let length = 0;
    let settled = false;
    const fail = (error) => {
      if (settled) return;
      settled = true;
      reject(error);
    };
    request.on("data", (chunk) => {
      length += chunk.length;
      if (length > maxBodyBytes) {
        fail(new RequestBodyError("too_large"));
        return;
      }
      chunks.push(chunk);
    });
    request.on("end", () => {
      if (settled) return;
      settled = true;
      resolve(Buffer.concat(chunks));
    });
    request.on("aborted", () => fail(new RequestBodyError("malformed")));
    request.on("error", () => fail(new RequestBodyError("malformed")));
  });
}

class RequestBodyError extends Error {
  constructor(code) {
    super(code);
    this.code = code;
  }
}

async function readRequestBody(request, response) {
  try {
    return await readBody(request);
  } catch (error) {
    if (error instanceof RequestBodyError && error.code === "too_large") {
      sendJSON(response, 413, { error: "request body too large" });
    } else {
      sendJSON(response, 400, { error: "invalid request body" });
    }
    return null;
  }
}

function forward(body) {
  const target = new URL(`${upstreamURL}/embeddings`);
  const client = target.protocol === "https:" ? https : http;
  return new Promise((resolve, reject) => {
    const request = client.request(target, {
      method: "POST",
      headers: {
        authorization: `Bearer ${upstreamKey}`,
        "content-type": "application/json",
        "content-length": body.length,
      },
    }, (response) => {
      const chunks = [];
      response.on("data", (chunk) => chunks.push(chunk));
      response.on("end", () => resolve({ status: response.statusCode ?? 502, headers: response.headers, body: Buffer.concat(chunks) }));
    });
    request.setTimeout(upstreamTimeoutMilliseconds, () => {
      request.destroy(new Error("upstream request timed out"));
    });
    request.on("error", reject);
    request.end(body);
  });
}

function filterResponseHeaders(headers) {
  const allowed = {};
  for (const key of ["content-type", "content-length", "retry-after"]) {
    if (headers[key] !== undefined) allowed[key] = headers[key];
  }
  return allowed;
}

function requestItemCount(body) {
  try {
    const parsed = JSON.parse(body.toString("utf8"));
    return Array.isArray(parsed.input) ? parsed.input.length : 0;
  } catch {
    return 0;
  }
}
