import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";
import { semanticPublicationRecords, verifySemanticPublication } from "./ontology_semantic_diagnostics.mjs";
import { verifySemanticAssessment } from "./ontology_assessment_diagnostics.mjs";

const expected = { correlationID: "correlation", operationKey: "operation", teamID: "team", profileID: "profile", reason: "hierarchy_cycle", credentials: ["credential-canary"] };
function fixture() {
  const base = { operation_id: "00000000-0000-4000-8000-000000000001", operation_key: "operation", team_id: "team", profile_id: "profile", correlation_id: "correlation" };
  const input = { token: "exact user token", password: "exact user password", description: "exact admitted fields " + "x".repeat(2400) };
  const records = [];
  for (const [kind, value] of [["input", input], ["rejection", { reason: "hierarchy_cycle" }]]) {
    const body = JSON.stringify(value);
    const parts = body.match(/.{1,2048}/gs);
    const sha256 = createHash("sha256").update(body).digest("hex");
    parts.forEach((body, index) => records.push({ ...base, msg: "ontology_semantic_publication", level: "DEBUG", payload_kind: kind, record_id: "", payload_chunk: { state: "captured", index, count: parts.length, body, sha256 } }));
  }
  records.push({ ...base, msg: "ontology_semantic_publication", level: "DEBUG", capture_state: "captured", payload_count: 2, chunk_count: records.length });
  records.push({ ...base, msg: "ontology_semantic_publication_rejected", level: "WARN", reason: "hierarchy_cycle", transaction_outcome: "not_committed" });
  return { input, records };
}

function assessmentFixture() {
  const base = { operation_id: "00000000-0000-4000-8000-000000000001", assessment_id: "assessment", team_id: "team", run_id: "run", batch_id: "batch", attempt: 0, level: "DEBUG" };
  const records = [];
  const chunks = (attrs, value) => {
    const body = JSON.stringify(value); const parts = body.match(/.{1,2048}/gs); const sha256 = createHash("sha256").update(body).digest("hex");
    parts.forEach((body, index) => records.push({ ...base, ...attrs, payload_chunk: { state: "captured", index, count: parts.length, sha256, body } }));
    return parts.length;
  };
  const request = { request_id: "assessment", items: [{ ref: "s0", preferred_definition_ref: "d0", context: { token: "exact admitted token", password: "exact admitted password" } }] };
  const count = chunks({ msg: "ontology_semantic_assessment", stage: "completed" }, { request, source_context: { Sources: [{ OwnerID: "owner", State: { content: "exact admitted content" } }] }, result: { assessment_id: "assessment", failure_code: "provider_response_invalid" } });
  records.push({ ...base, msg: "ontology_semantic_assessment", stage: "completed", capture_state: "captured", chunk_count: count });
  records.push({ ...base, level: "WARN", msg: "ontology_semantic_assessment_rejected", stage: "rejected", attempt: 1 });
  records.push({ ...base, level: "ERROR", msg: "ontology_semantic_assessment_failed", failure_code: "provider_response_invalid" });
  chunks({ msg: "ai_exchange_payload", attempt: 2, exchange_id: "exchange", direction: "request", transport_capture_state: "captured" }, { messages: [{ content: JSON.stringify(request) }, { content: JSON.stringify({ validation_errors: [{ field: "hierarchy_updates[0].parent_ref", message: "outside allowlist" }] }) }] });
  chunks({ msg: "ai_exchange_payload", attempt: 2, exchange_id: "exchange", direction: "response", transport_capture_state: "captured" }, { choices: [{ message: { content: "invalid complete response" } }] });
  return records;
}

