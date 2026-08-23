#!/usr/bin/env node

import { spawnSync } from "node:child_process";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const proxyURL = requiredEnv("DENSE_MEM_E2E_EMBEDDING_PROXY_URL").replace(/\/$/, "");
const prometheusURL = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");

let rpcID = 0;
const runID = `embedding-reconciliation-e2e-${Date.now()}`;

const listedResponse = await mcpJSON("tools/list", {});
if (listedResponse.error || !listedResponse.result) throw new Error("tools/list returned a bounded error");
const listed = listedResponse.result;
const toolNames = new Set((listed.tools ?? []).map((tool) => tool.name));
for (const name of ["remember", "get_submission_status", "recall_memory"]) {
  if (!toolNames.has(name)) throw new Error(`tools/list is missing ${name}`);
}

const remember = await mcpTool("remember", rememberInput());
const submissionID = requiredString(remember.submission_id, "remember submission_id");
const failed = await waitForFailedEmbeddingJobs();
const writeFailureProxy = await proxyJSON("/stats");
if (writeFailureProxy.mode !== "quota" || writeFailureProxy.requests !== 1 || writeFailureProxy.quota_failures !== 1) {
  throw new Error(`quota proxy did not prove one non-amplified write request: ${JSON.stringify(writeFailureProxy)}`);
}
const failedStatus = await mcpTool("get_submission_status", { submission_id: submissionID });
assertPublicDelayedStatus(failedStatus, submissionID);
const failedRecall = await mcpTool("recall_memory", { query: `Embedding reconciliation ${runID}`, limit: 10 });
const lexicalFailureVisible = (failedRecall.results ?? []).some((item) => String(item.context ?? "").includes(runID));
if (!lexicalFailureVisible) {
  throw new Error(`failed vectors were not recallable through lexical search: ${JSON.stringify(failedRecall)}`);
}

const ready = await httpJSON(`${userURL}/ready`, { method: "GET" }, false);
if (ready.status !== "ready" || ready.dependencies?.search_readiness !== "ok") {
  throw new Error(`readiness was blocked by asynchronous embedding failure: ${JSON.stringify(ready)}`);
}
if (ready.dependencies?.search_convergence !== "degraded") {
  throw new Error(`readiness did not report optional convergence degradation: ${JSON.stringify(ready)}`);
}

const beforeConvergence = await controlJSON("/search/convergence");
assertAttentionProjection(beforeConvergence.data);
const logs = await waitForFailureLog();
const serializedFailureLogs = JSON.stringify(logs);
if (!serializedFailureLogs.includes(teamID) || !serializedFailureLogs.includes("aggregation") || !serializedFailureLogs.includes("job_count")) {
  throw new Error("embedding failure operation log omitted bounded team or aggregation fields");
}
if (serializedFailureLogs.includes("insufficient_quota") || serializedFailureLogs.includes("provider response")) {
  throw new Error("operation logs exposed raw provider response details");
}
const metricBefore = await waitForPrometheusValue("sum(densemem_embedding_errors_total)", 1);

const proxyBefore = await proxyJSON("/stats");
const recallRequestIndex = writeFailureProxy.requests;
if (proxyBefore.mode !== "quota"
  || proxyBefore.requests !== writeFailureProxy.requests + 1
  || proxyBefore.quota_failures !== writeFailureProxy.quota_failures + 1
  || proxyBefore.request_item_counts[recallRequestIndex] !== 1) {
  throw new Error(`quota proxy did not prove one bounded recall-query request: ${JSON.stringify(proxyBefore)}`);
}
if (failed.totalAttempts.some((value) => value !== 1)) {
  throw new Error(`inline retry budget was spent before reconciliation: ${JSON.stringify(failed.totalAttempts)}`);
}

