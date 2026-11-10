import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import { captureSemanticAssessmentWindow, reconstructDiagnosticBody } from "./ontology_assessment_diagnostics.mjs";
const diagnosticSince = new Date().toISOString();

const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const providerURL = process.env.DENSE_MEM_E2E_PROVIDER_URL || "http://synchronous-write-provider:8787";
const a = await credential(teamID, "A");
const b = await credential(teamID, "B");
const shared = await credential(teamID, "shared");
const readOnly = await credential(teamID, "read-only", ["read"]);
const foreignTeam = (await control("/teams", { name: `session-C-${randomUUID()}` })).data;
const c = await credential(foreignTeam.id, "C");
assert((await list(a.api_key)).some((tool) => tool.name === "ingest_session"));
for (const actor of [shared, readOnly]) {
  assert((await list(actor.api_key)).some((tool) => tool.name === "ingest_session"));
  const privateResult = await call("ingest_session", request([{ event_id: "one", text: "Ari uses Go." }]), actor.api_key);
  assert.equal(privateResult.processing_state, "completed");
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
const ids = evidenceIDs(first);
assert.equal(ids.length, 1, JSON.stringify(first));
const afterFirst = await providerCalls();
assert(afterFirst > before);
assert.deepEqual(await call("ingest_session", firstRequest, a.api_key), first);
assert.equal(await providerCalls(), afterFirst, "replay must not invoke extraction or assessment");
const trace = await call("trace_memory", { evidence_id: ids[0] }, a.api_key);
assert(trace.evidence?.evidence_id === ids[0]);
for (const evidence of trace.occurrences) {
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
  const recall = await call("recall_memory", { query: "Ari Go", limit: 20 }, actor.api_key);
  assert(!recall.results.some((item) => first.events[0].evidence_ids.includes(item.evidence_id)));
  assertFailed(await rpc("trace_memory", { evidence_id: ids[0] }, actor.api_key));
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
assert.deepEqual(incremental.events.find((event) => event.event_id === firstEvent.event_id).evidence_ids, first.events[0].evidence_ids);
const newEvidence = incremental.events.find((event) => event.event_id === secondEvent.event_id);
assert.equal(newEvidence.disposition, "accepted");
assert.equal(newEvidence.evidence_ids.length, 1);
assert(!ids.includes(newEvidence.evidence_ids[0]));
const noFacts = await call("ingest_session", request([{ event_id: "empty", text: "Thanks. Let's continue." }]), a.api_key);
assert.equal(noFacts.processing_state, "completed");
assert.equal(noFacts.search_state, "not_required");
assert.deepEqual(noFacts.events[0].evidence_ids, []);
assert.equal(noFacts.memory_outcome, "no_memory");
assert.equal(first.contract_version, "dense-mem.v2.8.0");
const longEvent = { event_id: "long", text: (" ".repeat(506) + "Morgan uses Go.").padEnd(32_000) };
const long = await call("ingest_session", request([longEvent]), a.api_key);
assert.equal(long.processing_state, "completed");
assert.equal(evidenceIDs(long).length, 1);
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
const cancelledRequest = request([{ event_id: "cancelled", text: "Casey uses Ruby. [fixture-fault:embedding-session-cancel]" }]);
const cancel = new AbortController();
const embeddingsBeforeCancellation = (await providerHealth()).embedding_calls;
const pending = fetch(`${userURL}/mcp`, { method: "POST", signal: cancel.signal, headers: { Authorization: `Bearer ${a.api_key}`, "Content-Type": "application/json", Accept: "application/json", "MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": "ingest_session" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "ingest_session", arguments: cancelledRequest, _meta: { "io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": {} } } }) });
const rejected = assert.rejects(pending);
try {
 const deadline = Date.now() + 15000;
 while ((await providerHealth()).embedding_calls <= embeddingsBeforeCancellation && Date.now() < deadline) await delay(50);
 assert((await providerHealth()).embedding_calls > embeddingsBeforeCancellation, "cancellation must occur after provider dispatch");
} finally { cancel.abort(); }
await rejected;
const project = required("DENSE_MEM_E2E_COMPOSE_PROJECT");
assert.match(project, /^densemem-ci-[a-z0-9-]+$/);
const composeFiles = [required("DENSE_MEM_E2E_COMPOSE_FILE"), required("DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE")];
const postgres = spawnSync("docker", ["compose", ...composeFiles.flatMap((file) => ["-f", file]), "-p", project, "ps", "-q", "postgres"], { encoding: "utf8", timeout: 10000 });
assert.equal(postgres.status, 0);
const postgresID = postgres.stdout.trim();
assert.match(postgresID, /^[a-f0-9]{12,64}$/);
let diagnostic;
// Failure receipts and diagnostics each have a separate five-second persistence budget.
const diagnosticDeadline = Date.now() + 20000;
do {
 const query = spawnSync("docker", ["exec", "-i", postgresID, "sh", "-ec", 'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At'], { input: `BEGIN; SELECT set_config('app.tx_mode','system',true); SELECT diagnostics.body::text FROM session_submission_diagnostics AS diagnostics JOIN session_submissions AS submission USING(team_id,space_id,submission_id) WHERE submission.idempotency_key='${cancelledRequest.idempotency_key}'; COMMIT;`, encoding: "utf8", timeout: 10000 });
 assert.equal(query.status, 0, "cancelled diagnostic lookup failed");
 const encoded = query.stdout.split("\n").find((line) => line.startsWith("{"));
 if (encoded) { diagnostic = JSON.parse(encoded); break; }
 await delay(100);
} while (Date.now() < diagnosticDeadline);
assert(diagnostic, "cancelled production request must retain its diagnostic");
assert.deepEqual(diagnostic.payload.request, cancelledRequest);
assert.equal(diagnostic.payload.result.errors[0].code, "request_cancelled");
assert(!JSON.stringify(diagnostic).includes(a.api_key), "diagnostic must protect authentication material");
const logWindow = captureSemanticAssessmentWindow({ project, composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since: diagnosticSince, directory: (process.env.DENSE_MEM_E2E_REMEMBER_FAILURE_DIR || "/tmp/dense-mem-session-diagnostics")+"/session-v3" });
assert(!logWindow.includes(a.api_key) && !logWindow.includes(controlToken), "session stdout exposed credentials");
const envelopes = new Map();
for (const line of logWindow.split(/\r?\n/).filter(Boolean)) {
 const row=JSON.parse(line); assert.notEqual(row.level,"TRACE");
 if (row.msg!=="session_evidence_diagnostics") continue;
 assert.equal(row.team_id,teamID); assert(row.space_id && row.owner_profile_id && row.space_generation>0);
 const chunks=envelopes.get(row.submission_id)||[];chunks.push(row);envelopes.set(row.submission_id,chunks);
}
const sessions=[...envelopes.values()].map((chunks)=>{chunks.sort((a,b)=>a.payload_chunk.index-b.payload_chunk.index);return reconstructDiagnosticBody(chunks);});
const cancellation = sessions.find((body)=>body.payload.request.idempotency_key===cancelledRequest.idempotency_key);
assert(cancellation, "cancellation must be diagnosable from protected stdout alone");
assert.deepEqual(cancellation.payload.request,cancelledRequest);
assert.equal(cancellation.payload.result.errors[0].code,"request_cancelled");
assert(cancellation.payload.exchanges.some((exchange)=>["no_response","interrupted","truncated"].includes(exchange.CaptureState)), "session cancellation must expose incomplete provider capture");
for (const exchange of cancellation.payload.exchanges) {
 assert(exchange.StartedAt && exchange.CompletedAt && exchange.Model && exchange.Component);
 if (exchange.RequestBody) assert(Buffer.from(exchange.RequestBody,"base64").byteLength<=16*1024*1024);
 if (exchange.ResponseBody) assert(Buffer.from(exchange.ResponseBody,"base64").byteLength<=16*1024*1024);
}
assert(sessions.some((body)=>body.payload.result.errors?.some((error)=>error.code==="provider_response_invalid")), "malformed extraction must retain its terminal diagnostic");

