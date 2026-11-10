import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export function assessmentRecords(window) {
  return window.split(/\r?\n/).flatMap((line) => {
    const offset = line.indexOf("{");
    if (offset < 0) return [];
    try { const record = JSON.parse(line.slice(offset)); return /^(ontology_semantic_assessment|ai_exchange_payload)/.test(record.msg) ? [record] : []; }
    catch { return []; }
  });
}

function reconstruct(records) {
  assert(records.length > 0, "required diagnostic body is missing");
  const first = records[0].payload_chunk;
  assert.equal(records.length, first.count, "diagnostic body is incomplete");
  const body = records.map((record, index) => {
    const chunk = record.payload_chunk;
    assert.equal(record.level, "DEBUG");
    assert.equal(chunk.state, "captured", "diagnostic body is unavailable");
    assert.equal(chunk.index, index, "diagnostic chunks are missing, duplicated or reordered");
    assert.equal(chunk.count, first.count);
    assert.equal(chunk.sha256, first.sha256);
    assert(Buffer.byteLength(chunk.body) <= 2048);
    return chunk.body;
  }).join("");
  assert.equal(createHash("sha256").update(body).digest("hex"), first.sha256, "diagnostic hash mismatch");
  return JSON.parse(body);
}

export function verifySemanticAssessment(window, expected) {
  const records = assessmentRecords(window).filter((record) => record.assessment_id === expected.assessmentID && record.run_id === expected.runID && record.batch_id === expected.batchID);
  assert(records.length > 0, "required assessment log window is missing");
  const operations = new Set(records.map((record) => record.operation_id));
  assert.equal(operations.size, 1);
  assert.match(records[0].operation_id, /^[0-9a-f-]{36}$/);
  for (const record of records) {
    assert.equal(record.team_id, expected.teamID);
    assert.equal(record.batch_id, expected.batchID);
    assert.equal(record.run_id, expected.runID);
  }
  const serialized = JSON.stringify(records);
  for (const secret of expected.credentials ?? []) assert(!serialized.includes(secret), "assessment capture exposed operational credentials");
  const stages = new Map();
  for (const manifest of records.filter((record) => record.msg === "ontology_semantic_assessment" && !record.payload_chunk)) {
    assert.equal(manifest.capture_state, "captured");
    const chunks = records.filter((record) => record.msg === manifest.msg && record.stage === manifest.stage && record.attempt === manifest.attempt && record.payload_chunk);
    assert.equal(chunks.length, manifest.chunk_count);
    stages.set(`${manifest.stage}:${manifest.attempt}`, reconstruct(chunks));
  }
  const terminal = records.filter((record) => ["ontology_semantic_assessment_completed", "ontology_semantic_assessment_failed"].includes(record.msg));
  assert.equal(terminal.length, 1);
  assert.equal(terminal[0].msg, expected.failure ? "ontology_semantic_assessment_failed" : "ontology_semantic_assessment_completed");
  assert.equal(terminal[0].level, expected.failure ? "ERROR" : "INFO");
  assert.equal(terminal[0].failure_code, expected.failure ?? "");
  const completed = stages.get("completed:0");
  assert(completed?.source_context && completed.request, "source context and effective request are required");
  assert.equal(completed.result.assessment_id, expected.assessmentID);
  if (expected.sourceID) assert(completed.request.items.some((item) => JSON.stringify(item).includes(expected.sourceCanary)), "admitted source context was lost");
  if (expected.failure === "stale_input") {
    assert(completed.result.freshness_rejection?.expected && completed.result.freshness_rejection?.observed);
    assert(records.some((record) => record.msg === "ontology_semantic_assessment_rejected" && record.reason === expected.failure));
    assert(!completed.result.publication, "stale assessment partially published");
  }
  const exchanges = new Map();
  for (const record of records.filter((record) => record.msg === "ai_exchange_payload")) {
    assert.equal(record.transport_capture_state, "captured");
    assert(record.exchange_id && record.attempt > 0);
    const key = `${record.exchange_id}:${record.direction}`;
    const group = exchanges.get(key) ?? [];
    group.push(record); exchanges.set(key, group);
  }
  const bodies = Object.fromEntries([...exchanges].map(([key, group]) => [key, reconstruct(group)]));
  if (!expected.reuse) {
    assert(exchanges.size >= 2, "actual provider request and response are required");
    for (const record of records.filter((record) => record.msg === "ai_exchange_payload")) {
      assert(exchanges.has(`${record.exchange_id}:request`) && exchanges.has(`${record.exchange_id}:response`), "exchange direction missing");
    }
  }
  if (expected.regeneration) {
    assert(records.some((record) => record.msg === "ontology_semantic_assessment_rejected" && record.attempt === 1));
    assert(Object.values(bodies).some((body) => body.messages?.some((message) => message.content.includes('"validation_errors"'))), "complete regeneration feedback is missing");
  }
  return { records, stages: Object.fromEntries(stages), exchanges: bodies };
}

export function captureSemanticAssessmentWindow({ project, composeFile, since, directory }) {
  const result = spawnSync("docker", ["compose", "-p", project, "-f", composeFile, "logs", "--no-color", "--no-log-prefix", "--since", since, "server"], { encoding: "utf8", maxBuffer: 256 * 1024 * 1024 });
  assert(!result.error && result.status === 0, "complete Compose assessment log window is unavailable");
  mkdirSync(directory, { recursive: true, mode: 0o700 }); chmodSync(directory, 0o700);
  writeFileSync(join(directory, "complete-assessment-window.private.jsonl"), result.stdout, { mode: 0o600 });
  return result.stdout;
}

export function collectSemanticAssessmentProof({ project, composeFile, since, directory, assessments }) {
  const window = captureSemanticAssessmentWindow({ project, composeFile, since, directory });
  const proofs = assessments.map((expected) => {
    const proof = verifySemanticAssessment(window, expected);
    writeFileSync(join(directory, `${expected.assessmentID}.private.json`), JSON.stringify({ ...proof, retain_until: new Date(Date.now()+7*86400000).toISOString() }), { mode: 0o600 });
    for (const record of proof.records) console.log(`ontology_semantic_diagnostic_record ${JSON.stringify(record)}`);
    return proof;
  });
  return proofs;
}
