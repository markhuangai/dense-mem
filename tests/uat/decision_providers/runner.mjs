import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";

const userURL = required("DENSE_MEM_USER_URL");
const controlURL = required("DENSE_MEM_CONTROL_URL");
const token = required("DENSE_MEM_CONTROL_TOKEN");
const apiKey = required("DENSE_MEM_E2E_API_KEY");
const project = required("DENSE_MEM_E2E_COMPOSE_PROJECT");
const compose = required("DENSE_MEM_E2E_COMPOSE_FILE");
const fixtureURL = "http://synchronous-write-provider:8787";
const prefix = `Decision (${randomUUID().slice(0, 8)})`;
let childID = "";
const childName = `${project.slice(0, 35)}-decision-jev-${randomUUID().slice(0, 8)}`;
let completed = false;
let sequence = 0;

try {
  const initial = await control(controlURL);
  assert.equal(initial.effective.provider.configured, true);
  assert.deepEqual(initial.effective.effective, { deduplication: true, security_review: true, conflict_review: true, recall_reranking: true });
  assert(initial.items.every((item) => item.source === "default"));
  assert(!JSON.stringify(initial).includes("dense-mem-e2e-decision-key"));
  const stored = await remember(userURL, "Normal");
  assert.equal(stored.result.processing_state, "completed");
  const generated = (await getJSON(`${fixtureURL}/health`)).chat_requests.at(-1).generated_fields;
  assert(!generated.includes("evidence_security_results"));
  assert(!generated.includes("evidence_equivalence_results"));
  const count = (await fixture()).exchanges.length;
  const replay = await rpc(userURL, "remember", stored.request);
  assert.equal(replay.processing_state, "completed");
  assert.equal((await fixture()).exchanges.length, count, "unchanged replay must not invoke decisions");
  await fixture("regenerate");
  assert.equal((await remember(userURL, "Regenerate")).result.processing_state, "completed");
  await fixture("uncertain");
  assert.equal((await remember(userURL, "Uncertain")).result.processing_state, "completed");
  assert((await getJSON(`${fixtureURL}/health`)).chat_requests.at(-1).generated_fields.includes("evidence_security_results"));
  for (const mode of ["malformed", "refusal", "401", "429", "529"]) {
    await fixture(mode);
    const failed = (await remember(userURL, `Failure${mode}`)).result;
    assert.equal(failed.processing_state, "failed", `required decision ${mode} must fail`);
    assert((await fixture()).exchanges.some((item) => item.mode === mode), `required decision ${mode} must reach the provider`);
    assert(failed.evidence.every((item) => item.disposition === "not_stored"));
    assert(failed.relationship_results.every((item) => item.disposition === "not_stored"));
  }
  await fixture("normal");
  const recalled = await rpc(userURL, "recall_memory", { query: `${prefix} Normal`, limit: 2, relationship_limit: 2, community_limit: 0 });
  assert(recalled.results.some((item) => item.evidence_id === stored.result.evidence[0].evidence_id));
  await verifyReranking(userURL, "openai");
  await control(controlURL, [{ key: "DECISION_SECURITY_REVIEW_ENABLED", value: "false" }]);
  const saved = await control(controlURL);
  assert.equal(saved.effective.preferences.security_review, false);
  assert.equal(saved.effective.effective.deduplication, true);
  childID = createTypeSafeInstance();
  assert(/^[a-f0-9]{64}$/.test(childID), "test container identity is required");
  docker(["start",childID]);
  await until(async () => {
    const response = await fetch(`http://${childName}:8080/ready`);
    assert(response.ok, `TypeSafe instance readiness HTTP ${response.status}`);
    return true;
  });
  const typesafeControl = `http://${childName}:8090`;
  const typesafe = await control(typesafeControl);
  assert.equal(typesafe.effective.provider.provider, "typesafe");
  assert.equal(typesafe.effective.preferences.security_review, false, "saved off survives provider change and process startup");
  await control(typesafeControl, [{ key: "DECISION_SECURITY_REVIEW_ENABLED", value: "true" }]);
  const jev = await remember(`http://${childName}:8080`, "TypeSafe");
  assert.equal(jev.result.processing_state, "completed");
  await rpc(`http://${childName}:8080`, "recall_memory", { query: `${prefix} TypeSafe`, limit: 2, relationship_limit: 2, community_limit: 0 });
  await verifyReranking(`http://${childName}:8080`, "typesafe");
  const exchanges = (await fixture()).exchanges;
  assert(exchanges.some((item) => item.vendor === "openai"));
  assert(exchanges.some((item) => item.vendor === "typesafe"));
  await until(async () => {
    for (const event of ["decision.task.completed", "decision.question.prepared", "decision.answer.validated", "decision.policy.completed", "decision.validation.regenerate"]) {
      const response = await fetch(`${controlURL}/control/api/logs?limit=1&event=${encodeURIComponent(event)}`, { headers: { Authorization: `Bearer ${token}` } });
      assert(response.ok, `operation logs HTTP ${response.status}`);
      const logs = (await response.json()).data;
      if (!logs.some((entry) => entry.message === event)) return false;
    }
    return true;
  });
  process.stdout.write(`${JSON.stringify({ status: "ok", providers: ["openai", "typesafe"], protocol_exchanges: exchanges.length, live_quality: "pending_access" })}\n`);
  completed = true;
} finally {
  try { await fixture("normal"); } finally {
  if (childID) {
    const owned = JSON.parse(docker(["inspect", childID]))[0];
    assert.equal(owned.Name, `/${childName}`);
    assert.equal(owned.Config.Labels["com.docker.compose.project"], project);
    assert.equal(owned.Config.Labels["io.dense-mem.ci.scenario"], "decision_providers");
    if (!completed) process.stderr.write(docker(["logs", "--tail", "120", childID]));
    docker(["rm", "--force", childID]);
  }
  }
}

