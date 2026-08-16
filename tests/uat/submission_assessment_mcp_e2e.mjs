#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const prometheusURL = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const placementTimeoutSeconds = positiveIntEnv("DENSE_MEM_E2E_PLACEMENT_TIMEOUT_SECONDS", 720, 60, 1800);

let rpcID = 0;
const runID = `submission-assessment-e2e-${Date.now()}`;
const evidence = [
  "Project Aurora uses LedgerDB. Project Aurora uses Atlas.",
  "Atlas enables Relay.",
];
const verifierBefore = await prometheusValue("densemem_verifier_requests_total");

const coverageRunID = `${runID}:coverage`;
const coverageError = await mcpToolExpectError("remember", {
  evidence: [
    { content: "Covered uses evidence.", source_type: "document", source: coverageRunID, source_group: coverageRunID, idempotency_key: `${coverageRunID}:evidence:0` },
    { content: "Uncovered evidence.", source_type: "document", source: coverageRunID, source_group: coverageRunID, idempotency_key: `${coverageRunID}:evidence:1` },
  ],
  relationships: [simpleRelationship("Covered uses evidence.", "coverage-rel", 0, "Covered", "uses", "evidence", "uses", "project", "product", "Covered uses evidence.")],
});
if (!coverageError.includes("missing evidence indexes: [1]")) {
  throw new Error("remember coverage rejection did not identify the missing evidence index");
}
const coverageStaged = positiveCount(postgresRow(`
  SELECT count(*) FROM knowledge_ingests
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND idempotency_key = ${sqlLiteral(`${coverageRunID}:evidence:0`)}
`)[0]);
if (coverageStaged !== 0) {
  throw new Error("coverage-rejected remember unexpectedly staged an ingest");
}

seedPredicates("unrelated", 2500);

const remember = await mcpTool("remember", {
  evidence: evidence.map((content, index) => ({
    content,
    source_type: "document",
    source: `${runID}:evidence:${index}`,
    source_group: runID,
    idempotency_key: `${runID}:evidence:${index}`,
  })),
  relationships: [
    relationship("r:uses-ledger", 0, "Project Aurora", "uses", "LedgerDB", "uses", "project", "product", "Project Aurora uses LedgerDB."),
    relationship("r:uses-atlas", 0, "Project Aurora", "uses", "Atlas", "uses", "project", "product", "Project Aurora uses Atlas."),
    relationship("r:enables-relay", 1, "Atlas", "enables", "Relay", "enables", "product", "product", evidence[1]),
  ],
});
const submissionID = stringValue(remember.submission_id);
if (!submissionID) {
  throw new Error("remember did not return a submission_id");
}

await waitForCompletedPlacement(submissionID);
const verifierAfter = await waitForVerifierRequest(verifierBefore + 1);
const summary = submissionSummary(submissionID);

if (summary.assessments !== 1 || summary.completedItems !== 2 || summary.commitOutcomes !== 2) {
  throw new Error("submission did not complete as one atomic placement run");
}
if (summary.entityResolutions !== 6 || summary.relationshipObservations !== 3 || summary.verifications !== 3) {
  throw new Error("submission assessment did not preserve every submitted entity and relationship target");
}
if (summary.reviewTasks !== 0 || summary.registrationEvents !== 1 || summary.enablesRegistrations !== 1 || summary.createdRegistrations !== 1) {
  throw new Error("submission assessment did not use the controlled terminal registration path");
}
if (summary.searchDocuments !== 5) {
  throw new Error("atomic submission commit did not create evidence and relationship search documents");
}

