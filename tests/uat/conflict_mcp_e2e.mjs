#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userUrl = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const placementTimeoutSeconds = positiveIntEnv("DENSE_MEM_E2E_PLACEMENT_TIMEOUT_SECONDS", 720, 30, 1800);
const conflictReviewTimeoutSeconds = positiveIntEnv("DENSE_MEM_E2E_CONFLICT_REVIEW_TIMEOUT_SECONDS", 240, 60, 600);

let rpcID = 0;

const runID = `conflict-e2e-${Date.now()}`;
const olderEffectiveAt = "2026-07-20T00:00:00Z";
const newerEffectiveAt = "2026-07-22T00:00:00Z";
const primaryProjectName = `ConflictE2E ${runID} project`;
const postgresName = `ConflictE2E ${runID} PostgreSQL`;
const graphDBName = `ConflictE2E ${runID} GraphDB`;

const profileA = await createProfile("Conflict E2E A");
const profileB = await createProfile("Conflict E2E B");
const profileC = await createProfile("Conflict E2E C");

const first = await rememberAndWait(profileA.apiKey, {
  idempotencyKey: `${runID}:a`,
  evidence: `ConflictE2E ${runID}: Effective ${newerEffectiveAt}, 🚀 ${primaryProjectName} primary database is ${postgresName} according to profile A.`,
  sourceGroup: `${runID}:source:a`,
  validFrom: newerEffectiveAt,
  subject: { ref: "project", name: primaryProjectName, kind: "project" },
  object: { ref: "postgres", name: postgresName, kind: "product" },
  relationshipID: "rel:primary-db-a",
});
const firstTrace = await mcpTool(profileA.apiKey, "trace_memory", {
  relationship_id: first.relationshipID,
  include_evidence_content: true,
});
const subjectEntityID = stringAt(firstTrace, ["relationship", "subject_entity_id"]);
const postgresEntityID = stringAt(firstTrace, ["relationship", "object_entity_id"]);
if (!subjectEntityID || !postgresEntityID) {
  throw new Error(`trace did not return canonical entity IDs: ${JSON.stringify(firstTrace)}`);
}

const second = await rememberAndWait(profileB.apiKey, {
  idempotencyKey: `${runID}:b`,
  evidence: `ConflictE2E ${runID}: Effective ${olderEffectiveAt}, ${primaryProjectName} primary database is ${graphDBName} according to profile B.`,
  sourceGroup: `${runID}:source:b`,
  validFrom: olderEffectiveAt,
  subject: { ref: "project", name: primaryProjectName, kind: "project", knownEntityID: subjectEntityID },
  object: { ref: "graphdb", name: graphDBName, kind: "product" },
  relationshipID: "rel:primary-db-b",
});

const openConflict = await waitForRelationshipConflict(profileB.apiKey, second.relationshipID, "open");
const conflictID = String(openConflict.conflict_id);
const conflictVersion = Number(openConflict.version);
if (!conflictID || !Number.isInteger(conflictVersion) || conflictVersion < 1) {
  throw new Error(`invalid open conflict summary: ${JSON.stringify(openConflict)}`);
}

await rememberAndWait(profileC.apiKey, {
  idempotencyKey: `${runID}:c`,
  evidence: `ConflictE2E ${runID}: Effective ${newerEffectiveAt}, ${primaryProjectName} primary database is ${postgresName} according to profile C.`,
  sourceGroup: `${runID}:source:c`,
  validFrom: newerEffectiveAt,
  subject: { ref: "project", name: primaryProjectName, kind: "project", knownEntityID: subjectEntityID },
  object: { ref: "postgres", name: postgresName, kind: "product", knownEntityID: postgresEntityID },
  relationshipID: "rel:primary-db-c",
  conflictContext: { conflict_id: conflictID, expected_version: conflictVersion },
});

const review = await runAutomaticConflictReview(conflictID);
if (review.status !== "resolved") {
  throw new Error(`automatic conflict reviewer did not resolve ${conflictID}: ${JSON.stringify(review)}`);
}

const resolvedConflict = await waitForRelationshipConflict(profileB.apiKey, second.relationshipID, "resolved");
if (resolvedConflict.conflict_id !== conflictID || resolvedConflict.status !== "resolved") {
  throw new Error(`resolved recall returned wrong conflict: ${JSON.stringify(resolvedConflict)}`);
}

