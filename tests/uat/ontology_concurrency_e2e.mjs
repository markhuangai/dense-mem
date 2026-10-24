import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const controlURL = required("DENSE_MEM_CONTROL_URL");
const userURL = required("DENSE_MEM_USER_URL");
const providerURL = required("DENSE_MEM_E2E_PROVIDER_URL");
const headers = { Authorization: `Bearer ${required("DENSE_MEM_CONTROL_TOKEN")}`, "Content-Type": "application/json" };
const original = await control("/config/ontology-maintenance");
const teams = [];
let failure;
try {
  assert.equal((await control("/ontology/status")).window, undefined, "concurrency acceptance configures the fresh stack before its first window");
  assert.equal(original.effective.context_window_tokens, 0, "a fresh deployment must not invent a model context capacity");
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
  const missing = await control("/ontology/status");
  assert.equal(missing.configuration_error, "configuration_invalid");
  const rejected = await request("/ontology/runs", { operation_key: randomUUID() });
  assert.equal(rejected.status, 409);
  assert.equal((await control("/ontology/status")).window, undefined);
  assert.equal((await providerHealth()).active_ontology_requests, 0);
  await control("/ontology/pause", { operation_key: randomUUID() });
  for (const name of ["parallel", "serial"]) {
    const team = await control("/teams", { name: `ontology-${name}-${randomUUID()}` });
    teams.push(team);
    team.credential = await control(`/teams/${team.id}/credentials`, { name: "concurrency acceptance", scopes: ["read", "write"], rate_limit: 300 });
  }
  const overrides = [{ team_id: teams[0].id, max_concurrency: 2 }];
  await control("/config/ontology-maintenance", { items: [
    { key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }, { key: "ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" },
    { key: "ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY", value: "3" },
    { key: "ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY", value: "1" },
    { key: "ONTOLOGY_MAINTENANCE_TEAM_OVERRIDES_JSON", value: JSON.stringify(overrides) },
  ] }, "PATCH");
  for (const [teamIndex, team] of teams.entries()) {
    for (let index = 0; index < (teamIndex === 0 ? 25 : 3); index++) await remember(team.credential.api_key, `Atlas uses PostgreSQL. Concurrency sample ${teamIndex}-${index}.`);
  }
  const canonicalBefore = canonical();
  await fixture("hold");
  await control("/ontology/resume", { operation_key: randomUUID() });
  const health = await until(async () => {
    const next = await providerHealth();
    return next.active_ontology_requests === 3 ? next : null;
  }, 90000, "three model requests did not overlap under the configured caps");
  assert.equal(health.max_active_ontology_requests, 3);
  const running = JSON.parse(query(`SELECT COALESCE(jsonb_agg(jsonb_build_object('team',team_id,'sources',sources)),'[]') FROM ontology_maintenance_batches WHERE status='running' AND lease_until>clock_timestamp()`));
  assert.equal(running.filter((batch) => batch.team === teams[0].id).length, 2, "one team must have two simultaneous provider batches");
  assert.equal(running.filter((batch) => batch.team === teams[1].id).length, 1, "the inherited serial cap must retain another team's slot");
  const owned = running.flatMap((batch) => batch.sources.map((source) => `${batch.team}:${source.kind}:${source.id}`));
  assert.equal(new Set(owned).size, owned.length, "live batches must have disjoint stable source identities");
  const before = await control("/ontology/status");
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_TEAM_OVERRIDES_JSON", value: JSON.stringify([{ team_id: teams[0].id, max_concurrency: 1 }]) }] }, "PATCH");
  const pending = await control("/ontology/status");
  assert.deepEqual(pending.window.policy, before.window.policy, "saving a limit must not rewrite active window policy");
  assert.equal(pending.pending_policy.team_concurrency_overrides[0].max_concurrency, 1);
  for (const raw of ["null", JSON.stringify([{ team_id: teams[0].id, max_concurrency: 9 }]), JSON.stringify([...overrides, ...overrides])]) {
    const rejected = await request("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_TEAM_OVERRIDES_JSON", value: raw }] }, "PATCH");
    assert.equal(rejected.status, 422);
  }
  const denied = await fetch(`${controlURL}/control/api/config/ontology-maintenance`, { method: "PATCH", headers: { ...headers, Authorization: `Bearer ${teams[0].credential.api_key}` }, body: JSON.stringify({ items: [{ key: "ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY", value: "8" }] }) });
  assert.equal(denied.status, 401);
  await control("/ontology/pause", { operation_key: randomUUID() });
  await fixture("normal");
  await until(async () => Number(query(`SELECT count(*) FROM ontology_maintenance_batches WHERE status='running' AND lease_until>clock_timestamp()`)) === 0, 30000, "pause failed to drain admitted batches");
  assert.deepEqual(canonical(), canonicalBefore, "parallel ontology must preserve canonical Knowledge");
  const stopped = await providerHealth();
  await delay(1000);
  assert.equal((await providerHealth()).assessment_calls, stopped.assessment_calls, "pause admitted another provider request");
  await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] }, "PATCH");
  await control("/ontology/resume", { operation_key: randomUUID() });
  assert.equal((await request("/ontology/runs", { operation_key: randomUUID() })).status, 409);
  console.log(JSON.stringify({ scenario: "ontology_concurrency", status: "ok", same_team_parallel_batches: 2, inherited_team_limit: 1, global_limit: 3, exclusive_sources: true, pending_policy_preserved: true, canonical_preserved: true, pause_drained: true }));
} catch (error) { failure = error; }
finally {
  const failures = failure ? [failure] : [];
  for (const action of [
    () => control("/ontology/pause", { operation_key: randomUUID() }),
    () => fixture("normal"),
    ...teams.map((team) => () => control(`/teams/${team.id}`, undefined, "DELETE")),
    () => control("/config/ontology-maintenance", { items: original.items.map(({ key, value }) => ({ key, value })) }, "PATCH"),
    () => control("/ontology/resume", { operation_key: randomUUID() }),
  ]) { try { await action(); } catch (error) { failures.push(error); } }
  if (failures.length) throw new AggregateError(failures, "ontology concurrency acceptance or cleanup failed");
}

