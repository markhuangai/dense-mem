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
  for (const content of ["Atlas stores PostgreSQL data.", "Atlas stores PostgreSQL data and encrypts its backups."]) {
    const relationships = [{ ref: "storage", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }];
    if (content.includes("encrypts")) relationships.push({ ref: "backups", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "encrypts" }, object: { entity: { name: "backups", entity_kind: "document" } }, polarity: "+", evidence_indices: [0] });
    const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${shared.api_key}`, Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "remember", arguments: { idempotency_key: randomUUID(), evidence: [{ content, source_type: "document", source: "ontology-e2e", source_group: randomUUID() }], relationships } } }) });
    assert.equal(response.status, 200);
    const rpc = await response.json();
    assert.notEqual(rpc.result?.isError, true, JSON.stringify(rpc.error ?? rpc.result));
    assert.equal(JSON.parse(rpc.result.content[0].text).processing_state, "completed");
  }
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
  console.log(JSON.stringify({ status: "ok", scenario: "ontology_organization", window_id: status.window.id, counts: status.counts, durable_command_replay: true, operator_isolation: true }));
} finally {
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
