import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { writeFile } from "node:fs/promises";

export const name = "diagnostics";

export async function run({ rpc, rawRPC = rpc, expect }) {
  const attempts = {};
  const idempotencyKeys = {};
  for (const [label, marker, expectedState, expectedCode] of [
    ["completed", "", "completed", ""],
    ["policy", "[fixture-fault:security]", "failed", "submission_policy_rejected"],
    ["failed", "[fixture-fault:unavailable]", "failed", "provider_unavailable"],
  ]) {
    const request = rememberArguments(label, marker);
    const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: request.payload }));
    expect(result?.processing_state === expectedState, `${label} fixture must produce ${expectedState}: ${JSON.stringify(result)}`);
    expect(!expectedCode || result?.errors?.[0]?.code === expectedCode, `${label} fixture must preserve ${expectedCode}`);
    attempts[label] = result;
    idempotencyKeys[label] = request.idempotencyKey;
  }

  const diagnosticFaults = [
    ["repair", "[fixture-fault:repair]", "completed", ""],
    ["repair-exhausted", "[fixture-fault:repair-exhausted]", "failed", "provider_response_invalid"],
    ["provider-status", "[fixture-fault:unavailable]", "failed", "provider_unavailable"],
    ["provider-429", "[fixture-fault:status-429]", "failed", "provider_unavailable"],
    ["provider-500", "[fixture-fault:status-500]", "failed", "provider_unavailable"],
    ["malformed", "[fixture-fault:malformed]", "failed", "provider_response_invalid"],
    ["embedding", "[fixture-fault:embedding-count]", "failed", "embedding_response_invalid"],
    ["timeout", "[fixture-fault:timeout]", "failed", "provider_unavailable"],
  ];
  for (const [label, marker, expectedState, expectedCode] of diagnosticFaults) {
    const request = rememberArguments(label, marker);
    const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: request.payload }));
    expect(result?.processing_state === expectedState, `${label} fixture must produce ${expectedState}: ${JSON.stringify(result)}`);
    expect(!expectedCode || result?.errors?.[0]?.code === expectedCode, `${label} fixture must preserve ${expectedCode}: ${JSON.stringify(result)}`);
    attempts[label] = result;
    idempotencyKeys[label] = request.idempotencyKey;
  }

  const disconnectRequest = rememberArguments("disconnect", "[fixture-fault:embedding-cancel]");
  const controller = new AbortController();
  const disconnected = rawRPC("tools/call", { name: "remember", arguments: disconnectRequest.payload }, controller.signal);
  setTimeout(() => controller.abort(), 100);
  let disconnectError;
  try {
    await disconnected;
  } catch (error) {
    disconnectError = error;
  }
  expect(disconnectError?.name === "AbortError", `disconnect fixture must abort the client request: ${disconnectError}`);
  const disconnectRetry = terminalPayload(await rpc("tools/call", { name: "remember", arguments: disconnectRequest.payload }));
  expect(disconnectRetry?.processing_state === "completed", `disconnect retry must complete: ${JSON.stringify(disconnectRetry)}`);
  attempts.disconnect = disconnectRetry;
  idempotencyKeys.disconnect = disconnectRequest.idempotencyKey;

  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
  const token = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
  for (const path of [
    "/control/api/remember-attempts?limit=101",
    "/control/api/remember-attempts?limit=0",
    "/control/api/remember-attempts?team_id=bad",
    "/control/api/remember-attempts?outcome=unknown",
  ]) {
    const response = await fetch(`${controlURL}${path}`, { headers: { Authorization: `Bearer ${token}` } });
    const body = await response.text();
    expect(response.status === 422, `invalid diagnostics request ${path} must return 422: ${body}`);
    expect(body.length < 2048, `invalid diagnostics error ${path} must be bounded`);
    expect(!body.includes("diagnostics-persisted-secret") && !body.includes("dense-mem-e2e-verifier-key"), `invalid diagnostics error ${path} must not expose payloads`);
  }
  const diagnosticAttemptIDs = {};
  for (const [label, idempotencyKey] of Object.entries(idempotencyKeys)) {
    const rows = postgresQuery(`
      SELECT attempt_id::text || '|' || outcome
      FROM remember_attempts
      WHERE team_id = '${sqlLiteral(teamID)}'::uuid
        AND idempotency_key = '${sqlLiteral(idempotencyKey)}'
      ORDER BY created_at DESC, attempt_id DESC;
    `).split(/\r?\n/).filter(Boolean);
    expect(rows.length >= 1, `${label} fixture must create a diagnostic attempt: ${rows.join(",")}`);
    const failed = rows.find((row) => row.endsWith("|failed"));
    diagnosticAttemptIDs[label] = (failed || rows[0]).split("|", 1)[0];
  }

  for (const [label, result] of Object.entries(attempts)) {
    if (label === "disconnect") continue;
    const list = await controlJSON(controlURL, token, `/control/api/remember-attempts?team_id=${encodeURIComponent(teamID)}&outcome=${result?.processing_state}&limit=100`);
    const item = (list.data || []).find((candidate) => candidate.attempt_id === diagnosticAttemptIDs[label]);
    expect(item, `control list must expose the ${label} Remember attempt`);
    expect(!Object.hasOwn(item, "public_result") && !Object.hasOwn(item, "diagnostics"), `${label} list must not expose result or artifact bytes`);
    expect(item.outcome === result?.processing_state, `${label} list must preserve the terminal outcome`);
  }

  for (const label of ["completed", "policy"]) {
    const terminalDetail = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${diagnosticAttemptIDs[label]}`);
    const diagnostics = terminalDetail.data?.diagnostics || {};
    expect(Array.isArray(diagnostics.provider_exchanges), `${label} detail must expose provider exchange state`);
    if (label === "completed") {
      const publicResult = terminalDetail.data?.public_result || {};
      const allowedKeys = new Set(["contract_version", "submission_id", "submission_kind", "processing_state", "search_state", "correlation_id", "evidence", "relationship_results", "errors", "warnings"]);
      expect(Object.keys(publicResult).every((key) => allowedKeys.has(key)), "completed detail public result must use the terminal allowlist");
      expect(!Object.hasOwn(publicResult, "secret"), "completed detail public result must not expose secret fields");
      expect(Array.isArray(terminalDetail.data?.events) && terminalDetail.data.events.length >= 1, "completed attempt detail must expose its event transcript");
    } else {
      expect(diagnostics.original_request?.request_body?.includes('"name":"remember"'), `${label} detail must expose the logical original request`);
      expect(diagnostics.caller_response?.response_body?.includes('"isError":true'), `${label} detail must expose the caller response envelope`);
    }
  }

  for (const label of diagnosticFaults.filter(([, , state]) => state === "failed").map(([name]) => name)) {
    const detail = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${diagnosticAttemptIDs[label]}`);
    const diagnostics = detail.data?.diagnostics || {};
    expect(diagnostics.original_request?.request_body?.includes('"name":"remember"'), `${label} detail must expose the logical original request`);
    expect(diagnostics.provider_exchanges?.length >= 1, `${label} detail must expose provider exchanges`);
    expect(diagnostics.caller_response?.response_body?.includes('"isError":true'), `${label} detail must expose the caller response envelope`);
  }

  const repairRows = postgresQuery(`
    SELECT assessor_turns
    FROM remember_attempts
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND idempotency_key = '${sqlLiteral(idempotencyKeys.repair)}'
    ORDER BY created_at DESC, attempt_id DESC LIMIT 1;
  `);
  expect(repairRows === "2", `successful repair must retain two assessor turns: ${repairRows}`);

  const failed = attempts.failed;
  const failedList = await controlJSON(controlURL, token, `/control/api/remember-attempts?team_id=${encodeURIComponent(teamID)}&outcome=failed&limit=100`);
  const item = (failedList.data || []).find((candidate) => candidate.attempt_id === diagnosticAttemptIDs.failed);
  expect(item, "control list must expose the failed Remember attempt by fixture ID");
  expect(!Object.hasOwn(item, "public_result") && !Object.hasOwn(item, "diagnostics"), "attempt list must not expose diagnostic bytes");

  const eventID = randomUUID();
  postgresQuery(`
    INSERT INTO remember_attempt_events (team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind, outcome, metadata)
    SELECT team_id, '${eventID}'::uuid, attempt_id, owner_profile_id, 2, 'assessment', 'diagnostic_metadata', 'failed',
      '{"markup":"<script>bad()</script>","secret":"diagnostics-persisted-secret"}'::jsonb
    FROM remember_attempts
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND attempt_id = '${sqlLiteral(item.attempt_id)}'::uuid;
  `);

  const expiredDiagnosticID = randomUUID();
  postgresQuery(`
    INSERT INTO remember_attempt_diagnostics (
      team_id, diagnostic_id, attempt_id, owner_profile_id, sequence_no, kind, component,
      request_bytes, request_content_type, outcome, capture_state, captured_at, expires_at
    ) SELECT team_id, '${expiredDiagnosticID}'::uuid, attempt_id, owner_profile_id, 99, 'provider_exchange', 'fixture',
      convert_to('{"expired":true}', 'UTF8'), 'application/json', 'captured', 'captured', clock_timestamp() - interval '8 days', clock_timestamp() - interval '1 second'
    FROM remember_attempts
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND attempt_id = '${sqlLiteral(item.attempt_id)}'::uuid;
  `);

  const detail = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${item.attempt_id}`);
  expect(detail.data?.events?.length >= 2, "attempt detail must expose the event transcript");
  expect(detail.data.events[0].sequence_no === 1 && detail.data.events[1].sequence_no === 2, "attempt events must remain ordered");
  expect(detail.data.events[1].metadata?.markup === "<script>bad()</script>", "detail must retain persisted metadata for safe rendering");
  const failedDiagnostics = detail.data?.diagnostics || {};
  expect(failedDiagnostics.original_request?.request_body?.includes('"name":"remember"'), "failed detail must expose the original request body");
  expect(failedDiagnostics.provider_exchanges?.length >= 1, "failed detail must expose provider exchanges");
  expect(failedDiagnostics.caller_response?.response_body?.includes('"isError":true'), "failed detail must expose the response returned to the caller");
  const expired = (detail.data?.diagnostics?.provider_exchanges || []).find((candidate) => candidate.diagnostic_id === expiredDiagnosticID);
  expect(expired?.capture_state === "expired" && !Object.hasOwn(expired, "request_body"), "expired diagnostic must retain state without its body");

  const disconnectDetail = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${diagnosticAttemptIDs.disconnect}`);
  const disconnectDiagnostics = disconnectDetail.data?.diagnostics || {};
  expect(disconnectDiagnostics.original_request?.request_body?.includes('"name":"remember"'), "disconnect detail must expose the original request body");
  expect(disconnectDiagnostics.provider_exchanges?.some((exchange) => ["no_response", "interrupted", "truncated"].includes(exchange.capture_state)), "disconnect detail must expose a bounded interrupted provider state");

  const logs = await controlJSON(controlURL, token, "/control/api/logs?limit=100");
  const serializedLogs = JSON.stringify(logs);
  expect(serializedLogs.includes("control_remember_attempt_diagnostic_access"), "diagnostic access must be present in the control operation audit");
  expect(!serializedLogs.includes("Diagnostics provider failure") && !serializedLogs.includes("diagnostics-persisted-secret") && !serializedLogs.includes("dense-mem-e2e-verifier-key"), "diagnostics content and credentials must not reach logs");
  const serverLogs = composeServerLogs();
  expect(!serverLogs.includes("Diagnostics provider failure") && !serverLogs.includes("diagnostics-persisted-secret") && !serverLogs.includes("dense-mem-e2e-verifier-key"), "diagnostics content and credentials must not reach server logs");
  const fixtureFile = process.env.DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE;
  if (fixtureFile) {
    await writeFile(fixtureFile, JSON.stringify({ failed_attempt_id: item.attempt_id, diagnostic_id: failedDiagnostics.original_request?.diagnostic_id || "" }), "utf8");
  }
  return { mode: name, outcomes: Object.fromEntries(Object.entries(attempts).map(([label, result]) => [label, result?.processing_state])), attempt_id: item.attempt_id, diagnostic_id: failedDiagnostics.original_request?.diagnostic_id || "" };
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
