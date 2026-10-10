import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const apiKey = required("DENSE_MEM_E2E_API_KEY");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const original = (await control("/config/ontology-maintenance")).data;
let paused = false;
try {
  const initial = (await control("/ontology/status")).data;
  await control("/ontology/pause", { operation_key: randomUUID() });
  paused = true;
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
  assert.equal((await control("/ontology/status")).data.paused, true);
  const shared = (await control(`/teams/${teamID}/credentials`, { name: "Ontology shared acceptance", scopes: ["read", "write"], rate_limit: 300, memory_binding: "shared_only" })).data;
  assert.equal(shared.credential.memory_binding, "shared_only");
  const second = (await control(`/teams/${teamID}/credentials`, { name: "Ontology owner B", scopes: ["read", "write"], rate_limit: 300, memory_binding: "shared_only" })).data;
  const reader = (await control(`/teams/${teamID}/credentials`, { name: "Ontology reader C", scopes: ["read", "write"], rate_limit: 300, memory_binding: "shared_only" })).data;
  const privateCredential = (await control(`/teams/${teamID}/credentials`, { name: "Ontology private source", scopes: ["read", "write"], rate_limit: 300, memory_binding: "credential_private" })).data;
  const foreignTeam = (await control("/teams", { name: `ontology-foreign-${randomUUID()}` })).data;
  const foreignCredential = (await control(`/teams/${foreignTeam.id}/credentials`, { name: "Ontology foreign source", scopes: ["read", "write"], rate_limit: 300, memory_binding: "shared_only" })).data;
  const texts = ["Atlas stores PostgreSQL data.", "Atlas uses PostgreSQL for data storage.", "Atlas stores PostgreSQL data and encrypts its backups."];
  const sourceIDs = [];
  const relationshipIDs = [];
  for (const [index, content] of texts.entries()) {
    sourceIDs.push(await remember(content, index === 1 ? second.api_key : shared.api_key, relationshipIDs));
  }
  assert.equal(new Set(sourceIDs).size, 3, "force-inserted paraphrases must retain distinct source handles");
  const privateID = await remember(texts[0], privateCredential.api_key);
  const foreignID = await remember(texts[0], foreignCredential.api_key);
  await expectStatus("/ontology/runs", { operation_key: randomUUID(), max_batches: 101 }, 422);
  await expectStatus("/ontology/runs", { operation_key: randomUUID(), team_id: "untrusted" }, 422);
  await expectStatus("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }, { key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] }, 422, "PATCH");
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
  for (let index = 0; index < 150; index++) {
    status = (await control("/ontology/status")).data;
    if (status.discovery_complete && status.counts.pending === 0 && status.counts.organized > initial.counts.organized) break;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  assert.equal(status.discovery_complete, true);
  assert.equal(status.counts.pending, 0);
  assert.equal(status.counts.failed, 0);
  assert.equal(status.counts.budget_deferred, 0);
  assert(status.counts.organized > initial.counts.organized);
  assert(status.window.charged_input_tokens > 0, "assessment attempts must debit the global window");
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
  const relationshipDiscovery = await mcp("recall_memory", { ...args, query: "database platform", relationship_limit: 20 }, reader.api_key);
  const displayedRelationshipIDs = relationshipDiscovery.related_relationships.flatMap((item) => [item.relationship_id, ...item.equivalent_relationship_ids]);
  assert(relationshipIDs.some((id) => displayedRelationshipIDs.includes(id)), "published alias must discover existing Relationship handles");
  const usesSlots = relationshipDiscovery.related_relationships.filter((item) => item.predicate === "uses" && relationshipIDs.includes(item.relationship_id));
  assert.equal(usesSlots.length, 1, "existing semantic grouping must retain one uses slot across owners");
  for (const temporalField of ["valid_at", "known_at"]) {
    const temporal = await mcp("recall_memory", { ...args, [temporalField]: new Date().toISOString() }, reader.api_key);
    assert.equal(temporal.results.filter((item) => sourceIDs.includes(item.evidence_id)).length, 3);
    assert(temporal.degradations.some((item) => item.code === "ontology_temporal_not_supported"));
  }
  const combinedFallback = await mcp("recall_memory", { ...args, query: "Atlas PostgreSQL [fixture-fault:embedding-malformed]", relationship_limit: 20, known_at: new Date().toISOString() }, reader.api_key);
  assert(combinedFallback.degradations.some((item) => item.frontier === "evidence" && item.code === "provider_unavailable"), "the provider fixture must make the query vector unavailable");
  assert(combinedFallback.degradations.some((item) => item.frontier === "relationships" && item.code === "relationship_vector_warming"), "relationship recall must report its vector fallback");
  assert(combinedFallback.degradations.some((item) => item.frontier === "relationships" && item.code === "ontology_temporal_not_supported"), "the vector fallback must retain the independent ontology omission");
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] }, "PATCH");
  const ordinary = await mcp("recall_memory", args, reader.api_key);
  assert.equal(ordinary.results.filter((item) => sourceIDs.includes(item.evidence_id)).length, 3);
  assert(ordinary.results.every((item) => item.equivalent_evidence_ids.length === 0 && !item.equivalents_truncated));
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
  const deniedRetraction = await rpc("retract_evidence", { evidence_ids: [sourceIDs[0]], reason: "wrong owner regression", idempotency_key: randomUUID() }, reader.api_key);
  assert(deniedRetraction.error || deniedRetraction.result?.isError, "reader C must not mutate owner A's source");
  const retracted = await mcp("retract_evidence", { evidence_ids: [sourceIDs[0]], reason: "withdraw grouping source", idempotency_key: randomUUID() }, shared.api_key);
  assert(retracted.retracted_evidence_ids.includes(sourceIDs[0]));
  const stale = await mcp("recall_memory", args, reader.api_key);
  assert(!stale.results.some((item) => item.evidence_id === sourceIDs[0] || item.equivalent_evidence_ids.includes(sourceIDs[0])));
  assert(stale.results.some((item) => item.evidence_id === sourceIDs[1] && item.equivalent_evidence_ids.length === 0));
  assert(stale.degradations.some((item) => item.code === "ontology_stale"));
  const staleRelationships = await mcp("recall_memory", { ...args, query: "database platform", relationship_limit: 20 }, reader.api_key);
  for (const item of staleRelationships.related_relationships) {
    assert(!item.evidence_ids.includes(sourceIDs[0]));
    assert(!item.evidence_ids.includes(privateID));
    assert(!item.evidence_ids.includes(foreignID));
  }
  const accountingAfter = (await control("/ontology/status")).data.window;
  assert.equal(accountingAfter.charged_input_tokens, accountingBefore.charged_input_tokens, "Recall added organization provider input tokens");
  assert.equal(accountingAfter.charged_output_tokens, accountingBefore.charged_output_tokens, "Recall added organization provider output tokens");
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
  await fixtureMode("normal");
  const history = (await control("/ontology/runs?limit=200")).data;
  for (const run of history.runs) {
    if (run.status === "completed") assert.equal(run.failure_code || "", "", "completed runs must have no current failure reason");
  }
  console.log(JSON.stringify({ status: "ok", scenario: "ontology_organization", window_id: status.window.id, counts: status.counts, durable_command_replay: true, operator_isolation: true, grouped_recall: true, original_sources_retained: true, private_and_foreign_isolation: true, stale_group_fallback: true, recall_organization_provider_tokens: 0, bounded_ambiguity_run: ambiguousRun.id, retryable_failure_run: failedRun.id }));
} finally {
  await fixtureMode("normal");
  if (paused) await control("/ontology/resume", { operation_key: randomUUID() });
  await control("/config/ontology-maintenance", { items: original.items.map(({ key, value }) => ({ key, value })) }, "PATCH");
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
async function remember(content, key, relationshipIDs = []) {
  const relationships = [{ ref: "storage", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }];
  if (content.includes("encrypts")) relationships.push({ ref: "backups", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "encrypts" }, object: { entity: { name: "backups", entity_kind: "document" } }, polarity: "+", evidence_indices: [0] });
  const result = await mcp("remember", { idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-e2e", source_group: "ontology-e2e", force_insert: true }], relationships }, key);
  assert.equal(result.processing_state, "completed");
  relationshipIDs.push(...result.relationship_results.flatMap((item) => item.splits.map((split) => split.relationship_id)));
  const evidence = result.evidence.find((item) => item.disposition === "stored");
  assert(evidence?.evidence_id, "Remember did not retain the source evidence");
  return evidence.evidence_id;
}

async function fixtureMode(mode) {
  const response = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/ontology-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) });
  assert.equal(response.status, 200);
}
async function rememberFixture(key, content) {
  const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "remember", arguments: { idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-followups", source_group: randomUUID() }], relationships: [{ ref: "storage", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }] } } }) });
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