const loserTrace = await mcpTool(profileB.apiKey, "trace_memory", {
  relationship_id: second.relationshipID,
  include_transitions: true,
  include_evidence_content: true,
});
if (stringAt(loserTrace, ["relationship", "relationship_status"]) !== "superseded") {
  throw new Error(`losing relationship was not superseded: ${JSON.stringify(loserTrace.relationship)}`);
}
if (!Array.isArray(loserTrace.conflicts) || !loserTrace.conflicts.some((item) => item.conflict_id === conflictID && item.status === "resolved")) {
  throw new Error(`trace did not include resolved conflict: ${JSON.stringify(loserTrace.conflicts)}`);
}

const overdueResolution = await resolveOverdueConflictThroughVerifier();

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  conflict_id: conflictID,
  first_relationship_id: first.relationshipID,
  losing_relationship_id: second.relationshipID,
  overdue_conflict_id: overdueResolution.conflictID,
  overdue_resolution_method: overdueResolution.method,
}, null, 2));

async function createProfile(name) {
  const response = await httpJSON(`${controlUrl}/control/api/teams/${teamID}/profiles`, {
    method: "POST",
    headers: {
      "Authorization": `Bearer ${controlToken}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      name,
      role: "member",
      scopes: ["read", "write"],
      rate_limit: 300,
    }),
  });
  const apiKey = stringAt(response, ["data", "api_key"]);
  const profileID = stringAt(response, ["data", "key", "id"]);
  if (!apiKey || !profileID) {
    throw new Error(`profile creation response missing api key/profile id (api_key_present=${Boolean(apiKey)}, profile_id_present=${Boolean(profileID)})`);
  }
  return { apiKey, profileID };
}

async function rememberAndWait(apiKey, input) {
  const evidence = input.evidence;
  const result = await mcpTool(apiKey, "remember", {
    evidence: [{
      content: evidence,
      source_type: "document",
      source: input.sourceGroup,
      source_group: input.sourceGroup,
      idempotency_key: input.idempotencyKey,
      ...(input.authority ? { authority: input.authority } : {}),
    }],
    relationships: [relationshipHint(input, evidence)],
  });
  const submissionID = String(result.submission_id ?? "");
  if (!submissionID) {
    throw new Error(`remember did not return submission_id: ${JSON.stringify(result)}`);
  }
  return await waitForPlacement(apiKey, submissionID, input.relationshipID);
}

function relationshipHint(input, evidence) {
  const predicateSurface = "primary database";
  const subjectStart = evidence.indexOf(input.subject.name);
  const predicateStart = evidence.indexOf(predicateSurface, subjectStart);
  const objectStart = evidence.indexOf(input.object.name, predicateStart);
  if (subjectStart < 0 || predicateStart < 0 || objectStart < 0) {
    throw new Error(`could not derive relationship spans for ${input.relationshipID}`);
  }
  const codePointOffset = (utf16Offset) => Array.from(evidence.slice(0, utf16Offset)).length;
  const subjectEnd = subjectStart + input.subject.name.length;
  const predicateEnd = predicateStart + predicateSurface.length;
  const objectEnd = objectStart + input.object.name.length;
  return {
    ref: input.relationshipID,
    subject: {
      name: input.subject.name,
      entity_kind: input.subject.kind,
      ...(input.subject.knownEntityID ? { known_entity_id: input.subject.knownEntityID } : {}),
      span: { evidence_index: 0, start: codePointOffset(subjectStart), end: codePointOffset(subjectEnd) },
    },
    predicate: {
      proposed_key: "primary_database",
      surface: predicateSurface,
      span: { evidence_index: 0, start: codePointOffset(predicateStart), end: codePointOffset(predicateEnd) },
    },
    object: {
      entity: {
        name: input.object.name,
        entity_kind: input.object.kind,
        ...(input.object.knownEntityID ? { known_entity_id: input.object.knownEntityID } : {}),
        span: { evidence_index: 0, start: codePointOffset(objectStart), end: codePointOffset(objectEnd) },
      },
    },
    polarity: "+",
    modality: "statement",
    ...(input.validFrom ? { valid_from: input.validFrom } : {}),
    ...(input.conflictContext ? { conflict_context: input.conflictContext } : {}),
    supports: [{ evidence_index: 0, start: 0, end: Array.from(evidence).length }],
  };
}

async function waitForPlacement(apiKey, submissionID, proposalID) {
  let lastSummary = {};
  const attempts = Math.ceil((placementTimeoutSeconds * 1000) / 2_000);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const placement = await mcpTool(apiKey, "get_submission_status", { submission_id: submissionID });
    if (placement.processing_state === "failed" || placement.processing_state === "quarantined") {
      throw new Error(`placement failed: ${JSON.stringify(placement)}`);
    }
    const row = postgresQuery(`
      SELECT COALESCE(observation.relationship_id::text, ''), observation.owner_profile_id::text
      FROM relationship_observations AS observation
      WHERE observation.team_id = ${sqlLiteral(teamID)}::uuid
        AND observation.ingest_id = ${sqlLiteral(submissionID)}::uuid
        AND observation.relationship_id IS NOT NULL
      ORDER BY observation.created_at ASC, observation.observation_id ASC
      LIMIT 1
    `).split("|");
    lastSummary = { processing_state: placement.processing_state, relationship_id: row[0] || "" };
    if (placement.processing_state === "completed" && row[0]) {
      return {
        ingestID: submissionID,
        relationshipID: String(row[0]),
        ownerProfileID: String(row[1]),
      };
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for placement ${submissionID} after ${placementTimeoutSeconds}s: ${JSON.stringify(lastSummary)}`);
}

async function waitForRelationshipConflict(apiKey, relationshipID, status) {
  for (let attempt = 0; attempt < 90; attempt += 1) {
    const trace = await mcpTool(apiKey, "trace_memory", { relationship_id: relationshipID });
    const conflict = (trace.conflicts ?? []).find((item) => (
      item.status === status
      && (item.positions ?? []).some((position) => (position.relationship_ids ?? []).includes(relationshipID))
    ));
    if (conflict) {
      return conflict;
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for ${status} conflict for relationship ${relationshipID}`);
}

async function waitForConflictID(apiKey, query, conflictID, status) {
  for (let attempt = 0; attempt < 90; attempt += 1) {
    const recall = await mcpTool(apiKey, "recall_memory", { query, limit: 10 });
    const conflict = (recall.conflicts ?? []).find((item) => item.conflict_id === conflictID && item.status === status);
    if (conflict) {
      return conflict;
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for ${status} conflict ${conflictID}`);
}

async function resolveOverdueConflictThroughVerifier() {
  const marker = `Overdue Conflict E2E ${runID}`;
  const overdueProjectName = `${marker} project`;
  const overduePostgresName = `${marker} PostgreSQL`;
  const overdueGraphDBName = `${marker} GraphDB`;
  const firstOverdue = await rememberAndWait(profileA.apiKey, {
    idempotencyKey: `${runID}:overdue:a`,
    evidence: `${marker}: ${overdueProjectName} primary database is ${overduePostgresName} according to profile A.`,
    sourceGroup: `${runID}:overdue:source:a`,
    authority: "primary",
    subject: { ref: "overdue-project", name: overdueProjectName, kind: "project" },
    object: { ref: "overdue-postgres", name: overduePostgresName, kind: "product" },
    relationshipID: "rel:overdue-primary-db-a",
  });
  const overdueTrace = await mcpTool(profileA.apiKey, "trace_memory", {
    relationship_id: firstOverdue.relationshipID,
    include_evidence_content: true,
  });
  const overdueSubjectEntityID = stringAt(overdueTrace, ["relationship", "subject_entity_id"]);
  const overduePostgresEntityID = stringAt(overdueTrace, ["relationship", "object_entity_id"]);
  if (!overdueSubjectEntityID || !overduePostgresEntityID) {
    throw new Error(`overdue trace did not return canonical entity IDs: ${JSON.stringify(overdueTrace)}`);
  }
  if (overdueSubjectEntityID === subjectEntityID) {
    throw new Error(`overdue fixture resolved onto the earlier conflict subject: ${JSON.stringify(overdueTrace.relationship)}`);
  }

  const secondOverdue = await rememberAndWait(profileB.apiKey, {
    idempotencyKey: `${runID}:overdue:b`,
    evidence: `${marker}: ${overdueProjectName} primary database is ${overdueGraphDBName} according to profile B.`,
    sourceGroup: `${runID}:overdue:source:b`,
    authority: "primary",
    subject: { ref: "overdue-project", name: overdueProjectName, kind: "project", knownEntityID: overdueSubjectEntityID },
    object: { ref: "overdue-graphdb", name: overdueGraphDBName, kind: "product" },
    relationshipID: "rel:overdue-primary-db-b",
  });
  const openOverdueConflict = await waitForRelationshipConflict(profileB.apiKey, secondOverdue.relationshipID, "open");
  const overdueConflictID = stringAt(openOverdueConflict, ["conflict_id"]);
  if (!overdueConflictID) {
    throw new Error(`overdue conflict did not return an ID: ${JSON.stringify(openOverdueConflict)}`);
  }
  const overdueRelationshipIDs = new Set((openOverdueConflict.positions ?? [])
    .flatMap((position) => position.relationship_ids ?? []));
  if (overdueRelationshipIDs.size !== 2 || !overdueRelationshipIDs.has(firstOverdue.relationshipID) || !overdueRelationshipIDs.has(secondOverdue.relationshipID)) {
    throw new Error(`overdue conflict did not contain the two tied positions: ${JSON.stringify(openOverdueConflict)}`);
  }

  if (!Number.isFinite(Date.parse(stringAt(openOverdueConflict, ["review_due_at"])))) {
    throw new Error(`overdue conflict did not return a valid review_due_at: ${JSON.stringify(openOverdueConflict)}`);
  }
  const overdueReview = await runAutomaticConflictReview(overdueConflictID);
  if (overdueReview.status !== "resolved" || overdueReview.planStatus !== "applied") {
    throw new Error(`overdue conflict reviewer did not complete: ${JSON.stringify(overdueReview)}`);
  }
  if (!["ai", "last_write_wins"].includes(overdueReview.method)) {
    throw new Error(`overdue conflict used an unexpected resolution method: ${JSON.stringify(overdueReview)}`);
  }
  if (!overdueReview.assessmentAttemptID || overdueReview.derivationCount < 1) {
    throw new Error(`overdue conflict did not record assessment/retraction lineage: ${JSON.stringify(overdueReview)}`);
  }

  await waitForConflictID(profileA.apiKey, marker, overdueConflictID, "resolved");
  const overdueTraces = await Promise.all([
    mcpTool(profileA.apiKey, "trace_memory", { relationship_id: firstOverdue.relationshipID, include_transitions: true }),
    mcpTool(profileB.apiKey, "trace_memory", { relationship_id: secondOverdue.relationshipID, include_transitions: true }),
  ]);
  const supersededCount = overdueTraces.filter((trace) => stringAt(trace, ["relationship", "relationship_status"]) === "superseded").length;
  if (supersededCount !== 1) {
    throw new Error(`overdue conflict did not suppress exactly one losing relationship: ${JSON.stringify(overdueTraces)}`);
  }
  return { conflictID: overdueConflictID, method: overdueReview.method };
}

async function runAutomaticConflictReview(conflictID) {
  markConflictReviewDue(conflictID);

  let last = {};
  const attempts = Math.ceil((conflictReviewTimeoutSeconds * 1000) / 2_000);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const row = postgresQuery(`
      SELECT conflict.status,
             COALESCE(plan.method, ''),
             COALESCE(plan.assessment_attempt_id::text, ''),
             COALESCE(plan.status, ''),
             (
               SELECT count(*)
               FROM relationship_conflict_evidence_derivations derivation
               WHERE derivation.team_id = conflict.team_id
                 AND derivation.conflict_id = conflict.conflict_id
             )
      FROM relationship_conflict_cases conflict
      LEFT JOIN LATERAL (
        SELECT method, assessment_attempt_id, status
        FROM relationship_conflict_resolution_plans
        WHERE team_id = conflict.team_id
          AND conflict_id = conflict.conflict_id
        ORDER BY created_at DESC, resolution_plan_id DESC
        LIMIT 1
      ) plan ON true
      WHERE conflict.team_id = ${sqlLiteral(teamID)}::uuid
        AND conflict.conflict_id = ${sqlLiteral(conflictID)}::uuid;
    `);
    const [status, method, assessmentAttemptID, planStatus, derivationCountRaw] = row.split("|");
    last = {
      status,
      method,
      assessmentAttemptID,
      planStatus,
      derivationCount: Number(derivationCountRaw),
    };
    if (status === "resolved") {
      return last;
    }
    if (attempt % 5 === 0) {
      markConflictReviewDue(conflictID);
      requeueCompletedConflictReviewRun(conflictID);
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for automatic conflict review ${conflictID}: ${JSON.stringify(last)}`);
}

function markConflictReviewDue(conflictID) {
  postgresQuery(`
    UPDATE relationship_conflict_cases
    SET review_due_at = clock_timestamp() - interval '1 second',
        next_review_at = clock_timestamp() - interval '1 second',
        attempts = 0,
        lease_worker_id = '',
        lease_until = NULL,
        last_error = '',
        updated_at = now()
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND conflict_id = ${sqlLiteral(conflictID)}::uuid
      AND status IN ('open', 'overdue')
      AND (lease_until IS NULL OR lease_until < clock_timestamp());
  `);
}

function requeueCompletedConflictReviewRun(conflictID) {
  postgresQuery(`
    WITH latest_run AS (
      SELECT review_run_id
      FROM relationship_conflict_review_runs
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND status = 'completed'
        AND local_run_date = CURRENT_DATE
      ORDER BY completed_at DESC NULLS LAST, review_run_id DESC
      LIMIT 1
    )
    UPDATE relationship_conflict_cases AS conflict
    SET last_review_run_id = latest_run.review_run_id,
        updated_at = now()
    FROM latest_run
    WHERE conflict.team_id = ${sqlLiteral(teamID)}::uuid
      AND conflict.conflict_id = ${sqlLiteral(conflictID)}::uuid
      AND conflict.last_review_run_id IS NULL
      AND conflict.status IN ('open', 'overdue')
      AND conflict.next_review_at <= clock_timestamp();
  `);
  postgresQuery(`
    UPDATE relationship_conflict_review_runs AS review_run
    SET status = 'failed',
        lease_until = NULL,
        completed_at = NULL,
        last_error = 'compose e2e automatic review requeue',
        updated_at = now()
      WHERE review_run.team_id = ${sqlLiteral(teamID)}::uuid
      AND review_run.status = 'completed'
      AND review_run.local_run_date = CURRENT_DATE
      AND EXISTS (
        SELECT 1
        FROM relationship_conflict_cases AS conflict
        WHERE conflict.team_id = review_run.team_id
          AND conflict.conflict_id = ${sqlLiteral(conflictID)}::uuid
          AND conflict.last_review_run_id = review_run.review_run_id
          AND conflict.status IN ('open', 'overdue')
          AND conflict.next_review_at <= clock_timestamp()
      );
  `);
}

function postgresQuery(sql) {
  const result = spawnSync("docker", [
    "compose",
    "-p",
    composeProject,
    "-f",
    composeFile,
    "exec",
    "-T",
    "postgres",
    "sh",
    "-ec",
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "conflict-e2e",
    sql,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`postgres query failed (${result.status}): ${result.stderr || result.stdout}`);
  }
  return result.stdout.trim();
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

async function mcpTool(apiKey, name, args) {
  const response = await httpJSON(`${userUrl}/mcp`, {
    method: "POST",
    headers: {
      "Authorization": `Bearer ${apiKey}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/call",
      params: { name, arguments: args },
    }),
  });
  if (response.error) {
    throw new Error(`MCP ${name} error: ${JSON.stringify(response.error)}`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} result missing text: ${JSON.stringify(response)}`);
  }
  return JSON.parse(text);
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: ${redactHTTPBody(text)}`);
  }
  return text ? JSON.parse(text) : {};
}

function redactHTTPBody(text) {
  return text.replace(/"api_key"\s*:\s*"[^"]*"/g, "\"api_key\":\"<redacted>\"");
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function positiveIntEnv(name, fallback, minimum, maximum) {
  const raw = process.env[name];
  if (!raw) {
    return fallback;
  }
  const value = Number(raw);
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new Error(`${name} must be an integer between ${minimum} and ${maximum}`);
  }
  return value;
}

function stringAt(value, path) {
  let current = value;
  for (const key of path) {
    if (current === null || typeof current !== "object") {
      return "";
    }
    current = current[key];
  }
  return typeof current === "string" ? current : "";
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