const overflowBefore = await prometheusValue("densemem_verifier_requests_total");
const overflowSubmission = overflowFixture();
seedPredicates("overflow", overflowSubmission.relationships.length, "overflow");
const overflowRemember = await mcpTool("remember", overflowSubmission);
const overflowSubmissionID = stringValue(overflowRemember.submission_id);
if (!overflowSubmissionID) {
  throw new Error("predicate overflow remember did not return a submission_id");
}
const overflowStatus = await waitForFailedSubmission(overflowSubmissionID);
const overflowErrors = Array.isArray(overflowStatus.errors) ? overflowStatus.errors : [];
if (!overflowErrors.some((item) => stringValue(item.code) === "submission_processing_failed")) {
  throw new Error("predicate overflow status did not expose its bounded terminal failure");
}
const overflowAfter = await prometheusValue("densemem_verifier_requests_total");
if (overflowAfter !== overflowBefore) {
  throw new Error("predicate overflow unexpectedly called the verifier");
}
const terminalFailures = await waitForPrometheusValueSelector(
  "densemem_assessor_terminal_failures_total",
  'stage="predicate_options_overflow"',
  1,
);
if (terminalFailures < 1) {
  throw new Error("predicate overflow did not record the bounded terminal metric");
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_id: submissionID,
  verifier_requests_before: verifierBefore,
  verifier_requests_after: verifierAfter,
  verifier_requests_after_overflow: overflowAfter,
  assessments: summary.assessments,
  completed_items: summary.completedItems,
  relationship_observations: summary.relationshipObservations,
  predicate_registration_events: summary.registrationEvents,
}, null, 2));

function relationship(ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText) {
  return relationshipForContent(evidence[evidenceIndex], ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText);
}

function simpleRelationship(content, ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText) {
  return relationshipForContent(
    content,
    ref,
    evidenceIndex,
    subject,
    predicateSurface,
    object,
    proposedKey,
    subjectKind,
    objectKind,
    supportText,
  );
}

function relationshipForContent(content, ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText) {
  const subjectSpan = span(content, subject, supportText === content ? 0 : content.indexOf(supportText));
  const predicateSpan = span(content, predicateSurface, supportText === content ? 0 : content.indexOf(supportText));
  const objectSpan = span(content, object, supportText === content ? 0 : content.indexOf(supportText));
  const supportSpan = span(content, supportText);
  return {
    ref,
    subject: {
      name: subject,
      entity_kind: subjectKind,
      span: { evidence_index: evidenceIndex, start: subjectSpan.start, end: subjectSpan.end },
    },
    predicate: {
      proposed_key: proposedKey,
      surface: predicateSurface,
      span: { evidence_index: evidenceIndex, start: predicateSpan.start, end: predicateSpan.end },
    },
    object: {
      entity: {
        name: object,
        entity_kind: objectKind,
        span: { evidence_index: evidenceIndex, start: objectSpan.start, end: objectSpan.end },
      },
    },
    polarity: "+",
    modality: "statement",
    supports: [{ evidence_index: evidenceIndex, start: supportSpan.start, end: supportSpan.end }],
  };
}

function seedPredicates(prefix, count, keyPrefix = `${runID}:${prefix}`) {
  postgresRow(`
    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds,
      allowed_object_kinds, relationship_kind, current_cardinality,
      lifecycle_state, origin, metadata
    )
    SELECT ${sqlLiteral(teamID)}::uuid,
           ${sqlLiteral(`${keyPrefix}_`)} || series::text,
           1,
           ARRAY[]::text[],
           ARRAY['project','product','organization','concept','other']::text[],
           ARRAY['project','product','organization','concept','other']::text[],
           'state', 'many', 'active', 'built_in', '{}'::jsonb
    FROM generate_series(0, ${count - 1}) AS series;
    SELECT count(*) FROM team_predicate_definitions
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND predicate_key LIKE ${sqlLiteral(`${keyPrefix}_%`)};
  `);
}

function overflowFixture() {
  const overflowEvidence = [];
  const relationships = [];
  for (let evidenceIndex = 0; evidenceIndex < Math.ceil(101 / 6); evidenceIndex += 1) {
    const clauses = [];
    const indexes = [];
    for (let slot = 0; slot < 6; slot += 1) {
      const index = evidenceIndex * 6 + slot;
      if (index >= 101) {
        break;
      }
      clauses.push(`A${index} x B${index}`);
      indexes.push(index);
    }
    const content = `${clauses.join(". ")}.`;
    overflowEvidence.push({
      content,
      source_type: "document",
      source: `${runID}:overflow:evidence:${evidenceIndex}`,
      source_group: `${runID}:overflow`,
      idempotency_key: `${runID}:overflow:evidence:${evidenceIndex}`,
    });
    for (const index of indexes) {
      relationships.push(relationshipForContent(
        content,
        `overflow-rel-${index}`,
        evidenceIndex,
        `A${index}`,
        "x",
        `B${index}`,
        `overflow_${index}`,
        "project",
        "product",
        `A${index} x B${index}`,
      ));
    }
  }
  return { evidence: overflowEvidence, relationships };
}

