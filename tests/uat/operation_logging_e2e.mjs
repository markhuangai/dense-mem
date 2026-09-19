#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");

const ready = await httpJSON(`${userURL}/ready`);
if (ready.response.status !== 200 || ready.body?.status !== "ready" || ready.body?.dependencies?.operation_log_sink !== "ok") {
  throw new Error(`operation log sink was not ready: ${JSON.stringify(ready.body)}`);
}

const liveness = await httpJSON(`${userURL}/health`);
if (liveness.response.status !== 200 || liveness.body?.status !== "ok") {
  throw new Error(`health check failed: ${JSON.stringify(liveness.body)}`);
}

const toolsList = await httpJSON(`${userURL}/mcp`, {
  method: "POST",
  headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
  body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list", params: {} }),
});
if (toolsList.response.status !== 200 || toolsList.body?.error || !toolsList.body?.result) {
  throw new Error(`business MCP result changed while logging: ${JSON.stringify(toolsList.body)}`);
}

const baselineBusinessResult = JSON.stringify(toolsList.body.result);
const eventMarker = uniqueEventMarker();
const markerFrom = new Date(Date.now() - 1000).toISOString();
const markedToolsList = await httpJSON(`${userURL}/mcp`, {
  method: "POST",
  headers: {
    Authorization: `Bearer ${apiKey}`,
    Accept: "application/json",
    "Content-Type": "application/json",
    "X-Forwarded-For": eventMarker,
  },
  body: JSON.stringify({ jsonrpc: "2.0", id: 3, method: "tools/list", params: {} }),
});
if (markedToolsList.response.status !== 200 || markedToolsList.body?.error || JSON.stringify(markedToolsList.body?.result) !== baselineBusinessResult) {
  throw new Error(`business MCP result changed for the exact-fanout probe: ${JSON.stringify(markedToolsList.body)}`);
}
let faultReady;
let saturation;
try {
  setOperationLogFault(true);
  const duringFault = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/list", params: {} }),
  });
  if (duringFault.response.status !== 200 || duringFault.body?.error || JSON.stringify(duringFault.body?.result) !== baselineBusinessResult) {
    throw new Error(`business MCP result changed during sink failure: ${JSON.stringify(duringFault.body)}`);
  }
  faultReady = await waitForSinkFailure();
  const sinkStatus = faultReady.body?.dependencies?.operation_log_sink;
  if (sinkStatus == null || sinkStatus === "ok") {
    throw new Error(`sink failure was not visible in readiness: ${JSON.stringify(faultReady.body)}`);
  }
  saturation = await runBoundedHealthLoad();
} finally {
  setOperationLogFault(false);
}

const recovered = await waitForSinkRecovery();
const logs = await controlJSON(`/logs?limit=500&sort=timestamp&direction=asc&from=${encodeURIComponent(markerFrom)}`);
const rows = Array.isArray(logs.body?.data) ? logs.body.data : [];
const matchingRows = rows.filter((row) => row?.message === "http_request" && row?.attrs?.remote_ip === eventMarker);
if (matchingRows.length !== 1) {
  throw new Error(`operation log fan-out persisted ${matchingRows.length} exact probe rows instead of one`);
}
const serverLogs = composeServerLogs();
if (countOccurrences(serverLogs, eventMarker) !== 1) {
  throw new Error("operation log fan-out did not deliver the exact probe to console exactly once");
}

const traceFilter = await controlJSON("/logs?severity=TRACE&limit=1");
if (traceFilter.response.status !== 200) {
  throw new Error(`TRACE operation-log filter was rejected: ${traceFilter.response.status}`);
}
const fatalFilter = await controlJSON("/logs?severity=FATAL&limit=1");
if (fatalFilter.response.status !== 200) {
  throw new Error(`FATAL operation-log filter was rejected: ${fatalFilter.response.status}`);
}

const invalidFilter = await controlJSON("/logs?severity=INVALID&limit=1");
if (invalidFilter.response.status < 400 || invalidFilter.response.status >= 500) {
  throw new Error(`invalid operation-log filter was not bounded: ${invalidFilter.response.status}`);
}

