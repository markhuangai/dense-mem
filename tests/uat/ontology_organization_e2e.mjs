import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { captureRememberFailure } from "./synchronous_write/cases/diagnostics.mjs";
import { assessmentRecords, captureSemanticAssessmentWindow } from "./ontology_assessment_diagnostics.mjs";

const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const apiKey = required("DENSE_MEM_E2E_API_KEY");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const failureDirectory = process.env.DENSE_MEM_E2E_REMEMBER_FAILURE_DIR || `/tmp/dense-mem-remember-failures-${randomUUID()}`;
const credentialTeams = new Map([[apiKey, teamID]]);
const diagnosticSince = new Date().toISOString();
const maintenanceObservationAttempts = 300;
const original = (await control("/config/ontology-maintenance")).data;
let paused = false;
let failure;
try {
  const initial = (await control("/ontology/status")).data;
  await control("/ontology/pause", { operation_key: randomUUID() });
  paused = true;
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }, { key: "ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" }] }, "PATCH");
  assert.equal((await control("/ontology/status")).data.paused, true);
  const shared = (await control(`/teams/${teamID}/credentials`, { name: "Ontology shared acceptance", scopes: ["read", "write"], rate_limit: 300 })).data;
  assert.equal(shared.credential.memory_access.team_shared.write, true);
  const second = (await control(`/teams/${teamID}/credentials`, { name: "Ontology owner B", scopes: ["read", "write"], rate_limit: 300 })).data;
  const reader = (await control(`/teams/${teamID}/credentials`, { name: "Ontology reader C", scopes: ["read", "write"], rate_limit: 300 })).data;
  const privateCredential = (await control(`/teams/${teamID}/credentials`, { name: "Ontology private source", scopes: ["read", "write"], rate_limit: 300 })).data;
  const foreignTeam = (await control("/teams", { name: `ontology-foreign-${randomUUID()}` })).data;
  const foreignCredential = (await control(`/teams/${foreignTeam.id}/credentials`, { name: "Ontology foreign source", scopes: ["read", "write"], rate_limit: 300 })).data;
  for (const credential of [shared, second, reader, privateCredential]) credentialTeams.set(credential.api_key, teamID);
  credentialTeams.set(foreignCredential.api_key, foreignTeam.id);
  const texts = ["Atlas stores PostgreSQL data.", "Atlas uses PostgreSQL for data storage.", "Atlas stores PostgreSQL data and encrypts its backups."];
  const sourceIDs = [];
  for (const [index, content] of texts.entries()) {
    sourceIDs.push(await remember(content, index === 1 ? second.api_key : shared.api_key));
  }
  assert.equal(new Set(sourceIDs).size, 3, "force-inserted paraphrases must retain distinct source handles");
  const privateID = await remember(texts[0], privateCredential.api_key, "private");
  const foreignID = await remember(texts[0], foreignCredential.api_key);
  const coordinationID = await remember("Atlas uses Redis for coordination.", shared.api_key);
  const coordinationAlternativeID = await remember("Atlas uses KeyDB for coordination.", shared.api_key);
  await remember("Atlas uses SQLite for backup storage.", shared.api_key);
  await remember("Atlas uses SQLite for backup storage.", second.api_key);
  await expectStatus("/ontology/runs", { operation_key: randomUUID(), max_batches: 101 }, 422);
  await expectStatus("/ontology/runs", { operation_key: randomUUID(), team_id: "untrusted" }, 422);
  await expectStatus("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }, { key: "ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" }, { key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] }, 422, "PATCH");
  await expectStatus("/ontology/runs", { operation_key: randomUUID() }, 409);
  const denied = await fetch(`${controlURL}/control/api/ontology/status`, { headers: { Authorization: `Bearer ${apiKey}` } });
  assert.equal(denied.status, 401, "MCP credentials must not authorize policy controls");
  await control("/ontology/resume", { operation_key: randomUUID() });
  paused = false;
  const key = randomUUID();
  const command = (await control("/ontology/runs", { operation_key: key, max_batches: 1 })).data;
  const replay = (await control("/ontology/runs", { operation_key: key, max_batches: 1 })).data;
  assert.equal(command.id, replay.id);
  assert.equal(command.window_id, replay.window_id);
  await expectStatus("/ontology/runs", { operation_key: key, max_batches: 2 }, 409);
  let status;
  for (let index = 0; index < maintenanceObservationAttempts; index++) {
    status = (await control("/ontology/status")).data;
    if (status.discovery_complete && status.counts.pending === 0 && status.counts.organized > initial.counts.organized) break;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert.equal(status.discovery_complete, true);
  assert.equal(status.counts.pending, 0);
  assert.equal(status.counts.failed, 0);
  assert(status.counts.organized > initial.counts.organized);
  assert(status.usage.attempts > 0, "provider dispatches must have durable attempt identities");
  assert.equal(status.usage.unreconciled_attempts, 0);
  const currentReceipts = JSON.parse(postgresQuery(`SELECT COALESCE(jsonb_agg(jsonb_build_object('source',source.source_id,'status',source.status,'assessment',source.assessment_id,'failure',assessment.body->'result'->>'failure_code')),'[]')
    FROM ontology_maintenance_sources AS source JOIN LATERAL (SELECT body FROM ontology_assessments WHERE team_id=source.team_id AND shared_space_id=source.shared_space_id AND space_generation=source.space_generation AND assessment_id=source.assessment_id LIMIT 1) AS assessment ON true
    WHERE source.team_id=${sqlLiteral(teamID)}::uuid AND source.source_kind='evidence' AND source.source_id IN (${sourceIDs.map(sqlLiteral).join(",")})`));
  assert.equal(currentReceipts.length, sourceIDs.length, "completed sources must reference durable assessments in their own scope");
  for (const receipt of currentReceipts) { assert.equal(receipt.status, "organized"); assert(receipt.assessment); assert(!receipt.failure); }
  assertCompletionTimestamps();

  const runs = (await control("/ontology/runs?limit=1")).data;
  assert.equal(runs.runs.length, 1);
  assert(runs.next_cursor, "run inspection must paginate");
  const all = (await control("/ontology/runs?limit=100")).data;
  assert(all.runs.find((run) => run.id === command.id).completed_batches <= 1);
  await control("/ontology/pause", { operation_key: randomUUID() });
  paused = true;
  const accountingBefore = (await control("/ontology/status")).data.window;
  const args = { query: "Atlas PostgreSQL", limit: 20 };
  const grouped = await mcp("recall_memory", args, reader.api_key);
  const relevant = grouped.results.filter((item) => sourceIDs.includes(item.evidence_id));
  assert.equal(relevant.length, 2, `equivalent sources must share one slot while the partial-overlap fact retains its own slot: ${JSON.stringify({ results: relevant, degradations: grouped.degradations, counts: status.counts })}`);
  const equivalent = relevant.find((item) => item.equivalent_evidence_ids.length === 1);
  assert(equivalent, "the paraphrase representative must expose its alternate original source");
  assert.deepEqual(new Set([equivalent.evidence_id, ...equivalent.equivalent_evidence_ids]), new Set(sourceIDs.slice(0, 2)));
  assert.equal(equivalent.equivalents_truncated, false);
  assert(relevant.some((item) => item.evidence_id === sourceIDs[2] && item.context.includes("encrypts")));
  for (const item of grouped.results) {
    assert(item.equivalent_evidence_ids.length <= 20);
    assert(!item.equivalent_evidence_ids.includes(privateID));
    assert(!item.equivalent_evidence_ids.includes(foreignID));
  }
  for (const knownID of [sourceIDs[0], sourceIDs[0].toUpperCase()]) {
    const known = await mcp("recall_memory", { ...args, known_evidence_ids: [knownID] }, reader.api_key);
    assert(!known.results.some((item) => sourceIDs.slice(0, 2).includes(item.evidence_id)));
    assert(known.results.some((item) => item.evidence_id === sourceIDs[2]));
  }
  for (const inaccessible of [privateID, foreignID, privateID.toUpperCase(), foreignID.toUpperCase()]) {
    const unchanged = await mcp("recall_memory", { ...args, known_evidence_ids: [inaccessible] }, reader.api_key);
    assert(unchanged.results.some((item) => sourceIDs.slice(0, 2).includes(item.evidence_id)), "inaccessible known handles cannot suppress visible evidence");
  }
  const discovered = await mcp("recall_memory", { ...args, query: "database platform" }, reader.api_key);
  assert(discovered.results.some((item) => sourceIDs.includes(item.evidence_id)), "published alias must discover its source evidence");
  await assertOntologyQueryScoring(reader.api_key, coordinationID, sourceIDs);
  const topicRecall = await mcp("recall_memory", { query: "Atlas", limit: 20, topic_limit: 10 }, reader.api_key);
  assert.equal(topicRecall.related_topics.length, 2, "shared Evidence must expose both direct topics");
  const topicIDs = topicRecall.related_topics.map((topic) => topic.topic_id);
  for (const [index, topic] of topicRecall.related_topics.entries()) {
    assert.equal(topic.rank, index + 1);
    assert(topic.name && topic.description && topic.eligible_evidence_count > 0);
    assert(!Object.hasOwn(topic, "relationships"));
    const follow = await mcp("recall_memory", { topic_ids: [topic.topic_id], limit: 50 }, reader.api_key);
    assert(follow.results.length > 0, "topic-only follow-up must retrieve exact Evidence");
    assert(!follow.results.some((item) => [privateID, foreignID].includes(item.evidence_id)), "topic counts and results must exclude private and foreign Evidence");
    const knownIDs = follow.results.flatMap((item) => [item.evidence_id, ...item.equivalent_evidence_ids]);
    const unseen = await mcp("recall_memory", { topic_ids: [topic.topic_id], limit: 50, known_evidence_ids: knownIDs }, reader.api_key);
    assert.equal(unseen.results.length, 0, "known Evidence must be excluded before topic selection");
    assert.equal(unseen.related_topics.length, 0, "known-only topics must have no eligible unseen hint");
  }
  const union = await mcp("recall_memory", { topic_ids: topicIDs, limit: 50 }, reader.api_key);
  assert(union.results.some((item) => item.evidence_id === coordinationID));
  assert(union.results.some((item) => item.evidence_id === coordinationAlternativeID));
  assert(union.results.some((item) => sourceIDs.includes(item.evidence_id)));
  const limitedTopics = await mcp("recall_memory", { query: "Atlas", limit: 20, topic_limit: 1 }, reader.api_key);
  assert.equal(limitedTopics.related_topics.length, 1);
  assert.equal(limitedTopics.topics_truncated, true);
  assert.equal(topicRecall.topics_truncated, false);
  const noTopics = await mcp("recall_memory", { query: "Atlas", topic_limit: 0 }, reader.api_key);
  assert.deepEqual(noTopics.related_topics, []);
  const pausedTopics = await mcp("recall_memory", { topic_ids: topicIDs, limit: 50 }, reader.api_key);
  assert.deepEqual(pausedTopics.related_topics, union.related_topics, "pause must preserve current organizational reads");
  const foreignTopics = await mcp("recall_memory", { query: "Atlas", topic_limit: 10 }, foreignCredential.api_key);
  const unavailableTopicIDs = [randomUUID(), foreignTopics.related_topics[0]?.topic_id].filter(Boolean);
  for (const id of unavailableTopicIDs) {
    const unavailable = await rpc("recall_memory", { topic_ids: [id] }, reader.api_key);
    assert(unavailable.result?.isError && unavailable.result.structuredContent.reason_code === "topic_unavailable", "foreign and missing topics must share bounded rejection");
    assert(!JSON.stringify(unavailable.result).includes(id));
  }
  const topicHeads = postgresQuery(`SELECT jsonb_agg(jsonb_build_object('record_id',record_id,'retired',retired)) FROM ontology_record_heads WHERE team_id=${sqlLiteral(teamID)}::uuid AND record_id=${sqlLiteral(topicIDs[0])}::uuid`);
  assert(JSON.parse(topicHeads)?.length === 1);
  postgresQuery(`UPDATE ontology_record_heads SET retired=true WHERE team_id=${sqlLiteral(teamID)}::uuid AND record_id=${sqlLiteral(topicIDs[0])}::uuid`);
  try {
    const unavailable = await rpc("recall_memory", { topic_ids: [topicIDs[0]] }, reader.api_key);
    assert(unavailable.result?.isError && unavailable.result.structuredContent.reason_code === "topic_unavailable", "retired topic must fail explicit filtering");
  } finally {
    postgresQuery(`UPDATE ontology_record_heads SET retired=false WHERE team_id=${sqlLiteral(teamID)}::uuid AND record_id=${sqlLiteral(topicIDs[0])}::uuid`);
  }
  for (const temporalField of ["valid_at", "known_at"]) {
    const temporal = await mcp("recall_memory", { topic_ids: topicIDs, limit: 50, [temporalField]: new Date().toISOString() }, reader.api_key);
    assert(temporal.results.some((item) => sourceIDs.includes(item.evidence_id)), "temporal Evidence must use current direct topic membership");
    assert(temporal.related_topics.length > 0);
  }
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] }, "PATCH");
  try {
    const unavailable = await rpc("recall_memory", { topic_ids: topicIDs }, reader.api_key);
    assert(unavailable.result?.isError && unavailable.result.structuredContent.reason_code === "topic_unavailable");
    const ordinary = await mcp("recall_memory", { query: "Atlas" }, reader.api_key);
    assert(ordinary.results.length > 0 && ordinary.related_topics.length === 0, "disabled organization must preserve ordinary retrieval");
    assert(!ordinary.degradations.some((item) => item.frontier === "topics"), "intentional disablement is not a topic failure");
  } finally {
    await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
  }
  const accountingAfter = (await control("/ontology/status")).data.window;
  assert.equal(accountingAfter.usage.reported_input_tokens, accountingBefore.usage.reported_input_tokens, "Recall added organization provider input tokens");
  assert.equal(accountingAfter.usage.reported_output_tokens, accountingBefore.usage.reported_output_tokens, "Recall added organization provider output tokens");
  await control("/ontology/pause", { operation_key: randomUUID() });
  paused = true;
  await fixtureMode("ambiguous");
  await rememberFixture(shared.api_key, "Atlas uses PostgreSQL for ontology status acceptance.");
  await control("/ontology/resume", { operation_key: randomUUID() });
  paused = false;
  const ambiguousCommand = (await control("/ontology/runs", { operation_key: randomUUID(), max_batches: 1 })).data;
  const ambiguousRun = await terminalRun(ambiguousCommand.id);
  assert.equal(ambiguousRun.status, "incomplete");
  assert.equal(ambiguousRun.completed_batches, 1);
  assert.equal(ambiguousRun.failure_code || "", "");
  assert.equal(ambiguousRun.retryable, false);
  await fixtureMode("normal");

  await control("/ontology/pause", { operation_key: randomUUID() });
  paused = true;
  await fixtureMode("fail-three-then-success");
  await rememberFixture(shared.api_key, "Atlas uses PostgreSQL for ontology retry acceptance.");
  await control("/ontology/resume", { operation_key: randomUUID() });
  paused = false;
  const failedCommand = (await control("/ontology/runs", { operation_key: randomUUID(), max_batches: 1 })).data;
  const failedRun = await terminalRun(failedCommand.id);
  assert.equal(failedRun.status, "incomplete");
  assert(failedRun.failure_code, "required failure remains visible");
  assert.equal(failedRun.retryable, true);
  const failedAssessments = JSON.parse(postgresQuery(`SELECT COALESCE(jsonb_agg(assessment.body->'result'),'[]')::text
    FROM ontology_maintenance_batches AS batch JOIN ontology_assessments AS assessment
      ON assessment.team_id=batch.team_id AND assessment.shared_space_id=batch.shared_space_id
      AND assessment.space_generation=batch.space_generation AND assessment.operation_key='maintenance:'||batch.batch_id::text
    WHERE batch.team_id=${sqlLiteral(teamID)}::uuid AND batch.run_id=${sqlLiteral(failedRun.id)}::uuid`));
  assert.equal(failedAssessments.length, 1);
  const rejected = failedAssessments[0];
  assert.equal(rejected.failure_code, "provider_response_invalid");
  assert(!rejected.publication, "exhausted validation must publish no subset");
  assert(rejected.outcomes.every((outcome) => outcome.status === "failed" && !outcome.record_ids?.length));
  assert.equal(rejected.attempts.length, 3);
  for (const attempt of rejected.attempts) {
    assert.equal(attempt.failure_code, "response_invalid");
    assert.deepEqual(Object.keys(attempt.validation).sort(), ["actual_count", "code", "expected_count", "field_family", "stage"]);
    assert.equal(attempt.validation.stage, "validate");
    assert.equal(attempt.validation.code, "item_count_mismatch");
    assert.equal(attempt.validation.field_family, "items");
    assert(Number.isInteger(attempt.validation.expected_count) && attempt.validation.expected_count > 0);
    assert.equal(attempt.validation.actual_count, 0);
  }
  await fixtureMode("normal");
  const history = (await control("/ontology/runs?limit=200")).data;
  for (const run of history.runs) {
    if (run.status === "completed") assert.equal(run.failure_code || "", "", "completed runs must have no current failure reason");
  }
  await finalizationRecovery(shared.api_key);
  await finalizationRecovery(shared.api_key, "provider_response_invalid");
  const diagnosticAttempts = JSON.parse(postgresQuery(`SELECT COALESCE(jsonb_agg(attempt),'[]')::text FROM ontology_assessments AS assessment CROSS JOIN LATERAL jsonb_array_elements(assessment.body->'result'->'attempts') AS attempt WHERE assessment.team_id=${sqlLiteral(teamID)}::uuid`));
  assert(diagnosticAttempts.length > 0, "ontology must retain actual provider attempt diagnostics");
  assert(diagnosticAttempts.some((attempt) => attempt.failure_code === "response_invalid"), "the adverse path must retain validation failure diagnostics");
  for (const attempt of diagnosticAttempts) {
    const diagnostics = attempt.diagnostics;
    assert(diagnostics, "every actual transport attempt must retain diagnostics");
    const beforeAdmission = ["queue", "prepare"].includes(diagnostics.failure_phase);
    const beforeHTTP = beforeAdmission || diagnostics.failure_phase === "admission";
    for (const phase of ["queue_ms", "admission_ms", "http_ms", "reconciliation_ms"]) {
      if ((phase === "admission_ms" && beforeAdmission) || (phase === "http_ms" && beforeHTTP)) {
        assert(!Object.hasOwn(diagnostics, phase), `unreached ${phase} must remain absent`);
      } else {
        assert(Number.isInteger(diagnostics[phase]) && diagnostics[phase] >= 0, `missing measured ${phase}`);
      }
    }
    if (attempt.failure_code === "response_invalid") {
      assert.equal(attempt.diagnostics.failure_phase, "validation");
      assert(attempt.validation, "rejected responses must retain validation details");
    } else {
      assert(!Object.hasOwn(attempt, "validation"), "successful and transport-failed attempts must omit validation rejection details");
    }
  }
  const topicWindow = captureSemanticAssessmentWindow({ project: required("DENSE_MEM_E2E_COMPOSE_PROJECT"), composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since: diagnosticSince, directory: failureDirectory+"/topic-selection" });
  const topicRecords=topicWindow.split(/\r?\n/).filter(Boolean).map((line)=>JSON.parse(line)).filter((row)=>row.team_id===teamID);
  for (const credential of credentialTeams.keys()) assert(!topicWindow.includes(credential),"topic diagnostic capture exposed credentials");
  const selectedLog=topicRecords.find((row)=>row.msg==="recall_topic_selection"&&row.outcome==="selected"&&row.selection?.memberships?.length);
  assert(selectedLog && selectedLog.level==="DEBUG" && selectedLog.selection.generation>0 && selectedLog.topics.length>0,"current memberships/counts/generation must be diagnosable from stdout");
  for (const id of [...unavailableTopicIDs, topicIDs[0]]) {
    const rejectedLog = topicRecords.find((row) => row.msg === "recall_topic_degraded" && Object.keys(row.rejected ?? {}).length > 0 && row.requested_ids?.includes(id) && row.rejected[id] === "unavailable");
    assert(rejectedLog && rejectedLog.level === "WARN" && rejectedLog.reason, "missing, foreign, and retired topic reasons must be diagnosable from stdout");
  }
  console.log(JSON.stringify({ status: "ok", scenario: "ontology_organization", window_id: status.window.id, counts: status.counts, durable_command_replay: true, operator_isolation: true, grouped_recall: true, original_sources_retained: true, private_and_foreign_isolation: true, stale_group_fallback: true, recall_organization_provider_tokens: 0, bounded_ambiguity_run: ambiguousRun.id, retryable_failure_run: failedRun.id }));
} catch (error) {
  failure = error;
  try {
    const window = captureSemanticAssessmentWindow({ project: required("DENSE_MEM_E2E_COMPOSE_PROJECT"), composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since: diagnosticSince,
      directory: `${failureDirectory}/ontology-organization` });
    const diagnosticTeams = new Set(credentialTeams.values());
    for (const record of assessmentRecords(window).filter((record) => diagnosticTeams.has(record.team_id))) console.log(`ontology_semantic_diagnostic_record ${JSON.stringify(record)}`);
  } catch (captureError) { failure = new AggregateError([error, captureError], "ontology organization failure capture is incomplete"); }
} finally {
  const failures = failure ? [failure] : [];
  for (const restore of [
    () => fixtureMode("normal"),
    async () => { if (paused) await control("/ontology/resume", { operation_key: randomUUID() }); },
    () => control("/config/ontology-maintenance", { items: original.items.map(({ key, value }) => ({ key, value })) }, "PATCH"),
  ]) {
    try { await restore(); } catch (error) { failures.push(error); }
  }
  if (failures.length === 1) throw failures[0];
  if (failures.length > 1) throw new AggregateError(failures, "ontology UAT and restoration failed", { cause: failure });
}

