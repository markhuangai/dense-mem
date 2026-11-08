import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export function semanticPublicationRecords(window) {
  return window.split(/\r?\n/).flatMap((line) => {
    const offset = line.indexOf("{");
    if (offset < 0) return [];
    let record;
    try { record = JSON.parse(line.slice(offset)); } catch { return []; }
    return /^ontology_semantic_(publication|manager_|draft_preview)/.test(String(record.msg)) ? [record] : [];
  });
}

function scopedSemanticRecords(window, expected) {
  assert(expected.operationKey || expected.draftID || expected.contentFree, "a trusted operation selector is required");
  const records = semanticPublicationRecords(window).filter((record) => record.correlation_id === expected.correlationID &&
    (expected.operationKey ? record.operation_key === expected.operationKey : expected.draftID ? record.draft_id === expected.draftID : true));
  assert(records.length > 0, "required publication capture is missing");
  const operations = new Set(records.map((record) => record.operation_id));
  assert.equal(operations.size, 1, "publication operation identity changed");
  assert.match(records[0].operation_id, /^[0-9a-f-]{36}$/);
  for (const record of records) {
    assert.equal(record.team_id, expected.teamID, "publication capture crossed team scope");
    assert.equal(record.profile_id, expected.profileID, "publication capture crossed profile scope");
  }
  const serialized = JSON.stringify(records);
  for (const credential of expected.credentials ?? []) {
    assert(!serialized.includes(credential), "publication capture exposed a credential canary");
  }
  return records;
}

export function verifySemanticPublication(window, expected) {
  const records = scopedSemanticRecords(window, expected);
  const outcomes = records.filter((record) => record.msg !== "ontology_semantic_publication");
  assert.equal(outcomes.length, 1, "publication must have one final outcome");
  const event = expected.outcomeEvent ?? "ontology_semantic_publication_rejected";
  const rejected = event.endsWith("_rejected");
  const preview = event === "ontology_semantic_draft_preview";
  assert.equal(outcomes[0].msg, event);
  assert.equal(outcomes[0].level, preview ? "DEBUG" : rejected ? "WARN" : "INFO");
  assert.equal(outcomes[0].reason, expected.reason);
  assert.equal(outcomes[0].transaction_outcome, preview ? "read_only" : rejected ? "not_committed" : "committed");
  if (expected.contentFree) {
    assert.equal(records.length, 1, "authorization rejection must not capture content");
    for (const key of ["draft_id", "operation_key", "payload_chunk", "error"]) assert(!Object.hasOwn(records[0], key), "authorization rejection exposed untrusted detail");
    return { operation_id: records[0].operation_id, reason: expected.reason, records, bodies: {} };
  }
  const manifests = records.filter((record) => record.msg === "ontology_semantic_publication" && !record.payload_chunk);
  assert.equal(manifests.length, 1, "required publication manifest is missing");
  const manifest = manifests[0];
  assert.equal(manifest.capture_state, "captured", "required publication capture is incomplete");
  const chunks = records.filter((record) => record.payload_chunk);
  assert.equal(chunks.length, manifest.chunk_count, "publication chunk count differs from manifest");
  const payloads = new Map();
  for (const record of chunks) {
    assert.equal(record.level, "DEBUG");
    const chunk = record.payload_chunk;
    assert.equal(chunk.state, "captured", "required publication body is unavailable");
    assert(Buffer.byteLength(chunk.body) <= 2048, "publication chunk exceeds the body bound");
    const key = `${record.payload_kind}:${record.record_id}`;
    const payload = payloads.get(key) ?? { chunks: [], count: chunk.count, sha256: chunk.sha256 };
    assert.equal(chunk.index, payload.chunks.length, "publication chunks are missing, duplicated or reordered");
    assert.equal(chunk.count, payload.count);
    assert.equal(chunk.sha256, payload.sha256);
    payload.chunks.push(chunk.body);
    payloads.set(key, payload);
  }
  assert.equal(payloads.size, manifest.payload_count, "publication payload count differs from manifest");
  const bodies = {};
  for (const [key, payload] of payloads) {
    assert.equal(payload.chunks.length, payload.count, "publication payload is incomplete");
    const body = payload.chunks.join("");
    assert.equal(createHash("sha256").update(body).digest("hex"), payload.sha256, "publication payload hash mismatch");
    bodies[key] = JSON.parse(body);
  }
  assert(bodies["input:"], "admitted publication input is missing");
  if (rejected) assert.equal(bodies["rejection:"]?.reason, expected.reason);
  if (expected.draftID) assert.equal(bodies["draft:"]?.id, expected.draftID);
  if (expected.preview) {
    assert.deepEqual(bodies["preview:"], expected.preview);
    assert.equal(outcomes[0].affected_sources, expected.preview.affected_sources);
    assert.equal(outcomes[0].complete, expected.preview.complete);
  }
  if (expected.input) assert.deepEqual(bodies["input:"], expected.input, "admitted fields did not round trip exactly");
  for (const parent of expected.parents ?? []) assert(bodies[`definition:${parent}`], "required parent revision is missing");
  if (expected.expectedVersion !== undefined) {
    assert.equal(bodies["rejection:"].expected, String(expected.expectedVersion));
    assert.equal(bodies["rejection:"].observed, String(expected.observedVersion));
  }
  return { operation_id: records[0].operation_id, reason: expected.reason, records, bodies };
}

export function collectSemanticPublicationProof({ project, composeFile, since, directory, operations }) {
  const result = spawnSync("docker", ["compose", "-p", project, "-f", composeFile, "logs", "--no-color", "--no-log-prefix", "--since", since, "server"], { encoding: "utf8", maxBuffer: 128 * 1024 * 1024 });
  assert(!result.error && result.status === 0, "complete Compose publication log window is unavailable");
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  chmodSync(directory, 0o700);
  const captures = operations.map((operation) => scopedSemanticRecords(result.stdout, operation));
  for (const records of captures) {
    writeFileSync(join(directory, `${records[0].operation_id}.private.jsonl`), records.map((record) => JSON.stringify(record)).join("\n") + "\n", { mode: 0o600 });
    for (const record of records) console.log(`ontology_semantic_diagnostic_record ${JSON.stringify(record)}`);
  }
  const proofs = operations.map((operation) => verifySemanticPublication(result.stdout, operation));
  for (const proof of proofs) {
    writeFileSync(join(directory, `${proof.operation_id}.private.json`), JSON.stringify(proof), { mode: 0o600 });
  }
  return proofs;
}