function createTypeSafeInstance() {
  const sourceID = docker(["compose", "-p", project, "-f", compose, "ps", "-q", "server"]).trim();
  assert(/^[a-f0-9]{64}$/.test(sourceID), "source production server identity is required");
  const source = JSON.parse(docker(["inspect", sourceID]))[0];
  assert.equal(source.Config.Labels["com.docker.compose.project"], project);
  assert.equal(source.Config.Labels["io.dense-mem.ci.scenario"], "decision_providers");
  const environment = new Map(source.Config.Env.map((entry) => { const index = entry.indexOf("="); return [entry.slice(0,index),entry.slice(index+1)]; }));
  environment.set("AI_DECISION_PROVIDER", "typesafe");
  environment.set("AI_DECISION_MODEL", "jev-1.14.0");
  assert(environment.get("POSTGRES_HOST") || environment.get("POSTGRES_DSN"), "private database configuration is required");
  assert(environment.get("CONTROL_PORTAL_TOKEN"), "private control configuration is required");
  assert(environment.get("AI_API_EMBEDDING_MODEL") && environment.get("AI_API_EMBEDDING_DIMENSIONS"), "embedding configuration is required");
  const network = required("DENSE_MEM_E2E_NETWORK");
  assert(Object.hasOwn(source.NetworkSettings.Networks, network), "source server must use the disposable scenario network");
  const labels = Object.entries(source.Config.Labels).filter(([key]) => key.startsWith("io.dense-mem.ci."));
  labels.push(["com.docker.compose.project", project], ["com.docker.compose.service", "decision-typesafe"], ["com.docker.compose.oneoff", "True"]);
  return docker(["create", "--name", childName, "--network", network, ...labels.flatMap(([key,value]) => ["--label",`${key}=${value}`]), ...Array.from(environment,([key,value]) => ["--env",`${key}=${value}`]).flat(), source.Image, ...(source.Config.Cmd || [])]).trim();
}

