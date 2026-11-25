import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { captureRememberFailure } from "./synchronous_write/cases/diagnostics.mjs";
import { rememberWithEmbeddingRetry } from "./synchronous_write/remember_retry.mjs";
import { assessmentRecords, captureSemanticAssessmentWindow, reconstructDiagnosticBody } from "./topic_assessment_diagnostics.mjs";

const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const apiKey = required("DENSE_MEM_E2E_API_KEY");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const failureDirectory = process.env.DENSE_MEM_E2E_REMEMBER_FAILURE_DIR || `/tmp/dense-mem-remember-failures-${randomUUID()}`;
const credentialTeams = new Map([[apiKey, teamID]]);
const diagnosticSince = new Date().toISOString();
const maintenanceObservationAttempts = 300;
const original = (await control("/config/topic-maintenance")).data;
let paused = false;
let failure;
try {
  const initial = (await control("/topic-maintenance/status")).data;
  await control("/topic-maintenance/pause", { operation_key: randomUUID() });
  paused = true;
  await control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }, { key: "TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" }] }, "PATCH");
  assert.equal((await control("/topic-maintenance/status")).data.paused, true);
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
  const memberships = JSON.parse(postgresQuery(`SELECT to_jsonb(topic_ids) FROM topic_assignment_outcomes WHERE team_id=${sqlLiteral(teamID)}::uuid AND evidence_id=${sqlLiteral(sourceIDs[2])}::uuid ORDER BY created_at DESC LIMIT 1`));
  assert.equal(memberships.length, 2, "enabled, paused Remember must publish both PostgreSQL and backup memberships");
  for (const id of memberships) {
    const result = await mcp("recall_memory", { topic_ids: [id], limit: 50 }, reader.api_key);
    assert(result.results.some((hit) => hit.evidence_id === sourceIDs[2]), "direct Topic lookup must work before background admission");
  }
  await expectStatus("/topic-maintenance/runs", { operation_key: randomUUID(), max_batches: 101 }, 422);
  await expectStatus("/topic-maintenance/runs", { operation_key: randomUUID(), team_id: "untrusted" }, 422);
  await expectStatus("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }, { key: "TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" }, { key: "TOPIC_MAINTENANCE_ENABLED", value: "false" }] }, 422, "PATCH");
  await expectStatus("/topic-maintenance/runs", { operation_key: randomUUID() }, 409);
  const denied = await fetch(`${controlURL}/control/api/topic-maintenance/status`, { headers: { Authorization: `Bearer ${apiKey}` } });
  assert.equal(denied.status, 401, "MCP credentials must not authorize policy controls");
  await control("/topic-maintenance/resume", { operation_key: randomUUID() });
  paused = false;
  const key = randomUUID();
  const command = (await control("/topic-maintenance/runs", { operation_key: key, max_batches: 1 })).data;
  const replay = (await control("/topic-maintenance/runs", { operation_key: key, max_batches: 1 })).data;
  assert.equal(command.id, replay.id);
  assert.equal(command.window_id, replay.window_id);
  await expectStatus("/topic-maintenance/runs", { operation_key: key, max_batches: 2 }, 409);
  let status;
  for (let index = 0; index < maintenanceObservationAttempts; index++) {
    status = (await control("/topic-maintenance/status")).data;
    if (status.coverage_complete && status.counts.organized > initial.counts.organized) break;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert.equal(status.discovery_complete, true);
  assert.equal(status.counts.pending, 0);
  assert.equal(status.counts.failed, 0);
  assert(status.counts.organized > initial.counts.organized);
  assert(status.usage.attempts > 0, "provider dispatches must have durable attempt identities");
  assert.equal(status.usage.unreconciled_attempts, 0);
  assert.equal(status.active_work, 0);
  assert.equal(status.coverage_complete, true, "Topic assignment and Community placement must both converge");
  const currentReceipts = JSON.parse(postgresQuery(`SELECT COALESCE(jsonb_agg(row_to_json(retained)),'[]') FROM (
    SELECT DISTINCT ON(evidence_id) evidence_id, status, method, input_hash FROM topic_assignment_outcomes
    WHERE team_id=${sqlLiteral(teamID)}::uuid AND evidence_id=ANY(ARRAY[${sourceIDs.map(sqlLiteral).join(",")}]::uuid[])
    ORDER BY evidence_id,created_at DESC,outcome_id DESC) AS retained`));
  assert.equal(currentReceipts.length, sourceIDs.length);
  assert(currentReceipts.every((row) => row.status === "assigned" && row.input_hash && row.method));
  await pollFinalization(() => Number(postgresQuery(`SELECT count(DISTINCT placed.topic_id) FROM community_placement_outcomes AS placed JOIN topic_record_heads AS head
    ON head.team_id=placed.team_id AND head.shared_space_id=placed.shared_space_id AND head.space_generation=placed.space_generation
      AND head.record_id=placed.topic_id AND head.version=placed.topic_version
    WHERE placed.team_id=${sqlLiteral(teamID)}::uuid AND placed.status IN ('placed','standalone')`)) >= 3, "terminal community placement for all three Topics");
  const placements = JSON.parse(postgresQuery(`SELECT jsonb_agg(jsonb_build_array(child.body->'definition'->>'label',parent.body->'definition'->>'label'))
    FROM community_placement_outcomes AS placement
    JOIN topic_revision_records AS child ON child.team_id=placement.team_id AND child.shared_space_id=placement.shared_space_id AND child.space_generation=placement.space_generation
      AND child.record_id=placement.topic_id AND child.version=placement.topic_version
    JOIN topic_record_heads AS head ON head.team_id=placement.team_id AND head.shared_space_id=placement.shared_space_id AND head.space_generation=placement.space_generation
      AND head.record_id=placement.parent_topic_id AND NOT head.retired
    JOIN topic_revision_records AS parent ON parent.team_id=head.team_id AND parent.shared_space_id=head.shared_space_id AND parent.space_generation=head.space_generation
      AND parent.record_id=head.record_id AND parent.version=head.version
    WHERE placement.team_id=${sqlLiteral(teamID)}::uuid AND placement.status='placed'`) || "[]");
  assert(placements.some(([child,parent]) => child === "storage-backups" && parent === "memory-storage"), "the backup topic must join the broader storage community");

  const runs = (await control("/topic-maintenance/runs?limit=1")).data;
  assert.equal(runs.runs.length, 1);
  assert(runs.next_cursor, "run inspection must paginate");
  const all = (await control("/topic-maintenance/runs?limit=100")).data;
  assert(all.runs.find((run) => run.id === command.id).completed_batches <= 1);
  await control("/topic-maintenance/pause", { operation_key: randomUUID() });
  paused = true;
  const accountingBefore = (await control("/topic-maintenance/status")).data.window;
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
  assert.equal(topicRecall.related_topics.length, 3, "shared Evidence must expose all three direct topics");
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
  const browserParams = new URLSearchParams({ limit: "50" });
  for (const id of topicIDs) browserParams.append("topic_ids", id);
  const browserUnion = await browserRecall(browserParams, reader.api_key);
  assert.deepEqual(browserUnion.results.map((item) => item.evidence_id).sort(), union.results.map((item) => item.evidence_id).sort(), "browser topic union must match MCP selection");
  const knownUnion = [...new Set(union.results.flatMap((item) => [item.evidence_id, ...item.equivalent_evidence_ids]))];
  assert(knownUnion.length > 1, "browser exclusions must exercise multiple Evidence IDs");
  for (const id of knownUnion) browserParams.append("known_evidence_ids", id);
  assert.deepEqual((await browserRecall(browserParams, reader.api_key)).results, [], "browser must bind every known-Evidence exclusion");
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
  const topicManager = (await control(`/teams/${teamID}/credentials`, { name: "Topic revision acceptance", role: "manager", scopes: ["read", "write"], rate_limit: 300 })).data;
  credentialTeams.set(topicManager.api_key, teamID);
  for (const temporalField of ["valid_at", "known_at"]) {
    const temporal = await mcp("recall_memory", { topic_ids: topicIDs, limit: 50, [temporalField]: new Date().toISOString() }, reader.api_key);
    assert(temporal.results.some((item) => sourceIDs.includes(item.evidence_id)), "temporal Evidence must use current direct topic membership");
    assert(temporal.related_topics.length > 0);
  }
  await control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "false" }] }, "PATCH");
  try {
    const unavailable = await rpc("recall_memory", { topic_ids: topicIDs }, reader.api_key);
    assert(unavailable.result?.isError && unavailable.result.structuredContent.reason_code === "topic_unavailable");
    const ordinary = await mcp("recall_memory", { query: "Atlas" }, reader.api_key);
    assert(ordinary.results.length > 0 && ordinary.related_topics.length === 0, "disabled organization must preserve ordinary retrieval");
    assert(!ordinary.degradations.some((item) => item.frontier === "topics"), "intentional disablement is not a topic failure");
  } finally {
    await control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
  }
  const accountingAfter = (await control("/topic-maintenance/status")).data.window;
  assert.equal(accountingAfter.usage.reported_input_tokens, accountingBefore.usage.reported_input_tokens, "Recall added organization provider input tokens");
  assert.equal(accountingAfter.usage.reported_output_tokens, accountingBefore.usage.reported_output_tokens, "Recall added organization provider output tokens");
  await control("/topic-maintenance/pause", { operation_key: randomUUID() });
  paused = true;
  await fixtureMode("ambiguous");
  const unclearID = await remember("Subject unclear in this note.", shared.api_key);
  assert.equal(postgresQuery(`SELECT status FROM topic_assignment_outcomes WHERE evidence_id=${sqlLiteral(unclearID)}::uuid ORDER BY created_at DESC LIMIT 1`), "uncategorized");
  await fixtureMode("fail-three-then-success");
  const baseline = canonicalFingerprints();
  const failed = await rpcAttempt("remember", { target_space:"team_shared", idempotency_key:randomUUID(), evidence:[{content:"Atlas PostgreSQL foreground assignment failure.",source_type:"document",force_insert:true}] }, shared.api_key);
  const terminal = JSON.parse(failed.result.content[0].text);
  assert.equal(terminal.processing_state, "failed");
  assert(terminal.errors.some((error) => error.code === "provider_response_invalid"));
  assert.equal(canonicalFingerprints(), baseline, "a required foreground assignment failure must roll back canonical intake");
  await fixtureMode("normal");
  await finalizationRecovery(shared.api_key);
  const staleAssignment = await staleAssignmentRevision(shared.api_key, topicManager.api_key);
  const topicWindow = captureSemanticAssessmentWindow({ project: required("DENSE_MEM_E2E_COMPOSE_PROJECT"), composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since: diagnosticSince, directory: failureDirectory+"/topic-selection" });
  const topicRecords=topicWindow.split(/\r?\n/).filter(Boolean).map((line)=>JSON.parse(line)).filter((row)=>row.team_id===teamID);
  for (const credential of [controlToken, ...credentialTeams.keys()]) assert(!topicWindow.includes(credential),"topic diagnostic capture exposed credentials");
  verifyTopicProcessLogs(topicRecords, staleAssignment);
  const topicChunks = new Map();
  for (const row of topicRecords.filter((row) => ["recall_topic_filter_payload", "recall_topic_selection_payload"].includes(row.msg))) {
    assert(row.diagnostic_capture_id, "topic payload must identify its summary");
    const chunks = topicChunks.get(row.diagnostic_capture_id) ?? [];
    chunks.push(row);
    topicChunks.set(row.diagnostic_capture_id, chunks);
  }
  const topicPayloads = new Map([...topicChunks].map(([id, chunks]) => {
    chunks.sort((left, right) => left.payload_chunk.index - right.payload_chunk.index);
    return [id, reconstructDiagnosticBody(chunks)];
  }));
  const selectedLog = topicRecords.find((row) => row.msg === "recall_topic_selection" && row.outcome === "selected" && row.recall_id === topicRecall.recall_id);
  assert(selectedLog && selectedLog.level === "DEBUG", "topic selection must be correlated to its Recall");
  const selection = topicPayloads.get(selectedLog.diagnostic_capture_id);
  assert(selection?.Diagnostics.generation > 0 && selection.Diagnostics.memberships.length > 0 && selection.Topics.length > 0, "current memberships/counts/generation must be diagnosable from stdout");
  assert.deepEqual(selection.Topics, topicRecall.related_topics);
  for (const topic of selection.Topics) {
    const counts = selection.Diagnostics.counts[topic.topic_id];
    assert.equal(counts.eligible_evidence_count, topic.eligible_evidence_count);
    assert(counts.matching_result_count > 0);
    assert.equal(counts.matching_result_count + counts.additional_evidence_count, topic.eligible_evidence_count);
  }
  assert(!selection.Diagnostics.eligible_ids.some((id) => [privateID, foreignID].includes(id)), "topic diagnostic eligibility must retain isolation");
  for (const id of unavailableTopicIDs) {
    const rejectedLog = topicRecords.find((row) => row.msg === "recall_topic_degraded" && row.requested_ids?.includes(id) && topicPayloads.get(row.diagnostic_capture_id)?.Rejected?.[id] === "unavailable");
    assert(rejectedLog && rejectedLog.level === "WARN" && rejectedLog.reason, "missing and foreign topic reasons must be diagnosable from stdout");
  }
  console.log(JSON.stringify({ status: "ok", scenario: "ontology_organization", window_id: status.window.id, counts: status.counts, durable_command_replay: true, operator_isolation: true, grouped_recall: true, original_sources_retained: true, private_and_foreign_isolation: true, foreground_multi_membership: true, community_parent_verified: true, recall_organization_provider_tokens: 0, foreground_abstention: true, atomic_retry: true }));
} catch (error) {
  failure = error;
  try {
    const window = captureSemanticAssessmentWindow({ project: required("DENSE_MEM_E2E_COMPOSE_PROJECT"), composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since: diagnosticSince,
      directory: `${failureDirectory}/ontology-organization` });
    const diagnosticTeams = new Set(credentialTeams.values());
    for (const record of assessmentRecords(window).filter((record) => diagnosticTeams.has(record.team_id))) console.log(`topic_semantic_diagnostic_record ${JSON.stringify(record)}`);
  } catch (captureError) { failure = new AggregateError([error, captureError], "ontology organization failure capture is incomplete"); }
} finally {
  const failures = failure ? [failure] : [];
  for (const restore of [
    () => fixtureMode("normal"),
    async () => { if (paused) await control("/topic-maintenance/resume", { operation_key: randomUUID() }); },
    () => control("/config/topic-maintenance", { items: original.items.map(({ key, value }) => ({ key, value })) }, "PATCH"),
  ]) {
    try { await restore(); } catch (error) { failures.push(error); }
  }
  if (failures.length === 1) throw failures[0];
  if (failures.length > 1) throw new AggregateError(failures, "ontology UAT and restoration failed", { cause: failure });
}

