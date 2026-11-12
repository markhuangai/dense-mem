#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const runID = `submission-terminal-errors-${Date.now()}`;
let rpcID = 0;

const terminal = await mcpOperationalResult("remember", clientControlledBudgetFixture());
const submissionID = requiredString(terminal.submission_id, "submission_id");
assertTerminalErrors(terminal);
assertZeroSemanticWrites(submissionID, "client-controlled Evidence budget failure");

const removed = await mcpRaw("get_submission_status", { submission_id: "00000000-0000-0000-0000-000000000000" });
if (removed.error?.code !== -32601 || removed.result !== undefined) {
  throw new Error("removed get_submission_status remained callable");
}
console.log(JSON.stringify({
  status: "ok",
  scenario: "submission_terminal_errors",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  submission_id: submissionID,
  processing_state: terminal.processing_state,
  error_code: terminal.errors[0]?.code,
  terminal_errors_nonempty: true,
  zero_partial_semantic_state: true,
  client_controlled_budget_guidance: true,
  closed_codes: true,
  structured_content_matches_text: true,
  operational_is_error: true,
  removed_status_tool: true,
}, null, 2));

function assertZeroSemanticWrites(submissionID, label) {
  const zeroSemanticWrites = postgresQuery(`
  SELECT count(*) FROM knowledge_ingests
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND ingest_id = ${sqlLiteral(submissionID)}::uuid
  UNION ALL
  SELECT count(*) FROM evidence_fragments
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND ingest_id = ${sqlLiteral(submissionID)}::uuid
  UNION ALL
  SELECT count(*) FROM semantic_assessments
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND attempt_id = ${sqlLiteral(submissionID)}::uuid
;
`).split(/\r?\n/).filter(Boolean).map(Number);
if (zeroSemanticWrites.length !== 3 || zeroSemanticWrites.some((count) => count !== 0)) {
    throw new Error(`${label} created partial semantic state: ${zeroSemanticWrites.join(",")}`);
  }
}

function assertTerminalErrors(status) {
  if (status.contract_version !== "dense-mem.v2.8.0" || status.processing_state !== "failed" || !status.errors?.length) throw new Error("Evidence-only failure must have a terminal typed outcome");
  if (Object.hasOwn(status, "relationship_results")) throw new Error("retired factual results returned");
  for (const item of status.errors) {
    if (item.code !== "input_budget_exceeded" || !item.message || item.message.length > 512 || !item.remediation || item.remediation.length > 512 || item.retryable !== false) throw new Error("Evidence budget error must be bounded, typed and non-retryable");
    if (item.reason_code !== "assessment_input" || item.details?.client_controlled !== true || item.next_action !== "resubmit_remember" || !item.remediation.includes("new idempotency_key")) throw new Error("Evidence budget failure must identify its owner and supported correction");
    if (/[\r\n]|Bearer |postgres(?:ql)?:\/\/|https?:\/\//i.test(`${item.message} ${item.remediation}`)) throw new Error("public budget error leaked operational detail");
  }
}

async function mcpOperationalResult(name, args) {
  const response = await mcpRaw(name, args);
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} returned a protocol error`);
  if (response.result.isError !== true) throw new Error(`MCP ${name} operational failure omitted isError`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} returned no JSON content`);
  const result = JSON.parse(text);
  if (stableJSON(result) !== stableJSON(response.result.structuredContent)) {
    throw new Error(`MCP ${name} text and structured content differed`);
  }
  return result;
}

async function mcpRaw(name, args) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }),
  });
  const text = await response.text();
  return text ? JSON.parse(text) : {};
}

function clientControlledBudgetFixture() {
  return {
    target_space: "team_shared", idempotency_key: `${runID}:client-budget`,
    evidence: Array.from({ length: 20 }, (_, index) => ({ content: `${index} ` + "界".repeat(995), source_type: "manual", source: `${runID}:${index}`, source_group: runID, force_insert: true })),
  };
}

function postgresQuery(sql) {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "submission-terminal-errors-e2e", sql], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) throw new Error("postgres terminal-error fixture failed");
  return result.stdout.trim();
}

function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }
function requiredString(value, field) { if (typeof value !== "string" || !value.trim()) throw new Error(`${field} missing`); return value; }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function stableJSON(value) { if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`; if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`; return JSON.stringify(value); }
function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
