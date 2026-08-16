#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const timeoutSeconds = positiveIntEnv("DENSE_MEM_E2E_PLACEMENT_TIMEOUT_SECONDS", 720, 60, 1800);

let rpcID = 0;
const runID = `submission-status-e2e-${Date.now()}`;

const listed = await toolsList();
const listedNames = new Set((listed.tools ?? []).map((tool) => tool.name));
for (const required of ["remember", "get_submission_status", "correct_relationship", "trace_memory", "export_memory_pack"]) {
  if (!listedNames.has(required)) {
    throw new Error(`tools/list is missing ${required}`);
  }
}
for (const removed of ["correct_entity_resolution", "get_memory_placement", "resolve_memory_placement", "find_memory_pack_candidates", "inspect_memory_pack", "import_memory_pack", "rollback_memory_pack_import"]) {
  if (listedNames.has(removed)) {
    throw new Error(`tools/list still exposes removed ${removed}`);
  }
}

const remember = await mcpSuccess("remember", rememberInput());
const submissionID = stringValue(remember.submission_id);
if (!submissionID || Object.hasOwn(remember, "ingest_id") || Object.hasOwn(remember, "placement")) {
  throw new Error(`remember returned a removed or missing identifier: ${JSON.stringify(remember)}`);
}
assertKeys(remember, ["submission_id", "submission_kind", "processing_state", "check_after_seconds", "status_tool", "correlation_id"]);
if (remember.submission_kind !== "remember") {
  throw new Error(`remember submission_kind = ${remember.submission_kind}`);
}
if (remember.status_tool !== "get_submission_status") {
  throw new Error("remember did not advertise get_submission_status");
}

const initial = await mcpSuccess("get_submission_status", { submission_id: submissionID });
assertStatusShape(initial, submissionID);
const terminal = await waitForTerminal(submissionID);
const repeated = await mcpSuccess("get_submission_status", { submission_id: submissionID });
assertStatusShape(repeated, submissionID);
if (stableJSON(terminal) !== stableJSON(repeated)) {
  throw new Error("repeated submission status changed unexpectedly");
}

const missing = await mcpRaw(apiKey, "get_submission_status", { submission_id: "00000000-0000-0000-0000-000000000000" });
if (!missing.error || missing.result !== undefined || typeof missing.error.message !== "string") {
  throw new Error("missing submission status did not fail with a bounded MCP error");
}

const otherProfile = await createCredential(teamID, `${runID}-other-profile`);
const crossProfile = await mcpRaw(otherProfile.apiKey, "get_submission_status", { submission_id: submissionID });
if (!crossProfile.error || crossProfile.result !== undefined || crossProfile.error.message.includes(submissionID)) {
  throw new Error("submission status leaked across profile ownership boundary");
}

const relationshipID = postgresQuery(`
  SELECT relationship_id::text
  FROM relationship_observations
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND ingest_id = ${sqlLiteral(submissionID)}::uuid
    AND relationship_id IS NOT NULL
  ORDER BY created_at ASC
  LIMIT 1;
`);
if (!relationshipID) {
  throw new Error("completed submission did not produce a relationship observation");
}

const relationshipEndpoints = postgresQuery(`
  SELECT concat(subject_entity_id::text, '|', object_entity_id::text)
  FROM relationship_records
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND relationship_id = ${sqlLiteral(relationshipID)}::uuid;
`);
const [subjectEntityID, originalObjectEntityID] = relationshipEndpoints.split("|");
if (!subjectEntityID || !originalObjectEntityID) {
  throw new Error(`could not resolve graph endpoints: ${relationshipEndpoints}`);
}

const defaultGraph = await userGraph();
if (defaultGraph.depth !== 2 || defaultGraph.limit !== 80) {
  throw new Error(`graph defaults are invalid: depth=${defaultGraph.depth}, limit=${defaultGraph.limit}`);
}
assertGraphContains(defaultGraph, relationshipID, originalObjectEntityID);

const graphBeforeCorrection = await userGraph({
  scope: "local",
  anchor_type: "entity",
  anchor_id: subjectEntityID,
  depth: "5",
  limit: "181",
  types: "entity,value",
});
if (graphBeforeCorrection.depth !== 5 || graphBeforeCorrection.limit !== 181) {
  throw new Error(`explicit graph bounds are invalid: depth=${graphBeforeCorrection.depth}, limit=${graphBeforeCorrection.limit}`);
}
assertGraphContains(graphBeforeCorrection, relationshipID, originalObjectEntityID);