function verifyTopicProcessLogs(records, stale) {
  const prepared = records.find((row) => row.msg === "topic_assignment.prepared" && row.matched_topics > 0 && row.proposed_topics > 0);
  assert(prepared?.assessment_id && prepared.operation_id && prepared.correlation_id, "mixed Topic assignment needs an assessment and Remember correlation");
  const shortlist = records.find((row) => row.msg === "topic_assignment.candidates" && row.assessment_id === prepared.assessment_id);
  assert(shortlist?.candidates.length && shortlist.operation_id === prepared.operation_id && shortlist.correlation_id === prepared.correlation_id);
  const successful = records.find((row) => row.msg === "structured_ai.completed" && row.assessment_id === prepared.assessment_id && row.capability === "topic_assignment");
  assert(successful && successful.outcome === "validated");
  const request = topicProviderExchange(records, successful, "request");
  const response = topicProviderExchange(records, successful, "response");
  assert(request.messages.some((message) => message.content.includes("encrypts its backups")));
  const assignment = JSON.parse(response.choices[0].message.content);
  assert(assignment.matches.length && assignment.proposals.length);

  const exhausted = records.find((row) => row.msg === "structured_ai.validation.exhausted" && row.capability === "topic_assignment");
  assert(exhausted?.assessment_id && exhausted.validation_stage && exhausted.issues?.length);
  const failed = records.find((row) => row.msg === "structured_ai.failed" && row.assessment_id === exhausted.assessment_id);
  assert.equal(failed?.attempt_count, 3);
  assert(failed.attempts.every((attempt) => attempt.exchange_id && attempt.transport?.queue_ms >= 0 && attempt.transport?.http_ms >= 0));
  topicProviderExchange(records, exhausted, "response");

  const placement = records.find((row) => row.msg === "community_placement.outcome" && row.status === "placed");
  assert(placement?.job_id && placement.run_id && placement.topic_version > 0 && placement.parent_topic_id);
  const selected = records.find((row) => row.msg === "community_placement.candidates" && row.job_id === placement.job_id);
  assert(selected?.candidates.some((ref) => ref.startsWith(placement.parent_topic_id + "@")));
  const completed = records.find((row) => row.msg === "structured_ai.completed" && row.batch_id === placement.job_id);
  assert(completed && completed.run_id === placement.run_id);
  const placed = topicProviderExchange(records, completed, "response");
  assert.equal(JSON.parse(placed.choices[0].message.content).parent_id, placement.parent_topic_id);

  const staleLogs = records.filter((row) => row.correlation_id === stale.correlationID);
  const oldShortlist = staleLogs.find((row) => row.msg === "topic_assignment.candidates" && row.candidates?.includes(stale.topicRef));
  const staleAssessment = staleLogs.find((row) => row.msg === "structured_ai.completed" && row.assessment_id === oldShortlist?.assessment_id);
  const trace = staleLogs.find((row) => row.msg === "structured_ai.attempt.completed" && row.exchange_id === staleAssessment?.exchange_id);
  const rejected = staleLogs.find((row) => row.msg === "remember_invocation_completed");
  assert(rejected?.level === "ERROR" && rejected.phase === "commit" && rejected.error_code === "stale_input");
  assert(oldShortlist?.operation_id === rejected.invocation_id && staleAssessment?.outcome === "validated" && trace?.level === "TRACE");
  const staleReply = topicProviderExchange(staleLogs, staleAssessment, "response");
  assert(JSON.parse(staleReply.choices[0].message.content).matches.some((match) => match.id === stale.topicID));
}