console.log(JSON.stringify({
  status: "ok",
  sink_ready: true,
  operation_log_fanout: true,
  exact_fanout: true,
  trace_filter: true,
  fatal_filter: true,
  bounded_adverse_filter: true,
  bounded_saturation: true,
  saturation_requests: saturation.requests,
  saturation_max_latency_ms: saturation.max_latency_ms,
  sink_failure_visible: true,
  sink_recovery_verified: true,
  business_result_preserved: true,
}, null, 2));

async function waitForSinkFailure() {
  let latest;
  for (let attempt = 0; attempt < 10; attempt++) {
    latest = await httpJSON(`${userURL}/ready`);
    if (latest.response.status !== 200 || latest.body?.dependencies?.operation_log_sink !== "ok") return latest;
    await sleep(500);
  }
  return latest;
}

async function waitForSinkRecovery() {
  let latest;
  for (let attempt = 0; attempt < 20; attempt++) {
    latest = await httpJSON(`${userURL}/ready`);
    if (latest.response.status === 200 && latest.body?.dependencies?.operation_log_sink === "ok") return latest;
    await sleep(250);
  }
  throw new Error(`sink readiness did not recover after the adverse probe: ${JSON.stringify(latest?.body)}`);
}

async function runBoundedHealthLoad() {
  const total = 4500;
  const batchSize = 250;
  const timeoutMs = 5000;
  let maxLatency = 0;
  let completed = 0;
  for (let offset = 0; offset < total; offset += batchSize) {
    const batch = Math.min(batchSize, total - offset);
    const results = await Promise.all(Array.from({ length: batch }, () => boundedHealthRequest(timeoutMs)));
    for (const result of results) {
      if (!result.ok) {
        throw new Error(`health request failed during sink saturation: ${result.error || result.status}`);
      }
      maxLatency = Math.max(maxLatency, result.latency_ms);
      completed += 1;
    }
  }
  return { requests: completed, max_latency_ms: maxLatency };
}

async function boundedHealthRequest(timeoutMs) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const started = Date.now();
  try {
    const result = await httpJSON(`${userURL}/health`, { signal: controller.signal });
    return { ok: result.response.status === 200, status: result.response.status, latency_ms: Date.now() - started };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error), latency_ms: Date.now() - started };
  } finally {
    clearTimeout(timer);
  }
}

function setOperationLogFault(enabled) {
  const sql = enabled
    ? `
      CREATE OR REPLACE FUNCTION public.dense_mem_e2e_operation_log_fault()
      RETURNS trigger LANGUAGE plpgsql AS $$
      BEGIN
        RAISE EXCEPTION 'operation log sink fault injected by UAT';
      END;
      $$;
      DROP TRIGGER IF EXISTS dense_mem_e2e_operation_log_fault ON operation_logs;
      CREATE TRIGGER dense_mem_e2e_operation_log_fault
      BEFORE INSERT ON operation_logs
      FOR EACH ROW EXECUTE FUNCTION public.dense_mem_e2e_operation_log_fault();
    `
    : `
      DROP TRIGGER IF EXISTS dense_mem_e2e_operation_log_fault ON operation_logs;
      DROP FUNCTION IF EXISTS public.dense_mem_e2e_operation_log_fault();
    `;
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile,
    "exec", "-T", "postgres", "sh", "-ec",
    'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "$1"',
    "operation-logging", sql,
  ], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) throw new Error(`operation log fault control failed (${result.status}): ${result.stderr || result.stdout}`);
}

function composeServerLogs() {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile, "logs", "--no-color", "server",
  ], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
  if (result.status !== 0) throw new Error(`could not inspect server console logs: ${result.error?.message || result.stderr || result.stdout}`);
  return `${result.stdout}\n${result.stderr}`;
}

function countOccurrences(text, marker) {
  return text.split(marker).length - 1;
}

function uniqueEventMarker() {
  const hex = randomUUID().replaceAll("-", "");
  const octet = (offset) => 1 + (Number.parseInt(hex.slice(offset, offset + 2), 16) % 254);
  return `203.${octet(0)}.${octet(2)}.${octet(4)}`;
}

function sleep(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function controlJSON(path) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    headers: { Authorization: `Bearer ${controlToken}`, Accept: "application/json" },
  });
}

async function httpJSON(url, options = {}) {
  const response = await fetch(url, options);
  const text = await response.text();
  let body = {};
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      throw new Error(`non-JSON response from ${url}: ${response.status}`);
    }
  }
  return { response, body };
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