const trace = await mcpSuccess("trace_memory", { relationship_id: relationshipID, include_evidence_content: true });
assertNoLegacySubmissionFields(trace);
for (const observation of trace.observations ?? []) {
  if (!observation.submission_id || Object.hasOwn(observation, "ingest_id")) {
    throw new Error("trace observation did not use submission_id exclusively");
  }
}
for (const evidence of trace.evidence ?? []) {
  if (!evidence.submission_id || Object.hasOwn(evidence, "ingest_id")) {
    throw new Error("trace evidence did not use submission_id exclusively");
  }
}

const exported = await mcpSuccess("export_memory_pack", {
  name: `${runID} export`,
  relationship_ids: [relationshipID],
  include_evidence: false,
});
assertKeys(exported, ["artifact_json", "content_sha256", "filename", "counts", "omissions"]);
if (typeof exported.artifact_json !== "string" || !exported.artifact_json.includes("dense-mem.memory-pack.v2.4")) {
  throw new Error("export did not return the current memory-pack format");
}

const correctionSupport = postgresQuery(`
  SELECT concat(record.version, '|', record.owner_profile_id::text, '|', support.fragment_id::text, '|', support.span_start, '|', support.span_end)
  FROM relationship_records AS record
  JOIN relationship_evidence_supports AS support
    ON support.team_id = record.team_id
   AND support.relationship_id = record.relationship_id
  WHERE record.team_id = ${sqlLiteral(teamID)}::uuid
    AND record.relationship_id = ${sqlLiteral(relationshipID)}::uuid
  ORDER BY support.created_at ASC, support.support_id ASC
  LIMIT 1;
`);
const [relationshipVersion, relationshipOwnerID, supportEvidenceID, supportStart, supportEnd] = correctionSupport.split("|");
if (!relationshipVersion || !relationshipOwnerID || !supportEvidenceID || supportStart === undefined || !supportEnd) {
  throw new Error(`could not resolve correction support: ${correctionSupport}`);
}
const correctionInput = {
  action: "submit",
  relationship_id: relationshipID,
  expected_version: Number(relationshipVersion),
  patch: {
    object_entity: {
      name: `${runID} corrected project`,
      entity_kind: "project",
    },
  },
  supports: [{ evidence_id: supportEvidenceID, start: Number(supportStart), end: Number(supportEnd) }],
  reason: "The relationship object was resolved to the wrong project.",
  idempotency_key: `${runID}:correct-relationship`,
};

const nonOwnerCorrection = await mcpRaw(otherProfile.apiKey, "correct_relationship", {
  ...correctionInput,
  idempotency_key: `${runID}:non-owner-correction`,
});
if (!nonOwnerCorrection.error || nonOwnerCorrection.result !== undefined || JSON.stringify(nonOwnerCorrection).includes(relationshipID)) {
  throw new Error("non-owner relationship correction was allowed or leaked the target ID");
}

const correction = await mcpSuccess("correct_relationship", correctionInput);
assertKeys(correction, ["submission_id", "submission_kind", "processing_state", "check_after_seconds", "status_tool", "correlation_id"]);
if (correction.submission_kind !== "relationship_correction" || correction.status_tool !== "get_submission_status") {
  throw new Error(`correction receipt is invalid: ${JSON.stringify(correction)}`);
}
const correctionStatus = await mcpSuccess("get_submission_status", { submission_id: correction.submission_id });
assertStatusShape(correctionStatus, correction.submission_id);
if (correctionStatus.submission_kind !== "relationship_correction" || correctionStatus.processing_state !== "completed") {
  throw new Error(`correction status is not completed: ${JSON.stringify(correctionStatus)}`);
}
const successorID = stringValue(correctionStatus.correction_result?.successor_relationship_id);
if (!successorID || successorID === relationshipID) {
  throw new Error(`correction did not create a successor: ${JSON.stringify(correctionStatus.correction_result)}`);
}
const crossProfileCorrectionStatus = await mcpRaw(otherProfile.apiKey, "get_submission_status", { submission_id: correction.submission_id });
if (!crossProfileCorrectionStatus.error || crossProfileCorrectionStatus.result !== undefined || JSON.stringify(crossProfileCorrectionStatus).includes(correction.submission_id)) {
  throw new Error("correction status leaked across profile ownership boundary");
}
const correctionState = postgresQuery(`
  SELECT concat(
    original.status, '|', successor.status, '|', successor.owner_profile_id::text, '|',
    successor.support_count, '|', count(reference.cross_reference_id)
  )
  FROM relationship_records AS original
  JOIN relationship_records AS successor
    ON successor.team_id = original.team_id
   AND successor.relationship_id = ${sqlLiteral(successorID)}::uuid
  LEFT JOIN relationship_cross_references AS reference
    ON reference.team_id = original.team_id
   AND reference.source_relationship_id = successor.relationship_id
   AND reference.target_relationship_id = original.relationship_id
   AND reference.kind = 'corrects'
  WHERE original.team_id = ${sqlLiteral(teamID)}::uuid
    AND original.relationship_id = ${sqlLiteral(relationshipID)}::uuid
  GROUP BY original.status, successor.status, successor.owner_profile_id, successor.support_count;
`);
const [originalState, successorState, successorOwner, successorSupportCount, correctionRefs] = correctionState.split("|");
if (originalState !== "superseded" || successorState !== "active" || successorOwner !== relationshipOwnerID || Number(successorSupportCount) < 1 || correctionRefs !== "1") {
  throw new Error(`relationship correction state is invalid: ${correctionState}`);
}

