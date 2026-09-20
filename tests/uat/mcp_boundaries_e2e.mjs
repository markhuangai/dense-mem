#!/usr/bin/env node

import { randomUUID } from "node:crypto";
import { request as httpRequest } from "node:http";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");

const feedbackTool = "submit_recall_session_feedback";
const dreamTools = ["list_dreams", "get_dream", "resolve_dream_feedback"];
const activeEvalTools = ["eval_list_knowledge_refs", "eval_run_dream_cycle", "eval_run_recall_case"];
const removedTools = [
  "correct_entity_resolution",
  "eval_get_manifest",
  "eval_get_knowledge_item",
  "eval_list_recall_feedback_events",
  "eval_get_recall_feedback_event",
  "eval_score_retrieval_case",
];
let rpcID = 0;

await updateRecallFeedback(false);
await updateDreaming(true, false);
await updateTeamDreaming(false);

let names = await listedToolNames();
for (const required of ["remember", "retract_evidence", "correct_relationship", "recall_memory", "trace_memory", "export_memory_pack"]) {
  assertHas(names, required, "production core tool");
}
for (const absent of [feedbackTool, ...dreamTools, ...activeEvalTools, ...removedTools]) {
  assertMissing(names, absent, "disabled or non-production tool");
}
for (const hidden of [feedbackTool, ...dreamTools, ...activeEvalTools, ...removedTools]) {
  await assertToolNotFound(hidden, {});
}

await assertNotificationIsNotCounted();
await assertSSELookupRejection();
await assertConcurrentToolCalls();
await assertUsageMetricDeltas();
await assertTransportLogOutcomes();

await updateRecallFeedback(true);
names = await listedToolNames();
assertHas(names, feedbackTool, "enabled recall feedback tool");
const recall = await mcpSuccess("recall_memory", { query: "MCP boundary e2e query with no required match", limit: 1 });
const feedbackAction = (recall.suggested_actions ?? []).find((item) => item?.tool === feedbackTool);
if (!feedbackAction || feedbackAction.recall_event_id !== recall.recall_id) {
  throw new Error(`recall did not suggest feedback with its persisted recall ID: ${JSON.stringify(recall.suggested_actions)}`);
}
if ((recall.suggested_actions ?? []).some((item) => item?.tool === "resolve_dream_feedback")) {
  throw new Error("recall suggested Dream feedback while team Dreaming was disabled");
}

await updateTeamDreaming(true);
names = await listedToolNames();
for (const tool of dreamTools) assertHas(names, tool, "team-enabled Dream tool");

await updateTeamDreaming(false);
await updateDreaming(true, true);
names = await listedToolNames();
for (const tool of dreamTools) assertHas(names, tool, "force-enabled Dream tool");

await updateTeamDreaming(true);
await updateDreaming(false, false);
names = await listedToolNames();
for (const tool of dreamTools) assertMissing(names, tool, "globally disabled Dream tool");
await assertToolNotFound("resolve_dream_feedback", {});

console.log(JSON.stringify({
  status: "ok",
  production_eval_tools_absent: true,
  removed_tools_absent: true,
  recall_feedback_gate: true,
  recall_feedback_hint: true,
  team_dreaming_gate: true,
  force_dreaming_gate: true,
  global_dreaming_disable: true,
}, null, 2));

async function listedToolNames() {
  const response = await rpc("tools/list", {});
  if (response.error || !response.result) {
    throw new Error("tools/list returned an error");
  }
  return new Set((response.result.tools ?? []).map((tool) => tool.name));
}