const scheduledAt = nextUTCMinute(5);
const reconciliationTimezone = chooseReconciliationTimezone(beforeConvergence.data?.latest_run?.local_run_date);
const scheduledLocalTime = formatTimeInZone(scheduledAt, reconciliationTimezone);
await controlJSON("/config/general", {
  method: "PATCH",
  body: JSON.stringify({ items: [
    { key: "APP_TIMEZONE", value: reconciliationTimezone },
    { key: "EMBEDDING_RECONCILIATION_START_TIME_LOCAL", value: scheduledLocalTime },
  ] }),
});
const config = await controlJSON("/config/general");
const configured = (config.data?.items ?? []).find((item) => item.key === "EMBEDDING_RECONCILIATION_START_TIME_LOCAL");
if (configured?.effective_value !== scheduledLocalTime) {
  throw new Error(`control portal did not persist strict reconciliation schedule: ${JSON.stringify(config.data)}`);
}

await proxyJSON("/control/mode", { method: "POST", body: JSON.stringify({ mode: "forward" }) });
const firstRecoveryRequest = await waitForCanaryRequest(proxyBefore.requests, scheduledAt);
if (firstRecoveryRequest.request_item_counts?.[proxyBefore.requests] !== 1) {
  throw new Error(`first post-window provider request was not a one-item canary: ${JSON.stringify(firstRecoveryRequest)}`);
}

const recovered = await waitForConvergedProjection();
if (recovered.latest_run?.status !== "completed" || recovered.latest_run?.canary_outcome !== "succeeded") {
  throw new Error(`reconciliation did not complete successfully: ${JSON.stringify(recovered)}`);
}
if (recovered.latest_run.recovered_count !== 1 || recovered.latest_run.requeued_count < Math.max(failed.count - 1, 0)) {
  throw new Error(`reconciliation accounting did not separate completed canary from requeued backlog: ${JSON.stringify(recovered.latest_run)}`);
}

const finalStatus = await waitForCurrentSubmission(submissionID);
if (finalStatus.degradations?.some((item) => item.code === "search_indexing_delayed")) {
  throw new Error(`public submission status retained a stale indexing degradation: ${JSON.stringify(finalStatus)}`);
}
const proxyAfter = await proxyJSON("/stats");
if (proxyAfter.forwarded < 1 || proxyAfter.request_item_counts[proxyBefore.requests] !== 1) {
  throw new Error(`proxy did not forward the recovery canary: ${JSON.stringify(proxyAfter)}`);
}
await waitForPrometheusValue("sum(densemem_embedding_reconciliation_canaries_total{outcome=\"succeeded\"})", 1);
const recoveryLogs = await waitForRecoveryLog();
const serializedRecoveryLogs = JSON.stringify(recoveryLogs);
if (!serializedRecoveryLogs.includes(teamID) || !serializedRecoveryLogs.includes("aggregation") || !serializedRecoveryLogs.includes("job_count")) {
  throw new Error("embedding recovery operation log omitted bounded team or aggregation fields");
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_id: submissionID,
  failed_jobs: failed.count,
  initial_provider_requests: writeFailureProxy.requests,
  failed_recall_query_requests: proxyBefore.requests - writeFailureProxy.requests,
  recovery_canary_item_count: proxyAfter.request_item_counts[proxyBefore.requests],
  final_search_status: finalStatus.search_state,
  reconciliation_status: recovered.status,
  reconciliation_canary: recovered.latest_run.canary_outcome,
  operator_guidance_verified: true,
  public_error_bounded: true,
  readiness_structural: true,
  lexical_failure_visibility: lexicalFailureVisible,
  metrics_verified: true,
  recovery_log_verified: true,
}, null, 2));