function topicProviderExchange(records, stage, direction) {
  const chunks = records.filter((row) => row.msg === "ai_exchange_payload" && row.exchange_id === stage.exchange_id
    && row.assessment_id === stage.assessment_id && row.direction === direction && row.payload_chunk);
  assert(chunks.every((row) => row.transport_capture_state === "captured"));
  return reconstructDiagnosticBody(chunks.sort((left, right) => left.payload_chunk.index - right.payload_chunk.index));
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

async function organization(path, key, body) {
  const response = await fetch(`${userURL}/ui/api/topics${path}`, { method: body ? "POST" : "GET", headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
  assert(response.ok, `organization ${path} returned ${response.status}: ${(await response.clone().text()).slice(0, 500)}`);
  return (await response.json()).data;
}

async function browserRecall(params, key) {
  const response = await fetch(`${userURL}/ui/api/recall?${params}`, { headers: { Authorization: `Bearer ${key}` } });
  assert(response.ok, `browser Recall returned ${response.status}: ${(await response.clone().text()).slice(0, 500)}`);
  return (await response.json()).data;
}

async function rpc(name, args, key) {
  if (name === "remember") return rememberWithEmbeddingRetry(args, (request) => rpcAttempt(name, request, key));
  return rpcAttempt(name, args, key);
}
async function rpcAttempt(name, args, key) {
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
    await control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "false" }] }, "PATCH");
    const ordinary = await mcp("recall_memory", request, readerKey);
    assert.equal(ordinary.results[0]?.evidence_id, ordinaryID);
    await control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
    const unrelatedMembership = await mcp("recall_memory", request, readerKey);
    assert.equal(unrelatedMembership.results[0]?.evidence_id, ordinaryID, "ontology membership must not promote a less relevant vector candidate");
    rankFirst(ontologyIDs[0]);
    const relevantDiscovery = await mcp("recall_memory", request, readerKey);
    assert(ontologyIDs.includes(relevantDiscovery.results[0]?.evidence_id), "query-relevant ontology evidence must retain its ranked place");
    console.log(JSON.stringify({ scenario: "ontology_query_scoring", membership_adds_no_vote: true, relevant_discovery_ranked_first: true }));
  } finally {
    postgresQuery(`UPDATE search_documents AS document SET embedding=original.embedding::vector FROM (VALUES ${rows.map((row) => `(${sqlLiteral(row.id)}::uuid,${sqlLiteral(row.embedding)})`).join(",")}) AS original(id,embedding) WHERE document.search_document_id=original.id`);
    await control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
  }
}