const correctedObjectEntityID = postgresQuery(`
  SELECT object_entity_id::text
  FROM relationship_records
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND relationship_id = ${sqlLiteral(successorID)}::uuid;
`);
if (!correctedObjectEntityID || correctedObjectEntityID === originalObjectEntityID) {
  throw new Error(`correction did not replace the graph endpoint: ${correctedObjectEntityID || "missing"}`);
}
const graphAfterCorrection = await userGraph({
  scope: "local",
  anchor_type: "entity",
  anchor_id: subjectEntityID,
  depth: "5",
  limit: "181",
  types: "entity,value",
});
assertGraphContains(graphAfterCorrection, successorID, correctedObjectEntityID);
assertGraphOmits(graphAfterCorrection, relationshipID, originalObjectEntityID);

const ledgerTables = postgresQuery(`
  SELECT concat(
    COALESCE(to_regclass('public.skill_pack_imports')::text, ''), '|',
    COALESCE(to_regclass('public.skill_pack_import_changes')::text, ''), '|',
    COALESCE(to_regclass('public.submission_quarantine_payloads')::text, ''), '|',
    COALESCE(to_regclass('public.submission_quarantine_tombstones')::text, '')
  );
`);
const [imports, changes, quarantinePayloads, quarantineTombstones] = ledgerTables.split("|");
if (imports || changes || !quarantinePayloads || !quarantineTombstones) {
  throw new Error(`database removal/retention boundary is wrong: ${ledgerTables}`);
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_id: submissionID,
  processing_state: terminal.processing_state,
  search_state: terminal.search_state,
  relationship_id: relationshipID,
  correction_submission_id: correction.submission_id,
  successor_relationship_id: successorID,
  graph_anchor_entity_id: subjectEntityID,
  graph_original_object_entity_id: originalObjectEntityID,
  graph_corrected_object_entity_id: correctedObjectEntityID,
  listed_tool_count: listedNames.size,
  removed_tools_absent: true,
  profile_isolation: true,
  memory_pack_export: true,
  relationship_correction_owner_only: true,
  relationship_correction_successor: true,
  import_ledger_tables_absent: true,
}, null, 2));

async function toolsList() {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }),
  });
  if (response.error || !response.result) {
    throw new Error("tools/list returned a bounded error");
  }
  return response.result;
}

function rememberInput() {
  const content = "Project Aurora uses LedgerDB.";
  const subject = "Project Aurora";
  const predicate = "uses";
  const object = "LedgerDB";
  const subjectStart = 0;
  const predicateStart = subject.length + 1;
  const objectStart = predicateStart + predicate.length + 1;
  return {
    evidence: [{
      content,
      source_type: "document",
      source: `${runID}:source`,
      source_group: runID,
      idempotency_key: `${runID}:evidence`,
    }],
    relationships: [{
      ref: `${runID}:relationship`,
      subject: {
        name: subject,
        entity_kind: "project",
        span: { evidence_index: 0, start: subjectStart, end: subjectStart + subject.length },
      },
      predicate: {
        proposed_key: predicate,
        surface: predicate,
        span: { evidence_index: 0, start: predicateStart, end: predicateStart + predicate.length },
      },
      object: {
        entity: {
          name: object,
          entity_kind: "product",
          span: { evidence_index: 0, start: objectStart, end: objectStart + object.length },
        },
      },
      polarity: "+",
      modality: "statement",
      supports: [{ evidence_index: 0, start: 0, end: Array.from(content).length }],
    }],
  };
}

async function waitForTerminal(id) {
  const attempts = Math.ceil((timeoutSeconds * 1000) / 2000);
  let last = "";
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const status = await mcpSuccess("get_submission_status", { submission_id: id });
    assertStatusShape(status, id);
    last = stringValue(status.processing_state);
    const searchTerminal = ["current", "not_required", "failed"].includes(status.search_state);
    if (["completed", "rejected", "failed", "quarantined"].includes(last) && searchTerminal) {
      if (last !== "completed") {
        throw new Error(`status scenario submission reached ${last}`);
      }
      if (status.search_state === "failed") {
        throw new Error("status scenario search projection failed");
      }
      return status;
    }
    await delay(2000);
  }
  throw new Error(`timed out waiting for terminal submission status (last ${last || "unknown"})`);
}

