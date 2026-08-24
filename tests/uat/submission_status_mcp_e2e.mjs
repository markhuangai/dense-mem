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
const listedTools = listed.tools ?? [];
const listedNames = new Set(listedTools.map((tool) => tool.name));
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
assertConverterReadableToolSchemas(listedTools);

const remember = await mcpSuccess("remember", rememberInput());
const submissionID = stringValue(remember.submission_id);
if (!submissionID || Object.hasOwn(remember, "ingest_id") || Object.hasOwn(remember, "placement")) {
  throw new Error(`remember returned a removed or missing identifier: ${JSON.stringify(remember)}`);
}
assertKeys(remember, ["contract_version", "submission_id", "submission_kind", "processing_state", "check_after_seconds", "status_tool", "correlation_id"]);
if (remember.contract_version !== "dense-mem.v2.6") {
  throw new Error(`remember contract_version = ${remember.contract_version}`);
}
if (remember.submission_kind !== "remember") {
  throw new Error(`remember submission_kind = ${remember.submission_kind}`);
}
if (remember.status_tool !== "get_submission_status") {
  throw new Error("remember did not advertise get_submission_status");
}
if (!stringValue(remember.correlation_id)) {
  throw new Error("remember did not return a correlation_id");
}

const initial = await mcpSuccess("get_submission_status", { submission_id: submissionID });
assertStatusShape(initial, submissionID);
assertRememberStatusMetadata(initial, remember);
const terminal = await waitForTerminal(submissionID);
assertRememberStatusMetadata(terminal, remember, true);
const repeated = await mcpSuccess("get_submission_status", { submission_id: submissionID });
assertStatusShape(repeated, submissionID);
if (stableJSON(terminal) !== stableJSON(repeated)) {
  throw new Error("repeated submission status changed unexpectedly");
}
const diagnostics = await waitForControlSubmission(submissionID);
assertSubmissionDiagnostics(diagnostics, terminal, rememberInput().evidence[0].content);
const timeline = await waitForSubmissionTimeline(submissionID);
assertSubmissionTimeline(timeline, submissionID, remember.correlation_id);

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
assertKeys(correction, ["contract_version", "submission_id", "submission_kind", "processing_state", "check_after_seconds", "status_tool", "correlation_id"]);
if (correction.contract_version !== "dense-mem.v2.6") {
  throw new Error(`correction contract_version = ${correction.contract_version}`);
}
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
  converter_readable_schemas: true,
  actionable_status_metadata: true,
  control_submission_diagnostics: true,
  exact_submission_timeline: true,
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
  return {
    idempotency_key: `${runID}:batch`,
    evidence: [{
      content,
      source_type: "document",
      source: `${runID}:source`,
      source_group: runID,
    }],
    relationships: [{
      ref: `${runID}:relationship`,
      subject: {
        name: subject,
        entity_kind: "project",
      },
      predicate: {
        proposed_key: predicate,
      },
      object: {
        entity: {
          name: object,
          entity_kind: "product",
        },
      },
      polarity: "+",
      evidence_indices: [0],
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
  assertKeys(status, ["contract_version", "submission_id", "submission_kind", "processing_state", "search_state", "check_after_seconds", "evidence", "errors", "degradations"]);
  if (status.contract_version !== "dense-mem.v2.6") {
    throw new Error(`submission status contract_version = ${status.contract_version}`);
  }
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
  for (const item of status.errors) {
    if (!isRecord(item) || typeof item.code !== "string" || typeof item.message !== "string" ||
        typeof item.retryable !== "boolean" || typeof item.next_action !== "string" ||
        typeof item.remediation !== "string") {
      throw new Error("submission status returned incomplete error guidance");
    }
    if (!["poll_status", "resubmit_submission", "retry_correction", "contact_operator", "none"].includes(item.next_action)) {
      throw new Error(`submission status returned unknown next_action ${item.next_action}`);
    }
  }
}

function assertRememberStatusMetadata(status, receipt, terminal = false) {
  if (status.submission_kind !== "remember" || status.correlation_id !== receipt.correlation_id) {
    throw new Error("remember status lost its submission kind or correlation_id");
  }
  if (!Number.isInteger(status.attempts) || !Number.isInteger(status.max_attempts) ||
      status.attempts < 0 || status.max_attempts < 1 || status.attempts > status.max_attempts) {
    throw new Error("remember status returned invalid attempt metadata");
  }
  const submittedAt = requiredTimestamp(status.submitted_at, "submitted_at");
  const updatedAt = requiredTimestamp(status.updated_at, "updated_at");
  if (updatedAt < submittedAt) {
    throw new Error("remember status updated_at precedes submitted_at");
  }
  for (const field of ["next_attempt_at", "started_at", "completed_at"]) {
    if (status[field] !== undefined) requiredTimestamp(status[field], field);
  }
  if (terminal && status.processing_state === "completed" && status.completed_at === undefined) {
    throw new Error("completed remember status omitted completed_at");
  }
}

function assertConverterReadableToolSchemas(tools) {
  const rememberTool = tools.find((tool) => tool?.name === "remember");
  const description = stringValue(rememberTool?.description);
  if (!description.includes('{"object":{"entity"') || !description.includes('"entity_kind"') ||
      !description.includes('{"object":{"value"') || !description.includes('"type"') || !description.includes('"value"')) {
    throw new Error("remember description omitted exact Entity or Value object examples");
  }
  for (const tool of tools) {
    if (!isRecord(tool) || !isRecord(tool.inputSchema)) {
      throw new Error("tools/list returned a tool without an inputSchema object");
    }
    rejectCompositionKeywords(tool.inputSchema, `tool ${tool.name}.inputSchema`);
  }
}

function rejectCompositionKeywords(value, path) {
  if (Array.isArray(value)) {
    value.forEach((item, index) => rejectCompositionKeywords(item, `${path}[${index}]`));
    return;
  }
  if (!isRecord(value)) return;
  for (const [key, item] of Object.entries(value)) {
    if (["oneOf", "anyOf", "allOf", "not", "if", "then", "else"].includes(key)) {
      throw new Error(`${path} contains converter-hostile ${key}`);
    }
    rejectCompositionKeywords(item, `${path}.${key}`);
  }
}

async function waitForControlSubmission(submissionID) {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const list = await controlJSON(`/control/api/submissions?team_id=${encodeURIComponent(teamID)}&limit=100&offset=0`);
    const summary = (list.data ?? []).find((item) => item?.submission_id === submissionID);
    if (summary) {
      const detail = await controlJSON(`/control/api/teams/${encodeURIComponent(teamID)}/submissions/${encodeURIComponent(submissionID)}`);
      return { list, summary, detail: detail.data };
    }
    await delay(1000);
  }
  throw new Error("control submission diagnostics did not expose the durable submission");
}

function assertSubmissionDiagnostics({ list, summary, detail }, terminal, evidenceContent) {
  if (!Array.isArray(list.data) || !list.data.every((item) => item?.team_id === teamID)) {
    throw new Error("control submission list ignored the exact team filter");
  }
  if (summary.processing_state !== terminal.processing_state || summary.correlation_id !== terminal.correlation_id ||
      summary.attempts !== terminal.attempts || summary.max_attempts !== terminal.max_attempts || summary.evidence_count !== terminal.evidence.length) {
    throw new Error("control submission summary diverged from authoritative status");
  }
  assertStatusShape(detail, terminal.submission_id);
  assertRememberStatusMetadata(detail, terminal, true);
  if (detail.team_id !== teamID || detail.processing_state !== terminal.processing_state || detail.evidence_count !== terminal.evidence.length) {
    throw new Error("control submission detail diverged from authoritative status");
  }
  const serialized = JSON.stringify({ list, detail });
  if (serialized.includes(evidenceContent) || /provider_response|normalized_response|\"proposal\"/i.test(serialized)) {
    throw new Error("control submission diagnostics exposed evidence or provider payloads");
  }
}

async function waitForSubmissionTimeline(submissionID) {
  const query = new URLSearchParams({
    team_id: teamID,
    reference_type: "submission",
    reference_id: submissionID,
    sort: "timestamp",
    direction: "asc",
    limit: "100",
    offset: "0",
  });
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const page = await controlJSON(`/control/api/logs?${query.toString()}`);
    const messages = new Set((page.data ?? []).map((item) => item?.message));
    if (messages.has("submission_accepted") && messages.has("submission_completed")) {
      const completed = await controlJSON(`/control/api/logs?${query.toString()}&event=submission_completed`);
      return { page, completed };
    }
    await delay(1000);
  }
  throw new Error("submission lifecycle events did not reach the operation log");
}

