import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { captureSemanticAssessmentWindow, reconstructDiagnosticBody } from "../../ontology_assessment_diagnostics.mjs";

export const name = "diagnostics";
const rememberModel = "dense-mem-synchronous-write-e2e-remember";

export async function run({ rpc, rawRPC = rpc, expect, runStep }) {
  const diagnosticSince = new Date().toISOString();
  const attempts = {};
  const idempotencyKeys = {};
  for (const [label, marker, expectedState, expectedCode] of [
    ["completed", "", "completed", ""],
    ["policy", "[fixture-fault:security]", "failed", "submission_policy_rejected"],
    ["failed", "[fixture-fault:unavailable]", "failed", "provider_unavailable"],
  ]) {
    await runStep(label, async () => {
      const request = rememberArguments(label, marker);
      const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: request.payload }));
      expect(result?.processing_state === expectedState, `${label} fixture must produce ${expectedState}: ${JSON.stringify(result)}`);
      expect(!expectedCode || result?.errors?.[0]?.code === expectedCode, `${label} fixture must preserve ${expectedCode}`);
      attempts[label] = result;
      idempotencyKeys[label] = request.idempotencyKey;
    });
  }

  const diagnosticFaults = [
    ["repair", "[fixture-fault:repair]", "completed", ""],
    ["repair-exhausted", "[fixture-fault:repair-exhausted]", "failed", "provider_response_invalid"],
    ["provider-status", "[fixture-fault:unavailable]", "failed", "provider_unavailable"],
    ["provider-429", "[fixture-fault:status-429]", "failed", "provider_unavailable"],
    ["provider-500", "[fixture-fault:status-500]", "failed", "provider_unavailable"],
    ["malformed", "[fixture-fault:malformed]", "failed", "provider_response_invalid"],
    ["assessment-invalid", "[fixture-fault:assessment-invalid]", "failed", "provider_response_invalid"],
    ["embedding", "[fixture-fault:embedding-count]", "failed", "embedding_response_invalid"],
    ["embedding-409", "[fixture-fault:embedding-status-409]", "failed", "embedding_unavailable"],
    ["timeout", "[fixture-fault:timeout]", "failed", "provider_unavailable"],
  ];
  for (const [label, marker, expectedState, expectedCode] of diagnosticFaults) {
    await runStep(label, async () => {
      const request = rememberArguments(label, marker);
      const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: request.payload }));
      expect(result?.processing_state === expectedState, `${label} fixture must produce ${expectedState}: ${JSON.stringify(result)}`);
      expect(!expectedCode || result?.errors?.[0]?.code === expectedCode, `${label} fixture must preserve ${expectedCode}: ${JSON.stringify(result)}`);
      attempts[label] = result;
      idempotencyKeys[label] = request.idempotencyKey;
    });
  }

  await runStep("disconnect", async () => {
    const disconnectRequest = rememberArguments("disconnect", "[fixture-fault:embedding-cancel]");
    const controller = new AbortController();
    const disconnected = rawRPC("tools/call", { name: "remember", arguments: disconnectRequest.payload }, controller.signal);
    setTimeout(() => controller.abort(), 1_000);
    let disconnectError;
    try {
      await disconnected;
    } catch (error) {
      disconnectError = error;
    }
    expect(disconnectError?.name === "AbortError", `disconnect fixture must abort the client request: ${disconnectError}`);
    let disconnectRetry;
    for (let retryAttempt = 0; retryAttempt < 3; retryAttempt += 1) {
      disconnectRetry = terminalPayload(await rpc("tools/call", { name: "remember", arguments: disconnectRequest.payload }));
      if (disconnectRetry?.processing_state === "completed") break;
      expect(disconnectRetry?.errors?.[0]?.retryable === true, `disconnect retry returned a non-retryable failure: ${JSON.stringify(disconnectRetry)}`);
    }
    expect(disconnectRetry?.processing_state === "completed", `disconnect retry must complete: ${JSON.stringify(disconnectRetry)}`);
    attempts.disconnect = disconnectRetry;
    idempotencyKeys.disconnect = disconnectRequest.idempotencyKey;
  });

  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
  const token = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
  await runStep("assessment-disconnect", async () => {
    const assessmentDisconnect = rememberArguments("assessment-disconnect", "[fixture-fault:assessment-timeout]");
    const providerHealthURL = "http://synchronous-write-provider:8787/health";
    const providerTrace = await fetch(providerHealthURL, { signal: AbortSignal.timeout(5_000) });
    expect(providerTrace.ok, `assessment fixture health failed: ${providerTrace.status}`);
    const assessmentRequestOffset = (await providerTrace.json()).chat_requests.length;
    const assessmentController = new AbortController();
    const assessmentCall = fetch(`${requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "")}/mcp`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${requiredEnv("DENSE_MEM_E2E_API_KEY")}`,
        Accept: "application/json, text/event-stream",
        "Content-Type": "application/json",
        "Mcp-Protocol-Version": "2026-07-28",
        "Mcp-Method": "tools/call",
        "Mcp-Name": "remember",
      },
      body: JSON.stringify({ jsonrpc: "2.0", id: Date.now(), method: "tools/call", params: {
        name: "remember",
        arguments: assessmentDisconnect.payload,
        _meta: { "io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": {} },
      } }),
      signal: assessmentController.signal,
    }).then(async (response) => {
      const body = await response.text();
      expect(response.ok, `assessment disconnect fixture returned HTTP ${response.status}`);
      return body;
    });
    try {
      await Promise.all([
        assert.rejects(assessmentCall, { name: "AbortError" }),
        (async () => {
          const signal = AbortSignal.any([assessmentController.signal, AbortSignal.timeout(20_000)]);
          while (true) {
            const response = await fetch(providerHealthURL, { signal });
            expect(response.ok, `assessment fixture health failed: ${response.status}`);
            const requests = (await response.json()).chat_requests.slice(assessmentRequestOffset);
            if (requests.some((request) => request.model === rememberModel &&
              request.schema_name === "dense_mem_evidence_assessment_response" && request.fault === "assessment-timeout")) {
              assessmentController.abort();
              return;
            }
            await new Promise((resolve) => setTimeout(resolve, 50));
          }
        })(),
      ]);
    } finally {
      assessmentController.abort();
      await assessmentCall.catch(() => {});
    }
    let assessmentFailure = "";
    for (let attempt = 0; attempt < 25; attempt += 1) {
      assessmentFailure = postgresQuery(`
        SELECT error_code || '|' || failed_phase || '|' || document_count || '|' || outcome
        FROM remember_attempts
        WHERE team_id = '${sqlLiteral(teamID)}'::uuid
          AND idempotency_key = '${sqlLiteral(assessmentDisconnect.idempotencyKey)}'
        ORDER BY created_at DESC, attempt_id DESC LIMIT 1;
      `);
      if (assessmentFailure) break;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    expect(assessmentFailure === "request_cancelled|assessment|0|failed", `assessment disconnect must retain cancellation without a semantic commit: ${assessmentFailure}`);
  });

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
      const allowedKeys = new Set(["contract_version", "space_kind", "submission_id", "submission_kind", "processing_state", "search_state", "correlation_id", "evidence", "errors", "warnings"]);
      expect(Object.keys(publicResult).every((key) => allowedKeys.has(key)), "completed detail public result must use the terminal allowlist");
      expect(publicResult.space_kind === "team_shared", "completed detail must preserve the requested destination");
      expect(!Object.hasOwn(publicResult, "secret"), "completed detail public result must not expose secret fields");
      expect(Array.isArray(terminalDetail.data?.events) && terminalDetail.data.events.length >= 1, "completed attempt detail must expose its event transcript");
    } else {
      if (label === "policy") {
        expect(diagnostics.original_request?.request_body?.includes('"name":"remember"'), `${label} detail must retain the admitted request`);
        expect(!diagnostics.original_request?.request_body?.includes("diagnostics-persisted-secret"), `${label} request must not expose rejected evidence`);
      } else {
        expect(diagnostics.original_request?.request_body?.includes('"name":"remember"'), `${label} detail must expose the logical original request`);
      }
      expect(diagnostics.caller_response?.response_body?.includes('"isError":true'), `${label} detail must expose the caller response envelope`);
    }
  }

  for (const label of [...diagnosticFaults.filter(([, , state]) => state === "failed").map(([name]) => name), "failed"]) {
    const detail = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${diagnosticAttemptIDs[label]}`);
    const diagnostics = detail.data?.diagnostics || {};
    expect(diagnostics.original_request?.request_body?.includes('"name":"remember"'), `${label} detail must expose the logical original request`);
    expect(diagnostics.provider_exchanges?.length >= 1, `${label} detail must expose provider exchanges`);
    expect(diagnostics.caller_response?.response_body?.includes('"isError":true'), `${label} detail must expose the caller response envelope`);
    if (label === "failed") {
      const assessorExchanges = diagnostics.provider_exchanges.filter((exchange) => exchange.component === "assessor");
      expect(assessorExchanges.length >= 1, "failed Remember diagnostic must retain the assessor exchange");
      expect(assessorExchanges.every((exchange) => exchange.model === rememberModel), `Remember diagnostic used an unexpected model: ${JSON.stringify(assessorExchanges)}`);
    }
  }

  const validationAttempt = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${diagnosticAttemptIDs["assessment-invalid"]}`);
  const validationEvent = (validationAttempt.data?.events || []).find((event) => event.metadata?.assessor_validation);
  const validation = validationEvent?.metadata?.assessor_validation;
  expect(validation && Array.isArray(validation.turns), "semantic assessor failure must retain bounded validation turns");
  expect(validation.failure_class === "malformed_exhausted", `semantic assessor failure must retain its exhausted classification: ${JSON.stringify(validation)}`);
  expect(validation.turns.length === 3, `semantic assessor failure must retain all three turns: ${JSON.stringify(validation)}`);
  expect(validation.turns.every((turn) => Array.isArray(turn.fields) && Array.isArray(turn.field_families) && typeof turn.error_count === "number" && typeof turn.truncated === "boolean"), "validation turns must expose normalized fields, families, counts, and truncation");
  expect(validation.turns.some((turn) => turn.error_count > 0 && turn.fields.length > 0 && turn.field_families.length > 0), "semantic assessor failure must retain rejected validation fields and families");
  expect(!JSON.stringify(validation).includes("provider_secret") && !JSON.stringify(validation).includes("Diagnostics provider failure"), "validation metadata must exclude provider content");

  const embeddingRejection = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${diagnosticAttemptIDs["embedding-409"]}`);
  const rejectedExchanges = embeddingRejection.data.diagnostics.provider_exchanges.filter((exchange) => exchange.component === "embedding");
  expect(rejectedExchanges.length === 1, "embedding 409 must retain one exchange without automatic retry");
  expect(rejectedExchanges[0].status_code === 409 && rejectedExchanges[0].response_body.includes("fixture_embedding_conflict"), "embedding 409 must retain its provider status and body");
  expect(attempts["embedding-409"].errors[0].reason_code === "embedding_request_rejected", "embedding 409 must preserve the request-rejection classification");
  expect(embeddingRejection.data.document_count === 0, "embedding rejection must not partially publish search documents");

  for (const label of ["completed", "repair", "repair-exhausted", "assessment-invalid", "provider-429", "timeout"]) {
    const detail = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-attempts/${diagnosticAttemptIDs[label]}`);
    const measured = detail.data?.events?.find((event) => event.metadata?.assessor_diagnostics)?.metadata.assessor_diagnostics;
    expect(measured && Number.isInteger(measured.preparation_ms) && measured.preparation_ms >= 0, `${label} must retain assessment preparation timing`);
    expect(Number.isInteger(measured.elapsed_ms) && measured.elapsed_ms >= measured.preparation_ms, `${label} must retain inclusive assessment duration`);
    expect(Array.isArray(measured.turns) && measured.turns.length >= 1 && measured.turns.length <= 3, `${label} must retain bounded attempted turns`);
    expect(measured.turns.every((turn) => Number.isInteger(turn.elapsed_ms) && turn.provider && Number.isInteger(turn.provider.queue_ms) && Number.isInteger(turn.provider.http_ms)), `${label} must distinguish provider queue and HTTP durations`);
    if (label === "repair") {
      expect(measured.turns.length === 2 && measured.turns[0].error_count > 0 && measured.turns[1].error_count === 0, `${label} must retain failed-turn history after successful repair`);
      expect(attempts[label].evidence.every((result) => result.disposition === "stored"), `${label} must preserve exact Evidence`);
    }
    if (label === "repair-exhausted") expect(measured.turns.length === 3, "exhausted validation must retain three complete attempts");
    if (label === "provider-429") expect(measured.turns[0].failure_class === "rate_limited" && measured.turns[0].status_code === 429, "rate limit must remain distinct from invalid response");
    if (label === "timeout") expect(measured.turns[0].failure_class === "timeout" && !Object.hasOwn(measured.turns[0], "validation_ms"), "timeout before validation must leave validation timing absent");
    expect(!Object.hasOwn(detail.data.public_result, "assessor_diagnostics"), "assessment diagnostics must remain outside public memory results");
    expect(!JSON.stringify(measured).includes("provider_secret") && !JSON.stringify(measured).includes("Diagnostics provider failure"), "assessment diagnostics must exclude response content");
  }

  stopDisabledTelemetryPrometheus();
  const usage = await controlJSON(controlURL, token, "/control/api/metrics?window_minutes=60");
  const totals = usage.data?.system || {};
  expect(typeof totals.requests === "number" && typeof totals.errors === "number", "usage metrics must retain HTTP request totals");
  expect(typeof totals.mcp_tool_calls === "number" && totals.mcp_tool_calls > 0, "usage metrics must expose dispatched MCP calls");
  expect(typeof totals.mcp_tool_failures === "number" && totals.mcp_tool_failures > 0, "usage metrics must expose MCP failures separately");

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
      convert_to('{"expired":true}', 'UTF8'), 'application/json', 'captured', 'captured', clock_timestamp() - interval '8 days', clock_timestamp() - interval '2 days'
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
  expect(disconnectDiagnostics.caller_response?.capture_state === "not_delivered", "disconnect detail must mark the caller response as not delivered");

  const logs = await controlJSON(controlURL, token, "/control/api/logs?limit=100");
  const serializedLogs = JSON.stringify(logs);
  expect(serializedLogs.includes("control_remember_attempt_diagnostic_access"), "diagnostic access must be present in the control operation audit");
  expect(!serializedLogs.includes("Diagnostics provider failure") && !serializedLogs.includes("diagnostics-persisted-secret") && !serializedLogs.includes("dense-mem-e2e-verifier-key"), "diagnostics content and credentials must not reach logs");
  const serverLogs = captureSemanticAssessmentWindow({ project: requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT"), composeFile: requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE"), since: diagnosticSince,
    directory: `${process.env.DENSE_MEM_E2E_REMEMBER_FAILURE_DIR || "/tmp/dense-mem-remember-failures"}/evidence-only-debug` });
  expect(!serverLogs.includes("dense-mem-synchronous-write-e2e-key") && !serverLogs.includes(requiredEnv("DENSE_MEM_E2E_API_KEY")), "credential protector must remove operational and request-scoped credentials");
  const records = serverLogs.split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line));
  expect(!records.some((row) => row.level === "TRACE"), "DEBUG runtime must filter TRACE");
  const requestBodies = records.filter((row) => row.msg === "ai_exchange_payload" && row.direction === "request");
  expect(requestBodies.length > 0 && requestBodies.every((row) => row.level === "DEBUG"), "DEBUG must capture actual provider requests");
  const serialized = requestBodies.map((row) => row.payload_chunk?.body || "").join("");
  expect(serialized.includes("admitted token") && serialized.includes("admitted password") && serialized.includes("admitted secret"), "admitted user fields must survive credential protection");
  for (const row of records.filter((row) => row.msg === "ai_exchange_payload")) expect(Buffer.byteLength(row.payload_chunk.body) <= 2048, "provider chunks must retain the 2048-byte bound");
  const providerProof = diagnoseProviderHTTPLog(records.filter((row) => row.msg === "ai_exchange_payload").map(JSON.stringify).join("\n"));
  expect(providerProof.exchanges.some((item) => ["no_response", "interrupted", "truncated"].includes(item.transport_capture_state) || item.capture_state !== "captured"), "cancellation must retain explicit incomplete provider capture");
  expect(records.some((row) => row.msg === "remember_assessment_diagnostics" || row.assessor_diagnostics), "log-only diagnosis must include assessment attempts and validation cause");
  for (const [label, expectedCode] of [["malformed", "provider_response_invalid"], ["assessment-invalid", "provider_response_invalid"], ["timeout", "provider_unavailable"], ["assessment-disconnect", "request_cancelled"]]) {
    const matchingInvocations = new Set();
    const requests = new Map();
    for (const row of requestBodies) {
      const group = requests.get(row.exchange_id) || [];
      group.push(row); requests.set(row.exchange_id, group);
    }
    for (const chunks of requests.values()) {
      chunks.sort((a,b) => a.payload_chunk.index-b.payload_chunk.index);
      if (JSON.stringify(reconstructDiagnosticBody(chunks)).includes("[fixture:diagnostics-"+label+"]")) matchingInvocations.add(chunks[0].invocation_id);
    }
    const terminal = records.find((row) => row.msg === "remember_invocation_completed" && matchingInvocations.has(row.invocation_id) && row.error_code === expectedCode);
    expect(terminal?.level === "ERROR" && terminal.canonical_attempt_id && terminal.duration_ms >= 0, label+" lacks correlated terminal failure logs");
    const phase = records.find((row) => row.msg === "remember_phase_completed" && row.attempt_id === terminal.canonical_attempt_id && row.phase === "assessment");
    expect(phase && phase.duration_ms >= 0 && phase.outcome !== "completed", label+" lacks assessment phase timing");
    const diagnostic = phase.assessor_diagnostics || records.find((row) => row.submission_id === terminal.canonical_attempt_id && row.assessor_diagnostics)?.assessor_diagnostics;
    expect(diagnostic?.turns?.length >= 1 && diagnostic.turns.length <= 3 && diagnostic.remaining_budget_ms >= 0, label+" lacks bounded attempts/deadline diagnostics");
    expect(diagnostic.turns.every((turn) => turn.attempt >= 1 && turn.elapsed_ms >= 0 && turn.remaining_budget_ms >= 0), label+" lacks per-attempt timing and deadline");
    expect(diagnostic.turns.some((turn) => turn.validation_stage || turn.failure_class || turn.issues?.length), label+" lacks validation or provider failure cause");
    const exchanges = providerProof.exchanges.filter((row) => row.invocation_id === terminal.invocation_id);
    expect(exchanges.some((row) => row.direction === "request") && exchanges.some((row) => row.direction === "response"), label+" lacks complete protected exchange directions");
    expect(exchanges.every((row) => row.started_at && row.elapsed_ms >= 0), label+" lacks exchange timings");
    if (label === "timeout" || label === "assessment-disconnect") expect(exchanges.some((row) => row.transport_capture_state !== "captured"), label+" lacks explicit interrupted capture");
  }


  const expiredInvocationID = randomUUID();
  postgresQuery(`
    INSERT INTO remember_invocation_diagnostics (
      team_id, invocation_id, owner_profile_id, request_hash, correlation_id,
      classification, outcome, failed_phase, error_code, retryable, duration_ms,
      request_bytes, request_capture_state, provider_exchanges,
      response_bytes, response_capture_state, created_at, completed_at, expires_at
    ) SELECT team_id, '${expiredInvocationID}'::uuid, owner_profile_id, 'expired-invocation-hash', 'expired-invocation-correlation',
      'execution', 'failed', 'commit', 'provider_unavailable', false, 1,
      convert_to('{"expired":true}', 'UTF8'), 'captured',
      '[{"diagnostic_id":"expired-invocation-provider","sequence_no":1,"kind":"provider_exchange","component":"fixture","outcome":"captured","capture_state":"expired","expires_at":"2026-08-01T00:00:00Z"}]'::jsonb,
      convert_to('{"expired":true}', 'UTF8'), 'captured',
      clock_timestamp() + interval '1 day', clock_timestamp() + interval '1 day', clock_timestamp() + interval '2 days'
    FROM remember_attempts
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND attempt_id = '${sqlLiteral(item.attempt_id)}'::uuid;
  `);
  const expiredInvocation = await controlJSON(controlURL, token, `/control/api/teams/${teamID}/remember-invocations/${expiredInvocationID}`);
  expect(expiredInvocation.data?.provider_exchanges?.[0]?.capture_state === "expired" && !Object.hasOwn(expiredInvocation.data.provider_exchanges[0], "request_body"), "expired invocation capture must retain state without its body");

  for (const literal of ["true", "false"]) {
    const retryableFilter = await controlResponse(controlURL, token, `/control/api/remember-invocations?team_id=${teamID}&retryable=${literal}&limit=1`);
    expect(retryableFilter.response.status === 200, `${literal} Remember invocation retryable filter must be accepted: ${retryableFilter.text}`);
  }
  for (const literal of ["1", "0", "t", "f", "TRUE", "False"]) {
    const retryableFilter = await controlResponse(controlURL, token, `/control/api/remember-invocations?team_id=${teamID}&retryable=${literal}&limit=1`);
    expect(retryableFilter.response.status === 422 && retryableFilter.text.length < 2048, `invalid Remember invocation retryable filter must be bounded for ${literal}: ${retryableFilter.text}`);
  }

  const fixtureFile = process.env.DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE;
  if (fixtureFile) {
    await writeFile(fixtureFile, JSON.stringify({
      failed_attempt_id: item.attempt_id,
      diagnostic_id: failedDiagnostics.original_request?.diagnostic_id || "",
      validation_attempt_id: diagnosticAttemptIDs["assessment-invalid"],
      expired_invocation_id: expiredInvocationID,
    }), "utf8");
  }
  return {
    mode: name,
    outcomes: Object.fromEntries(Object.entries(attempts).map(([label, result]) => [label, result?.processing_state])),
    attempt_id: item.attempt_id,
    diagnostic_id: failedDiagnostics.original_request?.diagnostic_id || "",
    validation_attempt_id: diagnosticAttemptIDs["assessment-invalid"],
    expired_invocation_id: expiredInvocationID,
    assessment_disconnect: "request_cancelled",
  };
}

export async function captureRememberFailure({ control, teamID, correlationID, directory }) {
  assert(teamID, "failed Remember credential has no known team");
  assert(correlationID, "failed Remember has no correlation ID for diagnostic lookup");
  const list = await control(`/remember-attempts?team_id=${encodeURIComponent(teamID)}&outcome=failed&limit=100`);
  const matches = (list.data || []).filter((item) => item.correlation_id === correlationID);
  assert.equal(matches.length, 1, "failed Remember must match exactly one authoritative attempt");
  const attemptID = matches[0].attempt_id;
  assert.match(attemptID, /^[0-9a-f-]{36}$/);
  const detail = (await control(`/teams/${teamID}/remember-attempts/${attemptID}`)).data;
  assert.equal(detail.attempt_id, attemptID);
  assert.equal(detail.correlation_id, correlationID);
  assert.equal(detail.team_id, teamID);
  assert.equal(detail.outcome, "failed");
  const encoded = JSON.stringify(detail);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  await writeFile(join(directory, `${attemptID}.private.json`), encoded, { mode: 0o600, flag: "wx" });
  const summary = diagnoseRememberFailure(detail, encoded);
  await writeFile(join(directory, `${attemptID}.summary.json`), JSON.stringify(summary), { mode: 0o600, flag: "wx" });
  return summary;
}

export function diagnoseRememberFailure(detail, encoded = JSON.stringify(detail)) {
  return {
    type: "remember_failure_diagnostics", team_id: detail.team_id, attempt_id: detail.attempt_id, correlation_id: detail.correlation_id,
    error_code: detail.error_code, failed_phase: detail.failed_phase, duration_ms: detail.duration_ms,
    sha256: createHash("sha256").update(encoded).digest("hex"),
    provider_exchanges: (detail.diagnostics?.provider_exchanges || []).map((exchange) => ({
      diagnostic_id: exchange.diagnostic_id, component: exchange.component, model: exchange.model,
      status_code: exchange.status_code, outcome: exchange.outcome, capture_state: exchange.capture_state,
      response_analysis: analyzeFailureResponse(exchange),
    })),
  };
}

export function diagnoseProviderHTTPLog(encoded) {
  const exchanges = new Map();
  for (const line of encoded.split(/\r?\n/).filter(Boolean)) {
    const row = JSON.parse(line);
    assert.equal(row.msg, "ai_exchange_payload");
    assert(row.exchange_id && ((row.team_id && row.run_id && row.batch_id) || row.invocation_id), "provider capture correlation is missing");
    const key = `${row.exchange_id}:${row.direction}`;
    const entry = exchanges.get(key) || { row, chunks: [] };
    entry.chunks.push(row.payload_chunk);
    exchanges.set(key, entry);
  }
  const summaries = [];
  for (const { row, chunks } of exchanges.values()) {
    chunks.sort((a, b) => a.index - b.index);
    assert.equal(chunks.length, chunks[0].count, "provider capture chunk count mismatch");
    const body = chunks.map((chunk, index) => {
      assert.equal(chunk.index, index, "provider capture chunk missing");
      for (const key of ["count", "sha256", "state"]) assert.equal(chunk[key], chunks[0][key], "provider capture chunks disagree");
      return chunk.body;
    }).join("");
    if (chunks[0].state === "captured") assert.equal(createHash("sha256").update(body).digest("hex"), chunks[0].sha256, "provider capture digest mismatch");
    summaries.push({
      exchange_id: row.exchange_id, invocation_id: row.invocation_id, team_id: row.team_id, run_id: row.run_id, batch_id: row.batch_id,
      direction: row.direction, status_code: row.status_code || 0, started_at: row.started_at, elapsed_ms: row.elapsed_ms,
      capture_state: chunks[0].state, transport_capture_state: row.transport_capture_state,
      sha256: chunks[0].sha256, protected_bytes: Buffer.byteLength(body),
      response_analysis: row.direction === "response" ? analyzeFailureResponse({ response_body: body, status_code: row.status_code }) : undefined,
    });
  }
  return { type: "provider_http_diagnostics", sha256: createHash("sha256").update(encoded).digest("hex"), exchanges: summaries };
}

function analyzeFailureResponse(exchange) {
  if (!exchange.response_body) return "response_body_unavailable";
  let body;
  try { body = JSON.parse(exchange.response_body); } catch { return "response_not_json"; }
  if (body?.error?.message === `embedding upstream returned status ${exchange.status_code}`) return "fixture_discarded_upstream_error_body";
  if (body?.error?.code === "fixture_embedding_conflict") return "deterministic_fixture_conflict";
  return body?.error || Array.isArray(body?.errors) ? "provider_error_details_in_temporary_capture" : "no_structured_provider_error";
}

function rememberArguments(label, marker) {
  const suffix = randomUUID();
  const idempotencyKey = `synchronous-write-diagnostics-${label}-${suffix}`;
  return { idempotencyKey, payload: {
    target_space: "team_shared",
    evidence: [{ content: `Dense-Mem stores durable memory in PostgreSQL. [fixture:diagnostics-${label}] ${suffix} ${marker} User fields {"token":"admitted token","password":"admitted password","secret":"admitted secret"}.`, source_type: "manual" }],
    idempotency_key: idempotencyKey,
  } };
}

async function controlJSON(base, token, path) {
  const result = await controlResponse(base, token, path);
  assert.equal(result.response.status, 200, `control request ${path} failed with ${result.response.status}: ${result.text}`);
  return result.text ? JSON.parse(result.text) : {};
}

async function controlResponse(base, token, path) {
  const response = await fetch(`${base}${path}`, { headers: { Authorization: `Bearer ${token}` } });
  return { response, text: await response.text() };
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

function stopDisabledTelemetryPrometheus() {
  if (process.env.DENSE_MEM_E2E_TELEMETRY_DISABLED !== "1") return;
  const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
  const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "stop", "prometheus"], { cwd: process.cwd(), encoding: "utf8" });
  if (result.status !== 0) throw new Error("telemetry-disabled harness could not stop its disposable Prometheus service");
}

function sqlLiteral(value) {
  return String(value).replaceAll("'", "''");
}
