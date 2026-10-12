import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const required = (name) => { assert(process.env[name], `${name} is required`); return process.env[name]; };
const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
let manager = required("DENSE_MEM_E2E_API_KEY");
const original = await control("/config/ontology-maintenance");
const initial = await control("/ontology/status");
const originalCommunity = await control("/config/community-detection");

try {
  await control("/ontology/pause", { operation_key: randomUUID() });
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
  await control("/config/community-detection", { items: [{ key: "COMMUNITY_DETECTION_ENABLED", value: "false" }] }, "PATCH");
  const shared = await credential(teamID, "manager", ["read", "write"]);
  assert.equal(shared.credential.memory_binding, "shared_only");
  manager = shared.api_key;
  const b = await credential(teamID, "manager", ["read", "write"]);
  const c = await credential(teamID, "member", ["read", "write"]);
  const readonly = await credential(teamID, "manager", ["read"]);
  assert.match(readonly.credential.id, /^[0-9a-f-]{36}$/);
  assert.match(teamID, /^[0-9a-f-]{36}$/);
  // Manager creation grants write, so seed restricted scopes only in this disposable fixture.
  executeFixtureSQL(`BEGIN; SET LOCAL app.tx_mode='system'; UPDATE credentials SET scopes=ARRAY['read']::text[] WHERE id='${readonly.credential.id}'::uuid AND team_id='${teamID}'::uuid; COMMIT;`);
  const privateKey = await credential(teamID, "manager", ["read", "write"], "credential_private");
  const foreignTeam = await control("/teams", { name: `ontology-management-foreign-${randomUUID()}` });
  const foreign = await credential(foreignTeam.id, "manager", ["read", "write"]);
  const ids = [await remember("Atlas stores PostgreSQL data.", manager), await remember("Atlas uses PostgreSQL for data storage.", b.api_key)];
  const privateID = await remember("Atlas stores PostgreSQL data.", privateKey.api_key);
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
  await settle();
  assert.equal((await recall()).length, 1, "initial paraphrases should share a slot");
  const revision = (await ui("/status")).revision;
  const input = { operation_key: randomUUID(), expected_version: 0, expected_revision: revision, reason: "Separate manager-selected paraphrases", edits: [{ action: "keep_separate", expected_version: 0, members: sources }] };
  const beforeCalls = await providerCalls();
  const draft = await ui("/drafts", input);
  assert.deepEqual(await ui("/drafts", input), draft);
  await expectStatus("/drafts", { ...input, reason: "Changed retry" }, manager, 409);
  await expectStatus(`/drafts/${draft.id}`, undefined, b.api_key, 404);
  await expectStatus(`/drafts/${draft.id}`, undefined, foreign.api_key, 404);
  assert.equal((await ui(`/drafts/${draft.id}`)).id, draft.id);
  const preview = await ui(`/drafts/${draft.id}/preview`, { expected_version: draft.version });
  assert.equal(preview.affected_sources, 2); assert(preview.complete); assert(preview.examples.length <= 10);
  assert.deepEqual(await providerCalls(), beforeCalls, "draft and preview must be provider-free");
  assert.deepEqual(snapshot(), canonical, "preview must preserve canonical facts and provenance");
  const competitor = await ui("/drafts", { ...input, operation_key: randomUUID() }, b.api_key);
  const command = { operation_key: randomUUID(), expected_version: draft.version, expected_revision: revision, reason: draft.reason };
  const published = await ui(`/drafts/${draft.id}/publish`, command);
  assert.deepEqual(await ui(`/drafts/${draft.id}/publish`, command), published);
  await expectStatus(`/drafts/${competitor.id}/publish`, { ...command, operation_key: randomUUID(), expected_version: competitor.version }, b.api_key, 409);
  assert.equal((await ui(`/drafts/${competitor.id}`, undefined, b.api_key)).state, "open");
  assert.deepEqual(await providerCalls(), beforeCalls, "publication must be provider-free");
  await settle();
  assert.equal((await recall()).length, 2, "separation must persist through maintenance");
  const rollback = { operation_key: randomUUID(), expected_revision: (await ui("/status")).revision, reason: "Restore grouping" };
  const rolled = await ui(`/publications/${published.publication.id}/rollback`, rollback);
  assert.equal((await ui(`/publications/${published.publication.id}/rollback`, rollback)).id, rolled.id);
  assert.equal((await ui(`/publications/${rolled.id}`)).rollback_of, published.publication.id);
  await settle();
  assert.equal((await recall()).length, 1, "rollback restores eligible grouping");
  const status = await ui("/status");
  for (const field of ["window", "config", "model", "global", "paused"]) assert(!Object.hasOwn(status, field), `team status exposed ${field}`);
  const definition = { action: "define", expected_version: 0, kind: "topic", definition: { key: `manager-${randomUUID()}`, label: "Manager storage vocabulary", description: "Pinned manager vocabulary", aliases: ["manual database vocabulary"] } };
  const pinned = await saveAndPublish([definition]);
  const pin = (await ui("/records?kind=override&limit=200")).records.find((record) => record.override?.action === "pin_definition" && pinned.publication.records.some((ref) => ref.id === record.id));
  assert(pin, "definition edit must create a persistent pin");
  await saveAndPublish([{ action: "remove_override", record_id: pin.id, expected_version: pin.version }]);
  assert((await ui(`/records/${pin.id}`)).retired, "explicit unpin retires pin override");
  assert.deepEqual(snapshot(), canonical, "corrections and rollback changed canonical facts or provenance");
  console.log("ontology management production HTTP/MCP: permissions, isolation, replay, stale conflicts, provider-free preview, split persistence, pin/unpin and rollback passed");
} finally {
  await control(initial.paused ? "/ontology/pause" : "/ontology/resume", { operation_key: randomUUID() });
  await control("/config/ontology-maintenance", { items: original.items.map(({ key, value }) => ({ key, value })) }, "PATCH");
  await control("/config/community-detection", { items: originalCommunity.items.map(({ key, value }) => ({ key, value })) }, "PATCH");
}