const finalRecall = await call("recall_memory", { query: "Taylor Ruby Ari Java Python", limit: 20 }, a.api_key);
assert(!finalRecall.results.some((item) => /(?:Taylor uses Ruby|Ari uses Java|Ari uses Python|Casey uses Ruby)/.test(item.context)));
await control(`/teams/${teamID}/credentials/${readOnly.credential.id}`, undefined, "DELETE");
const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${readOnly.api_key}`, "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: "revoked", method: "tools/list" }) });
assert.equal(response.status, 401);
console.log(JSON.stringify({ status: "ok", scenario: "session_ingest", exact_provenance: true, long_event_characters: 32000, atomic_failures: true, cancelled_diagnostic_retained: true, replay: true, incremental: true, isolation_actors: 3 }));

function required(name) { assert(process.env[name], `${name} is required`); return process.env[name]; }
function request(events, sessionID = randomUUID()) { return { idempotency_key: randomUUID(), framework: "generic", app_name: "session-e2e", user_id: "external-user", session_id: sessionID, events }; }
function evidenceIDs(result) { return [...new Set(result.events.flatMap((event) => event.evidence_ids))]; }
async function control(path, body, method = body ? "POST" : "GET") {
 const response = await fetch(`${controlURL}/control/api${path}`, { method, headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
 assert(response.ok, `control ${path}: ${response.status} ${(await response.clone().text()).slice(0, 500)}`); return response.json();
}
async function credential(team, name, scopes = ["read", "write"]) { return (await control(`/teams/${team}/credentials`, { name, scopes, rate_limit: 1000 })).data; }
async function wire(method, params, key) {
 const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method, params }) });
 assert.equal(response.status, 200); return response.json();
}
async function rpc(name, args, key) { return wire("tools/call", { name, arguments: args }, key); }
async function list(key) { return (await wire("tools/list", {}, key)).result.tools; }
async function call(name, args, key) { const response = await rpc(name, args, key); assert(!response.error && !response.result?.isError, JSON.stringify(response)); return response.result.structuredContent || JSON.parse(response.result.content[0].text); }
function assertFailed(response) { assert(response.error || response.result?.isError, "expected rejection"); return response.result?.structuredContent || response.error?.data || JSON.parse(response.result.content[0].text); }
async function providerCalls() { const response = await fetch(`${providerURL.replace(/\/v1$/, "")}/health`); assert(response.ok); return (await response.json()).assessment_calls; }

async function providerHealth() { const response = await fetch(`${providerURL.replace(/\/v1$/, "")}/health`); assert(response.ok); return response.json(); }