function span(content, text, from = 0) {
  const byteIndex = content.indexOf(text, from);
  if (byteIndex < 0) {
    throw new Error("e2e fixture span text is absent");
  }
  return {
    start: Array.from(content.slice(0, byteIndex)).length,
    end: Array.from(content.slice(0, byteIndex + text.length)).length,
  };
}

async function waitForCompletedPlacement(submissionID) {
  const attempts = Math.ceil((placementTimeoutSeconds * 1000) / 2_000);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const placement = await mcpTool("get_submission_status", { submission_id: submissionID });
    const state = stringValue(placement.processing_state);
    if (state === "completed") {
      return;
    }
    if (["rejected", "failed", "quarantined"].includes(state)) {
      throw new Error(`submission reached unexpected terminal state ${state}`);
    }
    await delay(2_000);
  }
  throw new Error("timed out waiting for submission completion");
}

async function waitForFailedSubmission(submissionID) {
  const attempts = Math.ceil((placementTimeoutSeconds * 1000) / 2_000);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const placement = await mcpTool("get_submission_status", { submission_id: submissionID });
    const state = stringValue(placement.processing_state);
    if (state === "failed") {
      return placement;
    }
    if (["completed", "awaiting_review", "rejected", "quarantined"].includes(state)) {
      throw new Error(`predicate overflow reached unexpected terminal state ${state}`);
    }
    await delay(2_000);
  }
  throw new Error("timed out waiting for predicate overflow failure");
}

async function waitForVerifierRequest(expected) {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const observed = await prometheusValue("densemem_verifier_requests_total");
    if (observed === expected) {
      return observed;
    }
    if (observed > expected) {
      throw new Error("one submission caused more than one assessor conversation");
    }
    await delay(2_000);
  }
  throw new Error("submission did not record one assessor conversation");
}

