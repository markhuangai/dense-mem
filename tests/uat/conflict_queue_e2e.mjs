#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seededTeamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const reviewDriver = requiredEnv("DENSE_MEM_E2E_CONFLICT_REVIEW_DRIVER");
const prometheusURL = requiredEnv("DENSE_MEM_E2E_PROMETHEUS_URL").replace(/\/$/, "");
const runID = `conflict-queue-e2e-${Date.now()}`;
const submissionTimeoutSeconds = positiveIntEnv("DENSE_MEM_E2E_PLACEMENT_TIMEOUT_SECONDS", 240, 30, 900);

let rpcID = 0;

const queueFixture = await createConflictFixture("queue", seededTeamID);
const reviewFixture = await createConflictFixture("live-review", seededTeamID);
shapeQueueFixture(queueFixture, { overdue: true, lease: "active", nextReviewAt: "2999-01-01T00:00:00Z" });
shapeQueueFixture(reviewFixture, { overdue: true, lease: "idle", nextReviewAt: "2000-01-01T00:00:00Z" });

const initialQueue = await queueRequest(seededTeamID);
assert(initialQueue.data?.items?.some((item) => item.conflict_id === queueFixture.conflictID), "queue API omitted the shaped active conflict");
assert(initialQueue.data?.summary?.overdue_count >= 1, "queue summary omitted the overdue case");
assert(initialQueue.data?.summary?.active_lease_count >= 1, "queue summary omitted the active lease");
assert(initialQueue.data?.next_cursor === null, "single-page queue should return a null cursor");
assert(!JSON.stringify(initialQueue).includes("source_group_key"), "queue API exposed a raw source-group key");
assert(!JSON.stringify(initialQueue).includes("evidence_id"), "queue API exposed an evidence identifier");

const overdueQueue = await queueRequest(seededTeamID, "overdue");
assert(overdueQueue.data?.items?.every((item) => item.status === "overdue"), "overdue filter returned a non-overdue case");

const foreignTeam = await createTeam("foreign queue isolation");
const foreignQueue = await queueRequest(foreignTeam);
assert((foreignQueue.data?.items ?? []).length === 0, "foreign team unexpectedly saw queue rows");
assert(Number(foreignQueue.data?.summary?.open_count ?? 0) === 0, "foreign team inferred another team's queue count");

const reviewResult = runReview(reviewFixture, new Date(Date.now() + 2 * 24 * 60 * 60 * 1_000).toISOString());
assert(reviewResult.assessment_attempt_id, "live conflict review did not persist an assessment attempt");
const assessmentEvents = Number(postgresQuery(`
  SELECT count(*)::text
  FROM relationship_conflict_ai_assessment_events
  WHERE team_id = ${sqlLiteral(reviewFixture.teamID)}::uuid
    AND assessment_attempt_id = ${sqlLiteral(reviewResult.assessment_attempt_id)}::uuid
    AND action IN ('selected', 'abstained', 'failed')
`));
assert(assessmentEvents >= 1, "live conflict review did not persist a terminal assessment event");
const queueGauge = await waitForPrometheusMetric(
  `sum(densemem_conflict_queue_cases{team_id="${seededTeamID}",status="overdue"})`,
  (value) => value >= 1,
  60_000,
);
const collectionSuccess = await waitForPrometheusMetric(
  "densemem_conflict_queue_collection_success",
  (value) => value >= 1,
  60_000,
);

const telemetry = await controlJSON("/telemetry?window=1h&scope=system");
const collectionCard = (telemetry.data?.current_cards ?? []).find((card) => card.id === "conflict_queue_collection_success");
assert(collectionCard?.available === true && collectionCard.value >= 1, "operator telemetry omitted collection success");

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  team_id: seededTeamID,
  queue_conflict_id: queueFixture.conflictID,
  review_conflict_id: reviewFixture.conflictID,
  foreign_team_isolated: true,
  assessment_events: assessmentEvents,
  review_stage: reviewResult.stage,
  overdue_queue_gauge: queueGauge,
  collection_success: collectionSuccess,
  operator_collection_card: true,
}, null, 2));

