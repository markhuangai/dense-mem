#!/usr/bin/env node
import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";

const required = (name) => { assert(process.env[name], `${name} is required`); return process.env[name]; };
const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const runID = `memory-pack-${randomUUID()}`;
const content = `${runID} exact private Evidence. café 🧭 {"password":"admitted user text"}`;
const owner = await credential(teamID, "A");
const b = await credential(teamID, "B");
const foreign = await control("/teams", { name: `${runID}-C` });
const c = await credential(foreign.id, "C");
const remembered = await success(owner.api_key, "remember", { target_space: "private", idempotency_key: runID, evidence: [{ content, source_type: "document", source: runID, source_key: runID, source_revision: "1" }] });
assert.equal(remembered.processing_state, "completed");
const evidenceID = remembered.evidence[0].evidence_id;
for (const include of [undefined, true, false]) {
  const args = { name: " Private Evidence pack ", description: "v3 provenance", evidence_ids: [evidenceID], ...(include === undefined ? {} : { include_evidence: include }) };
  const result = await success(owner.api_key, "export_memory_pack", args);
  const artifact = JSON.parse(result.artifact_json);
  assert.equal(artifact.format, "dense-mem.memory-pack.v3.0");
  assert.equal(artifact.source.team_id, teamID);
  assert.equal(artifact.source.exported_by, owner.credential.id);
  assert.equal(artifact.evidence.length, 1);
  const item = artifact.evidence[0];
  assert.equal(item.evidence_id, evidenceID);
  assert(item.occurrences.length > 0 && item.source_revisions.length === 1 && item.decisions.length > 0);
  assert.equal(item.content_hash.replace(/^sha256:/, ""), createHash("sha256").update(content).digest("hex"));
  if (include === false) {
    assert(!Object.hasOwn(item, "content") && item.content_omitted);
    assert(!result.artifact_json.includes(content));
    assert(result.omissions.length > 0);
    assert.equal(result.omissions.length, 1);
    assert.equal(result.omissions[0].item_id, evidenceID);
    assert(result.omissions[0].reason.includes("evidence_content_excluded_by_request"));
  } else assert.equal(item.content, content);
  const hash = artifact.content_sha256;
  delete artifact.content_sha256;
  assert.equal(hash, createHash("sha256").update(JSON.stringify(artifact)).digest("hex"));
  assert.equal(result.content_sha256, hash);
  assert.equal(result.counts.evidence, 1);
  for (const actor of [b, c]) {
    const denied = await raw(actor.api_key, "export_memory_pack", args);
    assert(denied.result?.isError && denied.result.structuredContent.reason_code === "reference_not_found");
    assert(!JSON.stringify(denied).includes(content));
    assert(!JSON.stringify(denied).includes(evidenceID));
  }
}
for (const args of [
  { evidence_ids: [] }, { evidence_ids: Array.from({ length: 501 }, () => randomUUID()) },
  { evidence_ids: [evidenceID, evidenceID] }, { evidence_ids: ["bad"] },
  { evidence_ids: [evidenceID], relationship_ids: [] }, { evidence_ids: [evidenceID], include_entity_names: true },
]) {
  const rejected = await raw(owner.api_key, "export_memory_pack", { name: runID, ...args });
  assert(rejected.error || rejected.result?.isError, "invalid or retired export fields must reject");
}
console.log(JSON.stringify({ status: "ok", scenario: "memory_pack", exact_evidence: true, canonical_hash: true, provenance: true, omissions: true, private_ABC: true }));
async function control(path, body) {
  const response = await fetch(`${controlURL}/control/api${path}`, { method: "POST", headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" }, body: JSON.stringify(body) });
  assert(response.ok); return (await response.json()).data;
}
function credential(team, label) { return control(`/teams/${team}/credentials`, { name: `${runID}-${label}`, scopes: ["read", "write"], rate_limit: 300 }); }
async function raw(key, name, args) {
  const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name, arguments: args } }) });
  assert.equal(response.status, 200); return response.json();
}
async function success(key, name, args) {
  const result = await raw(key, name, args);
  assert(!result.error && !result.result?.isError, `${name}: ${JSON.stringify(result)}`);
  const payload = result.result.structuredContent;
  assert.deepEqual(JSON.parse(result.result.content[0].text), payload);
  return payload;
}