function assertStatusShape(status, id) {
  assertKeys(status, ["submission_id", "submission_kind", "processing_state", "search_state", "check_after_seconds", "evidence", "errors"]);
  if (status.submission_id !== id || Object.hasOwn(status, "ingest_id") || Object.hasOwn(status, "placement_run_id") || Object.hasOwn(status, "items") || Object.hasOwn(status, "review_tasks")) {
    throw new Error("submission status exposed a removed internal field");
  }
  if (!Array.isArray(status.evidence) || !Array.isArray(status.errors)) {
    throw new Error("submission status did not return bounded evidence/errors arrays");
  }
  for (const item of status.evidence) {
    assertKeys(item, ["evidence_id", "evidence_index", "superseded_evidence_ids", "search_state"]);
    if (Object.hasOwn(item, "content") || Object.hasOwn(item, "placement_item_id") || Object.hasOwn(item, "provider_response")) {
      throw new Error("submission status exposed raw or placement evidence details");
    }
  }
}

function assertNoLegacySubmissionFields(value) {
  const serialized = JSON.stringify(value);
  if (serialized.includes('"ingest_id"') || serialized.includes('"placement_run_id"')) {
    throw new Error("public trace output exposed a removed submission field");
  }
}

async function mcpSuccess(name, args) {
  const response = await mcpRaw(apiKey, name, args);
  if (response.error || response.result === undefined) {
    throw new Error(`MCP ${name} returned a bounded error`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} did not return JSON content`);
  }
  return JSON.parse(text);
}

async function mcpRaw(key, name, args) {
  return httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }),
  });
}

async function createCredential(targetTeamID, name) {
  const response = await httpJSON(`${controlURL}/control/api/teams/${targetTeamID}/credentials`, {
    method: "POST",
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
    body: JSON.stringify({ name, role: "member", scopes: ["read", "write"], rate_limit: 300 }),
  });
  const key = stringValue(response.data?.api_key);
  if (!key) {
    throw new Error("control API did not return a second credential key");
  }
  return { apiKey: key };
}

async function userGraph(params = {}) {
  const query = new URLSearchParams(params);
  const suffix = query.size > 0 ? `?${query.toString()}` : "";
  const response = await fetch(`${userURL}/ui/api/graph${suffix}`, {
    headers: { Authorization: `Bearer ${apiKey}` },
    cache: "no-store",
  });
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`graph HTTP ${response.status}: response body redacted`);
  }
  if (response.headers.get("cache-control") !== "no-store") {
    throw new Error(`graph cache-control = ${response.headers.get("cache-control") ?? "missing"}`);
  }
  const graph = text ? JSON.parse(text).data : undefined;
  if (!graph || !Array.isArray(graph.nodes) || !Array.isArray(graph.edges)) {
    throw new Error("graph response is missing nodes or edges");
  }
  return graph;
}

function assertGraphContains(graph, relationshipID, nodeID) {
  if (!graph.edges.some((edge) => edge.id === relationshipID)) {
    throw new Error(`graph is missing relationship ${relationshipID}`);
  }
  if (!graph.nodes.some((node) => node.id === nodeID)) {
    throw new Error(`graph is missing node ${nodeID}`);
  }
}

function assertGraphOmits(graph, relationshipID, nodeID) {
  if (graph.edges.some((edge) => edge.id === relationshipID)) {
    throw new Error(`graph retained superseded relationship ${relationshipID}`);
  }
  if (graph.nodes.some((node) => node.id === nodeID)) {
    throw new Error(`graph retained orphaned endpoint ${nodeID}`);
  }
}

function postgresQuery(sql) {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile,
    "exec", "-T", "postgres", "sh", "-ec",
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "submission-status-e2e", sql,
  ], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) {
    throw new Error(`postgres query failed (${result.status})`);
  }
  return result.stdout.trim();
}

function assertKeys(value, required) {
  for (const key of required) {
    if (!Object.hasOwn(value, key)) {
      throw new Error(`response is missing ${key}`);
    }
  }
}

function stableJSON(value) {
  if (Array.isArray(value)) return `[${value.map(stableJSON).sort().join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
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
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function positiveIntEnv(name, fallback, minimum, maximum) {
  const raw = process.env[name];
  if (!raw) return fallback;
  const parsed = Number(raw);
  if (!Number.isInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw new Error(`${name} must be an integer between ${minimum} and ${maximum}`);
  }
  return parsed;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
