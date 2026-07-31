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

let rpcID = 0;

const runID = `conflict-e2e-${Date.now()}`;
const olderEffectiveAt = "2026-07-20T00:00:00Z";
const newerEffectiveAt = "2026-07-22T00:00:00Z";

const profileA = await createProfile("Conflict E2E A");
const profileB = await createProfile("Conflict E2E B");
const profileC = await createProfile("Conflict E2E C");

const first = await rememberAndWait(profileA.apiKey, {
  idempotencyKey: `${runID}:a`,
  evidence: `ConflictE2E ${runID}: Effective ${newerEffectiveAt}, Dense-Mem primary database is PostgreSQL according to profile A.`,
  sourceGroup: `${runID}:source:a`,
  validFrom: newerEffectiveAt,
  subject: { ref: "project", name: "Dense-Mem", kind: "project" },
  object: { ref: "postgres", name: "PostgreSQL", kind: "product" },
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
  evidence: `ConflictE2E ${runID}: Effective ${olderEffectiveAt}, Dense-Mem primary database is GraphDB according to profile B.`,
  sourceGroup: `${runID}:source:b`,
  validFrom: olderEffectiveAt,
  subject: { ref: "project", name: "Dense-Mem", kind: "project", knownEntityID: subjectEntityID },
  object: { ref: "graphdb", name: "GraphDB", kind: "product" },
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
  evidence: `ConflictE2E ${runID}: Effective ${newerEffectiveAt}, Dense-Mem primary database is PostgreSQL according to profile C.`,
  sourceGroup: `${runID}:source:c`,
  validFrom: newerEffectiveAt,
  subject: { ref: "project", name: "Dense-Mem", kind: "project", knownEntityID: subjectEntityID },
  object: { ref: "postgres", name: "PostgreSQL", kind: "product", knownEntityID: postgresEntityID },
  relationshipID: "rel:primary-db-c",
  conflictContext: { conflict_id: conflictID, expected_version: conflictVersion },
});

const reviewNow = new Date(Date.now() + 8 * 24 * 60 * 60 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z");
const review = runConflictReview(reviewNow);
if (review.status !== "completed" || review.resolved_cases < 1) {
  throw new Error(`conflict reviewer did not resolve a case: ${JSON.stringify(review)}`);
}
const resolved = (review.results ?? []).find((item) => item.conflict_id === conflictID);
if (!resolved || resolved.outcome !== "resolve") {
  throw new Error(`conflict reviewer did not resolve ${conflictID}: ${JSON.stringify(review)}`);
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
    proposal: {
      entities: [
        entityProposal(input.subject),
        entityProposal(input.object),
      ],
      relationships: [{
        proposal_id: input.relationshipID,
        subject_ref: input.subject.ref,
        predicate: "primary_database",
        object_ref: input.object.ref,
        evidence: [{
          evidence_index: 0,
          start: 0,
          end: evidence.length,
        }],
        ...(input.validFrom ? { valid_from: input.validFrom } : {}),
        ...(input.conflictContext ? { conflict_context: input.conflictContext } : {}),
      }],
    },
  });
  const ingestID = String(result.ingest_id ?? "");
  if (!ingestID) {
    throw new Error(`remember did not return ingest_id: ${JSON.stringify(result)}`);
  }
  return await waitForPlacement(apiKey, ingestID, input.relationshipID);
}

function entityProposal(entity) {
  return {
    ref: entity.ref,
    name: entity.name,
    entity_kind: entity.kind,
    ...(entity.knownEntityID ? { known_entity_id: entity.knownEntityID } : {}),
  };
}

