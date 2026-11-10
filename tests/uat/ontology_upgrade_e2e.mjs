import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { automaticRefinementAcceptance } from "./ontology_refinement_e2e.mjs";

const controlURL = process.env.DENSE_MEM_CONTROL_URL;
const controlToken = process.env.DENSE_MEM_CONTROL_TOKEN;
assert(controlURL && controlToken, "ontology upgrade requires the control endpoint");

async function control(path, body) {
  const response = await fetch(`${controlURL}/control/api${path}`, {
    method: body ? "PATCH" : "GET",
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(10000),
  });
  assert.equal(response.status, 200, `control request failed: ${path}`);
  return (await response.json()).data;
}

const initial = await control("/ontology/status");
assert.equal(initial.paused, false, "startup must restore its automatic pause");
assert.equal(initial.enabled, true, "upgrade must retain saved enablement");
assert.equal(initial.configuration_error, "configuration_invalid", "missing model capacity remains visible");
assert.equal(initial.historical_usage.unavailable_usage_attempts, 1);
assert.equal(initial.usage.unreconciled_attempts, 0);
const refreshed = JSON.parse(postgresQuery("BEGIN; SET LOCAL app.tx_mode='system'; SELECT jsonb_build_object('matching',(SELECT count(*) FROM ontology_maintenance_markers AS marker JOIN ontology_record_heads AS head ON head.team_id=marker.team_id AND head.shared_space_id=marker.shared_space_id AND head.space_generation=marker.space_generation AND head.record_id::text=marker.anchor_id WHERE marker.anchor_kind='ontology_record_heads' AND head.names && ARRAY['upgrade-matching']::text[]),'refinement',(SELECT count(*) FROM ontology_maintenance_sources WHERE source_kind IN ('entity','predicate') AND eligible AND status='pending' AND reason='organization_policy_v2' AND revision=6),'assignments',(SELECT count(*) FROM ontology_record_heads WHERE kind='assignment' AND version=1)); COMMIT;"));
assert.equal(refreshed.matching, 1, "upgrade must enqueue the preserved definition without an existing notification");
assert.equal(refreshed.refinement, 2, "upgrade must refresh organized entity and predicate queue rows exactly once");
assert.equal(refreshed.assignments, 2, "upgrade must retain both current assignment revisions");
assert.equal(initial.discovery_complete, false, "queued policy refresh must not report readiness");
const provider = await fetch("http://synchronous-write-provider:8787/health", { signal: AbortSignal.timeout(10000) });
assert.equal(provider.status, 200);
assert.equal((await provider.json()).assessment_calls, 0, "upgrade preparation must not call a model");

await control("/config/ontology-maintenance", {
  items: [
    { key: "ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" },
    { key: "ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY", value: "8" },
    { key: "ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY", value: "8" },
  ],
});
let status;
for (let attempt = 0; attempt < 120; attempt++) {
  status = await control("/ontology/status");
  if (status.counts.organized > 0 && status.usage.attempts > 0 && status.active_work === 0 && status.usage.unreconciled_attempts === 0) break;
  await new Promise((resolve) => setTimeout(resolve, 1000));
}
assert.equal(status.paused, false);
assert(status.counts.organized > 0 && status.usage.attempts > 0, "normal maintenance must progress after configuration without an upgrade command");
assert.equal(status.active_work, 0);
assert.equal(status.usage.unreconciled_attempts, 0);
await automaticRefinementAcceptance({
  control: async (path, body) => {
    const response = await fetch(`${controlURL}/control/api${path}`, { method: "POST",
      headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" }, body: JSON.stringify(body) });
    assert(response.ok, `refinement control request failed: ${path} (${response.status})`);
    return response.json();
  },
  mcp: async (name, args, key) => {
    const response = await fetch(`${process.env.DENSE_MEM_USER_URL}/mcp`, { method: "POST",
      headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name, arguments: args } }) });
    assert.equal(response.status, 200);
    const rpc = await response.json();
    assert(!rpc.error && !rpc.result?.isError, `${name}: ${JSON.stringify(rpc.error ?? rpc.result)}`);
    return JSON.parse(rpc.result.content[0].text);
  },
  postgresQuery: (sql) => postgresQuery(`BEGIN; SET LOCAL app.tx_mode='system'; ${sql}; COMMIT;`),
  fixtureMode: async (mode) => {
    const response = await fetch(`${process.env.DENSE_MEM_E2E_PROVIDER_URL}/ontology-fixture`, { method: "POST",
      headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }) });
    assert.equal(response.status, 200);
  },
  canonicalFingerprints: (teamID) => {
    assert.match(teamID, /^[0-9a-f-]{36}$/);
    const tables = ["evidence_fragments", "evidence_occurrences", "evidence_sources", "evidence_source_revisions", "entity_records", "entity_names", "value_records", "relationship_records", "relationship_evidence_supports", "relationship_support_decision_events", "evidence_lifecycle_events", "evidence_quarantines"];
    return postgresQuery(["BEGIN", "SET LOCAL app.tx_mode='system'", ...tables.map((table) => `SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'[]') FROM ${table} AS row WHERE team_id='${teamID}'::uuid`), "COMMIT"].join(";\n"));
  },
  directory: `${process.env.DENSE_MEM_E2E_REMEMBER_FAILURE_DIR || "/tmp/dense-mem-remember-failures"}/ontology-refinement-debug`,
});
console.log(JSON.stringify({ scenario: "ontology_upgrade", status: "ok", control_available: true, missing_capacity_explicit: true, automatic_progress: true }));

function postgresQuery(sql) {
  const result = spawnSync("docker", ["compose", "-p", process.env.DENSE_MEM_E2E_COMPOSE_PROJECT, "-f", process.env.DENSE_MEM_E2E_COMPOSE_FILE,
    "exec", "-T", "postgres", "sh", "-ec", 'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "ontology-upgrade-e2e", sql], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}