async function createConflictFixture(label, teamID) {
  const profileA = await createCredential(teamID, `${runID} ${label} A`);
  const profileB = await createCredential(teamID, `${runID} ${label} B`);
  const subjectName = `${runID} ${label} project`;
  const objectAName = `${runID} ${label} PostgreSQL`;
  const objectBName = `${runID} ${label} GraphDB`;

  const first = await submitRelationship(profileA.apiKey, teamID, {
    label: `${label}-a`, subjectName, objectName: objectAName,
    sourceGroup: `${runID}:${label}:source:a`, authority: "primary",
  });
  const firstTrace = await mcpSuccess(profileA.apiKey, "trace_memory", { relationship_id: first.relationshipID, include_evidence_content: true });
  const subjectEntityID = stringAt(firstTrace, ["relationship", "subject_entity_id"]);
  const objectAEntityID = stringAt(firstTrace, ["relationship", "object_entity_id"]);
  assert(subjectEntityID && objectAEntityID, `first ${label} trace omitted canonical entities`);

  const second = await submitRelationship(profileB.apiKey, teamID, {
    label: `${label}-b`, subjectName, objectName: objectBName,
    subjectEntityID, sourceGroup: `${runID}:${label}:source:b`, authority: "primary",
  });
  const conflict = await currentConflict(profileB.apiKey, second.relationshipID, "open");
  return {
    teamID,
    profileA,
    profileB,
    subjectName,
    objectAName,
    objectAEntityID,
    subjectEntityID,
    relationshipA: first.relationshipID,
    relationshipB: second.relationshipID,
    conflictID: String(conflict.conflict_id),
  };
}

async function submitRelationship(apiKey, teamID, input) {
  const evidence = `${input.subjectName} primary database is ${input.objectName}.`;
  const subjectStart = evidence.indexOf(input.subjectName);
  const predicateStart = evidence.indexOf("primary database", subjectStart);
  const objectStart = evidence.indexOf(input.objectName, predicateStart);
  const receipt = await mcpSuccess(apiKey, "remember", {
    idempotency_key: `${runID}:${input.label}`,
    evidence: [{
      content: evidence,
      source_type: "document",
      source: input.sourceGroup,
      source_group: input.sourceGroup,
      authority: input.authority,
      idempotency_key: `${runID}:${input.label}:evidence`,
    }],
    relationships: [{
      ref: `${input.label}:relationship`,
      subject: {
        name: input.subjectName,
        entity_kind: "project",
        ...(input.subjectEntityID ? { known_entity_id: input.subjectEntityID } : {}),
        span: { evidence_index: 0, start: runeOffset(evidence, subjectStart), end: runeOffset(evidence, subjectStart + input.subjectName.length) },
      },
      predicate: {
        proposed_key: "primary_database",
        surface: "primary database",
        span: { evidence_index: 0, start: runeOffset(evidence, predicateStart), end: runeOffset(evidence, predicateStart + "primary database".length) },
      },
      object: { entity: {
        name: input.objectName,
        entity_kind: "product",
        span: { evidence_index: 0, start: runeOffset(evidence, objectStart), end: runeOffset(evidence, objectStart + input.objectName.length) },
      } },
      polarity: "+",
      modality: "statement",
      supports: [{ evidence_index: 0, start: 0, end: Array.from(evidence).length }],
    }],
  });
  const submissionID = String(receipt.submission_id ?? "");
  assert(submissionID, `remember ${input.label} omitted submission ID`);
  const status = await waitForSubmission(apiKey, submissionID);
  assert(status.processing_state === "completed", `submission ${input.label} was ${status.processing_state}`);
  const evidenceID = String(status.evidence?.[0]?.evidence_id ?? "");
  assert(evidenceID, `submission ${input.label} omitted evidence lineage`);
  const relationshipID = postgresQuery(`
    SELECT observation.relationship_id::text
    FROM relationship_observations AS observation
    WHERE observation.team_id = ${sqlLiteral(teamID)}::uuid
      AND observation.ingest_id = ${sqlLiteral(submissionID)}::uuid
      AND observation.relationship_id IS NOT NULL
    ORDER BY observation.created_at, observation.observation_id
    LIMIT 1
  `);
  assert(relationshipID, `submission ${input.label} omitted relationship lineage`);
  return { submissionID, relationshipID, evidenceID };
}

function shapeQueueFixture(fixture, { overdue, lease, nextReviewAt }) {
  const dueAt = overdue ? "2000-01-01T00:00:00Z" : "2999-01-01T00:00:00Z";
  const leaseUntil = lease === "active" ? "2999-01-01T00:00:00Z" : null;
  const sql = `
    UPDATE relationship_conflict_cases
    SET status = ${sqlLiteral(overdue ? "overdue" : "open")},
        review_due_at = ${sqlLiteral(dueAt)},
        next_review_at = ${sqlLiteral(nextReviewAt)},
        lease_worker_id = ${sqlLiteral(lease === "active" ? "conflict-queue-e2e" : "")},
        lease_until = ${leaseUntil === null ? "NULL" : `${sqlLiteral(leaseUntil)}::timestamptz`},
        updated_at = now()
    WHERE team_id = ${sqlLiteral(fixture.teamID)}::uuid
      AND conflict_id = ${sqlLiteral(fixture.conflictID)}::uuid
  `;
  postgresMutation(sql);
}

async function queueRequest(teamID, status = "") {
  const suffix = status ? `?status=${encodeURIComponent(status)}` : "";
  return controlJSON(`/teams/${teamID}/conflicts/queue${suffix}`);
}

