#!/usr/bin/env node

import { spawnSync } from "node:child_process";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
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
  `Subject 1 uses Store 1. ${runID} ${rejectedMarker} bad evidence exceeds the provider input limit.`,
  `Subject 2 uses Store 2. ${runID} good evidence beta uses the durable store.`,
];
const remember = await mcpTool("remember", {
  evidence: content.map((value, index) => ({
    content: value,
    source_type: "document",
    source: `${runID}:${index}`,
    source_group: runID,
    idempotency_key: `${runID}:evidence:${index}`,
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
      modality: "statement",
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

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_id: submissionID,
  provider_requests: stats.requests,
  input_rejection_failures: stats.input_rejection_failures,
  forwarded_requests: stats.forwarded,
  completed_jobs: jobs.filter((job) => job.status === "completed").length,
  rejected_jobs: jobs.filter((job) => job.failure_code === "embedding_input_rejected").length,
  public_status_bounded: true,
}, null, 2));

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

async function proxyJSON(path) {
  return httpJSON(`${proxyURL}${path}`, { method: "GET" });
}

function postgresQuery(sql) {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "embedding-resilience-e2e", sql], { encoding: "utf8" });
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
function delay(milliseconds) { return new Promise((resolve) => setTimeout(resolve, milliseconds)); }
