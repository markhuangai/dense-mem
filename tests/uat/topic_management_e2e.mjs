import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { collectSemanticPublicationProof } from "./topic_semantic_diagnostics.mjs";

const required = (name) => { assert(process.env[name], `${name} is required`); return process.env[name]; };
const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
let manager = required("DENSE_MEM_E2E_API_KEY");
const original = await control("/config/topic-maintenance");
const initial = await control("/topic-maintenance/status");

try {
  assert.equal(initial.window, undefined, "management acceptance configures the fresh stack before its first window");
  await control("/topic-maintenance/pause", { operation_key: randomUUID() });
  await control("/config/topic-maintenance", { items: [
    { key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }, { key: "TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" },
    { key: "TOPIC_MAINTENANCE_MAX_CONCURRENCY", value: "8" }, { key: "TOPIC_MAINTENANCE_TEAM_MAX_CONCURRENCY", value: "8" },
  ] }, "PATCH");
  const shared = await credential(teamID, "manager", ["read", "write"]);
  assert.equal(shared.credential.memory_access.team_shared.write, true);
  manager = shared.api_key;
  const b = await credential(teamID, "manager", ["read", "write"]);
  const c = await credential(teamID, "member", ["read", "write"]);
  const readonly = await credential(teamID, "manager", ["read"]);
  assert.match(readonly.credential.id, /^[0-9a-f-]{36}$/);
  assert.match(teamID, /^[0-9a-f-]{36}$/);
  // Manager creation grants write, so seed restricted scopes only in this disposable fixture.
  executeFixtureSQL(`BEGIN; SET LOCAL app.tx_mode='system'; UPDATE credentials SET scopes=ARRAY['read']::text[] WHERE id='${readonly.credential.id}'::uuid AND team_id='${teamID}'::uuid; COMMIT;`);
  const privateKey = await credential(teamID, "manager", ["read", "write"]);
  const foreignTeam = await control("/teams", { name: `ontology-management-foreign-${randomUUID()}` });
  const foreign = await credential(foreignTeam.id, "manager", ["read", "write"]);
  const ids = [await remember("Atlas stores PostgreSQL data.", manager), await remember("Atlas uses PostgreSQL for data storage.", b.api_key)];
  const privateID = await remember("Atlas stores PostgreSQL data.", privateKey.api_key, "private");
  const foreignID = await remember("Atlas stores PostgreSQL data.", foreign.api_key);
  const canonical = snapshot();
  await expectStatus("/status", undefined, c.api_key, 403);
  await ui("/status", undefined, readonly.api_key);
  await expectStatus("/edits", {}, readonly.api_key, 403);
  await expectStatus("/status?team_id=foreign", undefined, manager, 422);
  await expectStatus("/edits", { team_id: foreignTeam.id }, manager, 422);
  for (const id of [privateID, foreignID, randomUUID()]) assert.deepEqual((await ui(`/sources?kind=evidence&q=${id}`)).sources, []);
  const sources = [];
  for (const id of ids) {
    const page = await ui(`/sources?kind=evidence&q=${id}`);
    assert.equal(page.sources.length, 1);
    const { kind, version } = page.sources[0]; sources.push({ kind, id, version });
  }
  await settle(manager, async () => (await recall()).length === 1);
  const unusedKey = `atlas-postgresql-alternative-${randomUUID()}`;
  await saveEdits([{ action: "define", expected_version: 0, kind: "topic", definition: { key: unusedKey, label: "Atlas PostgreSQL alternative vocabulary", description: "PostgreSQL database storage", aliases: [] } }]);
  const topics = (await ui("/records?kind=topic&limit=200")).records;
  const added = topics.find((record) => record.definition?.key === unusedKey);
  assert(added);
  await settle(manager, async () => {
    const direct = (await mcp("recall_memory", { topic_ids: [added.id], limit: 20 }, manager)).results;
    const visible = new Set(direct.flatMap((hit) => [hit.evidence_id, ...hit.equivalent_evidence_ids]));
    return ids.every((id) => visible.has(id));
  });
  const edit = { action: "define", kind: "topic", expected_version: 0, definition: { key: `manager-${randomUUID()}`, label: "Manager storage vocabulary", description: "Shared database facilities", aliases: [] } };
  const input = { operation_key: randomUUID(), reason: "Review database vocabulary", edits: [edit] };
  for (const retired of [
    { action: "keep_separate", members: sources }, { action: "group_together", members: sources },
    { action: "pin_definition", definition_id: added.id }, { action: "remove_override", record_id: added.id },
    { action: "classify", source: sources[0], definition_id: added.id },
  ]) await expectStatus("/edits", { ...input, operation_key: randomUUID(), edits: [{expected_version:0,...retired}] }, manager, 422);
  const beforeCalls = await providerCalls();
  const competing = await Promise.all([
    request(`${userURL}/ui/api/topics`, "/edits", manager, input),
    request(`${userURL}/ui/api/topics`, "/edits", b.api_key, { ...input, operation_key: randomUUID() }),
  ]);
  assert.equal(competing.filter((response) => response.ok).length, 1, "only one same-name save can commit");
  assert.equal(competing.filter((response) => response.status === 422).length, 1);
  const winningKey = competing[0].ok ? manager : b.api_key;
  const published = await data(competing.find((response) => response.ok));
  assert.equal(published.records.length, 1);
  const catalog = await ui(`/records/${published.records[0].id}`);
  assert.equal(catalog.definition.label, edit.definition.label);
  if (winningKey === manager) {
    assert.equal((await ui("/edits", input)).id, published.id);
    await expectStatus("/edits", { ...input, reason: "Changed retry" }, manager, 409);
  }
  const oldDraft = { ...input, operation_key: randomUUID(), expected_version: 0, expected_revision: (await ui("/status")).revision };
  await expectStatus("/drafts", oldDraft, manager, 410);
  await expectStatus("/drafts", oldDraft, readonly.api_key, 403);
  assert.deepEqual(await providerCalls(), beforeCalls, "saving definitions is provider-free");
  const status = await ui("/status");
  for (const field of ["window", "config", "model", "global", "paused"]) assert(!Object.hasOwn(status, field), `team status exposed ${field}`);
  assert.deepEqual(snapshot(), canonical);
  await topicHierarchyAcceptance(shared.credential.id, [c, readonly]);
  assert.deepEqual(snapshot(), canonical, "semantic hierarchy changed canonical facts or provenance");
  await matchingAcceptance();

  console.log("Topic catalog HTTP/MCP permissions, isolation, replay, stale conflicts, provider-free Save and hierarchy checks passed");
} finally {
  await control(initial.paused ? "/topic-maintenance/pause" : "/topic-maintenance/resume", { operation_key: randomUUID() });
  await control("/config/topic-maintenance", { items: original.items.map(({ key, value }) => ({ key, value })) }, "PATCH");
}