async function currentConflict(apiKey, relationshipID, status) {
  for (let attempt = 0; attempt < 240; attempt += 1) {
    const trace = await mcpSuccess(apiKey, "trace_memory", { relationship_id: relationshipID });
    const conflict = (trace.conflicts ?? []).find((item) => item.status === status && (item.positions ?? []).some((position) => (position.relationship_ids ?? []).includes(relationshipID)));
    if (conflict) return conflict;
    await delay(250);
  }
  throw new Error(`timed out waiting for ${status} conflict`);
}

async function waitForSubmission(apiKey, submissionID) {
  const attempts = Math.ceil((submissionTimeoutSeconds * 1_000) / 250);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const status = await mcpSuccess(apiKey, "get_submission_status", { submission_id: submissionID });
    if (["completed", "rejected", "failed", "quarantined"].includes(status.processing_state)) return status;
    await delay(250);
  }
  throw new Error(`timed out waiting for submission ${submissionID}`);
}

async function waitForPrometheusMetric(query, predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await prometheusValue(query);
    if (value !== null && predicate(value)) return value;
    await delay(1_000);
  }
  throw new Error(`timed out waiting for Prometheus metric ${query}`);
}

function runReview(fixture, now) {
  const result = spawnSync(reviewDriver, [
    "--team-id", fixture.teamID,
    "--conflict-id", fixture.conflictID,
    "--now", now,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    env: process.env,
    encoding: "utf8",
    timeout: 60_000,
  });
  if (result.status !== 0) {
    const details = [result.error?.message, result.stderr || result.stdout].filter(Boolean).join(": ");
    throw new Error(`live conflict review driver failed (${result.status}): ${redact(details)}`);
  }
  const line = result.stdout.trim().split(/\r?\n/).findLast((item) => item.trim().startsWith("{"));
  if (!line) throw new Error(`live conflict review driver returned no JSON: ${redact(result.stdout)}`);
  return JSON.parse(line);
}

async function prometheusValue(query) {
  const url = new URL("/api/v1/query", `${prometheusURL}/`);
  url.searchParams.set("query", query);
  const response = await fetch(url);
  if (!response.ok) return null;
  const body = await response.json();
  const value = body.data?.result?.[0]?.value?.[1];
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

async function createTeam(label) {
  const response = await controlJSON("/teams", { method: "POST", body: JSON.stringify({ name: `${runID} ${label}`, description: "isolated conflict queue e2e fixture" }) });
  const teamID = String(response.data?.id ?? "");
  assert(teamID, `control API did not create ${label}`);
  return teamID;
}

async function createCredential(teamID, name) {
  const response = await controlJSON(`/teams/${teamID}/credentials`, { method: "POST", body: JSON.stringify({ name, role: "member", scopes: ["read", "write"], rate_limit: 300 }) });
  const apiKey = String(response.data?.api_key ?? "");
  const profileID = String(response.data?.credential?.id ?? "");
  assert(apiKey && profileID, `credential ${name} omitted key material or ID`);
  return { apiKey, profileID };
}

async function mcpSuccess(apiKey, name, args) {
  const response = await rpc(apiKey, "tools/call", { name, arguments: args });
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} failed: ${redact(JSON.stringify(response.error ?? {}))}`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} omitted JSON content`);
  return JSON.parse(text);
}

async function rpc(apiKey, method, params) {
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

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) throw new Error(`HTTP ${response.status} ${url}: ${redact(text)}`);
  return text ? JSON.parse(text) : {};
}

function postgresQuery(sql) {
  const normalized = sql.trim();
  if (!/^(SELECT|WITH)\b/i.test(normalized)) throw new Error("read-only PostgreSQL helper rejected a non-read query");
  return postgresExec(sql, "conflict-queue-e2e").trim();
}

function postgresMutation(sql) {
  if (!/^UPDATE relationship_conflict_cases\b/i.test(sql.trim())) throw new Error("queue e2e mutation helper only permits conflict case shaping");
  postgresExec(sql, "conflict-queue-e2e-mutation");
}

function postgresExec(sql, label) {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec",
    'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', label, sql,
  ], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) throw new Error(`PostgreSQL e2e query failed: ${redact(result.stderr || result.stdout)}`);
  return result.stdout;
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function stringAt(value, path) {
  let current = value;
  for (const key of path) current = current?.[key];
  return typeof current === "string" ? current : "";
}

function runeOffset(value, byteOffset) {
  return Array.from(value.slice(0, byteOffset)).length;
}

function positiveIntEnv(name, fallback, minimum, maximum) {
  const value = Number.parseInt(process.env[name] ?? String(fallback), 10);
  if (!Number.isInteger(value) || value < minimum || value > maximum) throw new Error(`${name} must be between ${minimum} and ${maximum}`);
  return value;
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required for conflict queue e2e`);
  return value;
}

function redact(value) {
  return String(value).replaceAll(/(api[_-]?key|authorization|password|token)[=:][^,\s}]+/gi, "$1=[redacted]");
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}