async function mcpSuccess(name, args) {
  const response = await rpc("tools/call", { name, arguments: args });
  if (response.error || response.result === undefined) {
    throw new Error(`MCP ${name} returned an error`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} did not return JSON content`);
  }
  return JSON.parse(text);
}

async function assertToolNotFound(name, args) {
  const response = await rpc("tools/call", { name, arguments: args });
  if (response.error?.code !== -32601 || response.result !== undefined) {
    throw new Error(`hidden tool ${name} was callable: ${JSON.stringify(response)}`);
  }
}

async function assertNotificationIsNotCounted() {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized", params: {} }),
  });
  const body = await response.text();
  if (![200, 202].includes(response.status) || body.length > 0) {
    throw new Error(`tools/call notification returned an unexpected response: ${response.status}`);
  }
}

async function assertSSELookupRejection() {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "text/event-stream", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name: "missing-observability-tool", arguments: {} } }),
  });
  const body = await response.text();
  if (response.status !== 200 || !body.includes("event: message") || !body.includes('"code":-32601')) {
    throw new Error(`SSE lookup rejection was not a bounded JSON-RPC error: ${response.status}`);
  }
}

async function assertConcurrentToolCalls() {
  const results = await Promise.all(Array.from({ length: 4 }, () => rpc("tools/call", {
    name: "recall_memory",
    arguments: { query: "concurrent MCP boundary probe", limit: 1 },
  })));
  if (results.some((result) => result.error || result.result === undefined)) {
    throw new Error("concurrent MCP tool calls returned an error");
  }
}

async function assertUsageMetricDeltas() {
  const before = await usageSnapshot();
  await mcpSuccess("recall_memory", { query: "exact MCP metrics success probe", limit: 1 });
  await assertToolNotFound("missing-exact-metrics-tool", {});
  await assertNotificationIsNotCounted();
  await assertSSELookupRejection();
  await assertConcurrentToolCalls();
  const after = await usageSnapshot();

  assertUsageDelta(before.data?.system, after.data?.system, "system", 8, 7, 2);
  const beforeTeam = (before.data?.teams ?? []).find((item) => item.team_id === teamID);
  const afterTeam = (after.data?.teams ?? []).find((item) => item.team_id === teamID);
  assertUsageDelta(beforeTeam, afterTeam, "team", 8, 7, 2);

  const keyDeltas = (after.data?.keys ?? []).map((item) => {
    const previous = (before.data?.keys ?? []).find((candidate) => candidate.key_id === item.key_id);
    return { item, calls: (item.mcp_tool_calls ?? 0) - (previous?.mcp_tool_calls ?? 0), failures: (item.mcp_tool_failures ?? 0) - (previous?.mcp_tool_failures ?? 0) };
  }).filter((delta) => delta.calls !== 0 || delta.failures !== 0);
  if (keyDeltas.length !== 1 || keyDeltas[0].calls !== 7 || keyDeltas[0].failures !== 2) {
    throw new Error(`MCP key metrics were not isolated to one credential: ${JSON.stringify(keyDeltas)}`);
  }

  const route = (after.data?.routes ?? []).find((item) => item.route === "/mcp" && item.method === "POST" && item.status_class === "2xx");
  const previousRoute = (before.data?.routes ?? []).find((item) => item.route === "/mcp" && item.method === "POST" && item.status_class === "2xx");
  assertUsageDelta(previousRoute, route, "route", 8, 7, 2);
}

async function assertTransportLogOutcomes() {
  const marker = `transport-pre-admission-${randomUUID()}`;
  const unmatchedMarker = `transport-unmatched-${randomUUID()}`;
  const markerFrom = new Date().toISOString();
  await mcpSuccess("recall_memory", { query: `transport-success-${randomUUID()}`, limit: 1 });
  await fetch(`${userURL}/unmatched/${unmatchedMarker}`, { method: "GET" });
  await rpc("tools/call", { name: "remember", arguments: { unexpected: marker } });
  await assertToolNotFound("missing-transport-failure-tool", {});
  if (process.env.DENSE_MEM_E2E_SCENARIO === "mcp_transport_cancellation") {
    await assertCancelledTransportOutcome(markerFrom);
  }
  let rows = [];
  for (let attempt = 0; attempt < 20; attempt += 1) {
    const page = await controlJSON(`/logs?limit=500&sort=timestamp&direction=desc&from=${encodeURIComponent(markerFrom)}`, { method: "GET" });
    rows = Array.isArray(page.data) ? page.data : [];
    const hasFailure = rows.some((row) => row?.message === "mcp_tool_outcome" && row?.attrs?.application_outcome === "tool_error");
    const hasSuccess = rows.some((row) => row?.message === "mcp_tool_outcome" && row?.attrs?.application_outcome === "success");
    if (hasFailure && hasSuccess) break;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  const serialized = JSON.stringify(rows);
  if (serialized.includes(marker) || serialized.includes(unmatchedMarker) || serialized.includes("missing-transport-failure-tool")) {
    throw new Error("transport logs retained rejected request content");
  }
  if (!rows.some((row) => row?.message === "mcp_tool_outcome" && row?.attrs?.application_outcome === "tool_error")) {
    throw new Error("persisted MCP tool failure outcome was missing");
  }
  if (!rows.some((row) => row?.message === "mcp_tool_outcome" && row?.attrs?.application_outcome === "success")) {
    throw new Error("persisted MCP tool success outcome was missing");
  }
}

async function assertCancelledTransportOutcome(markerFrom) {
  const providerURL = requiredEnv("DENSE_MEM_E2E_PROVIDER_URL").replace(/\/$/, "");
  const baseline = await httpJSON(`${providerURL}/health`);
  const baselineEmbeddingCalls = Number(baseline.embedding_calls || 0);
  const correlationID = randomUUID();
  const suffix = `${Date.now()}-${randomUUID()}`;
  const request = cancellableJSONPost(`${userURL}/mcp`, {
    Authorization: `Bearer ${apiKey}`,
    Accept: "application/json",
    "Content-Type": "application/json",
    "MCP-Protocol-Version": "2026-07-28",
    "Mcp-Method": "tools/call",
    "Mcp-Name": "remember",
    "X-Correlation-ID": correlationID,
  }, {
    jsonrpc: "2.0",
    id: ++rpcID,
    method: "tools/call",
    params: {
      name: "remember",
      arguments: {
        evidence: [{
          content: `Dense-Mem stores durable memory in PostgreSQL. [fixture:transport-cancel] [fixture-fault:embedding-cancel] ${suffix}`,
          source_type: "manual",
        }],
        relationships: [{
          ref: "durable-store",
          subject: { name: "Dense-Mem", entity_kind: "project" },
          predicate: { proposed_key: "stores_memory_in" },
          object: { value: { type: "string", value: "PostgreSQL" } },
          polarity: "+",
          evidence_indices: [0],
        }],
        idempotency_key: `mcp-boundaries-transport-cancel-${suffix}`,
      },
      _meta: { "io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": {} },
    },
  });

  let providerObserved = false;
  for (let attempt = 0; attempt < 40; attempt += 1) {
    const health = await httpJSON(`${providerURL}/health`);
    if (Number(health.embedding_calls || 0) > baselineEmbeddingCalls) {
      providerObserved = true;
      break;
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  if (!providerObserved) throw new Error("cancellation provider fixture did not observe the embedding request");

  request.abort();
  let aborted = true;
  try {
    await request.promise;
  } catch {}
  for (let attempt = 0; attempt < 40; attempt += 1) {
    const page = await controlJSON(`/logs?limit=500&sort=timestamp&direction=desc&from=${encodeURIComponent(markerFrom)}`, { method: "GET" });
    const rows = Array.isArray(page.data) ? page.data : [];
    const correlated = rows.filter((row) => rowCorrelationID(row) === correlationID);
    const applicationOutcome = correlated.some((row) => row?.message === "mcp_tool_outcome" && row?.attrs?.application_outcome === "cancelled");
    const deliveryStage = correlated.some((row) => row?.message === "http_request" && row?.attrs?.delivery_stage === "disconnect_observed");
    if (applicationOutcome && deliveryStage) return;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`canceled MCP transport outcome was missing (client_aborted=${aborted}, provider_observed=${providerObserved})`);
}

function cancellableJSONPost(url, headers, payload) {
  const target = new URL(url);
  const body = JSON.stringify(payload);
  let abort;
  const promise = new Promise((resolve, reject) => {
    const request = httpRequest({
      hostname: target.hostname,
      port: target.port,
      path: `${target.pathname}${target.search}`,
      method: "POST",
      headers,
    }, (response) => {
      response.resume();
      response.on("end", () => resolve(response.statusCode));
    });
    request.on("error", reject);
    abort = () => request.destroy();
    request.end(body);
  });
  return { promise, abort: () => abort?.() };
}

function rowCorrelationID(row) {
  return row?.correlation_id || row?.attrs?.correlation_id || "";
}

async function usageSnapshot() {
  return controlJSON("/metrics?window_minutes=60", { method: "GET" });
}

function assertUsageDelta(before, after, label, requestDelta, callDelta, failureDelta) {
  if (!before || !after) throw new Error(`${label} usage metrics row is missing`);
  const requests = (after.requests ?? 0) - (before.requests ?? 0);
  const errors = (after.errors ?? 0) - (before.errors ?? 0);
  const calls = (after.mcp_tool_calls ?? 0) - (before.mcp_tool_calls ?? 0);
  const failures = (after.mcp_tool_failures ?? 0) - (before.mcp_tool_failures ?? 0);
  if (requests !== requestDelta || errors !== 0 || calls !== callDelta || failures !== failureDelta) {
    throw new Error(`${label} usage delta mismatch: ${JSON.stringify({ requests, errors, calls, failures })}`);
  }
}

async function rpc(method, params) {
  return httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }),
  });
}

async function updateRecallFeedback(enabled) {
  await controlJSON("/config/recall-feedback", {
    method: "PATCH",
    body: JSON.stringify({ items: [
      { key: "RECALL_FEEDBACK_ENABLED", value: String(enabled) },
      { key: "RECALL_FEEDBACK_RETENTION_DAYS", value: "30" },
    ] }),
  });
}

async function updateDreaming(enabled, forceEnabled) {
  await controlJSON("/config/dreaming", {
    method: "PATCH",
    body: JSON.stringify({ items: [
      { key: "DREAMING_ENABLED", value: String(enabled) },
      { key: "DREAMING_FORCE_ENABLED", value: String(forceEnabled) },
      { key: "DREAMING_START_TIME_LOCAL", value: "03:00" },
      { key: "DREAMING_MAX_OUTPUTS", value: "5" },
    ] }),
  });
}

async function updateTeamDreaming(enabled) {
  await controlJSON(`/teams/${teamID}`, {
    method: "PATCH",
    body: JSON.stringify({ config: { dreaming: { enabled } } }),
  });
}

async function controlJSON(path, options) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
  });
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: response body redacted`);
  }
  return text ? JSON.parse(text) : {};
}

function assertHas(names, name, label) {
  if (!names.has(name)) throw new Error(`${label} ${name} is missing`);
}

function assertMissing(names, name, label) {
  if (names.has(name)) throw new Error(`${label} ${name} is exposed`);
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
