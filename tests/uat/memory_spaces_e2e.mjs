#!/usr/bin/env node

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const telemetryToken = requiredEnv("DENSE_MEM_E2E_TELEMETRY_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const scenario = process.env.DENSE_MEM_E2E_MEMORY_SCENARIO || "credential_memory_binding";
let rpcID = 0;

const created = [];
const privateRead = await createCredential(teamID, "E2E private read", { scopes: ["read"] });
const sharedRead = await createCredential(teamID, "E2E shared read", { scopes: ["read"] });
const sharedWrite = await createCredential(teamID, "E2E shared write", { scopes: ["read", "write"] });
const privateWrite = await createCredential(teamID, "E2E private write", { scopes: ["read", "write"] });
created.push(privateRead, sharedRead, sharedWrite, privateWrite);

const listedBeforeLifecycle = await controlJSON(`/teams/${teamID}/credentials?limit=100&offset=0`);
const listedIDs = new Set((listedBeforeLifecycle.data ?? []).map((item) => item.id));
for (const item of [privateRead, sharedRead, privateWrite]) {
  assert(listedIDs.has(item.credential.id), `created credential ${item.credential.id} was missing from the team collection`);
}
assert(new Set([privateRead.credential.id, sharedRead.credential.id, privateWrite.credential.id]).size === 3, "multiple credentials did not retain distinct IDs");

for (const item of created) {
  assert(item.credential.memory_access.private.read && item.credential.memory_access.private.write, "credential lacks inherent private access");
  assert(item.credential.memory_access.team_shared.write === item.credential.scopes.includes("write"), "shared capability differs from current scopes");
}

if (scenario === "credential_memory_binding") {
  const expired = await createCredential(teamID, "expired credential", {
    role: "member", scopes: ["read", "write"], expires_at: new Date(Date.now() - 3600000).toISOString(),
  });
  assert(Object.values(expired.credential.memory_access).every((access) => !access.read && !access.write), "expired creation returned enabled access");
  const renewed = await rotateCredential(teamID, expired.credential.id);
  assert(renewed.credential.id === expired.credential.id, "expired rotation changed credential identity");
  assert(Object.values(renewed.credential.memory_access).every((access) => access.read && access.write), "expired rotation returned disabled access");
  const renewedCatalog = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${renewed.apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }),
  });
  assert(Array.isArray(renewedCatalog.result?.tools), "renewed credential could not authenticate");
  for (const [patch, sharedWritable] of [
    [{ scopes: ["read"] }, false],
    [{ scopes: ["read", "write"] }, true],
    [{ scopes: ["read"] }, false],
    [{ role: "manager" }, true],
    [{ role: "member" }, true],
  ]) {
    const updated = await controlJSON(`/teams/${teamID}/credentials/${sharedWrite.credential.id}`, {
      method: "PATCH", body: JSON.stringify(patch),
    });
    assert(updated.data.memory_access.private.write, "scope/role update lost private access");
    assert(updated.data.memory_access.team_shared.write === sharedWritable, "scope/role PATCH returned stale shared-write access");
  }
  await assertTeamReadOnlyPrivateWrite(privateRead.apiKey, sharedRead.apiKey);
  for (const binding of [null, "", "credential_private", "profile_private", "shared_only"]) {
    const rejected = await fetch(`${controlURL}/control/api/teams/${teamID}/credentials`, {
      method: "POST", headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
      body: JSON.stringify({ name: "obsolete-binding", scopes: ["read"], memory_binding: binding }),
    });
    assert(rejected.status >= 400 && rejected.status < 500, "obsolete memory_binding was accepted");
    assert((await rejected.text()).includes("memory_binding"), "obsolete binding error was not actionable");
  }
  const beforeRotation = rememberInput("Writable key privately remembers a workflow detail.", "writable key", "remembers", "workflow detail", "private");
  const remembered = await mcpSuccess("remember", beforeRotation, privateWrite.apiKey);
  assert(remembered.space_kind === "private", "explicit private target was not honored");
  const conflict = await mcpCall("remember", { ...beforeRotation, target_space: "team_shared" }, privateWrite.apiKey);
  assert(conflict.error || conflict.result?.isError, "changed target reused an idempotency key");
  const rotated = await rotateCurrentCredential(privateWrite.apiKey);
  assert(rotated.credential.id === privateWrite.credential.id, "rotation changed credential identity");
  assert(rotated.credential.memory_access.private.write, "rotation lost private access");
  const oldCredentialAccess = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${privateWrite.apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }),
  });
  assert(oldCredentialAccess.status >= 401 && oldCredentialAccess.status < 500, "old secret remained valid after current-key rotation");
  await waitForRecall("workflow detail", beforeRotation.evidence[0].content, rotated.apiKey);
  await revokeCredential(teamID, privateWrite.credential.id);
  const listedAfterRevoke = await controlJSON(`/teams/${teamID}/credentials?limit=100&offset=0`);
  const afterRevokeIDs = new Set((listedAfterRevoke.data ?? []).map((item) => item.id));
  assert(afterRevokeIDs.has(privateRead.credential.id), "revoking one credential removed another credential from the collection");
  assert(afterRevokeIDs.has(sharedRead.credential.id), "revoking one credential removed the shared-only credential from the collection");
  const denied = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${rotated.apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 99, method: "tools/list", params: {} }),
  });
  assert(denied.status >= 401 && denied.status < 500, "revoked credential remained usable");
} else if (scenario === "space_aware_recall") {
  const sharedNeedle = "memory space e2e mentions team shared evidence sentinel";
  const sharedSubmission = await mcpSuccess("remember", rememberInput(sharedNeedle, "memory space e2e", "mentions", "sentinel"), sharedWrite.apiKey);
  assert(sharedSubmission.processing_state === "completed", `shared Remember did not complete: ${JSON.stringify(sharedSubmission)}`);
  const metricsBeforeRecall = await readPrometheusMetrics();
  const recall = await waitForRecall(sharedNeedle, sharedNeedle, sharedWrite.apiKey);
  const metricsAfterRecall = await readPrometheusMetrics();
  for (const [operation, stage] of [
    ["search_contract", "contract"],
    ["evidence_recall", "full_text"],
    ["evidence_recall", "total"],
  ]) {
    const labels = { operation, stage, outcome: "success" };
    assert(
      readReadStageCount(metricsAfterRecall, labels) > readReadStageCount(metricsBeforeRecall, labels),
      `${operation}/${stage} read metrics did not increase after production Recall`,
    );
  }
  assert(!metricsAfterRecall.includes(sharedNeedle), "Prometheus exposition contained Recall query text");
  assert((recall.results ?? []).some((item) => item.context?.includes(sharedNeedle)), "team-shared recall positive control did not return the seeded evidence");
  for (const item of recall.results ?? []) assert(item.space_kind === "team_shared", `team-shared result lacked its space label: ${JSON.stringify(item)}`);
  const privateRecall = await waitForRecall(sharedNeedle, sharedNeedle, privateRead.apiKey);
  assert((privateRecall.results ?? []).some((item) => item.context?.includes(sharedNeedle)), "credential-private recall did not retain the authorized team-shared branch");
  for (const item of privateRecall.results ?? []) assert(item.space_kind === "team_shared", "private-bound read-only recall exposed an unexpected private result");
  const repeat = await mcpSuccess("recall_memory", { query: sharedNeedle, limit: 20 }, privateRead.apiKey);
  assert(JSON.stringify(privateRecall.results ?? []) === JSON.stringify(repeat.results ?? []), "space-aware recall was not deterministic");
} else if (scenario === "memory_space_isolation") {
  const otherTeam = await createTeam("E2E isolated team");
  const other = await createCredential(otherTeam.id, "isolated key", { scopes: ["read", "write"] });
  created.push(other);
  const isolatedNeedle = "isolated team mentions sentinel";
  const isolatedSubmission = await mcpSuccess("remember", rememberInput(isolatedNeedle, "isolated team", "mentions", "sentinel", "private"), other.apiKey);
  assert(isolatedSubmission.processing_state === "completed", `isolated Remember did not complete: ${JSON.stringify(isolatedSubmission)}`);
  await waitForRecall("isolated team sentinel", isolatedNeedle, other.apiKey);
  const recall = await mcpSuccess("recall_memory", { query: "isolated team sentinel", limit: 20 }, apiKey);
  assert(!(recall.results ?? []).some((item) => item.context?.includes(isolatedNeedle)), "cross-team evidence leaked into recall");
} else if (scenario === "memory_space_backfill") {
  const readiness = await fetch(`${userURL}/ready`);
  assert(readiness.ok, "memory-space migration did not leave the server ready");
  const listed = await controlJSON(`/teams/${teamID}/credentials?limit=100&offset=0`);
  assert((listed.data ?? []).every((item) => item.memory_access?.private?.write), "private capabilities were missing after migration");
} else {
  throw new Error(`unknown memory-space scenario ${scenario}`);
}