function postgresQuery(sql) {
  const scopedSQL = ["BEGIN", "SET LOCAL app.tx_mode='system'", "SET LOCAL app.current_team_id=''", "SET LOCAL app.current_profile_id=''", "SET LOCAL app.allowed_space_ids=''", sql, "COMMIT"].join(";\n");
  const result = spawnSync("docker", ["compose", "-p", required("DENSE_MEM_E2E_COMPOSE_PROJECT"), "-f", required("DENSE_MEM_E2E_COMPOSE_FILE"), "exec", "-T", "-e", "PGOPTIONS=-c app.storage_layout=feature-storage-v1 -c app.topic_layout=topic-storage-v1", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "ontology-evidence-e2e", scopedSQL], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  return result.stdout.trim();
}
function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }

async function fixtureMode(mode) {
  const response = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/ontology-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) });
  assert.equal(response.status, 200);
}

async function staleAssignmentRevision(writer, manager) {
  const { records } = await organization("/records?kind=topic&limit=200", manager);
  const topic = records.find((record) => record.definition?.label === "memory-storage");
  assert(topic, "a revision fence requires an existing Topic");
  const before = canonicalFingerprints();
  const reason = "Update storage vocabulary during an in-flight assignment";
  await fixtureMode("hold");
  const [intake, edited] = await Promise.allSettled([
    rpcAttempt("remember", { target_space: "team_shared", idempotency_key: randomUUID(), evidence: [
      { content: "PostgreSQL writes a new audit checkpoint.", source_type: "document", force_insert: true },
    ] }, writer),
    (async () => {
      try {
        await pollFinalization(async () => {
          const health = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/health`);
          assert.equal(health.status, 200);
          return (await health.json()).active_ontology_requests === 1;
        }, "foreground Topic provider hold", 12);
        await organization("/edits", manager, {
          operation_key: randomUUID(), reason, edits: [{ action: "define", kind: "topic", record_id: topic.id,
            expected_version: topic.edit_version, definition: { ...topic.definition, description: reason } }],
        });
      } finally {
        await fixtureMode("normal");
      }
    })(),
  ]);
  if (edited.status === "rejected") throw edited.reason;
  if (intake.status === "rejected") throw intake.reason;
  const receipt = JSON.parse(intake.value.result.content[0].text);
  assert.equal(receipt.processing_state, "failed");
  assert.equal(receipt.errors[0].code, "stale_input");
  assert.equal(canonicalFingerprints(), before, "stale Topic input must roll back the full Evidence write");
  return { correlationID: receipt.correlation_id, topicID: topic.id, topicRef: `${topic.id}@${topic.version}` };
}

async function finalizationRecovery(key) {
  const configure = (value) => control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value }] }, "PATCH");
  await configure("false");
  const id = await remember(`PostgreSQL restore processing ${randomUUID()}.`, key);
  const filter = `team_id=${sqlLiteral(teamID)}::uuid AND evidence_id=${sqlLiteral(id)}::uuid`;
  const assigned = () => Number(postgresQuery(`SELECT count(*) FROM topic_assignment_outcomes WHERE ${filter} AND status='assigned'`));
  const memberships = () => Number(postgresQuery(`SELECT count(*) FROM topic_record_heads WHERE ${filter} AND kind='assignment' AND NOT retired`));
  assert.equal(assigned(), 0);
  await configure("true");
  try {
    postgresQuery(`CREATE FUNCTION topic_e2e_finalization_failure() RETURNS trigger LANGUAGE plpgsql AS $$
      BEGIN IF NEW.team_id=${sqlLiteral(teamID)}::uuid AND NEW.source_id=${sqlLiteral(id)} AND NEW.status='organized' THEN
        RAISE EXCEPTION 'fixture finalization rejection'; END IF; RETURN NEW; END $$;
      CREATE TRIGGER topic_e2e_finalization_failure BEFORE UPDATE OF status ON topic_maintenance_sources FOR EACH ROW EXECUTE FUNCTION topic_e2e_finalization_failure()`);
    await control("/topic-maintenance/resume", { operation_key: randomUUID() });
    paused = false;
    await control("/topic-maintenance/runs", { operation_key: randomUUID(), max_batches: 100 });
    let failedRun;
    await pollFinalization(() => {
      failedRun = postgresQuery(`SELECT work.last_run_id::text FROM topic_maintenance_sources AS work
        JOIN topic_maintenance_runs AS run ON run.run_id=work.last_run_id
        WHERE work.team_id=${sqlLiteral(teamID)}::uuid AND work.source_kind='evidence' AND work.source_id=${sqlLiteral(id)}
          AND work.eligible AND work.status='failed' AND run.failure_code<>''`);
      return Boolean(failedRun);
    }, "retryable failed finalization for the imported evidence");
    assert.equal(assigned(), 0, "failed finalization must roll back its assignment outcome");
    assert.equal(memberships(), 0, "failed finalization must roll back direct membership");
    postgresQuery("DROP TRIGGER topic_e2e_finalization_failure ON topic_maintenance_sources");
    await control(`/topic-maintenance/runs/${failedRun}/retry`, { operation_key: randomUUID(), max_batches: 100 });
    await pollFinalization(() => assigned() > 0, "retried assignment publication");
    assert.equal(assigned(), 1, "one successful publication must survive retry");
    assert.equal(memberships(), 1);
  } finally {
    postgresQuery("DROP TRIGGER IF EXISTS topic_e2e_finalization_failure ON topic_maintenance_sources; DROP FUNCTION IF EXISTS topic_e2e_finalization_failure()");
    await control("/topic-maintenance/pause", { operation_key: randomUUID() });
    paused = true;
  }
}

async function pollFinalization(check, label, seconds = maintenanceObservationAttempts) {
  for (let index = 0; index < seconds; index++) {
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert.fail(`timed out waiting for ${label}`);
}

function canonicalFingerprints(scopeTeam = teamID) {
  const tables = ["evidence_fragments", "evidence_occurrences", "evidence_sources", "evidence_source_revisions", "evidence_lifecycle_events", "evidence_quarantines"];
  return postgresQuery(tables.map((table) => `SELECT '${table}',md5(COALESCE(jsonb_agg(to_jsonb(record) ORDER BY to_jsonb(record)::text)::text,'[]')) FROM ${table} AS record WHERE team_id=${sqlLiteral(scopeTeam)}::uuid`).join(";\n"));
}
