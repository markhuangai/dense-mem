#!/usr/bin/env node
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const key = required("DENSE_MEM_E2E_API_KEY");
await control("/ontology/pause", { operation_key: randomUUID() });
await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
const shared = await control(`/teams/${teamID}/credentials`, { name: "Community browser fixture", scopes: ["read", "write"], rate_limit: 300 });
assert.equal(shared.credential.memory_access.team_shared.write, true);
const subjects = Array.from({ length: 16 }, (_, index) => `Atlas coordination shard ${index + 1}`);
const stored = await mcp("remember", {
  target_space: "team_shared",
  idempotency_key: randomUUID(),
  evidence: subjects.map((subject) => ({ content: `${subject} uses Redis for coordination.`, source_type: "document", source: "community-browser-e2e", force_insert: true })),
  relationships: subjects.map((subject, index) => ({ ref: `coordination-${index}`, subject: { name: subject, entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "Redis", entity_kind: "product" } }, polarity: "+", evidence_indices: [index] })),
}, shared.api_key);
assert.equal(stored.processing_state, "completed");
assert.equal(stored.evidence.filter((item) => item.disposition === "stored").length, subjects.length);
assert.equal(new Set(stored.relationship_results.flatMap((item) => item.splits.map((split) => split.relationship_id))).size, subjects.length);
await control("/ontology/resume", { operation_key: randomUUID() });
const command = await control("/ontology/runs", { operation_key: randomUUID(), max_batches: 100 });
let status;
let run;
for (let i = 0; i < 150; i++) {
  const history = await control("/ontology/runs?limit=200");
  run = history.runs.find((item) => item.id === command.id);
  status = await control(`/teams/${teamID}/community/status`);
  const projection = status.projection_coverage;
  if (run?.status === "completed" && projection?.current_topics > 0 && projection.coverage_complete) break;
  await new Promise((resolve) => setTimeout(resolve, 1000));
}
assert.equal(status.legacy_processing_retired, true);
assert(status.current_community_count > 0);
assert.equal(run?.status, "completed", JSON.stringify({ run, community: status, ontology: await control("/ontology/status") }));
assert.equal(status.projection_coverage.coverage_complete, true, JSON.stringify(status));
await control("/ontology/pause", { operation_key: randomUUID() });
const recalled = await mcp("recall_memory", { query: "Atlas Redis", limit: 10 });
assert(recalled.related_communities.length > 0, JSON.stringify({ community: status, evidence_ids: recalled.results.map((item) => item.evidence_id), relationship_ids: recalled.related_relationships.map((item) => item.relationship_id), degradations: recalled.degradations }));
assert(recalled.related_communities.some((item) => item.summary.includes("memory coordination") && item.relationship_count >= subjects.length), JSON.stringify(recalled.related_communities));
assert(!Object.hasOwn(recalled,"related_hypotheses"));
const provider=await fetch(`${required("DENSE_MEM_E2E_PROVIDER_URL")}/health`);
assert.equal(provider.status,200);
assert.equal(((await provider.json()).chat_requests||[]).filter((item)=>["community_summary","dense_mem_dream_generation_response","dense_mem_evidence_discovery_response"].includes(item.schema_name)).length,0);
console.log(JSON.stringify({status:"ok",topic_communities:true,legacy_processing_retired:true,no_legacy_provider_calls:true}));

async function control(path,body,method=body?"POST":"GET") {
 const response=await fetch(`${controlURL}/control/api${path}`,{method,headers:{Authorization:`Bearer ${required("DENSE_MEM_CONTROL_TOKEN")}`,"Content-Type":"application/json"},...(body?{body:JSON.stringify(body)}:{})});
 assert(response.ok, `control ${path} returned ${response.status}`);
 return (await response.json()).data;
}
function required(name) {assert(process.env[name],`${name} is required`);return process.env[name];}

async function mcp(name, args, token = key) {
 const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${token}`, Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name, arguments: args } }) });
 assert.equal(response.status, 200);
 const rpc = await response.json();
 assert(!rpc.error && rpc.result?.isError !== true);
 return JSON.parse(rpc.result.content[0].text);
}
