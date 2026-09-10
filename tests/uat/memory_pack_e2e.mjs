#!/usr/bin/env node

import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const runID = `memory-pack-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
const evidenceSentinel = `${runID}-private-evidence-content`;
let rpcID = 0;

const ownerA = await createCredential(teamID, `${runID} owner A`);
const sameTeamB = await createCredential(teamID, `${runID} same-team B`);
const foreignTeam = await createTeam(`${runID} team C`);
const otherTeamC = await createCredential(foreignTeam.id, `${runID} other-team C`);

const spaces = await loadSpaces();
const ownerSpace = credentialSpace(spaces, ownerA.credential.id);
const fixture = seedPrivateRelationship({
  teamID,
  ownerID: ownerA.credential.id,
  spaceID: ownerSpace.id,
});

const options = [];
for (const [evidenceName, includeEvidence] of [
  ["omitted_default", undefined],
  ["explicit_true", true],
  ["explicit_false", false],
]) {
  for (const [namesName, includeEntityNames] of [
    ["names_omitted", undefined],
    ["names_true", true],
    ["names_false", false],
  ]) {
    const argumentsValue = baseExportArguments();
    if (includeEvidence !== undefined) argumentsValue.include_evidence = includeEvidence;
    if (includeEntityNames !== undefined) argumentsValue.include_entity_names = includeEntityNames;
    options.push({
      name: `${evidenceName}_${namesName}`,
      includeEvidence,
      includeEntityNames,
      arguments: argumentsValue,
    });
  }
}

const ownerResults = [];
for (const option of options) {
  const payload = await exportSuccess(ownerA.apiKey, option.arguments);
  assertExportPayload(payload, option);
  ownerResults.push({ name: option.name, counts: payload.counts, filename: payload.filename });
}

await assertInvalidExport(ownerA.apiKey, { ...baseExportArguments(), name: "n".repeat(257) }, "name exceeds maximum length of 256");
await assertInvalidExport(ownerA.apiKey, { ...baseExportArguments(), description: "d".repeat(1025) }, "description exceeds maximum length of 1024");
await assertInvalidExport(
  ownerA.apiKey,
  { ...baseExportArguments(), relationship_ids: Array.from({ length: 501 }, () => randomUUID()) },
  "relationship_ids exceeds maximum item count of 500",
);

const deniedResults = [];
for (const actor of [
  { name: "same_team_b", credential: sameTeamB },
  { name: "other_team_c", credential: otherTeamC },
]) {
  await assertAuthenticated(actor);
  for (const option of options) {
    const response = await exportRaw(actor.credential.apiKey, option.arguments);
    assert(response.status === 200 && !response.payload.error, `${actor.name}/${option.name} returned an unexpected transport response`);
    const result = response.payload.result;
    assert(result?.isError === true, `${actor.name}/${option.name} export was not a structured MCP denial`);
    const structured = result.structuredContent;
    assert(
      structured?.code === "invalid_input" &&
        structured.reason_code === "reference_not_found" &&
        structured.next_action === "refresh_state" &&
        structured.retryable === false,
      `${actor.name}/${option.name} denial was not the public reference_not_found envelope`,
    );
    const text = result.content?.[0]?.text;
    assert(typeof text === "string", `${actor.name}/${option.name} denial omitted the public text envelope`);
    const parsed = JSON.parse(text);
    assert(parsed.code === "invalid_input" && parsed.reason_code === "reference_not_found", `${actor.name}/${option.name} text envelope was not reference_not_found`);
    assert(!JSON.stringify(response).includes(evidenceSentinel), `${actor.name}/${option.name} response exposed private evidence content`);
    deniedResults.push({ actor: actor.name, option: option.name, status: response.status });
  }
}

console.log(JSON.stringify({
  status: "ok",
  scenario: "memory_pack",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  v24_export_options: ownerResults,
  private_relationship_id: fixture.relationshipID,
  abc_hydration_denied: deniedResults,
  denied_responses_redacted: true,
}, null, 2));

function baseExportArguments() {
  return {
    name: " Memory Pack private export ",
    description: "private evidence export boundary",
    relationship_ids: [fixture.relationshipID],
  };
}

function assertExportPayload(payload, option) {
  const label = option.name;
  assert(payload && typeof payload === "object", `${label} export returned no payload`);
  const artifactJSON = requiredString(payload.artifact_json, `${label} artifact_json`);
  const artifact = JSON.parse(artifactJSON);
  assert(artifact.format === "dense-mem.memory-pack.v2.4", `${label} returned the wrong artifact format`);
  assert(artifact.source?.team_id === teamID, `${label} artifact used the wrong team provenance`);
  assert(artifact.source?.exported_by === ownerA.credential.id, `${label} artifact used the wrong exporter provenance`);
  assert(Array.isArray(artifact.relationships) && artifact.relationships.length === 1, `${label} relationship projection was incomplete`);
  assert(artifact.relationships[0].source_relationship_id === fixture.relationshipID, `${label} relationship identity changed`);
  assert(artifact.relationships[0].source_owner_profile_id === ownerA.credential.id, `${label} relationship owner provenance changed`);
  if (option.includeEntityNames === false) {
    assert(!Object.hasOwn(artifact.relationships[0].subject, "display_name"), `${label} subject name was not omitted`);
    assert(!Object.hasOwn(artifact.relationships[0].object, "display_name"), `${label} object name was not omitted`);
  } else {
    assert(artifact.relationships[0].subject.display_name === "Memory Pack private subject", `${label} subject name was not hydrated`);
    assert(artifact.relationships[0].object.display_name === "Memory Pack private target", `${label} object name was not hydrated`);
  }
  assert(!artifactJSON.includes("candidate") && !artifactJSON.includes("hypothesis"), `${label} artifact included non-canonical semantic state`);
  assert(payload.filename === "memory-pack-private-export.memory-pack.json", `${label} filename changed: ${payload.filename}`);
  assert(payload.content_sha256 === artifact.content_sha256 && /^[0-9a-f]{64}$/.test(payload.content_sha256), `${label} artifact hash is invalid`);

  const hashable = JSON.parse(artifactJSON);
  delete hashable.content_sha256;
  const computedHash = createHash("sha256").update(JSON.stringify(hashable)).digest("hex");
  assert(computedHash === payload.content_sha256, `${label} content hash did not cover canonical artifact bytes`);

  const counts = payload.counts ?? {};
  if (option.includeEvidence === false) {
    assert(!Object.hasOwn(artifact, "evidence") && !Object.hasOwn(artifact, "evidence_supports"), `${label} artifact included support arrays`);
    assert(!Object.hasOwn(artifact.relationships[0], "support_evidence_ids"), `${label} relationship included support IDs`);
    assert(Number(counts.evidence) === 0 && Number(counts.evidence_supports) === 0, `${label} counts included omitted support`);
    assert(Array.isArray(payload.omissions) && payload.omissions.length === 1 && payload.omissions[0].reason === "support evidence omitted by request", `${label} omission metadata was incomplete`);
    assert(!artifactJSON.includes(evidenceSentinel), `${label} artifact exposed private evidence content`);
  } else {
    assert(artifact.evidence?.length === 1 && artifact.evidence[0].content === evidenceSentinel, `${label} evidence content was not hydrated`);
    assert(artifact.evidence_supports?.length === 1 && artifact.evidence_supports[0].evidence_id === fixture.fragmentID, `${label} support provenance was not hydrated`);
    assert(artifact.relationships[0].support_evidence_ids?.[0] === fixture.fragmentID, `${label} relationship support IDs were not hydrated`);
    assert(Number(counts.evidence) === 1 && Number(counts.evidence_supports) === 1, `${label} support counts were incomplete`);
    assert(Array.isArray(payload.omissions) && payload.omissions.length === 0, `${label} unexpectedly reported omissions`);
  }
}

async function exportSuccess(apiKey, argumentsValue) {
  const response = await exportRaw(apiKey, argumentsValue);
  if (response.status !== 200 || response.payload.error || response.payload.result?.isError) {
    throw new Error("owner export did not complete successfully");
  }
  const text = response.payload.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error("owner export result was missing bounded JSON content");
  const payload = JSON.parse(text);
  if (payload.code) throw new Error("owner export returned a typed error");
  return payload;
}

async function exportRaw(apiKey, argumentsValue) {
  return requestJSON(`${userURL}/mcp`, {
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
      params: { name: "export_memory_pack", arguments: argumentsValue },
    }),
  });
}

async function assertInvalidExport(apiKey, argumentsValue, expectedMessage) {
  const response = await exportRaw(apiKey, argumentsValue);
  const error = response.payload.error;
  const data = error?.data;
  assert(response.status === 200 && error?.code === -32602 && !response.payload.result, `invalid export returned an unexpected transport response: ${JSON.stringify(response)}`);
  assert(
    data?.code === "invalid_input" &&
      data.reason_code === "validation_failed" &&
      data.next_action === "correct_and_resubmit" &&
      data.retryable === false &&
      Array.isArray(data.issues) &&
      data.issues.some((issue) => issue.message?.includes(expectedMessage)),
    `invalid export omitted the documented validation envelope for ${expectedMessage}: ${JSON.stringify(data)}`,
  );
  assert(!JSON.stringify(response).includes(evidenceSentinel), "invalid export response exposed private evidence content");
}

async function assertAuthenticated(actor) {
  const response = await requestJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${actor.credential.apiKey}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/list",
      params: {},
    }),
  });
  assert(
    response.status === 200 &&
      !response.payload.error &&
      Array.isArray(response.payload.result?.tools),
    `${actor.name} credential authentication probe failed with HTTP ${response.status}`,
  );
}

async function createTeam(name) {
  const response = await controlJSON("/teams", { method: "POST", body: { name, description: "memory-pack export e2e" } });
  return { id: requiredString(response.data?.id, "created team ID") };
}

async function createCredential(targetTeamID, name) {
  const response = await controlJSON(`/teams/${targetTeamID}/credentials`, {
    method: "POST",
    body: { name, scopes: ["read", "write"], rate_limit: 300, memory_binding: "credential_private" },
  });
  const apiKey = requiredString(response.data?.api_key, "created API key");
  const credential = requiredObject(response.data?.credential, "created credential");
  return { apiKey, credential };
}

async function loadSpaces() {
  const response = await controlJSON("/private-memory/spaces?limit=500&offset=0");
  assert(Array.isArray(response.data), "private-memory spaces were not returned");
  return response.data;
}

function credentialSpace(spaces, credentialID) {
  const space = spaces.find((item) => item.kind === "credential_private" && item.owner_credential_id === credentialID);
  if (!space) throw new Error(`credential private space was not created for ${credentialID}`);
  return space;
}

function seedPrivateRelationship({ teamID: targetTeamID, ownerID, spaceID }) {
  const ids = {
    ingestID: randomUUID(),
    fragmentID: randomUUID(),
    subjectID: randomUUID(),
    objectID: randomUUID(),
    relationshipID: randomUUID(),
    observationID: randomUUID(),
    verificationID: randomUUID(),
    supportID: randomUUID(),
  };
  const subjectName = "Memory Pack private subject";
  const objectName = "Memory Pack private target";
  const statements = [
    "BEGIN",
    "SELECT set_config('app.tx_mode', 'system', true)",
    `INSERT INTO knowledge_ingests (
       team_id, ingest_id, owner_profile_id, idempotency_key, request_hash, source_summary,
       status, proposal, metadata, completed_at, space_id
     ) VALUES (
       ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.ingestID)}::uuid, ${sqlLiteral(ownerID)}::uuid,
       ${sqlLiteral(`${runID}:ingest`)}, ${sqlLiteral(`sha256:${ids.ingestID}`)}, ${sqlLiteral(evidenceSentinel)},
       'completed', '{}'::jsonb, '{}'::jsonb, now(), ${sqlLiteral(spaceID)}::uuid
     )`,
    `INSERT INTO evidence_fragments (
       team_id, fragment_id, ingest_id, owner_profile_id, evidence_index, content,
       content_hash, source_type, authority, source_ref, space_id
     ) VALUES (
       ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.fragmentID)}::uuid, ${sqlLiteral(ids.ingestID)}::uuid,
       ${sqlLiteral(ownerID)}::uuid, 0, ${sqlLiteral(evidenceSentinel)}, ${sqlLiteral(`sha256:${ids.fragmentID}`)},
       'manual', 'primary', ${sqlLiteral(`${runID}:memory-pack`)}, ${sqlLiteral(spaceID)}::uuid
     )`,
    `INSERT INTO team_predicate_definitions (
       team_id, predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
       relationship_kind, current_cardinality, lifecycle_state, origin, metadata, created_at
     ) SELECT ${sqlLiteral(targetTeamID)}::uuid, predicate_key, version, aliases, allowed_subject_kinds,
       allowed_object_kinds, relationship_kind, current_cardinality, lifecycle_state, 'built_in', metadata, created_at
       FROM predicate_definitions WHERE predicate_key = 'uses' AND version = 1
       ON CONFLICT (team_id, predicate_key, version) DO NOTHING`,
    `INSERT INTO entity_records (team_id, entity_id, entity_kind, metadata, space_id) VALUES
       (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.subjectID)}::uuid, 'project', '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid),
       (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.objectID)}::uuid, 'product', '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid)`,
    `INSERT INTO entity_names (
       team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind, metadata, space_id
     ) VALUES
       (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.subjectID)}::uuid, ${sqlLiteral(ownerID)}::uuid,
        ${sqlLiteral(subjectName)}, lower(${sqlLiteral(subjectName)}), 'canonical', '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid),
       (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.objectID)}::uuid, ${sqlLiteral(ownerID)}::uuid,
        ${sqlLiteral(objectName)}, lower(${sqlLiteral(objectName)}), 'canonical', '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid)`,
    `INSERT INTO relationship_records (
       team_id, relationship_id, owner_profile_id, semantic_group_key, subject_entity_id,
       predicate_key, predicate_version, object_entity_id, relationship_kind, current_cardinality,
       status, polarity, support_count, source_group_count, metadata, space_id
     ) VALUES (
       ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.relationshipID)}::uuid, ${sqlLiteral(ownerID)}::uuid,
       ${sqlLiteral(`${runID}:memory-pack`)}, ${sqlLiteral(ids.subjectID)}::uuid, 'uses', 1,
       ${sqlLiteral(ids.objectID)}::uuid, 'state', 'many', 'active', '+', 1, 1, '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid
     )`,
    `INSERT INTO relationship_observations (
       team_id, observation_id, relationship_id, ingest_id, owner_profile_id, subject_ref,
       original_predicate, object_ref, subject_entity_id, predicate_key, predicate_version,
       object_entity_id, evidence, metadata, space_id
     ) VALUES (
       ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.observationID)}::uuid, ${sqlLiteral(ids.relationshipID)}::uuid,
       ${sqlLiteral(ids.ingestID)}::uuid, ${sqlLiteral(ownerID)}::uuid, ${sqlLiteral(subjectName)}, 'uses',
       ${sqlLiteral(objectName)}, ${sqlLiteral(ids.subjectID)}::uuid, 'uses', 1, ${sqlLiteral(ids.objectID)}::uuid,
       jsonb_build_array(jsonb_build_object('fragment_id', ${sqlLiteral(ids.fragmentID)}, 'start', 0, 'end', char_length(${sqlLiteral(evidenceSentinel)}))),
       '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid
     )`,
    `INSERT INTO verification_events (
       team_id, verification_event_id, observation_id, owner_profile_id, evidence_verdict,
       confidence, rationale, model, response_hash, metadata, space_id
     ) VALUES (
       ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.verificationID)}::uuid, ${sqlLiteral(ids.observationID)}::uuid,
       ${sqlLiteral(ownerID)}::uuid, 'entailed', 0.99, 'memory pack e2e fixture', 'memory-pack-e2e',
       ${sqlLiteral(`sha256:${ids.verificationID}`)}, '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid
     )`,
    `INSERT INTO relationship_evidence_supports (
       team_id, support_id, relationship_id, observation_id, verification_event_id, fragment_id,
       owner_profile_id, source_group_key, span_start, span_end, quote, authority, metadata, space_id
     ) VALUES (
       ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.supportID)}::uuid, ${sqlLiteral(ids.relationshipID)}::uuid,
       ${sqlLiteral(ids.observationID)}::uuid, ${sqlLiteral(ids.verificationID)}::uuid, ${sqlLiteral(ids.fragmentID)}::uuid,
       ${sqlLiteral(ownerID)}::uuid, ${sqlLiteral(`${runID}:memory-pack`)}, 0, char_length(${sqlLiteral(evidenceSentinel)}),
       ${sqlLiteral(evidenceSentinel)}, 'primary', '{}'::jsonb, ${sqlLiteral(spaceID)}::uuid
     )`,
    `INSERT INTO relationship_support_decision_events (
       team_id, support_id, relationship_id, owner_profile_id, actor_profile_id, decision, reason, metadata, space_id
     ) VALUES (
       ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ids.supportID)}::uuid, ${sqlLiteral(ids.relationshipID)}::uuid,
       ${sqlLiteral(ownerID)}::uuid, ${sqlLiteral(ownerID)}::uuid, 'grant', 'memory pack e2e fixture', '{}'::jsonb,
       ${sqlLiteral(spaceID)}::uuid
     )`,
    "COMMIT",
  ];
  postgresExec(statements.join(";\n"));
  return { ...ids, teamID: targetTeamID, spaceID, subjectName, objectName, evidenceSentinel };
}

