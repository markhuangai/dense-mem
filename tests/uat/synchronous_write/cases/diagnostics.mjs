import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { writeFile } from "node:fs/promises";

export const name = "diagnostics";

export async function run({ rpc, expect }) {
  const attempts = {};
  const idempotencyKeys = {};
  for (const [label, marker, expectedState, expectedCode] of [
    ["completed", "", "completed", ""],
    ["rejected", "[fixture-fault:no-supported]", "rejected", "no_supported_memory"],
    ["quarantined", "[fixture-fault:security]", "quarantined", "submission_quarantined"],
    ["failed", "[fixture-fault:unavailable]", "failed", "provider_unavailable"],
  ]) {
    const request = rememberArguments(label, marker);
    const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: request.payload }));
    expect(result?.processing_state === expectedState, `${label} fixture must produce ${expectedState}: ${JSON.stringify(result)}`);
    expect(!expectedCode || result?.errors?.[0]?.code === expectedCode, `${label} fixture must preserve ${expectedCode}`);
    attempts[label] = result;
    idempotencyKeys[label] = request.idempotencyKey;
  }

  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
  const token = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
  const diagnosticAttemptIDs = {};
  for (const [label, idempotencyKey] of Object.entries(idempotencyKeys)) {
    const rows = postgresQuery(`
      SELECT attempt_id::text
      FROM remember_attempts
      WHERE team_id = '${sqlLiteral(teamID)}'::uuid
        AND idempotency_key = '${sqlLiteral(idempotencyKey)}'
      ORDER BY created_at DESC, attempt_id DESC;
    `).split(/\r?\n/).filter(Boolean);
    expect(rows.length === 1, `${label} fixture must create exactly one diagnostic attempt: ${rows.join(",")}`);
    diagnosticAttemptIDs[label] = rows[0];
  }

  for (const [label, result] of Object.entries(attempts)) {
    const list = await controlJSON(controlURL, token, `/control/api/remember-attempts?team_id=${encodeURIComponent(teamID)}&outcome=${label}&limit=100`);
    const item = (list.data || []).find((candidate) => candidate.attempt_id === diagnosticAttemptIDs[label]);
    expect(item, `control list must expose the ${label} Remember attempt`);
    expect(!Object.hasOwn(item, "public_result") && !Object.hasOwn(item, "artifacts"), `${label} list must not expose result or artifact bytes`);
    expect(item.outcome === result?.processing_state, `${label} list must preserve the terminal outcome`);
  }

  const failed = attempts.failed;
  const failedList = await controlJSON(controlURL, token, `/control/api/remember-attempts?team_id=${encodeURIComponent(teamID)}&outcome=failed&limit=100`);
  const item = (failedList.data || []).find((candidate) => candidate.attempt_id === diagnosticAttemptIDs.failed);
  expect(item, "control list must expose the failed Remember attempt by fixture ID");
  expect(!Object.hasOwn(item, "public_result") && !Object.hasOwn(item, "artifacts"), "attempt list must not expose result or artifact bytes");

  const eventID = randomUUID();
  postgresQuery(`
    INSERT INTO remember_attempt_events (team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind, outcome, metadata)
    SELECT team_id, '${eventID}'::uuid, attempt_id, owner_profile_id, 2, 'assessment', 'diagnostic_metadata', 'failed',
      '{"markup":"<script>bad()</script>","secret":"diagnostics-persisted-secret"}'::jsonb
    FROM remember_attempts
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND attempt_id = '${sqlLiteral(item.attempt_id)}'::uuid;
  `);

  const detail = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${item.attempt_id}`);
  expect(detail.data?.events?.length >= 2, "attempt detail must expose the event transcript");
  expect(detail.data.events[0].sequence_no === 1 && detail.data.events[1].sequence_no === 2, "attempt events must remain ordered");
  expect(detail.data.events[1].metadata?.markup === "<script>bad()</script>", "detail must retain persisted metadata for safe rendering");
  expect(detail.data?.artifacts?.length >= 1, "failed attempt detail must expose an artifact descriptor");
  const descriptor = detail.data.artifacts[0];
  const artifactResponse = await fetch(`${controlURL}/control/api/teams/${teamID}/remember-attempts/${item.attempt_id}/artifacts/${descriptor.artifact_id}`, { headers: { Authorization: `Bearer ${token}` } });
  expect(artifactResponse.status === 200, "unexpired failure artifact must be readable");
  const artifactText = await artifactResponse.text();
  expect(artifactText === `{"phase":"assessment","error_code":"provider_unavailable"}`, `artifact bytes must be scrubbed and deterministic: ${artifactText}`);
  expect(artifactResponse.headers.get("cache-control") === "no-store", "artifact reads must disable caching");

  const expiredArtifactID = randomUUID();
  const expiredBytes = Buffer.from(`{"expired":true}`);
  postgresQuery(`
    INSERT INTO remember_failure_artifacts (team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind, content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at)
    SELECT team_id, '${expiredArtifactID}'::uuid, attempt_id, owner_profile_id, 'failure', 'application/json', decode('${expiredBytes.toString("hex")}', 'hex'), ${expiredBytes.length}, 'sha256:${createHash("sha256").update(expiredBytes).digest("hex")}', clock_timestamp() - interval '2 days', clock_timestamp() - interval '1 second'
    FROM remember_attempts
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND attempt_id = '${sqlLiteral(item.attempt_id)}'::uuid;
  `);
  const expiredResponse = await fetch(`${controlURL}/control/api/teams/${teamID}/remember-attempts/${item.attempt_id}/artifacts/${expiredArtifactID}`, { headers: { Authorization: `Bearer ${token}` } });
  expect(expiredResponse.status === 404, "expired failure artifact must return a bounded 404");

  const logs = await controlJSON(controlURL, token, "/control/api/logs?limit=100");
  const serializedLogs = JSON.stringify(logs);
  expect(serializedLogs.includes(`/artifacts/${descriptor.artifact_id}`), "artifact access must be present in the control operation audit");
  expect(!serializedLogs.includes("Diagnostics provider failure") && !serializedLogs.includes("diagnostics-persisted-secret") && !serializedLogs.includes("dense-mem-e2e-verifier-key"), "diagnostics content and credentials must not reach logs");
  const serverLogs = composeServerLogs();
  expect(!serverLogs.includes("Diagnostics provider failure") && !serverLogs.includes("diagnostics-persisted-secret") && !serverLogs.includes("dense-mem-e2e-verifier-key"), "diagnostics content and credentials must not reach server logs");
  const fixtureFile = process.env.DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE;
  if (fixtureFile) {
    await writeFile(fixtureFile, JSON.stringify({ failed_attempt_id: item.attempt_id, artifact_id: descriptor.artifact_id }), "utf8");
  }
  return { mode: name, outcomes: Object.fromEntries(Object.entries(attempts).map(([label, result]) => [label, result?.processing_state])), attempt_id: item.attempt_id, artifact_id: descriptor.artifact_id };
}

function rememberArguments(label, marker) {
  const suffix = `${Date.now()}-${randomUUID()}`;
  const idempotencyKey = `synchronous-write-diagnostics-${label}-${suffix}`;
  return {
    idempotencyKey,
    payload: {
    evidence: [{ content: `Dense-Mem stores durable memory in PostgreSQL. [fixture:diagnostics-${label}] ${suffix} ${marker}`, source_type: "manual" }],
    relationships: [{
      ref: "durable-store",
      subject: { name: "Dense-Mem", entity_kind: "project" },
      predicate: { proposed_key: "stores_memory_in" },
      object: { value: { type: "string", value: "PostgreSQL" } },
      polarity: "+",
      evidence_indices: [0],
    }],
    idempotency_key: idempotencyKey,
    },
  };
}

async function controlJSON(base, token, path) {
  const response = await fetch(`${base}${path}`, { headers: { Authorization: `Bearer ${token}` } });
  const text = await response.text();
  assert.equal(response.status, 200, `control request ${path} failed with ${response.status}: ${text}`);
  return text ? JSON.parse(text) : {};
}

function terminalPayload(result) {
  return result?.content?.[0]?.text ? JSON.parse(result.content[0].text) : result;
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function postgresQuery(sql) {
  const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
  const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "issue305-diagnostics", sql], { cwd: process.cwd(), encoding: "utf8" });
  if (result.status !== 0) throw new Error(`diagnostics PostgreSQL fixture failed (${result.status})`);
  return result.stdout.trim();
}

function composeServerLogs() {
  const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
  const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "logs", "--no-color", "server"], { cwd: process.cwd(), encoding: "utf8" });
  if (result.status !== 0) throw new Error("diagnostics server log collection failed");
  return `${result.stdout}\n${result.stderr}`;
}

function sqlLiteral(value) {
  return String(value).replaceAll("'", "''");
}
