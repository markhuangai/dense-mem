#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const runID = `submission-terminal-errors-${Date.now()}`;
const predicatePrefix = runID.replace(/[^A-Za-z0-9]+/g, "_").toLowerCase();
let rpcID = 0;

seedOverflowPredicates();
const terminal = await mcpOperationalResult("remember", overflowFixture());
const submissionID = requiredString(terminal.submission_id, "submission_id");
assertOperationalError(terminal);

const removed = await mcpRaw("get_submission_status", { submission_id: "00000000-0000-0000-0000-000000000000" });
if (removed.error?.code !== -32601 || removed.result !== undefined) {
  throw new Error("removed get_submission_status remained callable");
}
console.log(JSON.stringify({
  status: "ok",
  scenario: "submission_terminal_errors",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  submission_id: submissionID,
  error_code: terminal.errors[0].code,
  terminal_errors_nonempty: true,
  structured_content_matches_text: true,
  operational_is_error: true,
  removed_status_tool: true,
}, null, 2));

function assertOperationalError(result) {
  const expectedKeys = ["contract_version", "correlation_id", "errors", "submission_id", "submission_kind"];
  const actualKeys = Object.keys(result).sort();
  if (stableJSON(actualKeys) !== stableJSON(expectedKeys)) {
    throw new Error(`operational error fields differed: ${JSON.stringify(actualKeys)}`);
  }
  if (result.contract_version !== "dense-mem.v2.6.1" || result.submission_kind !== "remember") {
    throw new Error(`operational error identity differed: ${JSON.stringify(result)}`);
  }
  requiredString(result.correlation_id, "correlation_id");
  if (!Array.isArray(result.errors) || result.errors.length !== 1) {
    throw new Error(`operational error count differed: ${JSON.stringify(result.errors)}`);
  }
  const expectedError = {
    code: "input_budget_exceeded",
    message: "the semantic assessor input exceeded the configured budget",
    retryable: false,
    next_action: "contact_operator",
    remediation: "Contact an operator with submission_id and correlation_id.",
  };
  if (stableJSON(result.errors[0]) !== stableJSON(expectedError)) {
    throw new Error(`operational error guidance differed: ${JSON.stringify(result.errors[0])}`);
  }
  if (/[\r\n]|api[_ -]?key|password|cookie|authorization|bearer|system prompt|stack trace|sqlstate|postgres(?:ql)?/i.test(JSON.stringify(result))) {
    throw new Error("operational error leaked prohibited data");
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

function overflowFixture() {
  const evidence = [];
  const relationships = [];
  for (let evidenceIndex = 0; evidenceIndex < Math.ceil(101 / 6); evidenceIndex += 1) {
    const clauses = [];
    const indexes = [];
    for (let slot = 0; slot < 6; slot += 1) {
      const index = evidenceIndex * 6 + slot;
      if (index >= 101) break;
      clauses.push(`A ${predicateKey(index)} B`);
      indexes.push(index);
    }
    const content = `${clauses.join(". ")}.`;
    evidence.push({ content, source_type: "document", source: `${runID}:evidence:${evidenceIndex}`, source_group: runID });
    for (const index of indexes) relationships.push(relationship(content, evidenceIndex, `${runID}:${index}`, "A", predicateKey(index), "B"));
  }
  return { evidence, relationships, idempotency_key: `${runID}:batch` };
}

function relationship(content, evidenceIndex, ref, subject, predicate, object) {
  return {
    ref,
    subject: { name: subject, entity_kind: "project" },
    predicate: { proposed_key: predicate },
    object: { entity: { name: object, entity_kind: "product" } },
    polarity: "+", evidence_indices: [evidenceIndex],
  };
}

function seedOverflowPredicates() {
  const team = sqlLiteral(teamID);
  const prefix = sqlLiteral(`${predicatePrefix}_predicate_`);
  postgresQuery(`
    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds,
      allowed_object_kinds, relationship_kind, current_cardinality,
      lifecycle_state, origin, metadata
    )
    SELECT ${team}::uuid, ${prefix} || series::text, 1, ARRAY[]::text[],
      ARRAY['project']::text[], ARRAY['product']::text[], 'state', 'many',
      'active', 'built_in', '{}'::jsonb
    FROM generate_series(0, 100) AS series
    ON CONFLICT (team_id, predicate_key, version) DO NOTHING;
  `);
}

function postgresQuery(sql) {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "submission-terminal-errors-e2e", sql], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) throw new Error("postgres terminal-error fixture failed");
  return result.stdout.trim();
}

function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }
function predicateKey(index) { return `${predicatePrefix}_predicate_${index}`; }
function requiredString(value, field) { if (typeof value !== "string" || !value.trim()) throw new Error(`${field} missing`); return value; }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function stableJSON(value) { if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`; if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`; return JSON.stringify(value); }
