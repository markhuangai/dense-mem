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
const receipt = await mcpSuccess("remember", overflowFixture());
const submissionID = requiredString(receipt.submission_id, "submission_id");
const terminal = await waitForTerminal(submissionID);
const repeated = await mcpSuccess("get_submission_status", { submission_id: submissionID });
if (stableJSON(terminal) !== stableJSON(repeated)) throw new Error("terminal status changed between polls");
assertTerminalErrors(terminal);

const missing = await mcpRaw("get_submission_status", { submission_id: "00000000-0000-0000-0000-000000000000" });
if (!missing.error || missing.result !== undefined || typeof missing.error.message !== "string" || missing.error.message.length > 512) {
  throw new Error("missing submission did not return a bounded error");
}
console.log(JSON.stringify({
  status: "ok",
  scenario: "submission_terminal_errors",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  submission_id: submissionID,
  processing_state: terminal.processing_state,
  terminal_errors_nonempty: true,
  closed_codes: true,
  stable_polling: true,
  missing_owner_bounded: true,
}, null, 2));

async function waitForTerminal(id) {
  for (let attempt = 0; attempt < 360; attempt += 1) {
    const status = await mcpSuccess("get_submission_status", { submission_id: id });
    if (["completed", "rejected", "failed", "quarantined"].includes(status.processing_state)) return status;
    await delay(2_000);
  }
  throw new Error("submission did not reach a terminal state");
}

function assertTerminalErrors(status) {
  if (!["rejected", "failed"].includes(status.processing_state) || !Array.isArray(status.errors) || status.errors.length === 0) {
    throw new Error("terminal failure returned an empty errors array");
  }
  const allowedCodes = new Set([
    "submission_semantic_hold", "submission_policy_rejected", "assessor_response_invalid", "assessor_unavailable",
    "submission_replacement_conflict", "submission_processing_failed", "search_indexing_delayed",
    "relationship_version_stale", "relationship_not_active", "object_kind_change_forbidden",
    "support_set_mismatch", "entity_not_found", "too_many_entity_candidates",
    "predicate_not_found", "predicate_subject_kind_mismatch", "predicate_object_kind_mismatch",
    "no_change", "confirmation_expired", "relationship_changed", "support_set_changed",
    "persistent_ambiguity", "inactive_relationship_collision",
  ]);
  const allowedMessages = new Set([
    "submission was rejected by semantic hold policy",
    "submission was rejected by semantic placement policy",
    "submission assessment returned an invalid response",
    "submission assessment was unavailable after bounded retries",
    "submission replacement conflicted with current state",
    "submission processing failed",
    "search indexing is delayed",
    "relationship version is stale",
    "relationship must be active, supported, and canonical",
    "a Value object cannot be replaced with an Entity",
    "supports must exactly match the relationship's effective evidence spans",
    "corrected Entity is not active and available to the team",
    "corrected Entity name has too many exact candidates",
    "predicate is not registered and active for the team",
    "predicate does not allow the corrected subject kind",
    "predicate does not allow the corrected object kind",
    "correction does not change the Relationship",
    "relationship correction confirmation expired",
    "relationship changed while confirmation was pending",
    "relationship supports changed while confirmation was pending",
    "selected Entity candidate is no longer available",
    "corrected Relationship collides with inactive or unsupported history",
    "Semantic search indexing is delayed.",
    "Semantic search indexing is delayed; check the control portal for recovery guidance.",
  ]);
  const seen = new Set();
  for (const item of status.errors) {
    if (item === null || typeof item !== "object" || Array.isArray(item) || typeof item.code !== "string" || typeof item.message !== "string") {
      throw new Error("terminal error fields were not strings");
    }
    const { code, message } = item;
    if (!allowedCodes.has(code) || !allowedMessages.has(message) || message.length === 0 || message.length > 512) {
      throw new Error("terminal error was not bounded and typed");
    }
    if (/[\r\n]|api[_-]?key|password|token|stack|provider|cookie|prompt|embedding|database|cross[- ]?team/i.test(message)) {
      throw new Error("terminal error leaked prohibited data");
    }
    if (seen.has(`${code}\0${message}`)) throw new Error("terminal errors were not deduplicated");
    seen.add(`${code}\0${message}`);
  }
}

async function mcpSuccess(name, args) {
  const response = await mcpRaw(name, args);
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} failed with a bounded error`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} returned no JSON content`);
  return JSON.parse(text);
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
    evidence.push({ content, source_type: "document", source: `${runID}:evidence:${evidenceIndex}`, source_group: runID, idempotency_key: `${runID}:evidence:${evidenceIndex}` });
    for (const index of indexes) relationships.push(relationship(content, evidenceIndex, `${runID}:${index}`, "A", predicateKey(index), "B"));
  }
  return { evidence, relationships, idempotency_key: `${runID}:batch` };
}

function relationship(content, evidenceIndex, ref, subject, predicate, object) {
  const subjectStart = content.indexOf(subject);
  const predicateStart = content.indexOf(predicate, subjectStart + subject.length);
  const objectStart = content.indexOf(object, predicateStart + predicate.length);
  return {
    ref,
    subject: { name: subject, entity_kind: "project", span: { evidence_index: evidenceIndex, start: subjectStart, end: subjectStart + subject.length } },
    predicate: { proposed_key: predicate, surface: predicate, span: { evidence_index: evidenceIndex, start: predicateStart, end: predicateStart + predicate.length } },
    object: { entity: { name: object, entity_kind: "product", span: { evidence_index: evidenceIndex, start: objectStart, end: objectStart + object.length } } },
    polarity: "+", modality: "statement", supports: [{ evidence_index: evidenceIndex, start: 0, end: Array.from(content).length }],
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
function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
