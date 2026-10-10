import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const providerURL = process.env.DENSE_MEM_E2E_PROVIDER_URL || "http://synchronous-write-provider:8787";
const a = await credential(teamID, "A", "credential_private");
const b = await credential(teamID, "B", "credential_private");
const shared = await credential(teamID, "shared", "shared_only");
const readOnly = await credential(teamID, "read-only", "credential_private", ["read"]);
const foreignTeam = (await control("/teams", { name: `session-C-${randomUUID()}` })).data;
const c = await credential(foreignTeam.id, "C", "credential_private");
assert((await list(a.api_key)).some((tool) => tool.name === "ingest_session"));
for (const actor of [shared, readOnly]) {
  assert(!(await list(actor.api_key)).some((tool) => tool.name === "ingest_session"));
  assertFailed(await rpc("ingest_session", request([{ event_id: "one", text: "Ari uses Go." }]), actor.api_key));
}
const timestamp = "2026-10-10T12:00:00Z";
const firstEvent = { event_id: "one", text: "  Ari uses Go. café 🧭\n", occurred_at: timestamp };
const firstRequest = request([firstEvent, firstEvent]);
const before = await providerCalls();
const first = await call("ingest_session", firstRequest, a.api_key);
assert.equal(first.processing_state, "completed");
assert.equal(first.accepted_event_count, 1);
assert.equal(first.duplicate_event_count, 1);
assert.equal(first.search_state, "current");
const ids = relationshipIDs(first);
assert.equal(ids.length, 1, JSON.stringify(first));
const afterFirst = await providerCalls();
assert(afterFirst > before);
assert.deepEqual(await call("ingest_session", firstRequest, a.api_key), first);
assert.equal(await providerCalls(), afterFirst, "replay must not invoke extraction, linking or assessment");
const trace = await call("trace_memory", { relationship_id: ids[0] }, a.api_key);
assert(trace.evidence.length > 0);
for (const evidence of trace.evidence) {
  const provenance = evidence.session;
  assert(provenance, "authorized trace must expose server-verified session provenance");
  assert.equal(provenance.framework, firstRequest.framework);
  assert.equal(provenance.app_name, firstRequest.app_name);
  assert.equal(provenance.user_id, firstRequest.user_id);
  assert.equal(provenance.session_id, firstRequest.session_id);
  assert.equal(provenance.event_id, firstEvent.event_id);
  assert.equal(provenance.occurred_at, timestamp);
  assert.equal(evidence.content, Array.from(firstEvent.text).slice(provenance.span_start, provenance.span_end).join(""));
}
for (const actor of [b, c]) {
  const recall = await call("recall_memory", { query: "Ari Go", limit: 20, relationship_limit: 20, community_limit: 0 }, actor.api_key);
  assert(!recall.related_relationships.some((item) => ids.includes(item.relationship_id)));
  assert(!recall.results.some((item) => first.events[0].evidence_ids.includes(item.evidence_id)));
  assertFailed(await rpc("trace_memory", { relationship_id: ids[0] }, actor.api_key));
}
const changed = request([{ event_id: "two", text: "Ari uses Rust." }, { ...firstEvent, text: "Ari uses Java." }], firstRequest.session_id);
const conflict = assertFailed(await rpc("ingest_session", changed, a.api_key));
assert.equal(conflict.reason_code, "session_event_conflict");
const timeConflict = request([{ ...firstEvent, occurred_at: "2026-10-11T12:00:00Z" }], firstRequest.session_id);
assert.equal(assertFailed(await rpc("ingest_session", timeConflict, a.api_key)).reason_code, "session_event_conflict");
const secondEvent = { event_id: "two", text: "Ari uses Rust." };
const incremental = await call("ingest_session", request([firstEvent, secondEvent], firstRequest.session_id), a.api_key);
assert.equal(incremental.accepted_event_count, 1);
assert.equal(incremental.duplicate_event_count, 1);
assert.equal(relationshipIDs(incremental).length, 1);
const noFacts = await call("ingest_session", request([{ event_id: "empty", text: "Thanks. Let's continue." }]), a.api_key);
assert.equal(noFacts.processing_state, "completed");
assert.equal(noFacts.search_state, "not_required");
assert.deepEqual(noFacts.events[0].evidence_ids, []);
const longEvent = { event_id: "long", text: (" ".repeat(506) + "Morgan uses Go.").padEnd(32_000) };
const long = await call("ingest_session", request([longEvent]), a.api_key);
assert.equal(long.processing_state, "completed");
assert.equal(relationshipIDs(long).length, 1);
const malformed = request([{ event_id: "bad", text: "Ari uses Java. [fixture-fault:assessment-malformed]" }]);
const failed = assertFailed(await rpc("ingest_session", malformed, a.api_key));
assert.equal(failed.processing_state, "failed");
assert.equal(failed.errors[0].code, "provider_response_invalid");
assert.deepEqual(failed.events[0].evidence_ids, []);
const overlap = { ...malformed, idempotency_key: randomUUID(), events: [...malformed.events, { event_id: "new", text: "Ari uses Python." }] };
assert.equal(assertFailed(await rpc("ingest_session", overlap, a.api_key)).reason_code, "session_event_retry_required");
const spoof = { ...request([{ event_id: "spoof", text: "Ari uses Go." }]), team_id: foreignTeam.id };
assertFailed(await rpc("ingest_session", spoof, a.api_key));
const embeddingFailure = request([{ event_id: "embedding-failure", text: "Taylor uses Ruby. [fixture-fault:embedding-malformed]" }]);
const embedding = assertFailed(await rpc("ingest_session", embeddingFailure, a.api_key));
assert.equal(embedding.processing_state, "failed");
assert.equal(embedding.errors[0].code, "embedding_response_invalid");
assert.deepEqual(embedding.events[0].evidence_ids, []);
const finalRecall = await call("recall_memory", { query: "Taylor Ruby Ari Java Python", limit: 20, relationship_limit: 20, community_limit: 0 }, a.api_key);
assert(!finalRecall.related_relationships.some((item) => ["Java", "Python", "Ruby"].includes(item.object?.name)));
await control(`/teams/${teamID}/credentials/${readOnly.credential.id}`, undefined, "DELETE");
const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${readOnly.api_key}`, "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: "revoked", method: "tools/list" }) });
assert.equal(response.status, 401);
console.log(JSON.stringify({ status: "ok", scenario: "session_ingest", exact_provenance: true, long_event_characters: 32000, atomic_failures: true, replay: true, incremental: true, isolation_actors: 3 }));

function required(name) { assert(process.env[name], `${name} is required`); return process.env[name]; }
function request(events, sessionID = randomUUID()) { return { idempotency_key: randomUUID(), framework: "generic", app_name: "session-e2e", user_id: "external-user", session_id: sessionID, events }; }
function relationshipIDs(result) { return [...new Set(result.relationship_results.flatMap((item) => (item.splits || []).map((split) => split.relationship_id).filter(Boolean)))]; }
async function control(path, body, method = body ? "POST" : "GET") {
 const response = await fetch(`${controlURL}/control/api${path}`, { method, headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
 assert(response.ok, `control ${path}: ${response.status} ${(await response.clone().text()).slice(0, 500)}`); return response.json();
}
async function credential(team, name, binding, scopes = ["read", "write"]) { return (await control(`/teams/${team}/credentials`, { name, scopes, rate_limit: 1000, memory_binding: binding })).data; }
async function wire(method, params, key) {
 const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method, params }) });
 assert.equal(response.status, 200); return response.json();
}
async function rpc(name, args, key) { return wire("tools/call", { name, arguments: args }, key); }
async function list(key) { return (await wire("tools/list", {}, key)).result.tools; }
async function call(name, args, key) { const response = await rpc(name, args, key); assert(!response.error && !response.result?.isError, JSON.stringify(response)); return response.result.structuredContent || JSON.parse(response.result.content[0].text); }
function assertFailed(response) { assert(response.error || response.result?.isError, "expected rejection"); return response.result?.structuredContent || response.error?.data || JSON.parse(response.result.content[0].text); }
async function providerCalls() { const response = await fetch(`${providerURL.replace(/\/v1$/, "")}/health`); assert(response.ok); return (await response.json()).assessment_calls; }