async function controlJSON(path, options = {}) {
  const response = await requestJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${controlToken}`, Accept: "application/json", ...(options.headers ?? {}) },
  });
  if (response.status < 200 || response.status > 299) throw new Error(`control ${path} returned HTTP ${response.status}`);
  return response.payload;
}

async function requestJSON(url, options = {}) {
  const headers = { Accept: "application/json", ...(options.headers ?? {}) };
  let body = options.body;
  if (body !== undefined && typeof body !== "string") {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(body);
  }
  const response = await fetch(url, { ...options, headers, body });
  const text = await response.text();
  let payload = {};
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    throw new Error(`HTTP ${response.status} returned non-JSON content`);
  }
  return { status: response.status, payload, text };
}

function postgresExec(sql) {
  const result = postgresCommand(sql);
  if (result.status !== 0) {
    const line = String(result.stderr ?? "").split(/\r?\n/).find((item) => item.includes("ERROR:"));
    const summary = (line ?? "PostgreSQL fixture mutation failed").replaceAll(evidenceSentinel, "[redacted]").replaceAll(runID, "[redacted]").slice(0, 500);
    throw new Error(summary);
  }
}

function postgresCommand(sql) {
  return spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec",
    'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "memory-pack-e2e", sql,
  ], { cwd: repositoryRoot(), encoding: "utf8", maxBuffer: 10 * 1024 * 1024 });
}

function repositoryRoot() {
  return fileURLToPath(new URL("../..", import.meta.url));
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function requiredString(value, label) {
  if (typeof value !== "string" || value.trim() === "") throw new Error(`${label} missing`);
  return value;
}

function requiredObject(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} missing`);
  return value;
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}
