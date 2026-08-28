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

let rpcID = 0;
const runID = `embedding-resilience-e2e-${Date.now()}`;
const rejectedMarker = "embed-reject!";

const content = [
  `Subject 0 uses Store 0. ${runID} good evidence alpha uses the durable store.`,
  `Subject 1 uses Store 1. ${runID} embedding fixture token ${rejectedMarker}.`,
  `Subject 2 uses Store 2. ${runID} good evidence beta uses the durable store.`,
];
const remember = await mcpTool("remember", {
  idempotency_key: `${runID}:batch`,
  evidence: content.map((value, index) => ({
    content: value,
    source_type: "document",
    source: `${runID}:${index}`,
    source_group: runID,
  })),
  relationships: content.map((value, index) => {
    const subject = `Subject ${index}`;
    const predicate = "uses";
    const object = `Store ${index}`;
    return {
      ref: `${runID}:relationship:${index}`,
      subject: { name: subject, entity_kind: "concept" },
      predicate: { proposed_key: predicate },
      object: { entity: { name: object, entity_kind: "concept" } },
      polarity: "+",
      evidence_indices: [index],
    };
  }),
});
const submissionID = requiredString(remember.submission_id, "remember submission_id");

const jobs = await waitForEvidenceJobs();
const stats = await proxyJSON("/stats");
if (stats.mode !== "input_rejected" || stats.input_rejection_failures < 1 || stats.forwarded < 2) {
  throw new Error(`embedding proxy did not observe split good/bad calls: ${JSON.stringify(stats)}`);
}
if (!jobs.some((job) => job.status === "failed" && job.failure_code === "embedding_input_rejected")) {
  throw new Error(`bad evidence was not isolated as embedding_input_rejected: ${JSON.stringify(jobs)}`);
}
if (jobs.filter((job) => job.status === "completed").length < 2) {
  throw new Error(`good evidence was not completed after recursive split: ${JSON.stringify(jobs)}`);
}

const status = await mcpTool("get_submission_status", { submission_id: submissionID });
if (status.submission_id !== submissionID || !["failed", "processing", "completed"].includes(status.processing_state)) {
  throw new Error(`embedding resilience status was not a bounded public projection: ${JSON.stringify(status)}`);
}
if (JSON.stringify(status).includes(rejectedMarker) || JSON.stringify(status).includes("payload_too_large")) {
  throw new Error("public submission status exposed provider input details");
}

const repair = await runScheduledDocumentRepair();

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_id: submissionID,
  provider_requests: stats.requests,
  input_rejection_failures: stats.input_rejection_failures,
  forwarded_requests: stats.forwarded,
  completed_jobs: jobs.filter((job) => job.status === "completed").length,
  rejected_jobs: jobs.filter((job) => job.failure_code === "embedding_input_rejected").length,
  failed_repair_batch_item_count: repair.failedBatchItemCount,
  failed_repair_status: repair.failedStatus,
  repair_batch_item_count: repair.batchItemCount,
  repaired_documents: repair.updatedCount,
  public_status_bounded: true,
}, null, 2));

