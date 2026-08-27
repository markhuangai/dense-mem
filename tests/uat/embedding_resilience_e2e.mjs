#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

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
  `Subject 1 uses Store 1. ${runID} embedding fixture token ${rejectedMarker}.`,
  `Subject 2 uses Store 2. ${runID} good evidence beta uses the durable store.`,
];
const terminal = await mcpTool("remember", {
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
const remember = terminal.value;
const stats = await proxyJSON("/stats");
if (stats.mode !== "input_rejected" || stats.requests !== 1 || stats.input_rejection_failures !== 1 ||
    stats.forwarded !== 0 || stableJSON(stats.request_item_counts) !== "[6]") {
  throw new Error(`embedding proxy did not observe the bounded batch call: ${JSON.stringify(stats)}`);
}
if (!terminal.isError) {
  throw new Error("embedding failure omitted MCP isError");
}
const expectedKeys = ["contract_version", "correlation_id", "errors", "submission_id", "submission_kind"];
if (stableJSON(Object.keys(remember).sort()) !== stableJSON(expectedKeys)) {
  throw new Error(`embedding failure fields differed: ${JSON.stringify(Object.keys(remember).sort())}`);
}
const expectedError = {
  code: "embedding_unavailable",
  message: "the embedding provider was unavailable",
  retryable: true,
  next_action: "retry_same_request",
  remediation: "Retry the same request with the same idempotency_key after the transient failure clears.",
};
if (stableJSON(remember.errors) !== stableJSON([expectedError])) {
  throw new Error(`embedding failure was not returned in the originating Remember result: ${JSON.stringify(remember)}`);
}
if (JSON.stringify(remember).includes(rejectedMarker) || JSON.stringify(remember).includes("payload_too_large")) {
  throw new Error("Remember result exposed provider input details");
}
const databaseState = JSON.parse(postgresQuery(`
  SELECT jsonb_build_object(
    'attempts', (
      SELECT count(*) FROM remember_attempts
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND idempotency_key = ${sqlLiteral(`${runID}:batch`)}
        AND outcome = 'failed'
        AND error_code = 'embedding_unavailable'
    ),
    'ingests', (
      SELECT count(*) FROM knowledge_ingests
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND idempotency_key = ${sqlLiteral(`${runID}:batch`)}
    ),
    'fragments', (
      SELECT count(*) FROM evidence_fragments
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND content LIKE ${sqlLiteral(`%${runID}%`)}
    ),
    'documents', (
      SELECT count(*) FROM search_documents
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND document_text LIKE ${sqlLiteral(`%${runID}%`)}
    )
  )::text;
`));
if (stableJSON(databaseState) !== stableJSON({ attempts: 1, ingests: 0, fragments: 0, documents: 0 })) {
  throw new Error(`embedding failure left partial canonical state: ${JSON.stringify(databaseState)}`);
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_id: remember.submission_id ?? "",
  provider_requests: stats.requests,
  input_rejection_failures: stats.input_rejection_failures,
  forwarded_requests: stats.forwarded,
  request_item_counts: stats.request_item_counts,
  inline_embedding_failure: true,
  zero_canonical_writes: true,
}, null, 2));

async function mcpTool(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }),
  });
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} returned a bounded error`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} returned no JSON content`);
  const value = JSON.parse(text);
  if (stableJSON(value) !== stableJSON(response.result.structuredContent)) {
    throw new Error(`MCP ${name} text and structured content differed`);
  }
  return { value, isError: response.result.isError === true };
}

async function proxyJSON(path) {
  return httpJSON(`${proxyURL}${path}`, { method: "GET" });
}

function postgresQuery(sql) {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec",
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "embedding-resilience-e2e", sql,
  ], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) throw new Error("postgres query failed");
  return result.stdout.trim();
}

async function httpJSON(url, options) {
  const response = await fetch(url, { ...options, signal: options?.signal ?? AbortSignal.timeout(30_000) });
  const text = await response.text();
  if (!response.ok) throw new Error(`HTTP ${response.status} ${new URL(url).pathname}`);
  return text ? JSON.parse(text) : {};
}

function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }
function stableJSON(value) { if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`; if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`; return JSON.stringify(value); }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