async function remember(base, suffix) {
  const name = `${prefix} ${suffix}`;
  const request = { target_space: "team_shared", idempotency_key: `decision-${randomUUID()}`, evidence: [{ content: `${name} uses PostgreSQL.`, source_type: "manual" }], relationships: [{ ref: "uses", subject: { name, entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }] };
  return { request, result: await rpc(base, "remember", request) };
}
async function verifyReranking(base, vendor) {
  for (const frontier of ["evidence", "relationships"]) {
    await fixture("normal");
    const args = { query: prefix, limit: frontier === "evidence" ? 3 : 1, relationship_limit: frontier === "relationships" ? 2 : 0, community_limit: 0 };
    const original = await rpc(base, "recall_memory", args);
    const field = frontier === "evidence" ? "results" : "related_relationships";
    const id = frontier === "evidence" ? "evidence_id" : "relationship_id";
    assert(original[field].length >= 2, `${vendor} ${frontier} needs distinguishable authorized candidates`);
    const preferred = original[field].at(-1)[id];
    assert.notEqual(preferred, original[field][0][id]);
    const start = (await fixture()).exchanges.length;
    await getJSON(`${fixtureURL}/decision-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: "ranking", preferred_ids: [preferred] }) });
    const ranked = await rpc(base, "recall_memory", { ...args, ...(frontier === "evidence" ? { limit: 1 } : {}) });
    assert.equal(ranked[field][0][id], preferred, `${vendor} ${frontier} must apply relevance scores before final selection`);
    if (frontier === "evidence") assert.equal(ranked.results.length, 1);
    const scoring = (await fixture()).exchanges.slice(start);
    assert(scoring.some((item) => item.vendor === vendor && item.names.some((name) => name.startsWith(frontier === "evidence" ? "evidence_" : "relationship_")) && item.types.includes("score")), `${vendor} ${frontier} must reach the score endpoint`);
    await fixture("429");
    const degraded = await rpc(base, "recall_memory", args);
    assert.deepEqual(degraded[field].map((item) => item[id]), original[field].map((item) => item[id]), `${vendor} ${frontier} failure must retain original ranking`);
    assert(degraded.degradations.some((item) => item.code === "decision_reranking_unavailable" && item.frontier === frontier && item.optional === true));
    assert((await fixture()).exchanges.slice(start).some((item) => item.vendor === vendor && item.mode === "429" && item.types.includes("score")));
  }
  await fixture("normal");
}
async function rpc(base, name, arguments_) {
  const response = await fetch(`${base}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: ++sequence, method: "tools/call", params: { name, arguments: arguments_ } }) });
  assert(response.ok, `MCP HTTP ${response.status}`);
  const value = await response.json(); assert(!value.error, "MCP error");
  return value.result.structuredContent || JSON.parse(value.result.content.find((item) => item.type === "text").text);
}
async function control(base, items) {
  const response = await fetch(`${base}/control/api/config/decisions`, { method: items ? "PATCH" : "GET", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, ...(items ? { body: JSON.stringify({ items }) } : {}) });
  assert(response.ok, `decision control HTTP ${response.status}`); return (await response.json()).data;
}
async function fixture(value) { return getJSON(`${fixtureURL}/decision-fixture`, value ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: value }) } : undefined); }
async function getJSON(url, init) { const response = await fetch(url, init); assert(response.ok, `fixture HTTP ${response.status}`); return response.json(); }
async function until(check) { const deadline = Date.now() + 30000; let cause; while (Date.now() < deadline) { try { if (await check()) return; } catch (error) { cause = error; } await new Promise((resolve) => setTimeout(resolve, 250)); } throw new Error("decision scenario condition did not complete", { cause }); }
function docker(args) { const result = spawnSync("docker", args, { encoding: "utf8", maxBuffer: 2 * 1024 * 1024 }); assert.equal(result.status, 0, "decision test container operation failed"); return result.stdout; }
function required(name) { const value = process.env[name]; assert(value, `${name} is required`); return value.replace(/\/$/, ""); }