async function runScheduledDocumentRepair() {
  const staleDocumentID = requiredUUID(postgresQuery(`
    UPDATE search_documents
    SET search_state = 'failed', embedding = NULL, embedding_updated_at = NULL,
        embedding_error = 'resilience repair fixture', updated_at = clock_timestamp()
    WHERE team_id = '${sqlEscape(teamID)}'::uuid
      AND search_document_id = (
        SELECT search_document_id
        FROM search_documents
        WHERE team_id = '${sqlEscape(teamID)}'::uuid
          AND source_kind = 'evidence'
          AND document_text LIKE '%${sqlEscape(runID)}%'
          AND document_text LIKE '%${sqlEscape(rejectedMarker)}%'
        ORDER BY created_at, search_document_id
        LIMIT 1
      )
    RETURNING search_document_id::text;
  `), "drifted search document id");
  const before = (await controlJSON("/search/convergence")).data;
  if (before?.status !== "attention_required" || before.drifted_documents < 1) {
    throw new Error(`document drift was not visible before repair: ${JSON.stringify(before)}`);
  }

  const unchangedBeforeFailure = repairDocumentAndSourceFingerprint(staleDocumentID);
  const failedScheduledAt = nextUTCMinute(5);
  const failedTimezone = chooseReconciliationTimezone(before?.latest_run?.local_run_date, failedScheduledAt);
  const failedLocalDate = formatDateInZone(failedScheduledAt, failedTimezone);
  const failedProxyBefore = await proxyJSON("/stats");
  await scheduleDocumentRepair(failedScheduledAt, failedTimezone);
  const failedRequest = await waitForRepairRequest(failedProxyBefore.requests, failedScheduledAt, failedLocalDate);
  const failedBatchItemCount = failedRequest.request_item_counts?.[failedProxyBefore.requests] ?? 0;
  if (failedBatchItemCount < 1 || failedBatchItemCount > 256) {
    throw new Error(`failed scheduled repair was not one bounded document batch: ${JSON.stringify(failedRequest)}`);
  }
  const failed = await waitForRepairRun(failedLocalDate, ["deferred", "failed"]);
  if (failed.latest_run?.selected_count < 1 || failed.latest_run.updated_count !== 0 || failed.latest_run.embedded_count !== 0) {
    throw new Error(`failed scheduled repair did not retain bounded untouched counters: ${JSON.stringify(failed.latest_run)}`);
  }
  if (repairDocumentAndSourceFingerprint(staleDocumentID) !== unchangedBeforeFailure) {
    throw new Error("failed scheduled repair changed the selected document or its canonical source");
  }
  const failedProxyAfter = await proxyJSON("/stats");
  if (failedProxyAfter.mode !== "input_rejected" || failedProxyAfter.input_rejection_failures <= failedProxyBefore.input_rejection_failures) {
    throw new Error(`scheduled repair did not retain the adverse provider failure: ${JSON.stringify(failedProxyAfter)}`);
  }

  const recoveryScheduledAt = nextUTCMinute(5);
  const recoveryTimezone = chooseReconciliationTimezone(failed.latest_run.local_run_date, recoveryScheduledAt);
  const recoveryLocalDate = formatDateInZone(recoveryScheduledAt, recoveryTimezone);
  if (recoveryLocalDate === failedLocalDate) throw new Error("recovery schedule reused the failed local run date");
  await scheduleDocumentRepair(recoveryScheduledAt, recoveryTimezone);
  const proxyBefore = await proxyJSON("/stats");
  await proxyJSON("/control/mode", { method: "POST", body: JSON.stringify({ mode: "forward" }) });
  const repairedRequest = await waitForRepairRequest(proxyBefore.requests, recoveryScheduledAt, recoveryLocalDate);
  const batchItemCount = repairedRequest.request_item_counts?.[proxyBefore.requests] ?? 0;
  if (batchItemCount < 1 || batchItemCount > 256) {
    throw new Error(`scheduled repair was not one bounded document batch: ${JSON.stringify(repairedRequest)}`);
  }
  const converged = await waitForConvergedRepair();
  if (converged.latest_run?.status !== "completed" || converged.latest_run.local_run_date !== recoveryLocalDate || converged.latest_run.updated_count < 1) {
    throw new Error(`scheduled repair did not persist document counters: ${JSON.stringify(converged)}`);
  }
  return {
    failedBatchItemCount,
    failedStatus: failed.latest_run.status,
    batchItemCount,
    updatedCount: converged.latest_run.updated_count,
  };
}

async function waitForEvidenceJobs() {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const rows = postgresQuery(`
      SELECT job.status, job.failure_code
      FROM embedding_jobs AS job
      JOIN search_documents AS document
        ON document.team_id = job.team_id
       AND document.search_document_id = job.search_document_id
      WHERE job.team_id = '${sqlEscape(teamID)}'::uuid
        AND job.source_kind = 'evidence'
        AND document.document_text LIKE '%${sqlEscape(runID)}%'
      ORDER BY job.created_at, job.embedding_job_id;
    `);
    const jobs = rows.split("\n").filter(Boolean).map((row) => {
      const [status, failureCode = ""] = row.split("|");
      return { status, failure_code: failureCode };
    });
    if (jobs.length >= 3 && jobs.every((job) => ["completed", "failed", "stale", "cancelled"].includes(job.status))) return jobs;
    await delay(1_000);
  }
  throw new Error("timed out waiting for mixed embedding jobs");
}

async function mcpTool(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }),
  });
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} returned a bounded error`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} returned no JSON content`);
  return JSON.parse(text);
}

async function proxyJSON(path, options = {}) {
  return httpJSON(`${proxyURL}${path}`, {
    method: "GET",
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers ?? {}) },
  });
}

async function controlJSON(path, options = {}) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  });
}