function required(name) { assert(process.env[name], `${name} is required`); return process.env[name]; }
async function request(path, body, method = body ? "POST" : "GET") {
  return fetch(`${controlURL}/control/api${path}`, { method, headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
}
async function control(path, body, method) {
  const response = await request(path, body, method);
  assert(response.ok, `control ${path} returned ${response.status}: ${(await response.clone().text()).slice(0, 500)}`);
  return response.json();
}
async function expectStatus(path, body, status, method) { assert.equal((await request(path, body, method)).status, status); }

async function rpc(name, args, key) {
  const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name, arguments: args } }) });
  assert.equal(response.status, 200);
  const envelope = await response.json();
  if (name === "remember") {
    const terminal = envelope.result?.content?.[0]?.text ? JSON.parse(envelope.result.content[0].text) : envelope.error;
    if (terminal?.processing_state === "failed" || envelope.result?.isError || envelope.error) {
      try {
        console.error(JSON.stringify(await captureRememberFailure({ control, teamID: credentialTeams.get(key), correlationID: terminal?.correlation_id,
          directory: failureDirectory })));
      } catch (error) {
        throw new AggregateError([new Error("Remember failed"), error], "Remember failure diagnostic capture failed");
      }
    }
  }
  return envelope;
}
async function mcp(name, args, key) {
  const result = await rpc(name, args, key);
  assert(!result.error && result.result?.isError !== true, `${name} failed: ${JSON.stringify(result.error ?? result.result)}`);
  return JSON.parse(result.result.content[0].text);
}
async function remember(content, key, targetSpace = "team_shared") {
  const result = await mcp("remember", { target_space: targetSpace, idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-e2e", source_group: "ontology-e2e", force_insert: true }] }, key);
  assert.equal(result.processing_state, "completed");
  const evidence = result.evidence.find((item) => item.disposition === "stored");
  assert(evidence?.evidence_id, "Remember did not retain exact Evidence");
  return evidence.evidence_id;
}

async function assertOntologyQueryScoring(readerKey, ordinaryID, ontologyIDs) {
  const rows = JSON.parse(postgresQuery(`SELECT jsonb_agg(jsonb_build_object('id',search_document_id,'source',source_id,'embedding',embedding::text,'dimensions',embedding_dimensions))
    FROM search_documents WHERE team_id=${sqlLiteral(teamID)}::uuid AND source_kind='evidence'
      AND space_id=dense_mem_team_shared_space(${sqlLiteral(teamID)}::uuid) AND search_state='current' AND embedding IS NOT NULL`));
  assert(rows.some((row) => row.source === ordinaryID));
  assert(rows.some((row) => row.source === ontologyIDs[0]));
  const dimension = rows[0].dimensions;
  assert(rows.every((row) => row.dimensions === dimension));
  const vector = (axis) => JSON.stringify(Array.from({ length: dimension }, (_, index) => Number(index === axis)));
  const selected = rows.map((row) => `${sqlLiteral(row.id)}::uuid`).join(",");
  const rankFirst = (id) => postgresQuery(`UPDATE search_documents SET embedding=(CASE WHEN source_id=${sqlLiteral(id)}::uuid THEN ${sqlLiteral(vector(0))} ELSE ${sqlLiteral(vector(1))} END)::vector WHERE search_document_id IN (${selected})`);
  const request = { query: "database platform [fixture-ranking:ontology]", limit: 1 };
  try {
    rankFirst(ordinaryID);
    await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] }, "PATCH");
    const ordinary = await mcp("recall_memory", request, readerKey);
    assert.equal(ordinary.results[0]?.evidence_id, ordinaryID);
    await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
    const unrelatedMembership = await mcp("recall_memory", request, readerKey);
    assert.equal(unrelatedMembership.results[0]?.evidence_id, ordinaryID, "ontology membership must not promote a less relevant vector candidate");
    rankFirst(ontologyIDs[0]);
    const relevantDiscovery = await mcp("recall_memory", request, readerKey);
    assert(ontologyIDs.includes(relevantDiscovery.results[0]?.evidence_id), "query-relevant ontology evidence must retain its ranked place");
    console.log(JSON.stringify({ scenario: "ontology_query_scoring", membership_adds_no_vote: true, relevant_discovery_ranked_first: true }));
  } finally {
    postgresQuery(`UPDATE search_documents AS document SET embedding=original.embedding::vector FROM (VALUES ${rows.map((row) => `(${sqlLiteral(row.id)}::uuid,${sqlLiteral(row.embedding)})`).join(",")}) AS original(id,embedding) WHERE document.search_document_id=original.id`);
    await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
  }
}

