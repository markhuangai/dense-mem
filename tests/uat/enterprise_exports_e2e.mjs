#!/usr/bin/env node
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { randomUUID, createHash } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { assertTerminalRememberResult } from "./synchronous_write/surface.mjs";

if (process.env.DENSE_MEM_E2E_EXPORT_PERFORMANCE === "1") {
  await (await import("../eval/scripts/enterprise_exports_performance.mjs")).measure();
  process.exit(0);
}

const required = (name) => { assert.ok(process.env[name], `${name} is required`); return process.env[name]; };
const userURL = required("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = required("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = required("DENSE_MEM_CONTROL_TOKEN");
const apiKey = required("DENSE_MEM_E2E_API_KEY");
const teamID = required("DENSE_MEM_E2E_TEAM_ID");
const project = required("DENSE_MEM_E2E_COMPOSE_PROJECT");
assert.match(project, /^densemem-ci-[a-z0-9-]+$/);
const composeFiles = [required("DENSE_MEM_E2E_COMPOSE_FILE"), required("DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE")];
const composeArgs = ["compose", ...composeFiles.flatMap((file) => ["-f", file]), "-p", project];
const docker = (args) => { const result = spawnSync("docker", args, { encoding: "utf8", timeout: 30000, maxBuffer: 8 << 20 }); assert.equal(result.status, 0, `Docker operation ${args[0]} failed`); return result.stdout.trim(); };
const serviceID = (service) => { const value = docker([...composeArgs, "ps", "-q", service]); assert.match(value, /^[a-f0-9]{12,64}$/); return value; };
const collectorID = serviceID("enterprise-collector");
const serverID = serviceID("server");
const inspected = JSON.parse(docker(["inspect", serverID]))[0];
const canary = "enterprise-export-evidence-content-canary";
const prohibited = [canary, apiKey, controlToken, "private-erasure-", "export-correlation-content-canary"];
let rpcID = 0;
const scan = (raw) => { for (const value of prohibited) assert.ok(!raw.includes(value), "prohibited content reached an external export"); };
async function controlRaw(endpoint, options = {}, token = controlToken) {
  return fetch(`${controlURL}/control/api${endpoint}`, { ...options, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", ...options.headers }, signal: options.signal || AbortSignal.timeout(10000) });
}
async function controlJSON(endpoint, options = {}) {
  const response = await controlRaw(endpoint, { ...options, body: options.body ? JSON.stringify(options.body) : undefined });
  assert.ok(response.ok, `control operation failed with HTTP ${response.status}`); return (await response.json()).data;
}
async function tool(name, args, { allowError = false, key = apiKey, signal } = {}) {
  const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json", "X-Correlation-ID": "export-correlation-content-canary" }, body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }), signal: signal || AbortSignal.timeout(15000) });
  assert.equal(response.status, 200); const body = await response.json(); assert.ok(!body.error, "MCP returned an unexpected protocol error");
  if (!allowError) assert.ok(body.result && !body.result.isError, "MCP operation failed");
  return body.result.structuredContent;
}
function rememberArguments(label, fault = "") {
  return { idempotency_key: `exports-${label}`, evidence: [{ content: `Dense-Mem stores durable memory in PostgreSQL. ${canary} ${fault}`, source_type: "manual", source_key: `exports:${label}`, source_revision: "1" }], relationships: [{ ref: "durable-store", subject: { name: "Dense-Mem", entity_kind: "project" }, predicate: { proposed_key: "stores_memory_in" }, object: { value: { type: "string", value: "PostgreSQL" } }, polarity: "+", evidence_indices: [0] }] };
}

for (const endpoint of ["/audit/export", "/diagnostics/bundle"]) {
  for (const token of [apiKey, "revoked-session-canary"]) {
    const denied = await controlRaw(endpoint, {}, token); assert.equal(denied.status, 401); scan(await denied.text());
  }
}
const second = await controlJSON("/teams", { method: "POST", body: { name: "exports-second-team-content-canary" } });
prohibited.push("exports-second-team-content-canary");
const manager = await controlJSON(`/teams/${teamID}/credentials`, { method: "POST", body: { name: "exports-team-manager", role: "manager", scopes: ["read", "write"] } });
prohibited.push(manager.api_key);
for (const endpoint of ["/audit/export", "/diagnostics/bundle"]) assert.equal((await controlRaw(endpoint, {}, manager.api_key)).status, 401);
const seed = rememberArguments("seed");
const remembered = assertTerminalRememberResult(await tool("remember", seed));
assert.equal(remembered.processing_state, "completed");
const relationshipID = remembered.relationship_results[0].splits[0].relationship_id;
const trace = await tool("trace_memory", { relationship_id: relationshipID }); assert.equal(trace.relationship.relationship_id, relationshipID);
await tool("recall_memory", { query: "Dense-Mem durable memory PostgreSQL" });
const failed = assertTerminalRememberResult(await tool("remember", rememberArguments("provider-failure", "[fixture-fault:unavailable]"), { allowError: true }));
assert.equal(failed.processing_state, "failed"); assert.ok(failed.errors.length > 0);
const foreign = await controlJSON(`/teams/${second.id}/credentials`, { method: "POST", body: { name: "exports-foreign", scopes: ["read", "write"] } });
prohibited.push(foreign.api_key);
const deniedTrace = await tool("trace_memory", { relationship_id: relationshipID }, { key: foreign.api_key, allowError: true });
assert.ok(deniedTrace.code || deniedTrace.errors?.length);
assert.ok(!JSON.stringify(deniedTrace).includes(canary), "foreign-team denial exposed evidence content");

let cursor = "";
const events = [];
for (let page = 0; page < 100; page++) {
  const response = await controlRaw(`/audit/export?limit=1${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`);
  assert.equal(response.status, 200); const raw = await response.text(); scan(raw); assert.ok(Buffer.byteLength(raw) <= (1 << 20));
  const lines = raw.trim().split("\n").map(JSON.parse); const checkpoint = lines.at(-1);
  assert.equal(checkpoint.type, "checkpoint"); assert.equal(checkpoint.version, 1); assert.ok(checkpoint.events <= 1);
  for (const event of lines.slice(0, -1)) { assert.equal(event.type, "event"); assert.ok(!Object.hasOwn(event, "metadata")); assert.ok(!Object.hasOwn(event, "after_payload")); events.push(event); }
  cursor = checkpoint.cursor; if (!checkpoint.more) break;
}
assert.equal(new Set(events.map((event) => event.id)).size, events.length);
assert.ok(events.some((event) => event.team_id === teamID)); assert.ok(events.some((event) => event.team_id === second.id));
const scoped = await controlRaw(`/audit/export?team_id=${teamID}`); const scopedRaw = await scoped.text(); scan(scopedRaw);
const scopedLines = scopedRaw.trim().split("\n").map(JSON.parse);
assert.ok(scopedLines.slice(0, -1).every((event) => event.team_id === teamID));
assert.equal((await controlRaw(`/audit/export?team_id=${second.id}&cursor=${encodeURIComponent(scopedLines.at(-1).cursor)}`)).status, 422);
assert.equal((await controlRaw("/audit/export?limit=1001")).status, 422);
const cancelled = new AbortController(); const download = controlRaw("/audit/export", { signal: cancelled.signal }); cancelled.abort(); await assert.rejects(download);
assert.equal((await controlRaw("/audit/export")).status, 200);

const privateCase = spawnSync(process.execPath, [new URL("./private_memory_erasure_e2e.mjs", import.meta.url).pathname], { env: process.env, encoding: "utf8", timeout: 300000, maxBuffer: 8 << 20 });
assert.equal(privateCase.status, 0, "private erasure and legal-hold production regressions failed");
const postgresID = serviceID("postgres");
function sql(statement) {
  const result = spawnSync("docker", ["exec", "-i", postgresID, "sh", "-ec", 'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -At'], { input: statement, encoding: "utf8", timeout: 10000 });
  assert.equal(result.status, 0, "export authorization fixture SQL failed"); return result.stdout.trim();
}
const [providerID, identityID] = sql("BEGIN; SELECT set_config('app.tx_mode','system',true); SELECT provider_id,id FROM sso_identities WHERE active ORDER BY id LIMIT 1; COMMIT;").split("\n").find((line) => /^[0-9a-f-]{36}\|[0-9a-f-]{36}$/.test(line)).split("|");
const group = "enterprise-export-control-admins";
const adminGroup = await controlJSON(`/sso/providers/${providerID}/control-admin-groups`, { method: "POST", body: { group_id: group, group_name: group, enabled: true } });
const sessionToken = `enterprise-export-control-session-${randomUUID()}`;
const managerToken = `enterprise-export-manager-session-${randomUUID()}`;
prohibited.push(sessionToken, managerToken);
const hash = (value) => createHash("sha256").update(value).digest("hex");
sql(`BEGIN; SELECT set_config('app.tx_mode','system',true);
  UPDATE team_memberships SET team_admin=true WHERE actor_identity_id='${identityID}'::uuid;
  INSERT INTO sso_control_sessions(session_hash,identity_id,provider_id,group_ids,csrf_hash,expires_at)
  VALUES ('${hash(sessionToken)}','${identityID}','${providerID}',ARRAY['${group}'],'${hash("csrf")}',now()+interval '1 hour'),
         ('${hash(managerToken)}','${identityID}','${providerID}',ARRAY['ordinary-team-manager'],'${hash("csrf")}',now()+interval '1 hour'); COMMIT;`);
for (const endpoint of ["/audit/export", "/diagnostics/bundle"]) {
  const admin = await controlRaw(endpoint, { headers: { Cookie: `dense_mem_control_session=${sessionToken}` } }, "");
  assert.equal(admin.status, 200); scan(await admin.text());
  assert.equal((await controlRaw(endpoint, { headers: { Cookie: `dense_mem_control_session=${managerToken}` } }, "")).status, 401);
}
await controlJSON(`/sso/providers/${providerID}/control-admin-groups/${adminGroup.id}`, { method: "PATCH", body: { group_id: group, group_name: group, enabled: false } });
for (const endpoint of ["/audit/export", "/diagnostics/bundle"]) assert.equal((await controlRaw(endpoint, { headers: { Cookie: `dense_mem_control_session=${sessionToken}` } }, "")).status, 401);

const temporary = fs.mkdtempSync(path.join(os.tmpdir(), "enterprise-exports-"));
let disabledID = "";
try {
  const disabledName = `${project.slice(0, 46)}-exports-disabled`;
  const environment = inspected.Config.Env.filter((entry) => !/^(OTLP_ENABLED|AUDIT_EXPORT_ENABLED|DIAGNOSTIC_BUNDLE_ENABLED)=/.test(entry));
  environment.push("OTLP_ENABLED=false", "AUDIT_EXPORT_ENABLED=false", "DIAGNOSTIC_BUNDLE_ENABLED=false");
  const labels = Object.fromEntries(Object.entries(inspected.Config.Labels).filter(([name]) => name.startsWith("io.dense-mem.ci.")));
  labels["io.dense-mem.ci.compose-project"] = project;
  disabledID = docker(["create", "--name", disabledName, "--network", `${project}_ci`, ...Object.entries(labels).flatMap(([name, value]) => ["--label", `${name}=${value}`]), ...environment.flatMap((entry) => ["--env", entry]), inspected.Image]);
  docker(["start", disabledID]);
  let ready = false;
  for (let attempt = 0; attempt < 45; attempt++) { try { ready = (await fetch(`http://${disabledName}:8080/ready`, { signal: AbortSignal.timeout(2000) })).ok; } catch {} if (ready) break; await delay(1000); }
  assert.ok(ready, "disabled export instance did not become ready");
  for (const endpoint of ["audit/export", "diagnostics/bundle"]) assert.equal((await fetch(`http://${disabledName}:8090/control/api/${endpoint}`, { headers: { Authorization: `Bearer ${controlToken}` } })).status, 404);
  const before = await controlRaw("/diagnostics/bundle"); const beforeRaw = await before.text(); scan(beforeRaw); const bundle = JSON.parse(beforeRaw);
  assert.equal(bundle.version, 1); assert.ok(bundle.config_presence.audit_export); assert.ok(bundle.exporters.traces.enabled); assert.ok(Buffer.byteLength(beforeRaw) <= (1 << 20));
  let payload = "";
  for (let attempt = 0; attempt < 40; attempt++) {
    const destination = path.join(temporary, "telemetry.json");
    const copied = spawnSync("docker", ["cp", `${collectorID}:/telemetry.json`, destination], { encoding: "utf8", timeout: 10000 });
    if (copied.status === 0) { payload = fs.readFileSync(destination, "utf8"); if (payload.includes("resourceMetrics") && payload.includes("resourceSpans")) break; }
    await delay(2000);
  }
  scan(payload); assert.ok(payload.includes("resourceMetrics") && payload.includes("resourceSpans"), "Collector did not receive both signals");
  for (const name of ["mcp.remember", "mcp.recall_memory", "mcp.trace_memory"]) assert.ok(payload.includes(name), `missing ${name} span`);
  assert.ok(payload.includes("densemem_mcp_tool_results_total"));
  for (const name of ["team_id", "profile_id", "credential_id", "exception.message", "baggage"]) assert.ok(!payload.includes(`"key":"${name}"`) && !payload.includes(`"key": "${name}"`), "unapproved external attribute");
  // Rootless CI lacks a cgroup freezer; stop the Collector process instead.
  docker(["kill", "--signal=SIGSTOP", collectorID]);
  try {
    const start = Date.now();
    await Promise.all(Array.from({ length: 8 }, () => tool("remember", seed)));
    await Promise.all(Array.from({ length: 8 }, () => tool("trace_memory", { relationship_id: relationshipID })));
    assert.ok(Date.now() - start < 10000, "slow Collector blocked memory operations");
    const healthDeadline = Date.now() + 12000;
    let degraded;
    do {
      await delay(500);
      degraded = JSON.parse(await (await controlRaw("/diagnostics/bundle")).text());
      if (degraded.exporters.traces.degraded) break;
    } while (Date.now() < healthDeadline);
    assert.ok(degraded.exporters.traces.degraded); assert.ok(degraded.exporters.traces.failures > 0); assert.ok(degraded.exporters.pending_spans <= 2048);
  } finally { docker(["kill", "--signal=SIGCONT", collectorID]); }
  await tool("trace_memory", { relationship_id: relationshipID });
} finally {
  if (disabledID) docker(["rm", "--force", disabledID]);
  for (const entry of fs.readdirSync(temporary)) fs.unlinkSync(path.join(temporary, entry));
  fs.rmdirSync(temporary);
}
console.log(JSON.stringify({ status: "ok", collector_digest: "sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6", static_control_auth: true, ordinary_manager_denied: true, prohibited_content_absent: true, instance_and_team_export: true, cursor_scope_bound: true, bounded_pages: true, cancellation: true, disabled_endpoints: true, collector_outage_nonblocking: true, private_erasure_and_legal_hold: true }));