function rememberInput() {
  const first = `Embedding reconciliation ${runID} uses daily canaries.`;
  const second = `The ${runID} provider quota uses the active contract.`;
  const firstSubject = "Embedding reconciliation";
  const firstPredicate = "uses";
  const firstObject = "daily canaries";
  return {
    idempotency_key: `${runID}:batch`,
    evidence: [
      { content: first, source_type: "document", source: `${runID}:first`, source_group: runID },
      { content: second, source_type: "document", source: `${runID}:second`, source_group: runID },
    ],
    relationships: [{
      ref: `${runID}:relationship:first`,
      subject: { name: firstSubject, entity_kind: "concept" },
      predicate: { proposed_key: firstPredicate },
      object: { entity: { name: firstObject, entity_kind: "concept" } },
      polarity: "+", evidence_indices: [0],
    }, {
      ref: `${runID}:relationship:second`,
      subject: { name: "provider quota", entity_kind: "concept" },
      predicate: { proposed_key: "uses" },
      object: { entity: { name: "active contract", entity_kind: "concept" } },
      polarity: "+", evidence_indices: [1],
    }],
  };
}

async function waitForFailedEmbeddingJobs() {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const row = postgresQuery(`
      SELECT concat(count(*), '|', COALESCE(string_agg(total_attempts::text, ',' ORDER BY embedding_job_id), ''))
      FROM embedding_jobs
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND status = 'failed'
        AND failure_code = 'provider_quota_exhausted'
        AND created_at > now() - interval '20 minutes';
    `);
    const [countText, attempts] = row.split("|");
    const count = Number(countText);
    if (Number.isInteger(count) && count >= 1) {
      return { count, totalAttempts: attempts ? attempts.split(",").map(Number) : [] };
    }
    await delay(2_000);
  }
  throw new Error("timed out waiting for quota-classified embedding jobs");
}

async function waitForFailureLog() {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const page = await controlJSON("/logs?limit=100&severity=warn");
    const serialized = JSON.stringify(page.data ?? page);
    if (serialized.includes("embedding_failure_recorded") && serialized.includes("provider_quota_exhausted")) return page;
    await delay(1_000);
  }
  throw new Error("timed out waiting for bounded embedding failure operation log");
}

async function waitForRecoveryLog() {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const page = await controlJSON("/logs?limit=100&severity=info");
    const serialized = JSON.stringify(page.data ?? page);
    if (serialized.includes("embedding_recovery_completed") && serialized.includes(teamID)) return page;
    await delay(1_000);
  }
  throw new Error("timed out waiting for bounded embedding recovery operation log");
}

async function waitForCanaryRequest(previousRequests, scheduledAt) {
  const deadline = scheduledAt.getTime() + 180_000;
  while (Date.now() < deadline) {
    const stats = await proxyJSON("/stats");
    if (stats.requests > previousRequests) return stats;
    await delay(2_000);
  }
  throw new Error("timed out waiting for the first scheduled canary provider request");
}

async function waitForConvergedProjection() {
  for (let attempt = 0; attempt < 180; attempt += 1) {
    const projection = (await controlJSON("/search/convergence")).data;
    if (projection?.status === "converged" && projection.queue?.failed === 0 && projection.queue?.queued === 0 && projection.queue?.processing === 0 && (projection.failure_groups ?? []).length === 0) return projection;
    await delay(2_000);
  }
  throw new Error("timed out waiting for search convergence");
}

async function waitForCurrentSubmission(id) {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const status = await mcpTool("get_submission_status", { submission_id: id });
    if (["current", "not_required"].includes(status.search_state) && ["completed", "processing"].includes(status.processing_state)) return status;
    await delay(2_000);
  }
  throw new Error("timed out waiting for current public submission status");
}

function assertPublicDelayedStatus(status, id) {
  if (status.submission_id !== id || status.search_state !== "failed") throw new Error(`failed submission status is not bounded: ${JSON.stringify(status)}`);
  if (!Array.isArray(status.errors) || status.errors.length !== 0) throw new Error(`indexing degradation became a terminal submission error: ${JSON.stringify(status)}`);
  if (!status.degradations?.some((item) => item.frontier === "search" && item.optional === true && item.code === "search_indexing_delayed")) {
    throw new Error(`submission status omitted the search_indexing_delayed degradation: ${JSON.stringify(status)}`);
  }
  if (JSON.stringify(status).includes("insufficient_quota")) throw new Error("public status exposed provider failure details");
}

