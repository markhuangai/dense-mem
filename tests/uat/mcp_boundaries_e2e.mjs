#!/usr/bin/env node

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");

const feedbackTool = "submit_recall_session_feedback";
const dreamTools = ["list_dreams", "get_dream", "resolve_dream_feedback"];
const activeEvalTools = ["eval_list_knowledge_refs", "eval_run_dream_cycle", "eval_run_recall_case"];
const removedTools = [
  "correct_entity_resolution",
  "eval_get_manifest",
  "eval_get_knowledge_item",
  "eval_list_recall_feedback_events",
  "eval_get_recall_feedback_event",
  "eval_score_retrieval_case",
];
let rpcID = 0;

await updateRecallFeedback(false);
await updateDreaming(true, false);
await updateTeamDreaming(false);

let names = await listedToolNames();
for (const required of ["remember", "get_submission_status", "retract_evidence", "correct_relationship", "recall_memory", "trace_memory", "export_memory_pack"]) {
  assertHas(names, required, "production core tool");
}
for (const absent of [feedbackTool, ...dreamTools, ...activeEvalTools, ...removedTools]) {
  assertMissing(names, absent, "disabled or non-production tool");
}
for (const hidden of [feedbackTool, ...dreamTools, ...activeEvalTools, ...removedTools]) {
  await assertToolNotFound(hidden, {});
}

await updateRecallFeedback(true);
names = await listedToolNames();
assertHas(names, feedbackTool, "enabled recall feedback tool");
const recall = await mcpSuccess("recall_memory", { query: "MCP boundary e2e query with no required match", limit: 1 });
const feedbackAction = (recall.suggested_actions ?? []).find((item) => item?.tool === feedbackTool);
if (!feedbackAction || feedbackAction.recall_event_id !== recall.recall_id) {
  throw new Error(`recall did not suggest feedback with its persisted recall ID: ${JSON.stringify(recall.suggested_actions)}`);
}
if ((recall.suggested_actions ?? []).some((item) => item?.tool === "resolve_dream_feedback")) {
  throw new Error("recall suggested Dream feedback while team Dreaming was disabled");
}

await updateTeamDreaming(true);
names = await listedToolNames();
for (const tool of dreamTools) assertHas(names, tool, "team-enabled Dream tool");

await updateTeamDreaming(false);
await updateDreaming(true, true);
names = await listedToolNames();
for (const tool of dreamTools) assertHas(names, tool, "force-enabled Dream tool");

await updateTeamDreaming(true);
await updateDreaming(false, false);
names = await listedToolNames();
for (const tool of dreamTools) assertMissing(names, tool, "globally disabled Dream tool");
await assertToolNotFound("resolve_dream_feedback", {});

console.log(JSON.stringify({
  status: "ok",
  production_eval_tools_absent: true,
  removed_tools_absent: true,
  recall_feedback_gate: true,
  recall_feedback_hint: true,
  team_dreaming_gate: true,
  force_dreaming_gate: true,
  global_dreaming_disable: true,
}, null, 2));

async function listedToolNames() {
  const response = await rpc("tools/list", {});
  if (response.error || !response.result) {
    throw new Error("tools/list returned an error");
  }
  return new Set((response.result.tools ?? []).map((tool) => tool.name));
}

async function mcpSuccess(name, args) {
  const response = await rpc("tools/call", { name, arguments: args });
  if (response.error || response.result === undefined) {
    throw new Error(`MCP ${name} returned an error`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} did not return JSON content`);
  }
  return JSON.parse(text);
}

async function assertToolNotFound(name, args) {
  const response = await rpc("tools/call", { name, arguments: args });
  if (response.error?.code !== -32601 || response.result !== undefined) {
    throw new Error(`hidden tool ${name} was callable: ${JSON.stringify(response)}`);
  }
}

async function rpc(method, params) {
  return httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }),
  });
}

async function updateRecallFeedback(enabled) {
  await controlJSON("/config/recall-feedback", {
    method: "PATCH",
    body: JSON.stringify({ items: [
      { key: "RECALL_FEEDBACK_ENABLED", value: String(enabled) },
      { key: "RECALL_FEEDBACK_RETENTION_DAYS", value: "30" },
    ] }),
  });
}

async function updateDreaming(enabled, forceEnabled) {
  await controlJSON("/config/dreaming", {
    method: "PATCH",
    body: JSON.stringify({ items: [
      { key: "DREAMING_ENABLED", value: String(enabled) },
      { key: "DREAMING_FORCE_ENABLED", value: String(forceEnabled) },
      { key: "DREAMING_START_TIME_LOCAL", value: "03:00" },
      { key: "DREAMING_MAX_OUTPUTS", value: "5" },
    ] }),
  });
}

async function updateTeamDreaming(enabled) {
  await controlJSON(`/teams/${teamID}`, {
    method: "PATCH",
    body: JSON.stringify({ config: { dreaming: { enabled } } }),
  });
}

async function controlJSON(path, options) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
  });
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: response body redacted`);
  }
  return text ? JSON.parse(text) : {};
}

function assertHas(names, name, label) {
  if (!names.has(name)) throw new Error(`${label} ${name} is missing`);
}

function assertMissing(names, name, label) {
  if (names.has(name)) throw new Error(`${label} ${name} is exposed`);
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