const assessmentExpected = { assessmentID: "assessment", teamID: "team", runID: "run", batchID: "batch", failure: "provider_response_invalid", regeneration: true, credentials: ["operational-credential"] };
test("assessment parser reconstructs protected request, context, complete regeneration and terminal failure", () => {
  const records = assessmentFixture();
  const otherBatch = records.map((record) => ({ ...record, run_id: "another-run", batch_id: "another-batch", operation_id: "another-operation" }));
  const proof = verifySemanticAssessment(window([...otherBatch, ...records]), assessmentExpected);
  assert.equal(proof.stages["completed:0"].request.items[0].context.token, "exact admitted token");
  assert.equal(Object.keys(proof.exchanges).length, 2);
});
for (const [name, mutate] of [
  ["missing exchange direction", (records) => records.filter((record) => record.direction !== "response")],
  ["incomplete body", (records) => { records[0].payload_chunk.count++; return records; }],
  ["bad hash", (records) => { records[0].payload_chunk.sha256 = "invalid"; return records; }],
  ["over-bound marker", (records) => { records[0].payload_chunk.state = "unavailable"; records[0].payload_chunk.reason_code = "body_bytes_exceeded"; return records; }],
  ["credential leak", (records) => { records[0].canary = "operational-credential"; return records; }],
  ["cross-team", (records) => { records[0].team_id = "foreign"; return records; }],
  ["misleading completion", (records) => { records.find((record) => record.msg.endsWith("_failed")).msg = "ontology_semantic_assessment_completed"; return records; }],
]) test(`assessment parser rejects ${name}`, () => assert.throws(() => verifySemanticAssessment(window(mutate(assessmentFixture())), assessmentExpected)));
const window = (records) => records.map((record) => `server-1 | ${JSON.stringify(record)}`).join("\n");

test("semantic parser reconstructs complete protected stdout without relying on telemetry", () => {
  const { input, records } = fixture();
  const other = { ...records[0], correlation_id: "other", operation_id: "unrelated" };
  const proof = verifySemanticPublication(window([other, ...records]), { ...expected, input });
  assert.deepEqual(proof.bodies["input:"], input);
  assert.equal(semanticPublicationRecords("unrelated startup\n" + window(records)).length, records.length);
});

test("manager parser requires content-free authorization rejection", () => {
  const record = { operation_id: "00000000-0000-4000-8000-000000000001", team_id: "team", profile_id: "profile", correlation_id: "correlation", msg: "ontology_semantic_manager_rejected", level: "WARN", reason: "unauthorized", transaction_outcome: "not_committed" };
  const denied = { ...expected, operationKey: undefined, contentFree: true, reason: "unauthorized", outcomeEvent: record.msg };
  verifySemanticPublication(window([record]), denied);
  assert.throws(() => verifySemanticPublication(window([{ ...record, draft_id: "foreign-draft" }]), denied));
  assert.throws(() => verifySemanticPublication(window([{ ...record, error: "denied body" }]), denied));
});

test("manager parser retains precise stale-draft rejection", () => {
  const { records } = fixture();
  records.at(-1).msg = "ontology_semantic_manager_rejected";
  for (const record of records) record.draft_id = "draft";
  verifySemanticPublication(window(records), { ...expected, outcomeEvent: "ontology_semantic_manager_rejected" });
});

for (const [name, mutate] of [
  ["missing window", () => []],
  ["missing chunk", (records) => records.filter((_, index) => index !== 0)],
  ["reordered chunk", (records) => [records[1], records[0], ...records.slice(2)]],
  ["hash mismatch", (records) => { records[0].payload_chunk.body += "x"; return records; }],
  ["foreign scope", (records) => { records[0].team_id = "foreign"; return records; }],
  ["credential leak", (records) => { records[0].canary = "credential-canary"; return records; }],
  ["unavailable capture", (records) => { records[0].payload_chunk.state = "unavailable"; return records; }],
  ["truncated manifest", (records) => { records.at(-2).capture_state = "truncated"; return records; }],
  ["misleading commit", (records) => { records.at(-1).msg = "ontology_semantic_publication_committed"; return records; }],
]) {
  test(`semantic parser rejects ${name}`, () => assert.throws(() => verifySemanticPublication(window(mutate(fixture().records)), expected)));
}