async function request(base, path, key, body, method = body ? "POST" : "GET") {
  return fetch(`${base}${path}`, { method, headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json", Accept: "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
}
async function data(response) { assert(response.ok, `request returned ${response.status}: ${(await response.clone().text()).slice(0, 500)}`); return (await response.json()).data; }
async function control(path, body, method) { return data(await request(`${controlURL}/control/api`, path, controlToken, body, method)); }
async function ui(path, body, key = manager, method) { return data(await request(`${userURL}/ui/api/ontology`, path, key, body, method)); }
async function expectStatus(path, body, key, status) { assert.equal((await request(`${userURL}/ui/api/ontology`, path, key, body)).status, status); }
async function credential(team, role, scopes, memory_binding = "shared_only") { return control(`/teams/${team}/credentials`, { name: `manager-e2e-${randomUUID()}`, role, scopes, memory_binding, rate_limit: 300 }); }
async function mcp(name, args, key = manager) {
  const response = await request(userURL, "/mcp", key, { jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name, arguments: args } });
  assert.equal(response.status, 200); const rpc = await response.json(); assert(!rpc.error && !rpc.result?.isError, `${name}: ${JSON.stringify(rpc.error ?? rpc.result)}`); return JSON.parse(rpc.result.content[0].text);
}
async function remember(content, key) {
  const result = await mcp("remember", { idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-manager-e2e", source_group: "ontology-manager-e2e", force_insert: true }], relationships: [{ ref: "storage", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }] }, key);
  assert.equal(result.processing_state, "completed"); const id = result.evidence.find((item) => item.disposition === "stored")?.evidence_id; assert(id); return id;
}
async function recall() {
  const result = await mcp("recall_memory", { query: "Atlas PostgreSQL", limit: 20, relationship_limit: 0, community_limit: 0 });
  const all = result.results.flatMap((item) => [item.evidence_id, ...item.equivalent_evidence_ids]);
  assert.equal(new Set(all).size, 2, "Recall must retain both authorized source handles"); return result.results;
}
async function settle() {
  await control("/ontology/resume", { operation_key: randomUUID() });
  await control("/ontology/runs", { operation_key: randomUUID(), max_batches: 100 });
  let complete = false;
  for (let i = 0; i < 150; i++) { const status = await ui("/status"); if (status.coverage_complete) { complete = true; break; } await new Promise((resolve) => setTimeout(resolve, 1000)); }
  assert(complete, "team maintenance did not complete"); await control("/ontology/pause", { operation_key: randomUUID() });
}
async function providerCalls() { const response = await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/health`); assert(response.ok); return (await response.json()).chat_requests; }
async function saveAndPublish(edits) {
  const draft = await ui("/drafts", { operation_key: randomUUID(), expected_version: 0, expected_revision: (await ui("/status")).revision, reason: "Definition pin lifecycle", edits });
  return ui(`/drafts/${draft.id}/publish`, { operation_key: randomUUID(), expected_version: draft.version, expected_revision: draft.base_revision, reason: draft.reason });
}
function snapshot() {
  const tables = ["evidence_fragments", "evidence_occurrences", "evidence_sources", "evidence_source_revisions", "entity_records", "entity_names", "value_records", "relationship_records", "relationship_evidence_supports", "relationship_support_decision_events", "evidence_lifecycle_events", "evidence_quarantines"];
  const sql = ["BEGIN", "SET LOCAL app.tx_mode='system'", ...tables.map((table) => `SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'[]') FROM ${table} AS row WHERE team_id='${teamID}'::uuid`), "COMMIT"].join(";\n");
  return executeFixtureSQL(sql);
}
function executeFixtureSQL(sql) {
  const result = spawnSync("docker", ["compose", "-p", required("DENSE_MEM_E2E_COMPOSE_PROJECT"), "-f", required("DENSE_MEM_E2E_COMPOSE_FILE"), "exec", "-T", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "ontology-management-e2e", sql], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr); return result.stdout.trim();
}