async function request(base, path, key, body, method = body ? "POST" : "GET") {
  return fetch(`${base}${path}`, { method, headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json", Accept: "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
}

async function topicHierarchyAcceptance(profileID, deniedActors) {
  const since = new Date(Date.now() - 1000).toISOString();
  const operations = [];
  const credentials = [manager, "dense-mem-ontology-e2e-key"];
  const keys = [`parent-${randomUUID()}`, `child-${randomUUID()}`];
  const define = (key, parent) => ({ action: "define", expected_version: 0, kind: "topic",
    definition: { key, label: key, description: "Topic hierarchy fixture", ...(parent ? { parent_id: parent } : {}) } });
  const parentID = (await saveEdits([define(keys[0])])).records[0].id;
  const childID = (await saveEdits([define(keys[1], parentID)])).records[0].id;
  const parent = await ui(`/records/${parentID}`);
  const child = await ui(`/records/${childID}`);
  assert.equal(child.definition.parent_id, parentID);
  for (const actor of deniedActors) {
    const correlationID = randomUUID();
    const response = await fetch(`${userURL}/ui/api/topics/edits`, { method: "POST", headers: { Authorization: `Bearer ${actor.api_key}`, "Content-Type": "application/json", "X-Correlation-ID": correlationID }, body: JSON.stringify({ reason: "unauthorized-content-canary" }) });
    assert.equal(response.status, 403);
    operations.push({ correlationID, teamID, profileID: actor.credential.id, reason: "unauthorized", contentFree: true, outcomeEvent: "topic_semantic_manager_rejected", credentials: [...credentials, actor.api_key, "unauthorized-content-canary"] });
  }
  const change = (record, definition) => ({ action: "define", record_id: record.id, kind: "topic", expected_version: record.edit_version ?? record.version, definition });
  const parentLink = change(parent, { ...parent.definition, parent_id: childID });
  await expectStatus("/edits", { operation_key: randomUUID(), reason: "Would create a hierarchy cycle", edits: [parentLink] }, manager, 422);
  const correction = change(child, { ...child.definition, description: "Updated Topic description" });
  const save = { operation_key: randomUUID(), reason: "Update Topic description", edits: [correction] };
  const published = await ui("/edits", save);
  assert.equal((await ui("/edits", save)).id, published.id);
  const current = await ui(`/records/${childID}`);
  assert.equal(current.definition.description, correction.definition.description);
  const staleInput = { ...save, operation_key: randomUUID(), reason: "Stale edit witness" };
  const correlationID = randomUUID();
  const rejection = await fetch(`${userURL}/ui/api/topics/edits`, { method: "POST", headers: { Authorization: `Bearer ${manager}`, "Content-Type": "application/json", "X-Correlation-ID": correlationID }, body: JSON.stringify(staleInput) });
  assert.equal(rejection.status, 409);
  operations.push({ correlationID, teamID, profileID, credentials, operationKey: staleInput.operation_key, input: staleInput,
    reason: "record_version", outcomeEvent: "topic_semantic_manager_rejected",
    expectedVersion: correction.expected_version, observedVersion: current.edit_version ?? current.version });
  const { parent_id, ...standalone } = current.definition;
  assert.equal(parent_id, parentID);
  await saveEdits([change(current, standalone)]);
  assert.equal((await ui(`/records/${childID}`)).definition.parent_id, undefined);
  const proof = collectSemanticPublicationProof({ project: required("DENSE_MEM_E2E_COMPOSE_PROJECT"), composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since,
    directory: fileURLToPath(new URL(`./test-results/semantic-private-${teamID}`, import.meta.url)), operations });
  assert.equal(proof.length, operations.length);
}
async function data(response) { assert(response.ok, `request returned ${response.status}: ${(await response.clone().text()).slice(0, 500)}`); return (await response.json()).data; }
async function control(path, body, method) { return data(await request(`${controlURL}/control/api`, path, controlToken, body, method)); }
async function ui(path, body, key = manager, method) { return data(await request(`${userURL}/ui/api/topics`, path, key, body, method)); }
async function expectStatus(path, body, key, status) { assert.equal((await request(`${userURL}/ui/api/topics`, path, key, body)).status, status); }
async function credential(team, role, scopes) { return control(`/teams/${team}/credentials`, { name: `manager-e2e-${randomUUID()}`, role, scopes, rate_limit: 300 }); }
async function mcp(name, args, key = manager) {
  const response = await request(userURL, "/mcp", key, { jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name, arguments: args } });
  assert.equal(response.status, 200); const rpc = await response.json(); assert(!rpc.error && !rpc.result?.isError, `${name}: ${JSON.stringify(rpc.error ?? rpc.result)}`); return JSON.parse(rpc.result.content[0].text);
}
async function remember(content, key, targetSpace = "team_shared") {
  const result = await mcp("remember", { target_space: targetSpace, idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-manager-e2e", source_group: "ontology-manager-e2e", force_insert: true }] }, key);
  assert.equal(result.processing_state, "completed"); const id = result.evidence.find((item) => item.disposition === "stored")?.evidence_id; assert(id); return id;
}
async function recall() {
  const result = await mcp("recall_memory", { query: "Atlas PostgreSQL", limit: 20 });
  const all = result.results.flatMap((item) => [item.evidence_id, ...item.equivalent_evidence_ids]);
  assert.equal(new Set(all).size, 2, "Recall must retain both authorized source handles"); return result.results;
}
async function settle(key, ready = () => true) {
  await control("/topic-maintenance/resume", { operation_key: randomUUID() });
  await control("/topic-maintenance/runs", { operation_key: randomUUID(), max_batches: 100 });
  let complete = false;
  for (let i = 0; i < 150; i++) { const status = await ui("/status", undefined, key); if (status.coverage_complete && await ready()) { complete = true; break; } await new Promise((resolve) => setTimeout(resolve, 1000)); }
  assert(complete, "team maintenance did not complete"); await control("/topic-maintenance/pause", { operation_key: randomUUID() });
}
async function providerCalls() { const response = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/health`); assert(response.ok); return (await response.json()).chat_requests; }
async function saveEdits(edits) {
  return ui("/edits", { operation_key: randomUUID(), reason: "Catalog definition update", edits });
}
function snapshot(scopeTeam = teamID) {
  assert.match(scopeTeam, /^[0-9a-f-]{36}$/);
  const tables = ["evidence_fragments", "evidence_occurrences", "evidence_sources", "evidence_source_revisions", "evidence_lifecycle_events", "evidence_quarantines"];
  const sql = ["BEGIN", "SET LOCAL app.tx_mode='system'", ...tables.map((table) => `SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'[]') FROM ${table} AS row WHERE team_id='${scopeTeam}'::uuid`), "COMMIT"].join(";\n");
  return executeFixtureSQL(sql);
}
function executeFixtureSQL(sql) {
  const result = spawnSync("docker", ["compose", "-p", required("DENSE_MEM_E2E_COMPOSE_PROJECT"), "-f", required("DENSE_MEM_E2E_COMPOSE_FILE"), "exec", "-T", "-e", "PGOPTIONS=-c app.storage_layout=feature-storage-v1 -c app.topic_layout=topic-storage-v1", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "ontology-management-e2e", sql], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr); return result.stdout.trim();
}

async function matchingAcceptance() {
  const team = await control("/teams", { name: `ontology-matching-${randomUUID()}` });
  const owner = await credential(team.id, "manager", ["read", "write"]);
  const key = owner.api_key;
  const definitions = [
    { key: "grammarnoise", label: "Grammar noise", description: "of and the", aliases: [] },
    { key: "suntracker", label: "Solar tracker", description: "heliostat", aliases: [] },
    { key: "about", label: "about", description: "Named project", aliases: [] },
    { key: "conjunctioncatalog", label: "Conjunction catalog", description: "Named catalog", aliases: ["and"] },
  ];
  await ui("/edits", { operation_key: randomUUID(), reason: "Matching acceptance vocabulary",
    edits: definitions.map((definition) => ({ action: "define", expected_version: 0, kind: "topic", definition })) }, key);
  const fixtureMode = async (mode) => {
    const response = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/ontology-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) });
    assert.equal(response.status, 200);
  };
  await fixtureMode("matching");
  try {
    for (const [content, expected] of [
      ["Atlas uses PostgreSQL as part of the system.", "memory-storage"],
      ["Atlas uses PostgreSQL for heliostat control.", "suntracker"],
      ["Atlas uses PostgreSQL for the about project.", "about"],
      ["Atlas uses PostgreSQL and backups.", "conjunctioncatalog"],
    ]) {
      const source = await remember(content, key);
      const before = snapshot(team.id);
      await settle(key);
      const records = (await ui("/records?kind=assignment&limit=200", undefined, key)).records;
      const current = records.filter((record) => record.current && record.assignment.source.id === source);
      const definitions = await Promise.all(current.map((record) => ui(`/records/${record.assignment.definition_id}`, undefined, key)));
      const keys = definitions.map((item) => item.definition.key);
      assert(keys.includes(expected), `expected ${expected} among current direct memberships: ${keys}`);
      assert(!keys.includes("grammarnoise"), "incidental stop words must not create a topic membership");
      assert.deepEqual(snapshot(team.id), before, "matching must preserve canonical sources and facts");
    }
  } finally {
    await fixtureMode("normal");
  }
}