function required(name) { assert(process.env[name], `${name} is required`); return process.env[name].replace(/\/$/, ""); }
function request(path, body, method = body ? "POST" : "GET") { return fetch(`${controlURL}/control/api${path}`, { method, headers, ...(body ? { body: JSON.stringify(body) } : {}) }); }
async function control(path, body, method) { const response = await request(path, body, method); assert(response.ok, `${path}: ${response.status}`); return response.status === 204 ? {} : (await response.json()).data; }
async function fixture(mode) { const response = await fetch(`${providerURL}/ontology-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) }); assert.equal(response.status, 200); }
async function providerHealth() { const response = await fetch(`${providerURL}/health`); assert.equal(response.status, 200); return response.json(); }
async function until(probe, milliseconds, message) { const deadline = Date.now() + milliseconds; while (Date.now() < deadline) { const result = await probe(); if (result) return result; await delay(100); } assert.fail(message); }
async function remember(key, content) {
  const body = JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "remember", arguments: { target_space: "team_shared", idempotency_key: randomUUID(), evidence: [{ content, force_insert: true, source: "ontology-concurrency", source_type: "document", source_group: randomUUID() }], relationships: [{ ref: "uses", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }] } } });
  for (let attempt = 0; attempt < 2; attempt++) {
    const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" }, body });
    assert.equal(response.status, 200);
    const rpc = await response.json();
    assert(!rpc.error && rpc.result?.content?.[0]?.text, `Remember returned an invalid fixture result: ${JSON.stringify(rpc.error ?? rpc.result)}`);
    const terminal = JSON.parse(rpc.result.content[0].text);
    if (attempt === 0 && terminal.processing_state === "failed" && terminal.errors?.length > 0 && terminal.errors.every((error) => error.code === "request_timeout" && error.retryable && error.next_action === "retry_same_request")) {
      console.warn("ontology concurrency fixture: retrying the same Remember request once after a retryable timeout");
      await delay(1000);
      continue;
    }
    assert.notEqual(rpc.result.isError, true, `Remember rejected the concurrency fixture: ${JSON.stringify(terminal)}`);
    assert.equal(terminal.processing_state, "completed");
    return;
  }
}
function query(sql) {
  const scoped = ["BEGIN", "SET LOCAL app.tx_mode='system'", sql, "COMMIT"].join(";\n");
  const result = spawnSync("docker", ["compose", "-p", required("DENSE_MEM_E2E_COMPOSE_PROJECT"), "-f", required("DENSE_MEM_E2E_COMPOSE_FILE"), "exec", "-T", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "ontology-concurrency-e2e", scoped], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}
function canonical() {
  return Object.fromEntries(["evidence_fragments", "entity_records", "value_records", "relationship_records", "relationship_evidence_supports"].map((table) => [table, query(`SELECT md5(COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'[]')) FROM ${table} AS row WHERE team_id IN (${teams.map((team) => `'${team.id}'::uuid`).join(",")})`)]));
}
