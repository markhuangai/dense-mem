import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";
import { semanticPublicationRecords, verifySemanticPublication } from "./ontology_semantic_diagnostics.mjs";

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
const window = (records) => records.map((record) => `server-1 | ${JSON.stringify(record)}`).join("\n");

test("semantic parser reconstructs complete protected stdout without relying on telemetry", () => {
  const { input, records } = fixture();
  const other = { ...records[0], correlation_id: "other", operation_id: "unrelated" };
  const proof = verifySemanticPublication(window([other, ...records]), { ...expected, input });
  assert.deepEqual(proof.bodies["input:"], input);
  assert.equal(semanticPublicationRecords("unrelated startup\n" + window(records)).length, records.length);
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
