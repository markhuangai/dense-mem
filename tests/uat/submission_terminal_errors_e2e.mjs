#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
let rpcID = 0;
const runID = `submission-terminal-errors-${Date.now()}`;
const predicatePrefix = runID.replace(/[^A-Za-z0-9]+/g, "_").toLowerCase();

seedOverflowPredicates();
const submission = await mcpSuccess("remember", overflowFixture());
const submissionID = requiredString(submission.submission_id, "submission_id");
const terminal = await waitForTerminal(submissionID);
const repeated = await mcpSuccess("get_submission_status", { submission_id: submissionID });
if (JSON.stringify(terminal) !== JSON.stringify(repeated)) throw new Error("terminal status changed between polls");
if (terminal.processing_state !== "failed" || !Array.isArray(terminal.errors) || terminal.errors.length === 0) throw new Error("terminal failure returned an empty errors array");
for (const item of terminal.errors ?? []) {
  if (!/^[a-z0-9_]+$/.test(String(item.code)) || typeof item.message !== "string" || item.message.length > 256) throw new Error("terminal error was not bounded and typed");
  if (/[\r\n]|api[_-]?key|password|token|stack/i.test(item.message)) throw new Error("terminal error leaked prohibited data");
}
const missing = await mcpRaw("get_submission_status", { submission_id: "00000000-0000-0000-0000-000000000000" });
if (!missing.error || missing.result !== undefined) throw new Error("missing submission did not return a bounded error");
console.log(JSON.stringify({ status: "ok", scenario: "submission_terminal_errors", submission_id: submissionID, processing_state: terminal.processing_state, stable_polling: true }, null, 2));

async function waitForTerminal(id) {
  for (let attempt = 0; attempt < 360; attempt += 1) {
    const status = await mcpSuccess("get_submission_status", { submission_id: id });
    if (["completed", "rejected", "failed", "quarantined"].includes(status.processing_state)) return status;
    await new Promise((resolve) => setTimeout(resolve, 2_000));
  }
  throw new Error("submission did not reach a terminal state");
}
async function mcpSuccess(name, args) {
  const response = await mcpRaw(name, args);
  if (response.error) throw new Error(`MCP ${name} failed (${response.error.code}): ${String(response.error.message ?? "error").slice(0, 256)}`);
  if (response.result === undefined) throw new Error(`MCP ${name} returned no result`);
  const text = response.result?.content?.[0]?.text;
  return text ? JSON.parse(text) : response.result;
}
async function mcpRaw(name, args) { const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${apiKey}`, "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }) }); const text = await response.text(); return text ? JSON.parse(text) : {}; }
function requiredString(value, field) { if (typeof value !== "string" || !value.trim()) throw new Error(`${field} missing`); return value; }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }

function relationship(content, evidenceIndex, ref, subject, predicate, object, subjectKind, objectKind) {
  const subjectStart = content.indexOf(subject);
  const predicateStart = content.indexOf(predicate, subjectStart + subject.length);
  const objectStart = content.indexOf(object, predicateStart + predicate.length);
  return {
    ref,
    subject: { name: subject, entity_kind: subjectKind, span: { evidence_index: evidenceIndex, start: subjectStart, end: subjectStart + subject.length } },
    predicate: { proposed_key: predicate, surface: predicate, span: { evidence_index: evidenceIndex, start: predicateStart, end: predicateStart + predicate.length } },
    object: { entity: { name: object, entity_kind: objectKind, span: { evidence_index: evidenceIndex, start: objectStart, end: objectStart + object.length } } },
    polarity: "+",
    modality: "statement",
    supports: [{ evidence_index: evidenceIndex, start: 0, end: Array.from(content).length }],
  };
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
    const text = `${clauses.join(". ")}.`;
    evidence.push({
      content: text,
      source_type: "document",
      source: `${runID}:overflow:evidence:${evidenceIndex}`,
      source_group: `${runID}:overflow`,
      idempotency_key: `${runID}:overflow:evidence:${evidenceIndex}`,
    });
    for (const index of indexes) {
      relationships.push(relationship(text, evidenceIndex, `${runID}:overflow:${index}`, "A", predicateKey(index), "B", "project", "product"));
    }
  }
  return {
    evidence,
    relationships,
    idempotency_key: `${runID}:overflow`,
  };
}

function predicateKey(index) {
  return `${predicatePrefix}_predicate_${index}`;
}

function seedOverflowPredicates() {
  const prefix = sqlLiteral(`${predicatePrefix}_predicate_`);
  const team = sqlLiteral(teamID);
  postgresQuery(`
    INSERT INTO semantic_team_refs (team_id)
    VALUES (${team}::uuid)
    ON CONFLICT (team_id) DO NOTHING;

    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds,
      allowed_object_kinds, relationship_kind, current_cardinality,
      lifecycle_state, origin, metadata
    )
    SELECT ${team}::uuid,
           ${prefix} || series::text,
           1,
           ARRAY[]::text[],
           ARRAY['project']::text[],
           ARRAY['product']::text[],
           'state',
           'many',
           'active',
           'built_in',
           '{}'::jsonb
    FROM generate_series(0, 100) AS series;

    SELECT count(*)
    FROM team_predicate_definitions
    WHERE team_id = ${team}::uuid
      AND predicate_key LIKE ${prefix} || '%';
  `);
}

function postgresQuery(sql) {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile,
    "exec", "-T", "postgres", "sh", "-ec",
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "submission-terminal-errors-e2e", sql,
  ], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) {
    throw new Error(`postgres query failed (${result.status})`);
  }
  return result.stdout.trim();
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}