function assertSubmissionTimeline({ page, completed }, submissionID, correlationID) {
  if (!Array.isArray(page.data) || page.data.length < 2 || !Array.isArray(completed.data) || completed.data.length < 1) {
    throw new Error("submission timeline is incomplete");
  }
  let previousTimestamp = Number.NEGATIVE_INFINITY;
  for (const item of page.data) {
    if (item.team_id !== teamID || item.correlation_id !== correlationID || item.attrs?.reference_type !== "submission" ||
        item.attrs?.reference_id !== submissionID) {
      throw new Error("submission timeline ignored an exact team or reference filter");
    }
    const timestamp = Date.parse(item.timestamp);
    if (!Number.isFinite(timestamp)) {
      throw new Error("submission timeline returned an invalid timestamp");
    }
    if (timestamp < previousTimestamp) {
      throw new Error("submission timeline ignored ascending timestamp order");
    }
    previousTimestamp = timestamp;
  }
  if (!completed.data.every((item) => item.message === "submission_completed" && item.attrs?.reference_id === submissionID)) {
    throw new Error("operation-log event filtering was not exact");
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

async function controlJSON(path, options = {}) {
  return httpJSON(`${controlURL}${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      Accept: "application/json",
      ...(options.headers ?? {}),
    },
  });
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

function requiredTimestamp(value, field) {
  if (typeof value !== "string" || !value.trim()) {
    throw new Error(`remember status omitted ${field}`);
  }
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) {
    throw new Error(`remember status returned invalid ${field}`);
  }
  return parsed;
}

function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
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