function postgresQuery(sql) {
  const scopedSQL = ["BEGIN", "SET LOCAL app.tx_mode='system'", "SET LOCAL app.current_team_id=''", "SET LOCAL app.current_profile_id=''", "SET LOCAL app.allowed_space_ids=''", sql, "COMMIT"].join(";\n");
  const result = spawnSync("docker", ["compose", "-p", required("DENSE_MEM_E2E_COMPOSE_PROJECT"), "-f", required("DENSE_MEM_E2E_COMPOSE_FILE"), "exec", "-T", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "ontology-evidence-e2e", scopedSQL], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  return result.stdout.trim();
}
function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }

async function fixtureMode(mode) {
  const response = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/ontology-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) });
  assert.equal(response.status, 200);
}
async function rememberFixture(key, content) {
  const result = await mcp("remember", { target_space: "team_shared", idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-followups", source_group: randomUUID() }] }, key);
  assert.equal(result.processing_state, "completed");
}
async function terminalRun(id) {
  const started = Date.now();
  console.log(JSON.stringify({ scenario: "ontology_organization", phase: "maintenance_run_wait", run_id: id }));
  for (let index = 0; index < maintenanceObservationAttempts; index++) {
    const page = (await control("/ontology/runs?limit=200")).data;
    const run = page.runs.find((run) => run.id === id);
    if (run && ["completed", "incomplete"].includes(run.status)) {
      console.log(JSON.stringify({ scenario: "ontology_organization", phase: "maintenance_run_terminal", run_id: id, status: run.status, failure_code: run.failure_code, elapsed_ms: Date.now() - started }));
      return run;
    }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert.fail(`maintenance run ${id} did not finish`);
}

async function finalizationRecovery(key, failureCode = "") {
  await control("/ontology/pause", { operation_key: randomUUID() });
  paused = true;
  await pollFinalization(() => postgresQuery(`SELECT count(*) FROM ontology_maintenance_batches WHERE team_id=${sqlLiteral(teamID)}::uuid AND status='running'`) === "0", "drain before finalization fixture");
  await remember(`Atlas uses PostgreSQL for finalization recovery ${randomUUID()}.`, key);
  await fixtureMode(failureCode ? "fail-three-then-success" : "normal");
  const canonicalBefore = canonicalFingerprints();
  const release = await finalizationBlocker();
  try {
    postgresQuery(`CREATE FUNCTION ontology_e2e_finalization_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
      BEGIN IF NEW.team_id=${sqlLiteral(teamID)}::uuid AND NEW.assessment_id IS NOT NULL AND NEW.assessment_id IS DISTINCT FROM OLD.assessment_id AND EXISTS(
        SELECT 1 FROM ontology_assessments AS assessment WHERE assessment.team_id=NEW.team_id
        AND assessment.shared_space_id=NEW.shared_space_id AND assessment.space_generation=NEW.space_generation AND assessment.assessment_id=NEW.assessment_id
        AND COALESCE(assessment.body->'result'->>'failure_code','')=${sqlLiteral(failureCode)}
        AND assessment.body->'result'->>'current'=${sqlLiteral(String(!failureCode))})
        THEN PERFORM pg_advisory_xact_lock(603002); END IF; RETURN NEW; END $$;
      CREATE TRIGGER ontology_e2e_finalization_barrier BEFORE UPDATE OF status ON ontology_maintenance_sources FOR EACH ROW EXECUTE FUNCTION ontology_e2e_finalization_barrier()`);
    await control("/ontology/resume", { operation_key: randomUUID() });
    paused = false;
    let receipt;
    await pollFinalization(() => {
      const body = postgresQuery(`SELECT jsonb_build_object('id',batch.batch_id,'run_id',batch.run_id,'lease_until',batch.lease_until,
        'backend',activity.pid,'query_started_at',activity.query_start)::text
        FROM ontology_maintenance_batches AS batch JOIN ontology_assessments AS assessment
        ON assessment.team_id=batch.team_id AND assessment.shared_space_id=batch.shared_space_id AND assessment.space_generation=batch.space_generation AND assessment.operation_key='maintenance:'||batch.batch_id::text
        JOIN pg_stat_activity AS activity ON activity.datname=current_database() AND activity.wait_event='advisory' AND activity.query LIKE '%UPDATE ontology_maintenance_sources SET status=%'
        WHERE batch.team_id=${sqlLiteral(teamID)}::uuid AND batch.status='running'
        AND COALESCE(assessment.body->'result'->>'failure_code','')=${sqlLiteral(failureCode)} AND assessment.body->'result'->>'current'=${sqlLiteral(String(!failureCode))} LIMIT 1`);
      if (body) receipt = JSON.parse(body);
      return Boolean(receipt);
    }, "durable receipt followed by blocked completion");
    await pollFinalization(() => postgresQuery(`SELECT count(*) FROM pg_stat_activity WHERE pid=${Number(receipt.backend)} AND query_start=${sqlLiteral(receipt.query_started_at)}::timestamptz AND wait_event='advisory'`) === "0", "five-second finalization cancellation", 20);
    const cancellationMS = Date.now() - Date.parse(receipt.query_started_at);
    assert(cancellationMS >= 4000, "the adverse case must exercise the finalization deadline");
    assert.equal(postgresQuery(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=${sqlLiteral(receipt.id)}::uuid`), "running");
    // Stop later batches from changing recovered rows before inspection.
    await control("/ontology/pause", { operation_key: randomUUID() });
    paused = true;
    const frozenStatus = (await control("/ontology/status")).data;
    assert.equal(frozenStatus.enabled, true, "pause must preserve ontology enablement for capture");
    assert.equal(frozenStatus.paused, true);
    assert(frozenStatus.active_work > 0, "reconciled receipts must not hide unfinished batch finalization");
    const attempts = postgresQuery(`SELECT COALESCE(jsonb_agg(to_jsonb(attempt_record) ORDER BY assessment_id,attempt)::text,'[]') FROM ontology_maintenance_attempts AS attempt_record WHERE batch_id=${sqlLiteral(receipt.id)}::uuid`);
    const admitted = JSON.parse(attempts);
    assert(admitted.every((attempt) => attempt.reconciled_at), "active attempts cannot be recovered");
    if (failureCode) assert.equal(admitted.length, 3, "the failed receipt must exhaust complete-response regeneration");
    const completedBefore = Number(postgresQuery(`SELECT completed_batches FROM ontology_maintenance_runs WHERE run_id=${sqlLiteral(receipt.run_id)}::uuid`));
    const receiptBody = postgresQuery(`SELECT md5(body::text) FROM ontology_assessments WHERE operation_key=${sqlLiteral(`maintenance:${receipt.id}`)}`);
    let recoveryBoundary;
    await pollFinalization(() => {
      recoveryBoundary = postgresQuery(`SELECT clock_timestamp()::text FROM pg_stat_activity
        WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%UPDATE ontology_maintenance_sources SET status=%'
        AND query_start>${sqlLiteral(receipt.query_started_at)}::timestamptz LIMIT 1`);
      return Boolean(recoveryBoundary);
    }, "recovery blocked after the canceled finalization");
    const started = Date.now();
    await release();
    await pollFinalization(() => postgresQuery(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=${sqlLiteral(receipt.id)}::uuid`) === "completed", "automatic receipt recovery");
    assert(Date.now() < Date.parse(receipt.lease_until), "recovery must precede native lease expiry");
    assert.equal(postgresQuery(`SELECT failure_code FROM ontology_maintenance_batches WHERE batch_id=${sqlLiteral(receipt.id)}::uuid`), failureCode);
    assertCompletionTimestamps(receipt.id, recoveryBoundary);
    assert(Number(postgresQuery(`SELECT completed_batches FROM ontology_maintenance_runs WHERE run_id=${sqlLiteral(receipt.run_id)}::uuid`)) > completedBefore, "recovery must count the completed batch");
    if (failureCode) {
      const outcomes = JSON.parse(postgresQuery(`SELECT jsonb_agg(jsonb_build_object('status',status,'reason',reason))::text FROM ontology_maintenance_sources WHERE team_id=${sqlLiteral(teamID)}::uuid AND assessment_id=(SELECT assessment_id FROM ontology_assessments WHERE operation_key=${sqlLiteral(`maintenance:${receipt.id}`)})`));
      assert(outcomes?.length > 0, "failed receipt must finalize its unchanged sources");
      assert(outcomes.every((outcome) => outcome.status === "failed" && outcome.reason === failureCode));
      const failedStatus = (await control("/ontology/status")).data;
      assert.equal(failedStatus.coverage_complete, false, "drained terminal failure is incomplete coverage");
      assert(failedStatus.counts.failed > 0);
    }
    assert.equal(postgresQuery(`SELECT count(*) FROM ontology_assessments WHERE operation_key=${sqlLiteral(`maintenance:${receipt.id}`)}`), "1");
    assert.equal(postgresQuery(`SELECT md5(body::text) FROM ontology_assessments WHERE operation_key=${sqlLiteral(`maintenance:${receipt.id}`)}`), receiptBody);
    assert.equal(postgresQuery(`SELECT COALESCE(jsonb_agg(to_jsonb(attempt_record) ORDER BY assessment_id,attempt)::text,'[]') FROM ontology_maintenance_attempts AS attempt_record WHERE batch_id=${sqlLiteral(receipt.id)}::uuid`), attempts, "recovery must not dispatch or alter provider attempts");
    assert.equal(canonicalFingerprints(), canonicalBefore, "ontology recovery changed canonical Knowledge");
    console.log(JSON.stringify({ scenario: "ontology_finalization_recovery", batch_id: receipt.id, failure_code: failureCode, cancellation_ms: cancellationMS, recovery_ms: Date.now() - started, recovered_before_expiry: true, receipt_and_attempts_preserved: true, canonical_preserved: true }));
  } finally {
    await release();
    postgresQuery("DROP TRIGGER IF EXISTS ontology_e2e_finalization_barrier ON ontology_maintenance_sources; DROP FUNCTION IF EXISTS ontology_e2e_finalization_barrier()");
  }
}

function assertCompletionTimestamps(batchID, earliest) {
  const selected = batchID ? `AND batch.batch_id=${sqlLiteral(batchID)}::uuid` : "";
  const boundary = earliest ? `OR batch.completed_at<${sqlLiteral(earliest)}::timestamptz` : "";
  const counts = JSON.parse(postgresQuery(`SELECT jsonb_build_object('completed',count(*),'invalid',count(*) FILTER(
    WHERE batch.completed_at IS NULL OR batch.completed_at<batch.created_at OR batch.completed_at<assessment.created_at
    OR run.updated_at<batch.completed_at ${boundary}))::text
    FROM ontology_maintenance_batches AS batch JOIN ontology_maintenance_runs AS run USING(run_id)
    JOIN ontology_assessments AS assessment ON assessment.team_id=batch.team_id AND assessment.shared_space_id=batch.shared_space_id
      AND assessment.space_generation=batch.space_generation AND assessment.operation_key='maintenance:'||batch.batch_id::text
    WHERE batch.team_id=${sqlLiteral(teamID)}::uuid AND batch.status='completed' ${selected}`));
  assert(counts.completed > 0, "timestamp checks must observe completed native batches");
  assert.equal(counts.invalid, 0, "native completion timestamps must follow creation, receipt, and barrier time");
}

async function pollFinalization(check, label, seconds = maintenanceObservationAttempts) {
  for (let index = 0; index < seconds; index++) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert.fail(`timed out waiting for ${label}`);
}

function canonicalFingerprints(scopeTeam = teamID) {
  const tables = ["evidence_fragments", "evidence_occurrences", "evidence_sources", "evidence_source_revisions", "evidence_lifecycle_events", "evidence_quarantines"];
  return postgresQuery(tables.map((table) => `SELECT '${table}',md5(COALESCE(jsonb_agg(to_jsonb(record) ORDER BY to_jsonb(record)::text)::text,'[]')) FROM ${table} AS record WHERE team_id=${sqlLiteral(scopeTeam)}::uuid`).join(";\n"));
}

async function finalizationBlocker() {
  const child = spawn("docker", ["compose", "-p", required("DENSE_MEM_E2E_COMPOSE_PROJECT"), "-f", required("DENSE_MEM_E2E_COMPOSE_FILE"), "exec", "-T", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At'], { cwd: fileURLToPath(new URL("../..", import.meta.url)), stdio: ["pipe", "pipe", "pipe"], timeout: 240000 });
  let output = "";
  let stderr = "";
  child.stdout.on("data", (data) => { output += data; });
  child.stderr.on("data", (data) => { stderr += data; });
  let exitCode;
  const closed = new Promise((resolve, reject) => {
    child.on("error", reject);
    child.on("close", (code) => { exitCode = code; resolve(); });
  });
  child.stdin.write("SELECT pg_advisory_lock(603002); SELECT 'densemem-finalization-locked';\n");
  let released = false;
  const release = async () => {
    if (!released) {
      released = true;
      child.stdin.end("SELECT pg_advisory_unlock(603002);\n\\q\n");
    }
    await closed;
    assert.equal(exitCode, 0, stderr);
  };
  try {
    await pollFinalization(() => {
      assert.equal(exitCode, undefined, stderr);
      return output.includes("densemem-finalization-locked");
    }, "finalization barrier", 10);
    return release;
  } catch (error) {
    await release();
    throw error;
  }
}