async function waitForPlacement(apiKey, ingestID, proposalID) {
  let lastSummary = {};
  const attempts = Math.ceil((placementTimeoutSeconds * 1000) / 2_000);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const placement = await mcpTool(apiKey, "get_memory_placement", { ingest_id: ingestID });
    if (placement.processing_state === "failed" || placement.processing_state === "quarantined") {
      throw new Error(`placement failed: ${JSON.stringify(placement)}`);
    }
    const outcomes = [];
    for (const item of placement.items ?? []) {
      for (const outcome of item.relationship_outcomes ?? []) {
        outcomes.push(outcome);
      }
    }
    lastSummary = {
      processing_state: placement.processing_state,
      item_count: Array.isArray(placement.items) ? placement.items.length : 0,
      outcomes: outcomes.map((item) => ({
        proposal_id: item.proposal_id,
        relationship_id: item.relationship_id,
        category: item.category,
        reason: item.reason,
      })),
    };
    let outcome = outcomes.find((item) => item.proposal_id === proposalID && item.relationship_id);
    const acceptedOutcomes = outcomes.filter((item) => item.relationship_id);
    if (!outcome && acceptedOutcomes.length === 1 && outcomes.length === 1) {
      outcome = acceptedOutcomes[0];
    }
    if (placement.processing_state === "completed" && outcome) {
      return {
        ingestID,
        relationshipID: String(outcome.relationship_id),
        ownerProfileID: String(outcome.owner_profile_id),
      };
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for placement ${ingestID} after ${placementTimeoutSeconds}s: ${JSON.stringify(lastSummary)}`);
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
  const marker = `OverdueConflictE2E ${runID}`;
  const overdueProjectName = `${marker} project`;
  const firstOverdue = await rememberAndWait(profileA.apiKey, {
    idempotencyKey: `${runID}:overdue:a`,
    evidence: `${marker}: ${overdueProjectName} primary database is PostgreSQL according to profile A.`,
    sourceGroup: `${runID}:overdue:source:a`,
    authority: "primary",
    subject: { ref: "overdue-project", name: overdueProjectName, kind: "project" },
    object: { ref: "overdue-postgres", name: "PostgreSQL", kind: "product" },
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
    evidence: `${marker}: ${overdueProjectName} primary database is GraphDB according to profile B.`,
    sourceGroup: `${runID}:overdue:source:b`,
    authority: "primary",
    subject: { ref: "overdue-project", name: overdueProjectName, kind: "project", knownEntityID: overdueSubjectEntityID },
    object: { ref: "overdue-graphdb", name: "GraphDB", kind: "product" },
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

  const overdueReviewDueAt = Date.parse(stringAt(openOverdueConflict, ["review_due_at"]));
  if (!Number.isFinite(overdueReviewDueAt)) {
    throw new Error(`overdue conflict did not return a valid review_due_at: ${JSON.stringify(openOverdueConflict)}`);
  }
  const overdueReviewNow = new Date(overdueReviewDueAt + 1_000).toISOString().replace(/\.\d{3}Z$/, "Z");
  const overdueReview = runConflictReview(overdueReviewNow);
  if (overdueReview.status !== "completed") {
    throw new Error(`overdue conflict reviewer did not complete: ${JSON.stringify(overdueReview)}`);
  }
  const resolution = (overdueReview.results ?? []).find((item) => item.conflict_id === overdueConflictID);
  if (!resolution || resolution.outcome !== "resolve") {
    throw new Error(`overdue conflict did not resolve: ${JSON.stringify(overdueReview)}`);
  }
  if (!["ai", "last_write_wins"].includes(resolution.resolution_method)) {
    throw new Error(`overdue conflict used an unexpected resolution method: ${JSON.stringify(resolution)}`);
  }
  if (!resolution.assessment_attempt_id || !Array.isArray(resolution.retracted_evidence_ids) || resolution.retracted_evidence_ids.length === 0) {
    throw new Error(`overdue conflict did not record assessment/retraction lineage: ${JSON.stringify(resolution)}`);
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
  return { conflictID: overdueConflictID, method: resolution.resolution_method };
}

function runConflictReview(now) {
  const result = spawnSync("docker", [
    "compose",
    "-p",
    composeProject,
    "-f",
    composeFile,
    "exec",
    "-T",
    "server",
    "/app/docker-entrypoint.sh",
    "/app/review-conflicts",
    "--team-id",
    teamID,
    "--now",
    now,
    "--timezone",
    "UTC",
    "--worker-id",
    `e2e-conflict-review-${Date.now()}`,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`review-conflicts failed (${result.status}): ${result.stderr || result.stdout}`);
  }
  try {
    return JSON.parse(result.stdout);
  } catch (error) {
    throw new Error(`review-conflicts returned non-JSON output: ${error.message}: ${result.stdout}`);
  }
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