async function waitForRepairRequest(previousRequests, scheduledAt, localRunDate) {
  const deadline = scheduledAt.getTime() + 180_000;
  while (Date.now() < deadline) {
    const stats = await proxyJSON("/stats");
    if (stats.requests > previousRequests) return stats;
    const projection = (await controlJSON("/search/convergence")).data;
    const run = projection?.latest_run;
    if (run?.local_run_date === localRunDate && ["completed", "deferred", "failed", "ambiguous"].includes(run.status)) {
      throw new Error(`scheduled repair run ended without a provider request: ${JSON.stringify(run)}`);
    }
    await delay(2_000);
  }
  const projection = (await controlJSON("/search/convergence")).data;
  throw new Error(`timed out waiting for scheduled document repair: ${JSON.stringify(projection?.latest_run ?? null)}`);
}

async function scheduleDocumentRepair(scheduledAt, timezone) {
  const startTime = formatTimeInZone(scheduledAt, timezone);
  await controlJSON("/config/general", {
    method: "PATCH",
    body: JSON.stringify({ items: [
      { key: "APP_TIMEZONE", value: timezone },
      { key: "EMBEDDING_RECONCILIATION_START_TIME_LOCAL", value: startTime },
    ] }),
  });
}

async function waitForRepairRun(localRunDate, statuses) {
  for (let attempt = 0; attempt < 180; attempt += 1) {
    const projection = (await controlJSON("/search/convergence")).data;
    const run = projection?.latest_run;
    if (run?.local_run_date === localRunDate && statuses.includes(run.status)) return projection;
    await delay(2_000);
  }
  throw new Error(`timed out waiting for scheduled repair run ${localRunDate}`);
}

async function waitForConvergedRepair() {
  for (let attempt = 0; attempt < 180; attempt += 1) {
    const projection = (await controlJSON("/search/convergence")).data;
    if (projection?.status === "converged" && projection.drifted_documents === 0) return projection;
    await delay(2_000);
  }
  throw new Error("timed out waiting for repaired search convergence");
}

function repairDocumentAndSourceFingerprint(searchDocumentID) {
  const fingerprint = postgresQuery(`
    SELECT concat_ws('|',
      document.search_document_id::text,
      document.source_version::text,
      document.document_version::text,
      document.search_state,
      document.document_hash,
      CASE WHEN document.embedding IS NULL THEN 'missing' ELSE 'present' END,
      COALESCE(document.embedding_updated_at::text, ''),
      COALESCE(document.embedding_error, ''),
      document.updated_at::text,
      fragment.fragment_id::text,
      fragment.content_hash,
      COALESCE(fragment.source_id::text, ''),
      COALESCE(fragment.source_revision_id::text, ''),
      COALESCE(fragment.space_id::text, ''),
      COALESCE(fragment.space_generation::text, '')
    )
    FROM search_documents AS document
    JOIN evidence_fragments AS fragment
      ON fragment.team_id = document.team_id
     AND fragment.fragment_id = document.source_id
    WHERE document.team_id = '${sqlEscape(teamID)}'::uuid
      AND document.search_document_id = '${sqlEscape(searchDocumentID)}'::uuid
      AND document.source_kind = 'evidence';
  `);
  if (!fingerprint) throw new Error("selected repair document or canonical source was not found");
  return fingerprint;
}

function postgresQuery(sql) {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "embedding-resilience-e2e", sql], { encoding: "utf8" });
  if (result.status !== 0) throw new Error("postgres query failed");
  return result.stdout.trim();
}

async function httpJSON(url, options) {
  const response = await fetch(url, { ...options, signal: options?.signal ?? AbortSignal.timeout(30_000) });
  const text = await response.text();
  if (!response.ok) throw new Error(`HTTP ${response.status} ${new URL(url).pathname}`);
  return text ? JSON.parse(text) : {};
}

function sqlEscape(value) { return String(value).replaceAll("'", "''"); }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function requiredString(value, label) { if (typeof value !== "string" || !value) throw new Error(`${label} is missing`); return value; }
function requiredUUID(value, label) {
  value = requiredString(value, label);
  if (!/^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/i.test(value)) throw new Error(`${label} is not a UUID`);
  return value;
}
function nextUTCMinute(offset) { const value = new Date(Date.now() + offset * 60_000); value.setUTCSeconds(0, 0); return value; }
function chooseReconciliationTimezone(existingLocalRunDate, scheduledAt = new Date()) {
  if (!existingLocalRunDate) return "UTC";
  for (const timezone of ["Pacific/Kiritimati", "Etc/GMT+12"]) {
    if (formatDateInZone(scheduledAt, timezone) !== existingLocalRunDate) return timezone;
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
function delay(milliseconds) { return new Promise((resolve) => setTimeout(resolve, milliseconds)); }
