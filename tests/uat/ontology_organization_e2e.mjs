import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const apiKey = required("DENSE_MEM_E2E_API_KEY");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const original = (await control("/config/ontology-maintenance")).data;
const summaryCallsBefore = await summaryCalls();
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
  const texts = ["Atlas stores PostgreSQL data.", "Atlas uses PostgreSQL for data storage.", "Atlas stores PostgreSQL data and encrypts its backups."];
  const sourceIDs = [];
  const relationshipIDs = [];
  for (const [index, content] of texts.entries()) {
    sourceIDs.push(await remember(content, index === 1 ? second.api_key : shared.api_key, relationshipIDs));
  }
  assert.equal(new Set(sourceIDs).size, 3, "force-inserted paraphrases must retain distinct source handles");
  const privateID = await remember(texts[0], privateCredential.api_key, [], "private");
  const foreignID = await remember(texts[0], foreignCredential.api_key);
  const coordinationID = await remember("Atlas uses Redis for coordination.", shared.api_key);
  const coordinationAlternativeID = await remember("Atlas uses KeyDB for coordination.", shared.api_key);
  await remember("Atlas uses SQLite for backup storage.", shared.api_key, relationshipIDs);
  await remember("Atlas uses SQLite for backup storage.", second.api_key, relationshipIDs);
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
  let projectionCoverage;
  for (let index = 0; index < 150; index++) {
    status = (await control("/ontology/status")).data;
    if (status.discovery_complete && status.counts.pending === 0 && status.counts.organized > initial.counts.organized) {
      projectionCoverage = (await control(`/teams/${teamID}/community/status`)).data.projection_coverage;
      if (projectionCoverage?.coverage_complete && projectionCoverage.current_topics >= 2) break;
    }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert.equal(status.discovery_complete, true);
  assert.equal(status.counts.pending, 0);
  assert.equal(status.counts.failed, 0);
  assert(status.counts.organized > initial.counts.organized);
  assert(projectionCoverage?.coverage_complete && projectionCoverage.current_topics >= 2, "alias discovery must exercise current topic projections");
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
  const args = { query: "Atlas PostgreSQL", limit: 20, relationship_limit: 0, community_limit: 0 };
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
  // One evidence slot leaves one uses group uncovered even with current projections.
  const relationshipDiscovery = await mcp("recall_memory", { ...args, query: "database platform", limit: 1, relationship_limit: 20 }, reader.api_key);
  const displayedRelationshipIDs = relationshipDiscovery.related_relationships.flatMap((item) => [item.relationship_id, ...item.equivalent_relationship_ids]);
  assert(relationshipIDs.some((id) => displayedRelationshipIDs.includes(id)), `published alias must discover existing Relationship handles: ${JSON.stringify({ expected_relationship_ids: relationshipIDs, returned_relationship_ids: displayedRelationshipIDs.slice(0, 20), degradations: relationshipDiscovery.degradations?.map(({ frontier, code }) => ({ frontier, code })), maintenance_counts: status.counts })}`);
  const usesSlots = relationshipDiscovery.related_relationships.filter((item) => item.predicate === "uses" && relationshipIDs.includes(item.relationship_id));
  assert.equal(usesSlots.length, 1, "existing semantic grouping must retain one uses slot across owners");
  assert.equal(usesSlots[0].equivalent_relationship_ids.length, 1, "the uncovered uses group must retain both owners' Relationship handles");
  for (const temporalField of ["valid_at", "known_at"]) {
    const temporal = await mcp("recall_memory", { ...args, [temporalField]: new Date().toISOString() }, reader.api_key);
    assert.equal(temporal.results.filter((item) => sourceIDs.includes(item.evidence_id)).length, 3);
    assert(temporal.degradations.some((item) => item.code === "ontology_temporal_not_supported"));
  }
  const combinedFallback = await mcp("recall_memory", { ...args, query: "Atlas PostgreSQL [fixture-fault:embedding-malformed]", relationship_limit: 20, known_at: new Date().toISOString() }, reader.api_key);
  assert(combinedFallback.degradations.some((item) => item.frontier === "evidence" && item.code === "provider_unavailable"), "the provider fixture must make the query vector unavailable");
  assert(combinedFallback.degradations.some((item) => item.frontier === "relationships" && item.code === "relationship_vector_warming"), "relationship recall must report its vector fallback");
  assert(combinedFallback.degradations.some((item) => item.frontier === "relationships" && item.code === "ontology_temporal_not_supported"), "the vector fallback must retain the independent ontology omission");
  const accountingAfterOntologyRecall = (await control("/ontology/status")).data.window;
  assert.equal(accountingAfterOntologyRecall.usage.reported_input_tokens, accountingBefore.usage.reported_input_tokens, "Recall added organization provider input tokens");
  assert.equal(accountingAfterOntologyRecall.usage.reported_output_tokens, accountingBefore.usage.reported_output_tokens, "Recall added organization provider output tokens");
  const historicalRunID = randomUUID();
  postgresQuery(`INSERT INTO community_snapshot_runs(team_id,space_id,space_generation,run_id,window_key,algorithm_kind,status,error)
    VALUES(${sqlLiteral(teamID)}::uuid,dense_mem_team_shared_space(${sqlLiteral(teamID)}::uuid),dense_mem_team_shared_generation(${sqlLiteral(teamID)}::uuid),
    ${sqlLiteral(historicalRunID)}::uuid,${sqlLiteral(`historical-status-${historicalRunID}`)},'louvain','failed','retained historical failure')`);
  await control("/ontology/resume", { operation_key: randomUUID() });
  paused = false;
  let communityStatus;
  for (let index = 0; index < 150; index++) {
    communityStatus = (await control(`/teams/${teamID}/community/status`)).data;
    if (communityStatus.projection_coverage?.current_topics >= 2 && communityStatus.projection_coverage.coverage_complete) break;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert(communityStatus.projection_coverage.current_topics >= 2, JSON.stringify(communityStatus));
  assert(!communityStatus.latest_run, "enabled ontology status must not present a historical failed legacy run as current work");
  await control("/ontology/pause", { operation_key: randomUUID() });
  paused = true;
  const accountingBeforeCommunityRecall = (await control("/ontology/status")).data.window;
  const topicArgs = { query: "Synthetic organization vocabulary", limit: 1, relationship_limit: 0, community_limit: 10, community_relationship_limit: 1 };
  const topicRecall = await mcp("recall_memory", topicArgs, reader.api_key);
  const completeTopicRecall = await mcp("recall_memory", { ...topicArgs, community_relationship_limit: 20 }, reader.api_key);
  assert.equal(topicRecall.related_communities.length, 2);
  assert(topicRecall.related_communities.some((topic) => topic.relationships_truncated), "fixture must exercise preview truncation");
  const healthyTopic = topicRecall.related_communities.find((item) => item.summary.includes("memory coordination"));
  assert(healthyTopic, "coordination topic was not independently published");
  assert(healthyTopic.relationships.some((item) => item.evidence_ids.some((id) => [coordinationID, coordinationAlternativeID].includes(id))));
  for (const topic of topicRecall.related_communities) {
    const completeTopic = completeTopicRecall.related_communities.find((item) => item.community_id === topic.community_id);
    assert(completeTopic);
    assert.equal(completeTopic.relationships_truncated, false);
    assert.equal(topic.relationship_count, completeTopic.relationship_count);
    assert(topic.relationships.length <= 1);
    assert.deepEqual(topic.relationships, completeTopic.relationships.slice(0, 1));
    assert.equal(topic.relationships_truncated, completeTopic.relationships.length > topic.relationships.length);
    for (const relationship of topic.relationships) {
      assert(!relationship.evidence_ids.includes(privateID));
      assert(!relationship.evidence_ids.includes(foreignID));
    }
  }
  const pausedTopics = await mcp("recall_memory", topicArgs, reader.api_key);
  assert.deepEqual(pausedTopics.related_communities.map((item) => item.community_id).sort(), topicRecall.related_communities.map((item) => item.community_id).sort());
  const failedTopic = topicRecall.related_communities.find((item) => item.community_id !== healthyTopic.community_id);
  assert(failedTopic, "failure fixture requires a second independently published topic");
  postgresQuery(`UPDATE community_topic_work SET status='failed',failure_code='fixture_projection_failed'
    WHERE team_id=${sqlLiteral(teamID)}::uuid AND topic_id=${sqlLiteral(failedTopic.logical_community_id)}::uuid;
    UPDATE community_records SET status='stale',stale_reason='fixture_projection_failed'
    WHERE team_id=${sqlLiteral(teamID)}::uuid AND community_id=${sqlLiteral(failedTopic.community_id)}::uuid`);
  const failedTopics = await mcp("recall_memory", topicArgs, reader.api_key);
  assert.equal(failedTopics.related_communities.length, 1);
  assert.equal(failedTopics.related_communities[0].community_id, healthyTopic.community_id);
  assert(failedTopics.degradations.some((item) => item.code === "community_projection_partial"));
  const failureStatus = (await control(`/teams/${teamID}/community/status`)).data.projection_coverage;
  assert.equal(failureStatus.failed_topics, 1);
  assert.equal(failureStatus.current_topics, 1);
  postgresQuery(`UPDATE community_topic_work SET status='current',failure_code=''
    WHERE team_id=${sqlLiteral(teamID)}::uuid AND topic_id=${sqlLiteral(failedTopic.logical_community_id)}::uuid;
    UPDATE community_records SET status='current',stale_reason=''
    WHERE team_id=${sqlLiteral(teamID)}::uuid AND community_id=${sqlLiteral(failedTopic.community_id)}::uuid`);
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] }, "PATCH");
  const disabledStatus = (await control(`/teams/${teamID}/community/status`)).data;
  assert.equal(disabledStatus.latest_run.run_id, historicalRunID);
  assert.equal(disabledStatus.latest_run.status, "failed");
  assert.equal(disabledStatus.latest_run.error, "community run failed");
  const noCommunity = await mcp("recall_memory", topicArgs, reader.api_key);
  assert.equal(noCommunity.related_communities.length, 0, "disabled ontology must not expose historical communities");
  assert(!Object.hasOwn(noCommunity, "related_hypotheses"));
  const ordinary = await mcp("recall_memory", args, reader.api_key);
  assert.equal(ordinary.results.filter((item) => sourceIDs.includes(item.evidence_id)).length, 3);
  assert(ordinary.results.every((item) => item.equivalent_evidence_ids.length === 0 && !item.equivalents_truncated));
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }, { key: "ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" }] }, "PATCH");
  const deniedRetraction = await rpc("retract_evidence", { evidence_ids: [sourceIDs[0]], reason: "wrong owner regression", idempotency_key: randomUUID() }, reader.api_key);
  assert(deniedRetraction.error || deniedRetraction.result?.isError, "reader C must not mutate owner A's source");
  const retracted = await mcp("retract_evidence", { evidence_ids: [sourceIDs[0]], reason: "withdraw grouping source", idempotency_key: randomUUID() }, shared.api_key);
  assert(retracted.retracted_evidence_ids.includes(sourceIDs[0]));
  const stale = await mcp("recall_memory", args, reader.api_key);
  assert(!stale.results.some((item) => item.evidence_id === sourceIDs[0] || item.equivalent_evidence_ids.includes(sourceIDs[0])));
  assert(stale.results.some((item) => item.evidence_id === sourceIDs[1] && item.equivalent_evidence_ids.length === 0));
  assert(stale.degradations.some((item) => item.code === "ontology_stale"));
  const partialTopics = await mcp("recall_memory", topicArgs, reader.api_key);
  assert.equal(partialTopics.related_communities.length, 1, "stale topic must be omitted without hiding the healthy topic");
  assert.equal(partialTopics.related_communities[0].community_id, healthyTopic.community_id);
  assert(partialTopics.degradations.some((item) => item.code === "community_projection_partial"));
  const temporalTopics = await mcp("recall_memory", { ...topicArgs, query: "Atlas Redis", relationship_limit: 20, known_at: new Date().toISOString() }, reader.api_key);
  assert.equal(temporalTopics.related_communities.length, 0);
  assert(temporalTopics.results.some((item) => item.evidence_id === coordinationID), "temporal retrieval must retain the original coordination evidence");
  assert(temporalTopics.degradations.some((item) => item.code === "community_temporal_not_supported"));
  assert.equal(await summaryCalls(), summaryCallsBefore, "topic projection or Recall added summary-model calls");
  const staleRelationships = await mcp("recall_memory", { ...args, query: "database platform", relationship_limit: 20 }, reader.api_key);
  for (const item of staleRelationships.related_relationships) {
    assert(!item.evidence_ids.includes(sourceIDs[0]));
    assert(!item.evidence_ids.includes(privateID));
    assert(!item.evidence_ids.includes(foreignID));
  }
  const accountingAfter = (await control("/ontology/status")).data.window;
  assert.equal(accountingAfter.usage.reported_input_tokens, accountingBeforeCommunityRecall.usage.reported_input_tokens, "Recall added organization provider input tokens");
  assert.equal(accountingAfter.usage.reported_output_tokens, accountingBeforeCommunityRecall.usage.reported_output_tokens, "Recall added organization provider output tokens");
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
  console.log(JSON.stringify({ status: "ok", scenario: "ontology_organization", window_id: status.window.id, counts: status.counts, durable_command_replay: true, operator_isolation: true, grouped_recall: true, original_sources_retained: true, private_and_foreign_isolation: true, stale_group_fallback: true, recall_organization_provider_tokens: 0, bounded_ambiguity_run: ambiguousRun.id, retryable_failure_run: failedRun.id }));
} catch (error) {
  failure = error;
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
  return response.json();
}
async function mcp(name, args, key) {
  const result = await rpc(name, args, key);
  assert(!result.error && result.result?.isError !== true, `${name} failed: ${JSON.stringify(result.error ?? result.result)}`);
  return JSON.parse(result.result.content[0].text);
}
async function remember(content, key, relationshipIDs = [], targetSpace = "team_shared") {
  const relationships = [{ ref: "storage", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: content.includes("Redis") ? "Redis" : content.includes("KeyDB") ? "KeyDB" : content.includes("SQLite") ? "SQLite" : "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }];
  if (content.includes("encrypts")) relationships.push({ ref: "backups", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "encrypts" }, object: { entity: { name: "backups", entity_kind: "document" } }, polarity: "+", evidence_indices: [0] });
  const result = await mcp("remember", { target_space: targetSpace, idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-e2e", source_group: "ontology-e2e", force_insert: true }], relationships }, key);
  assert.equal(result.processing_state, "completed");
  relationshipIDs.push(...result.relationship_results.flatMap((item) => item.splits.map((split) => split.relationship_id)));
  const evidence = result.evidence.find((item) => item.disposition === "stored");
  assert(evidence?.evidence_id, "Remember did not retain the source evidence");
  return evidence.evidence_id;
}

async function summaryCalls() {
  const response = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/health`);
  assert.equal(response.status, 200);
  return ((await response.json()).chat_requests || []).filter((item) => item.schema_name === "community_summary").length;
}

function postgresQuery(sql) {
  const scopedSQL = ["BEGIN", "SET LOCAL app.tx_mode='system'", "SET LOCAL app.current_team_id=''", "SET LOCAL app.current_profile_id=''", "SET LOCAL app.allowed_space_ids=''", sql, "COMMIT"].join(";\n");
  const result = spawnSync("docker", ["compose", "-p", required("DENSE_MEM_E2E_COMPOSE_PROJECT"), "-f", required("DENSE_MEM_E2E_COMPOSE_FILE"), "exec", "-T", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "ontology-community-e2e", scopedSQL], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  return result.stdout.trim();
}
function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }

async function fixtureMode(mode) {
  const response = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/ontology-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) });
  assert.equal(response.status, 200);
}
async function rememberFixture(key, content) {
  const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "remember", arguments: { target_space: "team_shared", idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-followups", source_group: randomUUID() }], relationships: [{ ref: "storage", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }] } } }) });
  assert.equal(response.status, 200);
  const rpc = await response.json();
  assert.notEqual(rpc.result?.isError, true, JSON.stringify(rpc.error ?? rpc.result));
  assert.equal(JSON.parse(rpc.result.content[0].text).processing_state, "completed");
}
async function terminalRun(id) {
  for (let index = 0; index < 150; index++) {
    const page = (await control("/ontology/runs?limit=200")).data;
    const run = page.runs.find((run) => run.id === id);
    if (run && ["completed", "incomplete"].includes(run.status)) return run;
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

async function pollFinalization(check, label, seconds = 150) {
  for (let index = 0; index < seconds; index++) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert.fail(`timed out waiting for ${label}`);
}

function canonicalFingerprints() {
  const tables = ["evidence_fragments", "evidence_occurrences", "evidence_sources", "evidence_source_revisions", "entity_records", "entity_names", "value_records", "relationship_records", "relationship_evidence_supports", "relationship_support_decision_events", "evidence_lifecycle_events", "evidence_quarantines"];
  return postgresQuery(tables.map((table) => `SELECT '${table}',md5(COALESCE(jsonb_agg(to_jsonb(record) ORDER BY to_jsonb(record)::text)::text,'[]')) FROM ${table} AS record WHERE team_id=${sqlLiteral(teamID)}::uuid`).join(";\n"));
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
