#!/usr/bin/env node

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const scenario = process.env.DENSE_MEM_E2E_MEMORY_SCENARIO || "credential_memory_binding";
let rpcID = 0;

const created = [];
const privateRead = await createCredential(teamID, "E2E private read", { scopes: ["read"], memory_binding: "credential_private" });
const sharedRead = await createCredential(teamID, "E2E shared read", { scopes: ["read"], memory_binding: "shared_only" });
const privateWrite = await createCredential(teamID, "E2E private write", { scopes: ["read", "write"], memory_binding: "credential_private" });
created.push(privateRead, sharedRead, privateWrite);

assert(privateRead.credential.memory_binding === "credential_private", "credential-private binding was not persisted");
assert(sharedRead.credential.memory_binding === "shared_only", "shared-only binding was not persisted");
assert(privateWrite.credential.memory_binding === "credential_private", "writable credential did not retain private binding");
assert(privateRead.credential.memory_space_kind === "credential_private", "private credential did not report its space kind");

if (scenario === "credential_memory_binding") {
  await assertReadOnlyCannotWrite(privateRead.apiKey);
  const rotated = await rotateCredential(teamID, privateWrite.credential.id);
  assert(rotated.credential.id === privateWrite.credential.id, "rotation changed credential identity");
  assert(rotated.credential.memory_binding === "credential_private", "rotation changed immutable binding");
  await revokeCredential(teamID, privateWrite.credential.id);
  const denied = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${rotated.apiKey}`, "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 99, method: "tools/list", params: {} }),
  });
  assert(denied.status >= 401 && denied.status < 500, "revoked credential remained usable");
} else if (scenario === "space_aware_recall") {
  const recall = await mcpSuccess("recall_memory", { query: "memory space e2e team shared query", limit: 20 }, apiKey);
  for (const item of recall.results ?? []) assert(item.space_kind === "team_shared", "team-shared result lacked its space label");
  const privateRecall = await mcpSuccess("recall_memory", { query: "memory space e2e private branch query", limit: 20 }, privateRead.apiKey);
  for (const item of privateRecall.results ?? []) assert(item.space_kind === "team_shared", "private-bound read-only recall exposed an unexpected private result");
  const repeat = await mcpSuccess("recall_memory", { query: "memory space e2e private branch query", limit: 20 }, privateRead.apiKey);
  assert(JSON.stringify(privateRecall.results ?? []) === JSON.stringify(repeat.results ?? []), "space-aware recall was not deterministic");
} else if (scenario === "memory_space_isolation") {
  const otherTeam = await createTeam("E2E isolated team");
  const other = await createCredential(otherTeam.id, "isolated key", { scopes: ["read", "write"], memory_binding: "credential_private" });
  created.push(other);
  await mcpSuccess("remember", rememberInput("isolated team mentions sentinel", "isolated team", "mentions", "sentinel"), other.apiKey);
  const recall = await mcpSuccess("recall_memory", { query: "isolated team sentinel", limit: 20 }, apiKey);
  assert(!(recall.results ?? []).some((item) => item.context?.includes("isolated team mentions sentinel")), "cross-team evidence leaked into recall");
} else if (scenario === "memory_space_backfill") {
  const readiness = await fetch(`${userURL}/ready`);
  assert(readiness.ok, "memory-space migration did not leave the server ready");
  const listed = await controlJSON(`/teams/${teamID}/credentials?limit=100&offset=0`);
  assert((listed.data ?? []).every((item) => item.memory_binding), "credential binding metadata was missing after migration");
} else {
  throw new Error(`unknown memory-space scenario ${scenario}`);
}

console.log(JSON.stringify({ status: "ok", scenario, immutable_bindings: true, labels_and_isolation: true }, null, 2));

async function assertReadOnlyCannotWrite(key) {
  const result = await mcpCall("remember", rememberInput("read-only must not write", "read-only", "mentions", "write"), key);
  assert(result.result?.isError === true || result.error, "read-only credential wrote memory");
}

async function createTeam(name) {
  return controlJSON("/teams", { method: "POST", body: JSON.stringify({ name, description: "memory-space e2e" }) }).then((body) => body.data);
}

async function createCredential(team, name, extra) {
  const body = await controlJSON(`/teams/${team}/credentials`, { method: "POST", body: JSON.stringify({ name, rate_limit: 300, ...extra }) });
  return { apiKey: body.data.api_key, credential: body.data.credential };
}

async function rotateCredential(team, id) {
  const body = await controlJSON(`/teams/${team}/credentials/${id}/rotate`, { method: "POST", body: JSON.stringify({}) });
  return { apiKey: body.data.api_key, credential: body.data.credential };
}

async function revokeCredential(team, id) {
  await controlJSON(`/teams/${team}/credentials/${id}`, { method: "DELETE" });
}

async function mcpSuccess(name, args, key) {
  const response = await mcpCall(name, args, key);
  if (response.error || response.result?.isError) {
    throw new Error(`MCP ${name} returned an error: ${JSON.stringify(response.error ?? response.result)}`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} did not return JSON content`);
  return JSON.parse(text);
}

async function mcpCall(name, args, key = apiKey) {
  return httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }),
  });
}

async function controlJSON(path, options = {}) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json", ...(options.headers ?? {}) },
  });
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  let body = {};
  try { body = text ? JSON.parse(text) : {}; } catch { body = { raw: text }; }
  if (!response.ok && response.status >= 500) throw new Error(`HTTP ${response.status} ${url}`);
  return body;
}

function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function assert(condition, message) { if (!condition) throw new Error(message); }

function rememberInput(content, subject, predicate, object) {
  const subjectStart = content.indexOf(subject);
  const predicateStart = content.indexOf(predicate);
  const objectStart = content.indexOf(object, predicateStart + predicate.length);
  return {
    evidence: [{ content, source_type: "document", source: "memory-space-e2e", source_group: "memory-space-e2e" }],
    relationships: [{
      ref: `memory-space-e2e:${Date.now()}:${Math.random()}`,
      subject: { name: subject, entity_kind: "project", span: { evidence_index: 0, start: subjectStart, end: subjectStart + subject.length } },
      predicate: { proposed_key: predicate, surface: predicate, span: { evidence_index: 0, start: predicateStart, end: predicateStart + predicate.length } },
      object: { entity: { name: object, entity_kind: "concept", span: { evidence_index: 0, start: objectStart, end: objectStart + object.length } } },
      polarity: "+", modality: "statement", supports: [{ evidence_index: 0, start: 0, end: Array.from(content).length }],
    }],
  };
}