function assertAttentionProjection(projection) {
  if (!projection || !["attention_required", "recovering"].includes(projection.status)) throw new Error(`operator projection did not require attention: ${JSON.stringify(projection)}`);
  if (!projection.failures?.some((item) => item.failure_code === "provider_quota_exhausted")) throw new Error("operator projection omitted quota failure code");
  const failureGroup = projection.failure_groups?.find((item) => item.team_id === teamID && item.failure_code === "provider_quota_exhausted");
  if (!failureGroup || failureGroup.status !== "attention_required" || failureGroup.failed_job_count < 1 || !failureGroup.guidance.includes("provider credit")) {
    throw new Error(`operator projection omitted affected-team guidance: ${JSON.stringify(projection)}`);
  }
}

async function mcpTool(name, args) {
  const response = await mcpJSON("tools/call", { name, arguments: args });
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} returned a bounded error`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} returned no JSON content`);
  return JSON.parse(text);
}

async function mcpJSON(method, params) {
  return httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }),
  });
}

async function controlJSON(path, options = {}) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json", ...(options.headers ?? {}) },
  });
}

async function proxyJSON(path, options = {}) {
  return httpJSON(`${proxyURL}${path}`, { ...options, headers: { "Content-Type": "application/json", ...(options.headers ?? {}) } });
}

async function prometheusValue(query) {
  const url = new URL("/api/v1/query", `${prometheusURL}/`);
  url.searchParams.set("query", query);
  const result = await httpJSON(url.toString(), { method: "GET" });
  return Number(result.data?.result?.[0]?.value?.[1] ?? 0);
}

async function waitForPrometheusValue(query, minimum) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const value = await prometheusValue(query);
    if (value >= minimum) return value;
    await delay(1_000);
  }
  throw new Error("timed out waiting for Prometheus metric");
}

function postgresQuery(sql) {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "embedding-reconciliation-e2e", sql], { encoding: "utf8" });
  if (result.status !== 0) throw new Error("postgres query failed");
  return result.stdout.trim();
}

async function httpJSON(url, options, throwOnError = true) {
  const response = await fetch(url, { ...options, signal: options?.signal ?? AbortSignal.timeout(30_000) });
  const text = await response.text();
  if (throwOnError && !response.ok) throw new Error(`HTTP ${response.status} ${url}: response body redacted`);
  return text ? JSON.parse(text) : { status: response.status };
}

function nextUTCMinute(offset) {
  const value = new Date(Date.now() + offset * 60_000);
  value.setUTCSeconds(0, 0);
  return value;
}

function chooseReconciliationTimezone(existingLocalRunDate) {
  if (!existingLocalRunDate) return "UTC";
  for (const timezone of ["Pacific/Kiritimati", "Etc/GMT+12"]) {
    if (formatDateInZone(new Date(), timezone) !== existingLocalRunDate) return timezone;
  }
  throw new Error(`could not choose a fresh reconciliation local date for ${existingLocalRunDate}`);
}

function formatDateInZone(value, timezone) {
  const parts = new Intl.DateTimeFormat("en-US", { timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(value);
  const fields = Object.fromEntries(parts.filter(({ type }) => type !== "literal").map(({ type, value: part }) => [type, part]));
  return `${fields.year}-${fields.month}-${fields.day}`;
}

function formatTimeInZone(value, timezone) {
  const parts = new Intl.DateTimeFormat("en-US", { timeZone: timezone, hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).formatToParts(value);
  const fields = Object.fromEntries(parts.filter(({ type }) => type !== "literal").map(({ type, value: part }) => [type, part]));
  return `${fields.hour}:${fields.minute}`;
}
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function requiredString(value, label) { if (typeof value !== "string" || !value) throw new Error(`${label} is required`); return value; }
function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }
function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