console.log(JSON.stringify({ status: "ok", scenario, explicit_destinations: true, labels_and_isolation: true }, null, 2));

async function assertTeamReadOnlyPrivateWrite(key, otherKey) {
  const request = rememberInput("Automation privately remembers an encountered workflow failure.", "automation", "remembers", "workflow failure", "private");
  const result = await mcpSuccess("remember", request, key);
  assert(result.processing_state === "completed" && result.space_kind === "private", "team-read-only key could not remember privately");
  await waitForRecall("workflow failure", request.evidence[0].content, key);
  const foreign = await mcpSuccess("recall_memory", { query: "workflow failure", limit: 20 }, otherKey);
  assert(!(foreign.results ?? []).some((item) => item.context?.includes(request.evidence[0].content)), "private automation memory leaked to another credential");
  const denied = await mcpCall("remember", { ...request, idempotency_key: "denied-shared:" + Date.now(), target_space: "team_shared" }, key);
  assert(denied.result?.isError || denied.error, "team-read-only key wrote shared memory");
  const missing = { ...request, idempotency_key: "missing-target:" + Date.now() };
  delete missing.target_space;
  const missingResult = await mcpCall("remember", missing, key);
  assert(missingResult.result?.isError || missingResult.error, "missing destination was silently selected");
}

