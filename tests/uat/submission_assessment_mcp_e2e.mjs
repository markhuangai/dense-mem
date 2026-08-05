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
const ingestID = stringValue(remember.ingest_id);
if (!ingestID) {
  throw new Error("remember did not return an ingest_id");
}

await waitForCompletedPlacement(ingestID);
const verifierAfter = await waitForVerifierRequest(verifierBefore + 1);
const summary = submissionSummary(ingestID);

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

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  ingest_id: ingestID,
  verifier_requests_before: verifierBefore,
  verifier_requests_after: verifierAfter,
  assessments: summary.assessments,
  completed_items: summary.completedItems,
  relationship_observations: summary.relationshipObservations,
  predicate_registration_events: summary.registrationEvents,
}, null, 2));

function relationship(ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText) {
  const content = evidence[evidenceIndex];
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

async function waitForCompletedPlacement(ingestID) {
  const attempts = Math.ceil((placementTimeoutSeconds * 1000) / 2_000);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const placement = await mcpTool("get_memory_placement", { ingest_id: ingestID });
    const state = stringValue(placement.processing_state);
    if (state === "completed") {
      return;
    }
    if (["awaiting_review", "failed", "quarantined"].includes(state)) {
      throw new Error(`submission reached unexpected terminal state ${state}`);
    }
    await delay(2_000);
  }
  throw new Error("timed out waiting for submission completion");
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

function submissionSummary(ingestID) {
  const runIDLiteral = sqlLiteral(ingestID);
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
      (SELECT count(*) FROM search_documents WHERE team_id = ${sqlLiteral(teamID)}::uuid) AS search_documents;
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

async function prometheusValue(metric) {
  const url = new URL("/api/v1/query", `${prometheusURL}/`);
  url.searchParams.set("query", `sum(${metric}{team_id="${teamID}"})`);
  const response = await httpJSON(url.toString(), { method: "GET" });
  const value = response.data?.result?.[0]?.value?.[1];
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) {
    throw new Error(`Prometheus returned a non-numeric ${metric}`);
  }
  return parsed;
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
