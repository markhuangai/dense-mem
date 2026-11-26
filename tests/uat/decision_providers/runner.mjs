import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { progressCall } from "../synchronous_write/cases/progress.mjs";
import { remember as rememberWithRetry, callTool } from "./rpc.mjs";

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
const typeSafeQuestionBudget = 2048;
let completed = false;

try {
  const initial = await control(controlURL);
  assert.equal(initial.effective.provider.configured, true);
  assert.deepEqual(initial.effective.effective, { deduplication: true, security_review: true, conflict_review: true, recall_reranking: true, topic_assignment: true, community_placement: true });
  assert(initial.items.every((item) => item.source === "default"));
  assert(!JSON.stringify(initial).includes("dense-mem-e2e-decision-key"));
  const beforeNormal = (await getJSON(`${fixtureURL}/health`)).chat_requests.length;
  const stored = await remember(userURL, "Normal");
  assert.equal(stored.result.processing_state, "completed");
  const normalCalls = (await getJSON(`${fixtureURL}/health`)).chat_requests.slice(beforeNormal);
  const normalPartitions = normalCalls.filter((call) => call.schema_name === "dense_mem_semantic_partition_response");
  assert(normalPartitions.length > 0 && normalPartitions.every((call) => !call.generated_fields.includes("evidence_security_results")),
    "a clear Decision verdict must omit security fields from partitioning");
  const consoleLogs = spawnSync("docker", ["compose", "-p", project, "-f", compose, "logs", "--no-color", "server"], { encoding: "utf8", maxBuffer: 16 * 1024 * 1024 });
  assert.equal(consoleLogs.status, 0, "decision console log collection must succeed");
  const consoleText = `${consoleLogs.stdout}\n${consoleLogs.stderr}`;
  assert(!consoleText.includes("dense-mem-e2e-decision-key"), "console payloads must protect the configured provider credential");
  const consoleRecords = consoleText.split("\n").flatMap((line) => {
    const start = line.indexOf("{");
    if (start < 0) return [];
    try { return [JSON.parse(line.slice(start))]; } catch { return []; }
  });
  assert(consoleRecords.some((record) => record.msg === "decision.question.prepared" && record.level === "DEBUG"), "admitted questions must reach console DEBUG logs");
  for (const direction of ["request", "response"]) {
    assert(consoleRecords.some((record) => record.msg === "decision.exchange.payload" && record.level === "DEBUG" && record.direction === direction && record.payload_chunk?.state === "captured"), `decision ${direction} payloads must reach console DEBUG logs`);
  }
  const generated = normalCalls.findLast((call) => call.schema_name === "dense_mem_evidence_assessment_response").generated_fields;
  assert(!generated.includes("evidence_security_results"));
  assert(!generated.includes("evidence_equivalence_results"));
  const count = (await fixture()).exchanges.length;
  const replay = await rpc(userURL, "remember", stored.request);
  assert.equal(replay.processing_state, "completed");
  assert.equal((await fixture()).exchanges.length, count, "unchanged replay must not invoke decisions");
  for (const fault of ["routed-field-repair", "routed-field-exhausted"]) {
    const start = (await getJSON(`${fixtureURL}/health`)).chat_requests.length;
    const beforeDecisions = (await fixture()).exchanges.length;
    const { result } = await remember(userURL, fault, fault);
    const calls = (await getJSON(`${fixtureURL}/health`)).chat_requests.slice(start).filter((request) => request.fault === fault);
    assert(calls.every((request) => !request.generated_fields.includes("evidence_security_results") && !request.generated_fields.includes("evidence_equivalence_results")), "repairs must retain routed schemas");
    const decisions = (await fixture()).exchanges.slice(beforeDecisions);
    assert.equal(decisions.filter((exchange) => exchange.names.some((name) => name.startsWith("security_"))).length, 1, "repairs must reuse the intake security decision");
    assert(decisions.length <= 2, "repairs must reuse the deduplication decision");
    if (fault === "routed-field-repair") {
      assert.equal(result.processing_state, "completed", "field-specific feedback must enable a complete repair");
      assert.equal(calls.length, 2);
      assert(result.evidence.every((item) => item.disposition === "stored"));
    } else {
      assert.equal(result.processing_state, "failed");
      assert.equal(calls.length, 3, "malformed responses must stop at the existing turn bound");
      assert(result.evidence.every((item) => item.disposition === "not_stored"));
    }
  }
  await fixture("regenerate");
  const regenerated = await progressCall({
    apiKey, token: JSON.stringify(randomUUID()),
    args: { target_space: "team_shared", idempotency_key: `decision-${randomUUID()}`, evidence: [{ content: `${prefix} Regenerate uses PostgreSQL.`, source_type: "manual" }] },
  });
  assert.equal(regenerated.terminal.result.structuredContent.processing_state, "completed");
  const progress = regenerated.progress.map((frame) => frame.params.message);
  const decisionCall = progress.findIndex((stage) => stage.startsWith("Calling intake security provider"));
  const regeneration = progress.findIndex((stage) => stage.startsWith("Regenerating intake security provider response"));
  const partitioning = progress.findIndex((stage) => stage.startsWith("Partitioning submitted text"));
  assert(decisionCall >= 0 && regeneration > decisionCall && partitioning > regeneration,
    "the initial request and regeneration must reach the client before partitioning");
  await fixture("uncertain");
  const beforeUncertain = (await getJSON(`${fixtureURL}/health`)).chat_requests.length;
  assert.equal((await remember(userURL, "Uncertain")).result.processing_state, "completed");
  const uncertainCalls = (await getJSON(`${fixtureURL}/health`)).chat_requests.slice(beforeUncertain);
  assert.deepEqual(uncertainCalls.map((call) => call.schema_name), ["dense_mem_semantic_partition_response", "dense_mem_evidence_assessment_response"]);
  assert(uncertainCalls[0].generated_fields.includes("evidence_security_results"), "uncertainty requires whole-document generation review");
  assert(!uncertainCalls[1].generated_fields.includes("evidence_security_results"), "assessment must reuse the validated intake review");
  for (const [fault, code] of [["security", "submission_policy_rejected"], ["partition-security-invalid", "provider_response_invalid"], ["partition-security-timeout", "provider_unavailable"]]) {
    const before = await getJSON(`${fixtureURL}/health`);
    const failed = (await remember(userURL, fault, fault)).result;
    assert.equal(failed.processing_state, "failed");
    assert.equal(failed.errors[0].code, code);
    assert(failed.evidence.every((item) => item.disposition === "not_stored"));
    const after = await getJSON(`${fixtureURL}/health`);
    assert.equal(after.embedding_calls, before.embedding_calls, "uncertain intake must complete review before using embeddings");
    const calls = after.chat_requests.slice(before.chat_requests.length);
    assert(calls.length > 0 && calls.every((entry) => entry.schema_name === "dense_mem_semantic_partition_response" && entry.generated_fields.includes("evidence_security_results")));
  }
  for (const mode of ["malformed", "refusal", "401", "429", "529"]) {
    await fixture(mode);
    const failed = (await remember(userURL, `Failure${mode}`)).result;
    assert.equal(failed.processing_state, "failed", `required decision ${mode} must fail`);
    assert((await fixture()).exchanges.some((item) => item.mode === mode), `required decision ${mode} must reach the provider`);
    assert(failed.evidence.every((item) => item.disposition === "not_stored"));
  }
  await fixture("normal");
  const recalled = await rpc(userURL, "recall_memory", { query: `${prefix} Normal`, limit: 2 });
  assert(recalled.results.some((item) => item.evidence_id === stored.result.evidence[0].evidence_id));
  await verifyReranking(userURL, "openai");
  await verifyTopicRouting(userURL, controlURL, "openai");
  await control(controlURL, [{ key: "DECISION_SECURITY_REVIEW_ENABLED", value: "false" }]);
  const saved = await control(controlURL);
  assert.equal(saved.effective.preferences.security_review, false);
  assert.equal(saved.effective.effective.deduplication, true);
  await control(controlURL, [{ key: "DECISION_MAX_INPUT_TOKENS", value: "922000" }, { key: "DECISION_MAX_QUESTION_INPUT_TOKENS", value: "922000" }]);
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
  assert(typesafe.effective.configuration_error.includes("TypeSafe"));
  assert(typesafe.items.some((item) => item.validation_error));
  const invalidPatch = await fetch(`${typesafeControl}/control/api/config/decisions`, { method: "PATCH", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, body: JSON.stringify({ items: [{ key: "DECISION_MAX_INPUT_TOKENS", value: "65000" }] }) });
  assert.equal(invalidPatch.status, 422);
  const restored = await control(typesafeControl, [{ key: "DECISION_MAX_INPUT_TOKENS", value: "" }, { key: "DECISION_MAX_QUESTION_INPUT_TOKENS", value: "" }]);
  assert(!restored.effective.configuration_error);
  assert(restored.items.filter((item) => item.key.startsWith("DECISION_MAX_")).every((item) => item.source === "default"));
  await control(typesafeControl, [{ key: "DECISION_SECURITY_REVIEW_ENABLED", value: "true" }]);
  const typesafeUser = `http://${childName}:8080`;
  const jev = await remember(typesafeUser, "TypeSafe");
  assert.equal(jev.result.processing_state, "completed");
  await control(controlURL, [{ key: "DECISION_MAX_INPUT_TOKENS", value: "922000" }, { key: "DECISION_MAX_QUESTION_INPUT_TOKENS", value: "922000" }]);
  await until(async () => Boolean((await control(typesafeControl)).effective.configuration_error));
  const beforeInvalid = (await fixture()).exchanges.length;
  const rejectedConfiguration = await callTool(typesafeUser, apiKey, "remember", { target_space: "team_shared", idempotency_key: `decision-${randomUUID()}`, evidence: [{ content: `${prefix} Invalid provider budget.`, source_type: "manual" }] });
  assert(rejectedConfiguration.isError || rejectedConfiguration.receipt.processing_state === "failed");
  assert.equal((await fixture()).exchanges.length, beforeInvalid, "invalid provider budgets must fail before HTTP dispatch");
  const invalidLogs = spawnSync("docker", ["logs", childID], { encoding: "utf8", maxBuffer: 32 * 1024 * 1024 });
  assert.equal(invalidLogs.status, 0);
  const invalidText = `${invalidLogs.stdout}\n${invalidLogs.stderr}`;
  assert(invalidText.includes("decision.configuration.invalid") && invalidText.includes("exceeds_provider_ceiling"));
  assert(!invalidText.includes("dense-mem-e2e-decision-key"));
  await control(typesafeControl, [{ key: "DECISION_MAX_INPUT_TOKENS", value: "" }, { key: "DECISION_MAX_QUESTION_INPUT_TOKENS", value: "" }]);
  const beforeBudget = (await fixture()).exchanges.length;
  const oversized = await callTool(typesafeUser, apiKey, "remember", {
    target_space: "team_shared", idempotency_key: `decision-${randomUUID()}`,
    evidence: [{ content: "These notes list one two three four five six seven eight nine ten. ".repeat(220), source_type: "manual" }],
  });
  assert.equal(oversized.isError, true);
  const budget = oversized.receipt.errors[0];
  assert.equal(budget.code, "input_budget_exceeded");
  assert.equal(budget.reason_code, "decision_input_budget_exceeded");
  assert.equal(budget.next_action, "resubmit_remember");
  assert.equal(budget.details.client_controlled, true);
  assert.equal(budget.details.unit, "tokens");
  assert.equal(budget.details.limit, typeSafeQuestionBudget);
  assert(budget.details.observed > budget.details.limit);
  assert.equal((await fixture()).exchanges.length, beforeBudget, "preflight rejects the request before Decision API dispatch");
  assert(oversized.receipt.evidence.every((item) => item.disposition === "not_stored"));
  await rpc(typesafeUser, "recall_memory", { query: `${prefix} TypeSafe`, limit: 2 });
  await verifyReranking(typesafeUser, "typesafe");
  await verifyTopicRouting(typesafeUser, typesafeControl, "typesafe");
  const exchanges = (await fixture()).exchanges;
  assert(exchanges.some((item) => item.vendor === "openai"));
  assert(exchanges.some((item) => item.vendor === "typesafe"));
  await until(async () => {
    for (const event of ["decision.task.completed", "decision.answer.validated", "decision.policy.completed", "decision.validation.regenerate"]) {
      const response = await fetch(`${controlURL}/control/api/logs?limit=1&event=${encodeURIComponent(event)}`, { headers: { Authorization: `Bearer ${token}` } });
      assert(response.ok, `operation logs HTTP ${response.status}`);
      const logs = (await response.json()).data;
      if (!logs.some((entry) => entry.message === event)) return false;
    }
    return true;
  });
  for (const event of ["decision.question.prepared", "decision.exchange.payload"]) {
    const response = await getJSON(`${controlURL}/control/api/logs?limit=1&event=${encodeURIComponent(event)}`, { headers: { Authorization: `Bearer ${token}` } });
    assert.equal(response.data.length, 0, "successful admitted request bodies must stay out of portal operation logs");
  }
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
  environment.set("AI_DECISION_MAX_QUESTION_INPUT_TOKENS", String(typeSafeQuestionBudget));
  assert(environment.get("POSTGRES_HOST") || environment.get("POSTGRES_DSN"), "private database configuration is required");
  assert(environment.get("CONTROL_PORTAL_TOKEN"), "private control configuration is required");
  assert(environment.get("AI_API_EMBEDDING_MODEL") && environment.get("AI_API_EMBEDDING_DIMENSIONS"), "embedding configuration is required");
  const network = required("DENSE_MEM_E2E_NETWORK");
  assert(Object.hasOwn(source.NetworkSettings.Networks, network), "source server must use the disposable scenario network");
  const labels = Object.entries(source.Config.Labels).filter(([key]) => key.startsWith("io.dense-mem.ci."));
  labels.push(["com.docker.compose.project", project], ["com.docker.compose.service", "decision-typesafe"], ["com.docker.compose.oneoff", "True"]);
  return docker(["create", "--name", childName, "--network", network, ...labels.flatMap(([key,value]) => ["--label",`${key}=${value}`]), ...Array.from(environment,([key,value]) => ["--env",`${key}=${value}`]).flat(), source.Image, ...(source.Config.Cmd || [])]).trim();
}

