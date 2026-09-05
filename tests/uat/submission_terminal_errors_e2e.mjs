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
assertTerminalErrors(terminal, { serverOwned: true });
assertZeroSemanticWrites(submissionID, "server-owned budget failure");

const clientControlled = await mcpOperationalResult("remember", clientControlledBudgetFixture());
const clientSubmissionID = requiredString(clientControlled.submission_id, "client-controlled submission_id");
assertTerminalErrors(clientControlled, { serverOwned: false });
assertZeroSemanticWrites(clientSubmissionID, "client-controlled budget failure");

const removed = await mcpRaw("get_submission_status", { submission_id: "00000000-0000-0000-0000-000000000000" });
if (removed.error?.code !== -32601 || removed.result !== undefined) {
  throw new Error("removed get_submission_status remained callable");
}
console.log(JSON.stringify({
  status: "ok",
  scenario: "submission_terminal_errors",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  submission_id: submissionID,
  client_controlled_submission_id: clientSubmissionID,
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
  WHERE team_id = '${sqlLiteral(teamID)}'::uuid
    AND ingest_id = '${sqlLiteral(submissionID)}'::uuid
  UNION ALL
  SELECT count(*) FROM evidence_fragments
  WHERE team_id = '${sqlLiteral(teamID)}'::uuid
    AND ingest_id = '${sqlLiteral(submissionID)}'::uuid
  UNION ALL
  SELECT count(*) FROM semantic_assessments
  WHERE team_id = '${sqlLiteral(teamID)}'::uuid
    AND attempt_id = '${sqlLiteral(submissionID)}'::uuid
  UNION ALL
  SELECT count(*) FROM relationship_observations
  WHERE team_id = '${sqlLiteral(teamID)}'::uuid
    AND ingest_id = '${sqlLiteral(submissionID)}'::uuid;
`).split(/\r?\n/).filter(Boolean).map(Number);
if (zeroSemanticWrites.length !== 4 || zeroSemanticWrites.some((count) => count !== 0)) {
    throw new Error(`${label} created partial semantic state: ${zeroSemanticWrites.join(",")}`);
  }
}

function assertTerminalErrors(status, options = {}) {
  if (!["failed"].includes(status.processing_state) || !Array.isArray(status.errors) || status.errors.length === 0) {
    throw new Error("terminal failure returned an empty errors array");
  }
  const allowedCodes = new Set([
    "provider_unavailable", "provider_response_invalid", "input_budget_exceeded", "configuration_invalid", "database_failure", "internal_failure",
    "submission_policy_rejected",
    "relationship_version_stale", "relationship_not_active", "object_kind_change_forbidden",
    "support_set_mismatch", "entity_not_found", "too_many_entity_candidates",
    "predicate_not_found", "predicate_subject_kind_mismatch", "predicate_object_kind_mismatch",
    "no_change", "confirmation_expired", "relationship_changed", "support_set_changed",
    "persistent_ambiguity", "inactive_relationship_collision",
  ]);
  const allowedMessages = new Set([
    "the semantic assessor was unavailable",
    "the semantic assessor returned an invalid response",
    "the semantic assessor input exceeded the configured budget",
    "Dense-Mem is missing valid semantic-assessor configuration",
    "Dense-Mem could not persist the submission",
    "Dense-Mem could not complete the submission",
    "search indexing is delayed",
    "submission was rejected by semantic policy",
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
  const allowedActions = new Set([
    "retry_same_request", "resubmit_remember", "retry_correction", "retry_dream_feedback", "contact_operator", "none",
  ]);
  const seen = new Set();
  for (const item of status.errors) {
    if (item === null || typeof item !== "object" || Array.isArray(item) ||
        typeof item.code !== "string" || typeof item.message !== "string" ||
        typeof item.retryable !== "boolean" || typeof item.next_action !== "string" ||
        typeof item.remediation !== "string") {
      throw new Error("terminal error fields were incomplete");
    }
    const { code, message, next_action: nextAction, remediation } = item;
    if (!allowedCodes.has(code) || !allowedMessages.has(message) || !allowedActions.has(nextAction) ||
        message.length === 0 || message.length > 512 || remediation.length === 0 || remediation.length > 512) {
      throw new Error("terminal error was not bounded and typed");
    }
    if (/[\r\n]|api[_-]?key|password|token|stack|provider|cookie|prompt|embedding|database|cross[- ]?team/i.test(`${message} ${remediation}`)) {
      throw new Error("terminal error leaked prohibited data");
    }
    if (seen.has(`${code}\0${message}`)) throw new Error("terminal errors were not deduplicated");
    seen.add(`${code}\0${message}`);
    if (code === "input_budget_exceeded") {
      if (item.details === null || typeof item.details !== "object" || Array.isArray(item.details)) {
        throw new Error("assessor budget failure details must be a bounded object");
      }
      if (options.serverOwned) {
        if (typeof item.details.server_owned !== "boolean" || item.reason_code !== "predicate_options_overflow" || item.details.server_owned !== true ||
            item.next_action !== "contact_operator" || !item.remediation.includes("operator")) {
          throw new Error("server-owned assessor budget failure did not expose operator guidance");
        }
      } else if (typeof item.details.client_controlled !== "boolean" || item.reason_code !== "assessment_input" || item.details.client_controlled !== true ||
          item.next_action !== "resubmit_remember" || !item.remediation.includes("new idempotency_key")) {
        throw new Error("client-controlled assessor budget failure did not expose correction guidance");
      }
    }
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

function clientControlledBudgetFixture() {
  const comment = "界".repeat(1000);
  const relationships = [];
  for (let index = 0; index < 200; index += 1) {
    relationships.push({
      ref: `${runID}:client-budget:${index}`,
      subject: { name: "Client subject", entity_kind: "project" },
      predicate: { proposed_key: "uses" },
      object: { entity: { name: "Client object", entity_kind: "product" } },
      polarity: "+",
      evidence_indices: [0],
      client_comment: comment,
    });
  }
  return {
    evidence: [{ content: "Client-controlled assessor budget input.", source_type: "manual", source: `${runID}:client-budget`, source_group: runID }],
    relationships,
    idempotency_key: `${runID}:client-budget`,
  };
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
function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