function submissionSummary(submissionID) {
  const runIDLiteral = sqlLiteral(submissionID);
  const row = postgresRow(`
    WITH run AS (
      SELECT placement_run_id
      FROM placement_runs
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND ingest_id = ${runIDLiteral}::uuid
    ), assessment AS (
      SELECT assessment_id
      FROM placement_assessments
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND ingest_id = ${runIDLiteral}::uuid
        AND assessment_scope = 'submission'
        AND placement_item_id IS NULL
    )
    SELECT
      (SELECT count(*) FROM assessment) AS assessments,
      (SELECT count(*) FROM placement_items WHERE team_id = ${sqlLiteral(teamID)}::uuid AND ingest_id = ${runIDLiteral}::uuid AND status = 'completed') AS completed_items,
      (SELECT count(*) FROM placement_outcomes WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM run) AND outcome_kind = 'submission_assessment_commit') AS commit_outcomes,
      (SELECT count(*) FROM entity_resolution_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND assessment_id = (SELECT assessment_id FROM assessment)) AS entity_resolutions,
      (SELECT count(*)
       FROM relationship_observations AS observation
       JOIN verification_events AS verification
         ON verification.team_id = observation.team_id
        AND verification.observation_id = observation.observation_id
       WHERE observation.team_id = ${sqlLiteral(teamID)}::uuid
         AND verification.assessment_id = (SELECT assessment_id FROM assessment)) AS relationship_observations,
      (SELECT count(*) FROM verification_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND assessment_id = (SELECT assessment_id FROM assessment)) AS verifications,
      (SELECT count(*) FROM review_tasks AS task JOIN placement_items AS item ON item.team_id = task.team_id AND item.placement_item_id = task.placement_item_id WHERE task.team_id = ${sqlLiteral(teamID)}::uuid AND item.ingest_id = ${runIDLiteral}::uuid) AS review_tasks,
      (SELECT count(*) FROM predicate_registration_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM run)) AS registration_events,
      (SELECT count(*) FROM predicate_registration_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM run) AND predicate_key = 'enables') AS enables_registrations,
      (SELECT count(*) FROM predicate_registration_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM run) AND registration_action = 'created') AS created_registrations,
      (SELECT count(*)
       FROM search_documents AS document
       WHERE document.team_id = ${sqlLiteral(teamID)}::uuid
         AND (
           (document.source_kind = 'evidence' AND document.source_id IN (
             SELECT item.fragment_id
             FROM placement_items AS item
             WHERE item.team_id = ${sqlLiteral(teamID)}::uuid
               AND item.placement_run_id = (SELECT placement_run_id FROM run)
           ))
           OR
           (document.source_kind = 'relationship' AND document.source_id IN (
             SELECT observation.relationship_id
             FROM relationship_observations AS observation
             JOIN verification_events AS verification
               ON verification.team_id = observation.team_id
              AND verification.observation_id = observation.observation_id
             WHERE observation.team_id = ${sqlLiteral(teamID)}::uuid
               AND verification.assessment_id = (SELECT assessment_id FROM assessment)
           ))
         )) AS search_documents;
  `);
  return {
    assessments: positiveCount(row[0]),
    completedItems: positiveCount(row[1]),
    commitOutcomes: positiveCount(row[2]),
    entityResolutions: positiveCount(row[3]),
    relationshipObservations: positiveCount(row[4]),
    verifications: positiveCount(row[5]),
    reviewTasks: positiveCount(row[6]),
    registrationEvents: positiveCount(row[7]),
    enablesRegistrations: positiveCount(row[8]),
    createdRegistrations: positiveCount(row[9]),
    searchDocuments: positiveCount(row[10]),
  };
}

async function mcpTool(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${apiKey}`,
      Accept: "application/json",
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
    throw new Error(`MCP ${name} returned a bounded error`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} did not return JSON content`);
  }
  return JSON.parse(text);
}

async function mcpToolExpectError(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${apiKey}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/call",
      params: { name, arguments: args },
    }),
  });
  if (!response.error || response.result !== undefined) {
    throw new Error(`MCP ${name} unexpectedly succeeded`);
  }
  const message = response.error?.message;
  if (typeof message !== "string") {
    throw new Error(`MCP ${name} returned an unrecognized bounded error`);
  }
  return message;
}

async function prometheusValue(metric) {
  return prometheusValueSelector(metric, `team_id="${teamID}"`);
}

async function prometheusValueSelector(metric, selector) {
  const url = new URL("/api/v1/query", `${prometheusURL}/`);
  url.searchParams.set("query", `sum(${metric}{${selector}})`);
  const response = await httpJSON(url.toString(), { method: "GET" });
  const value = response.data?.result?.[0]?.value?.[1];
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) {
    throw new Error(`Prometheus returned a non-numeric ${metric}`);
  }
  return parsed;
}

async function waitForPrometheusValueSelector(metric, selector, minimum) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const value = await prometheusValueSelector(metric, selector);
    if (value >= minimum) {
      return value;
    }
    await delay(2_000);
  }
  return prometheusValueSelector(metric, selector);
}

function postgresRow(sql) {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile,
    "exec", "-T", "postgres", "sh", "-ec",
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|" -c "$1"',
    "submission-assessment-e2e", sql,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`postgres summary query failed (${result.status})`);
  }
  return result.stdout.trim().split("|");
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function positiveCount(value) {
  const parsed = Number(value ?? 0);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new Error("postgres summary contained an invalid count");
  }
  return parsed;
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: response body redacted`);
  }
  return text ? JSON.parse(text) : {};
}

function stringValue(value) {
  return typeof value === "string" ? value.trim() : "";
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
  const parsed = Number(raw);
  if (!Number.isInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw new Error(`${name} must be an integer between ${minimum} and ${maximum}`);
  }
  return parsed;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