async function remember(base, suffix, fault = "") {
  const name = `${prefix} ${suffix}`;
  const request = { target_space: "team_shared", idempotency_key: `decision-${randomUUID()}`, evidence: [{ content: `${name} uses PostgreSQL.`, source_type: "manual" }] };
  if (fault) request.evidence[0].content += ` [fixture-fault:${fault}]`;
  return { request, result: await rememberWithRetry(base, apiKey, request) };
}
async function verifyTopicRouting(base, adminBase, vendor) {
  const admin = async (path, body, method = body ? "POST" : "GET") => {
    const response = await getJSON(`${adminBase}/control/api${path}`, { method, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
    return response.data;
  };
  const initial = await admin("/topic-maintenance/status");
  const saved = await admin("/config/topic-maintenance");
  const team = await admin("/teams", { name: `topic-decision-${vendor}-${randomUUID()}` });
  const actor = await admin(`/teams/${team.id}/credentials`, { name: "Topic decisions", role: "manager", scopes: ["read", "write"] });
  const store = async (content) => {
    const result = await rememberWithRetry(base, actor.api_key, { target_space: "team_shared", idempotency_key: randomUUID(), evidence: [{ content, source_type: "manual", force_insert: true }] });
    assert.equal(result.processing_state, "completed");
    assert.equal(result.evidence.length, 1);
    return result.evidence[0].evidence_id;
  };
  const generations = async () => (await getJSON(`${fixtureURL}/health`)).chat_requests.filter((call) => call.schema_name === "topic_assignment_v1").length;
  await admin("/topic-maintenance/pause", { operation_key: randomUUID() });
  try {
    await admin("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }, { key: "TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" }] }, "PATCH");
    await store("Atlas uses PostgreSQL.");
    const beforeGeneration = await generations();
    await fixture("topic-boundary");
    const start = (await fixture()).exchanges.length;
    const familiar = await store("Orion stores records in PostgreSQL.");
    assert.equal(await generations(), beforeGeneration, "a clear match with no uncovered subject must complete through Decisions");
    const mixed = await store("Vega keeps backups of PostgreSQL.");
    assert.equal(await generations(), beforeGeneration + 1, "a covered and an uncovered subject must request a complete structured assignment");
    await fixture("topic-uncategorized");
    const beforeUncategorized = (await fixture()).exchanges.length;
    await store("Pluto maintains PostgreSQL status dashboards.");
    const uncategorized = (await fixture()).exchanges.slice(beforeUncategorized).filter((item) => item.vendor === vendor && item.names.includes("uncovered"));
    assert.equal(uncategorized.length, 1, "uncategorized test must reach a Decision with existing candidates");
    assert.equal(await generations(), beforeGeneration + 1, "a clear uncategorized Decision must skip generation");
    await fixture("normal");
    const routed = (await fixture()).exchanges.slice(start).filter((item) => item.vendor === vendor && item.names.includes("uncovered"));
    assert.equal(routed.length, 3);
    assert(routed.every((item) => item.names.some((name) => name.startsWith("match_"))));
    if (vendor === "openai") {
      const logs = spawnSync("docker", ["compose", "-p", project, "-f", compose, "logs", "--no-color", "server"], { encoding: "utf8", maxBuffer: 32 * 1024 * 1024 });
      assert.equal(logs.status, 0);
      const records = `${logs.stdout}\n${logs.stderr}`.split("\n").flatMap((line) => {
        const start = line.indexOf("{");
        if (start < 0) return [];
        try { return [JSON.parse(line.slice(start))]; } catch { return []; }
      }).filter((entry) => entry.msg === "topic_assignment.decision_routing");
      assert(records.some((entry) => entry.selected_count > 0 && entry.uncovered_probability === 0.2 && entry.generation_triggered === false));
      assert(records.some((entry) => entry.selected_count === 0 && entry.uncovered_probability === 0.2 && entry.generation_triggered === false));
      assert(records.some((entry) => entry.uncovered_probability === 0.85 && entry.generation_triggered === true));
    }
    const recall = (args) => callTool(base, actor.api_key, "recall_memory", args).then((result) => result.receipt);
    const topics = (await recall({ query: "PostgreSQL backups", limit: 20, topic_limit: 10 })).related_topics;
    const storage = topics.find((topic) => topic.name === "memory-storage");
    const backups = topics.find((topic) => topic.name === "storage-backups");
    assert(storage && backups);
    const direct = async (id) => (await recall({ topic_ids: [id], limit: 20 })).results.flatMap((hit) => [hit.evidence_id, ...hit.equivalent_evidence_ids]);
    const storageIDs = await direct(storage.topic_id);
    assert(storageIDs.includes(familiar) && storageIDs.includes(mixed));
    const backupIDs = await direct(backups.topic_id);
    assert(backupIDs.includes(mixed) && !backupIDs.includes(familiar));
    if (vendor === "openai") {
      const record = async (id) => (await getJSON(`${base}/ui/api/topics/records/${id}`, {
        headers: { Authorization: `Bearer ${actor.api_key}` },
      })).data;
      const communityGenerations = async () => (await getJSON(`${fixtureURL}/health`)).chat_requests.filter((call) => call.schema_name === "community_placement_v1").length;
      const beforePlacement = await communityGenerations();
      const startPlacement = (await fixture()).exchanges.length;
      await fixture("community-partial-outage");
      try {
        await admin("/topic-maintenance/resume", { operation_key: randomUUID() });
        await admin("/topic-maintenance/runs", { operation_key: randomUUID(), max_batches: 20 });
        await until(async () => {
          const [parent, child] = await Promise.all([record(storage.topic_id), record(backups.topic_id)]);
          return parent.community_placement === "failed" && child.community_placement === "placed"
            && child.community_parent_id === storage.topic_id;
        }, 150_000);
        assert.equal(await communityGenerations(), beforePlacement, "a clear parent choice and a required provider outage must both skip generation");
        const placements = (await fixture()).exchanges.slice(startPlacement).filter((item) => item.vendor === vendor && item.names.includes("parent"));
        assert(placements.length >= 2);
      } finally {
        await admin("/topic-maintenance/pause", { operation_key: randomUUID() });
        await fixture("normal");
      }
    }
  } finally {
    await admin("/config/topic-maintenance", { items: saved.items.map(({ key, value }) => ({ key, value })) }, "PATCH");
    if (!initial.paused) await admin("/topic-maintenance/resume", { operation_key: randomUUID() });
  }
}

async function verifyReranking(base, vendor) {
  for (const frontier of ["evidence"]) {
    await fixture("normal");
    const args = { query: prefix, limit: 3 };
    const original = await rpc(base, "recall_memory", args);
    const field = "results";
    const id = "evidence_id";
    assert(original[field].length >= 2, `${vendor} ${frontier} needs distinguishable authorized candidates`);
    const preferred = original[field].at(-1)[id];
    assert.notEqual(preferred, original[field][0][id]);
    const start = (await fixture()).exchanges.length;
    await getJSON(`${fixtureURL}/decision-fixture`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: "ranking", preferred_ids: [preferred] }) });
    const ranked = await rpc(base, "recall_memory", { ...args, ...(frontier === "evidence" ? { limit: 1 } : {}) });
    assert.equal(ranked[field][0][id], preferred, `${vendor} ${frontier} must apply relevance scores before final selection`);
    if (frontier === "evidence") assert.equal(ranked.results.length, 1);
    const scoring = (await fixture()).exchanges.slice(start);
    assert(scoring.some((item) => item.vendor === vendor && item.names.some((name) => name.startsWith("evidence_")) && item.types.includes("score")), `${vendor} ${frontier} must reach the score endpoint`);
    await fixture("429");
    const degraded = await rpc(base, "recall_memory", args);
    assert.deepEqual(degraded[field].map((item) => item[id]), original[field].map((item) => item[id]), `${vendor} ${frontier} failure must retain original ranking`);
    assert(degraded.degradations.some((item) => item.code === "decision_reranking_unavailable" && item.frontier === frontier && item.optional === true));
    assert((await fixture()).exchanges.slice(start).some((item) => item.vendor === vendor && item.mode === "429" && item.types.includes("score")));
  }
  await fixture("normal");
}
async function rpc(base, name, arguments_) {
  return (await callTool(base, apiKey, name, arguments_)).receipt;
}
async function control(base, items) {
  const response = await fetch(`${base}/control/api/config/decisions`, { method: items ? "PATCH" : "GET", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, ...(items ? { body: JSON.stringify({ items }) } : {}) });
  assert(response.ok, `decision control HTTP ${response.status}`); return (await response.json()).data;
}
async function fixture(value) { return getJSON(`${fixtureURL}/decision-fixture`, value ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: value }) } : undefined); }
async function getJSON(url, init) { const response = await fetch(url, init); assert(response.ok, `fixture HTTP ${response.status}`); return response.json(); }
async function until(check, timeoutMS = 30_000) { const deadline = Date.now() + timeoutMS; let cause; while (Date.now() < deadline) { try { if (await check()) return; } catch (error) { cause = error; } await new Promise((resolve) => setTimeout(resolve, 250)); } throw new Error("decision scenario condition did not complete", { cause }); }
function docker(args) { const result = spawnSync("docker", args, { encoding: "utf8", maxBuffer: 2 * 1024 * 1024 }); assert.equal(result.status, 0, "decision test container operation failed"); return result.stdout; }
function required(name) { const value = process.env[name]; assert(value, `${name} is required`); return value.replace(/\/$/, ""); }