async function readPrometheusMetrics() {
  const response = await fetch(`${controlURL}/metrics`, { headers: { Authorization: `Bearer ${telemetryToken}` } });
  assert(response.status === 200, `authenticated Prometheus scrape returned ${response.status}`);
  return response.text();
}

function readReadStageCount(body, expected) {
  for (const line of body.split("\n")) {
    if (!line.startsWith("densemem_read_stage_duration_seconds_count{")) continue;
    const labelsText = line.slice(line.indexOf("{") + 1, line.indexOf("}"));
    const labels = Object.fromEntries([...labelsText.matchAll(/([a-z_]+)="([^"]*)"/g)].map((match) => [match[1], match[2]]));
    if (Object.entries(expected).every(([key, value]) => labels[key] === value)) {
      return Number(line.slice(line.indexOf("}") + 1).trim());
    }
  }
  return 0;
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

async function rotateCurrentCredential(key) {
  const body = await httpJSON(`${userURL}/ui/api/credential/rotate`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" },
    body: JSON.stringify({}),
  });
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

async function mcpCall(name, args, key = apiKey, httpOptions = {}) {
  return httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }),
  }, httpOptions);
}

async function controlJSON(path, options = {}) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json", ...(options.headers ?? {}) },
  });
}

async function httpJSON(url, options, { allowStatus = false } = {}) {
  const response = await fetch(url, options);
  const text = await response.text();
  let body = {};
  try { body = text ? JSON.parse(text) : {}; } catch { body = { raw: text }; }
  if (!response.ok && !allowStatus) throw new Error(`HTTP ${response.status} ${url}: ${text.slice(0, 400)}`);
  return body;
}

async function waitForRecall(query, needle, key, attempts = 30, delayMs = 1000) {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const recall = await mcpSuccess("recall_memory", { query, limit: 20 }, key);
    if ((recall.results ?? []).some((item) => item.context?.includes(needle))) return recall;
    if (attempt + 1 < attempts) await new Promise((resolve) => setTimeout(resolve, delayMs));
  }
  throw new Error(`recall never returned seeded evidence: ${needle}`);
}

function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function assert(condition, message) { if (!condition) throw new Error(message); }

function rememberInput(content, subject, predicate, object, targetSpace = "team_shared") {
  return {
    target_space: targetSpace,
    idempotency_key: `memory-space-e2e:${Date.now()}:${Math.random()}`,
    evidence: [{ content, source_type: "document", source: "memory-space-e2e", source_group: "memory-space-e2e" }],
    relationships: [{
      ref: `memory-space-e2e:${Date.now()}:${Math.random()}`,
      subject: { name: subject, entity_kind: "project" },
      predicate: { proposed_key: predicate },
      object: { entity: { name: object, entity_kind: "concept" } },
      polarity: "+", evidence_indices: [0],
    }],
  };
}
