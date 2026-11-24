import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { collectSemanticPublicationProof } from "./ontology_semantic_diagnostics.mjs";

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
  await expectStatus("/drafts", {}, readonly.api_key, 403);
  await expectStatus("/status?team_id=foreign", undefined, manager, 422);
  await expectStatus("/drafts", { team_id: foreignTeam.id }, manager, 422);
  for (const id of [privateID, foreignID, randomUUID()]) assert.deepEqual((await ui(`/sources?kind=evidence&q=${id}`)).sources, []);
  const sources = [];
  for (const id of ids) {
    const page = await ui(`/sources?kind=evidence&q=${id}`);
    assert.equal(page.sources.length, 1);
    const { kind, version } = page.sources[0]; sources.push({ kind, id, version });
  }
  await settle(manager, async () => (await recall()).length === 1);
  const unusedKey = `atlas-postgresql-alternative-${randomUUID()}`;
  await saveAndPublish([{ action: "define", expected_version: 0, kind: "topic", definition: { key: unusedKey, label: "Atlas PostgreSQL alternative vocabulary", description: "PostgreSQL database storage", aliases: [] } }]);
  const topics = (await ui("/records?kind=topic&limit=200")).records;
  const added = topics.find((record) => record.definition?.key === unusedKey);
  assert(added);
  await settle(manager, async () => {
    const direct = (await mcp("recall_memory", { topic_ids: [added.id], limit: 20 }, manager)).results;
    const visible = new Set(direct.flatMap((hit) => [hit.evidence_id, ...hit.equivalent_evidence_ids]));
    return ids.every((id) => visible.has(id));
  });
  const revision = (await ui("/status")).revision;
  const edit = { action: "define", kind: "topic", expected_version: 0, definition: { key: `manager-${randomUUID()}`, label: "Manager storage vocabulary", description: "Shared database facilities", aliases: [] } };
  const input = { operation_key: randomUUID(), expected_version: 0, expected_revision: revision, reason: "Review database vocabulary", edits: [edit] };
  for (const retired of [
    { action: "keep_separate", members: sources }, { action: "group_together", members: sources },
    { action: "pin_definition", definition_id: added.id }, { action: "remove_override", record_id: added.id },
    { action: "classify", source: sources[0], definition_id: added.id },
  ]) await expectStatus("/drafts", { ...input, operation_key: randomUUID(), edits: [{expected_version:0,...retired}] }, manager, 422);
  const beforeCalls = await providerCalls();
  const draft = await ui("/drafts", input);
  assert.deepEqual(await ui("/drafts", input), draft);
  await expectStatus("/drafts", { ...input, reason: "Changed retry" }, manager, 409);
  await expectStatus(`/drafts/${draft.id}`, undefined, b.api_key, 404);
  await expectStatus(`/drafts/${draft.id}`, undefined, foreign.api_key, 404);
  const preview = await ui(`/drafts/${draft.id}/preview`, { expected_version: draft.version });
  assert.equal(preview.changed_records, 1);
  assert(preview.complete && preview.examples.length <= 10);
  assert.deepEqual(snapshot(), canonical);
  const competitor = await ui("/drafts", { ...input, operation_key: randomUUID() }, b.api_key);
  const command = { operation_key: randomUUID(), expected_version: draft.version, expected_revision: revision, reason: draft.reason };
  const published = await ui(`/drafts/${draft.id}/publish`, command);
  assert.equal(published.publication.records.length, 1, "defining a Topic must not implicitly publish a pin");
  assert.deepEqual(await ui(`/drafts/${draft.id}/publish`, command), published);
  await expectStatus(`/drafts/${competitor.id}/publish`, { ...command, operation_key: randomUUID(), expected_version: competitor.version }, b.api_key, 409);
  assert.equal((await ui(`/drafts/${competitor.id}`, undefined, b.api_key)).state, "open");
  assert.deepEqual(await providerCalls(), beforeCalls, "editing, preview and publication must stay provider-free");
  const catalog = await ui(`/records/${published.publication.records[0].id}`);
  assert.equal(catalog.definition.label, edit.definition.label);
  const status = await ui("/status");
  for (const field of ["window", "config", "model", "global", "paused"]) assert(!Object.hasOwn(status, field), `team status exposed ${field}`);
  assert.deepEqual(snapshot(), canonical);
  await topicHierarchyAcceptance(shared.credential.id, [c, readonly]);
  assert.deepEqual(snapshot(), canonical, "semantic hierarchy changed canonical facts or provenance");
  await matchingAcceptance();

  console.log("Topic catalog HTTP/MCP permissions, isolation, replay, stale conflicts, provider-free preview, enrichment and rollback passed");
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
  const protectedInput = (input) => JSON.parse(credentials.reduce((body, credential) => body.replaceAll(credential, "[REDACTED]"), JSON.stringify(input, (key, value) =>
    (key === "aliases" && value.length === 0) || (key === "parent_id" && value === "") ? undefined : value)));
  const definition = (key, parent) => ({ key, label: key, description: "Exact admitted topic vocabulary", aliases: [], ...(parent ? { parent_id: parent } : {}) });
  const edit = (kind, value, previous) => ({ action: "define", kind, expected_version: previous?.version ?? 0, ...(previous ? { record_id: previous.id } : {}), definition: value });
  const find = async (kind, key) => {
    const value = (await ui(`/records?kind=${kind}&limit=200`)).records.find((record) => record.definition?.key === key);
    assert(value, "semantic definition was not published"); return value;
  };
  for (const actor of deniedActors) {
    const correlationID = randomUUID();
    const response = await fetch(`${userURL}/ui/api/topics/drafts`, { method: "POST", headers: { Authorization: `Bearer ${actor.api_key}`, "Content-Type": "application/json", "X-Correlation-ID": correlationID }, body: JSON.stringify({ reason: "unauthorized-content-canary" }) });
    assert.equal(response.status, 403);
    operations.push({ correlationID, teamID, profileID: actor.credential.id, reason: "unauthorized", contentFree: true, outcomeEvent: "topic_semantic_manager_rejected", credentials: [...credentials, actor.api_key, "unauthorized-content-canary"] });
  }
  async function rejected(edits, reason, parents = [], stale = false) {
    const observed = (await ui("/status")).revision;
    const input = { operation_key: randomUUID(), expected_version: 0, expected_revision: observed - (stale ? 1 : 0), reason: "Log-only semantic rejection", edits };
    const correlationID = randomUUID();
    const response = await fetch(`${userURL}/ui/api/topics/drafts`, { method: "POST", headers: { Authorization: `Bearer ${manager}`, "Content-Type": "application/json", "X-Correlation-ID": correlationID }, body: JSON.stringify(input) });
    assert.equal(response.status, stale || reason === "parent_unavailable" ? 409 : 422);
    operations.push({ operationKey: input.operation_key, correlationID, teamID, profileID, reason, parents, credentials, outcomeEvent: "topic_semantic_manager_rejected", input: protectedInput(input), ...(stale ? { expectedVersion: input.expected_revision, observedVersion: observed } : {}) });
  }
  for (const kind of ["topic"]) {
    const prefix = `semantic-${kind}-${randomUUID()}`;
    await saveAndPublish([edit(kind, definition(`${prefix}-parent`)), edit(kind, definition(`${prefix}-alternate`))]);
    const parent = await find(kind, `${prefix}-parent`);
    const alternate = await find(kind, `${prefix}-alternate`);
    const value = definition(`${prefix}-child`, parent.id);
    value.description = `Admitted {"token":"user token","password":"user password","secret":"user secret"} ${manager} dense-mem-ontology-e2e-key`;
    await saveAndPublish([edit(kind, value)]);
    const child = await find(kind, value.key);
    const changed = await saveAndPublish([edit(kind, { ...child.definition, label: `Edited ${kind}`, parent_id: alternate.id }, child)]);
    const reparented = await ui(`/records/${child.id}`);
    assert.equal(reparented.definition.parent_id, alternate.id);
    const rollback = { operation_key: randomUUID(), expected_revision: (await ui("/status")).revision, reason: "Restore semantic parent" };
    const rolled = await ui(`/publications/${changed.publication.id}/rollback`, rollback);
    assert.equal((await ui(`/publications/${changed.publication.id}/rollback`, rollback)).id, rolled.id);
    const restored = await ui(`/records/${child.id}`);
    assert.equal(restored.definition.parent_id, parent.id);
    await rejected([edit(kind, { ...restored.definition, parent_id: randomUUID() }, restored)], "parent_unavailable");
    await rejected([edit(kind, { ...parent.definition, parent_id: child.id }, parent)], "hierarchy_cycle", [parent.id, child.id]);
    await rejected([edit(kind, { ...value, key: `${prefix}-stale` })], "catalog_revision", [], true);
    const fingerprint = (record) => createHash("sha256").update(JSON.stringify(record)).digest("hex");
    assert.equal(fingerprint(await ui(`/records/${child.id}`)), fingerprint(restored), "rejected edits must leave the accepted definition unchanged");

    {
      const input = { operation_key: randomUUID(), expected_version: 0, expected_revision: (await ui("/status")).revision, reason: "Protected manager draft acceptance", edits: [edit(kind, { ...restored.definition, description: value.description }, restored)] };
      const correlationID = randomUUID();
      const savedResponse = await fetch(`${userURL}/ui/api/topics/drafts`, { method: "POST", headers: { Authorization: `Bearer ${manager}`, "Content-Type": "application/json", "X-Correlation-ID": correlationID }, body: JSON.stringify(input) });
      const saved = await data(savedResponse);
      operations.push({ operationKey: input.operation_key, correlationID, teamID, profileID, reason: "accepted", credentials, input: protectedInput(input), draftID: saved.id, outcomeEvent: "topic_semantic_manager_command" });
      const updated = await ui(`/drafts/${saved.id}`, { ...input, expected_version: saved.version, operation_key: randomUUID() }, manager, "PUT");
      for (const stale of [false, true]) {
        const expectedVersion = stale ? saved.version : updated.version;
        const previewInput = { expected_version: expectedVersion };
        const correlationID = randomUUID();
        const response = await fetch(`${userURL}/ui/api/topics/drafts/${saved.id}/preview`, { method: "POST", headers: { Authorization: `Bearer ${manager}`, "Content-Type": "application/json", "X-Correlation-ID": correlationID }, body: JSON.stringify(previewInput) });
        assert.equal(response.status, stale ? 409 : 200);
        const preview = stale ? undefined : (await response.json()).data;
        operations.push({ correlationID, teamID, profileID, credentials, draftID: saved.id, input: previewInput, reason: stale ? "draft_version" : "accepted", outcomeEvent: stale ? "topic_semantic_manager_rejected" : "topic_semantic_draft_preview", ...(stale ? { expectedVersion, observedVersion: updated.version } : { preview, parents: [parent.id] }) });
      }
      const command = { operation_key: randomUUID(), expected_version: updated.version, expected_revision: updated.base_revision, reason: "Discard diagnostic draft" };
      const discardCorrelation = randomUUID();
      const discarded = await data(await fetch(`${userURL}/ui/api/topics/drafts/${saved.id}/discard`, { method: "POST", headers: { Authorization: `Bearer ${manager}`, "Content-Type": "application/json", "X-Correlation-ID": discardCorrelation }, body: JSON.stringify(command) }));
      assert.equal(discarded.state, "discarded");
      operations.push({ operationKey: command.operation_key, correlationID: discardCorrelation, teamID, profileID, credentials, draftID: saved.id, input: command, reason: "accepted", outcomeEvent: "topic_semantic_manager_command" });
    }
    await saveAndPublish([edit(kind, { ...restored.definition, parent_id: undefined }, restored)]);
    const cleared = await ui(`/records/${child.id}`);
    assert(!Object.hasOwn(cleared.definition, "parent_id"));
  }
  const proof = collectSemanticPublicationProof({ project: required("DENSE_MEM_E2E_COMPOSE_PROJECT"), composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since,
    directory: fileURLToPath(new URL(`./test-results/semantic-private-${teamID}`, import.meta.url)), operations });
  assert.equal(proof.length, operations.length);
  console.log("ontology topic hierarchy: edit, reparent, replay, rollback and complete protected log-only rejection proof passed");
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
async function saveAndPublish(edits) {
  const draft = await ui("/drafts", { operation_key: randomUUID(), expected_version: 0, expected_revision: (await ui("/status")).revision, reason: "Catalog definition update", edits });
  return ui(`/drafts/${draft.id}/publish`, { operation_key: randomUUID(), expected_version: draft.version, expected_revision: draft.base_revision, reason: draft.reason });
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
  const draft = await ui("/drafts", { operation_key: randomUUID(), expected_version: 0, expected_revision: (await ui("/status", undefined, key)).revision,
    reason: "Matching acceptance vocabulary", edits: definitions.map((definition) => ({ action: "define", expected_version: 0, kind: "topic", definition })) }, key);
  await ui(`/drafts/${draft.id}/publish`, { operation_key: randomUUID(), expected_version: draft.version, expected_revision: draft.base_revision, reason: draft.reason }, key);
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
