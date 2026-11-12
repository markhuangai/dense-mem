import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { captureSemanticAssessmentWindow, reconstructDiagnosticBody } from "../../ontology_assessment_diagnostics.mjs";
import { createKnownEvidenceTeam, createKnownEvidenceCredential, postgresQuery, rememberCanonicalCounts } from "./remember.mjs";
import { diagnoseProviderHTTPLog } from "./diagnostics.mjs";

export const name = "progress";
const messages = ["Preparing intake", "Checking duplicate Evidence", "Assessing Evidence", "Preparing search state", "Committing"];
const rememberModel = "dense-mem-synchronous-write-e2e-remember";

export async function run({ expect, runStep }) {
  const since = new Date().toISOString();
  const receipts = [];
  const baseKey = required("DENSE_MEM_E2E_API_KEY");
  const teamID = required("DENSE_MEM_E2E_TEAM_ID");
  const providerURL = required("DENSE_MEM_E2E_PROVIDER_URL").replace(/\/$/, "");
  const provider = async (mode) => {
    const response = await fetch(providerURL + "/admission-fixture", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }),
    });
    assert.equal(response.status, 200);
  };
  const health = async () => {
    const response = await fetch(providerURL + "/health");
    assert.equal(response.status, 200);
    return response.json();
  };

  await runStep("slow-string-token", async () => {
    await provider("hold");
    let observedPending = false;
    try {
      const receipt = await progressCall({ apiKey: baseKey, args: argumentsFor("slow", "admission-hold", false),
        token: JSON.stringify("remember-" + randomUUID()), onProgress: async (frame) => {
          if (frame.params.message !== "Checking duplicate Evidence") return;
          await waitFor(async () => (await health()).held_admission_requests > 0, "provider did not hold the actual embedding request");
          observedPending = true;
          await provider("normal");
        } });
      assert.equal(observedPending, true, "progress must arrive before held required work completes");
      assertSuccess(receipt, 5);
      receipts.push(receipt);
    } finally { await provider("normal"); }
  });

  for (const token of ["0", "9007199254740993"]) {
    await runStep("numeric-token-" + token, async () => {
      const receipt = await progressCall({ apiKey: baseKey, args: argumentsFor("numeric"), token });
      assertSuccess(receipt, 5);
      receipts.push(receipt);
    });
  }

  await runStep("replay-json-no-token-and-conflict", async () => {
    const first = receipts[0];
    const before = (await health()).chat_requests.filter((item) => item.model === rememberModel).length;
    for (const options of [{ token: "0" }, { token: "0", accept: "application/json" }, {}]) {
      const replay = await progressCall({ apiKey: baseKey, args: first.args, ...options });
      assert.equal(replay.progress.length, 0);
      assert.equal(replay.terminal.result.structuredContent.submission_id, first.terminal.result.structuredContent.submission_id);
    }
    assert.equal((await health()).chat_requests.filter((item) => item.model === rememberModel).length, before);
    const changed = structuredClone(first.args);
    changed.evidence[0].content = "Changed complete request under the retained operation key.";
    const conflict = await progressCall({ apiKey: baseKey, args: changed, token: "0" });
    assert.equal(conflict.progress.length, 0);
    assert.equal(conflict.terminal.result.structuredContent.errors[0].code, "idempotency_conflict");
    for (const options of [{ token: "0", accept: "application/json" }, {}]) {
      const receipt = await progressCall({ apiKey: baseKey, args: argumentsFor("terminal-only"), ...options });
      assertSuccess(receipt, 0);
      receipts.push(receipt);
    }
  });

  for (const [fault, code, count] of [
    ["assessment-unavailable", "provider_unavailable", 3],
    ["repair-exhausted", "provider_response_invalid", 3],
    ["embedding-count", "embedding_response_invalid", 4],
  ]) {
    await runStep(fault, async () => {
      const receipt = await progressCall({ apiKey: baseKey, args: argumentsFor(fault, fault), token: JSON.stringify("failed-" + fault) });
      const terminal = receipt.terminal.result;
      assert.equal(terminal.isError, true);
      assert.equal(terminal.structuredContent.processing_state, "failed");
      assert.equal(terminal.structuredContent.errors[0].code, code);
      assert(terminal.structuredContent.errors[0].remediation);
      assert.equal(receipt.progress.length, count);
      assert(rememberCanonicalCounts(teamID, terminal.structuredContent.submission_id).every((value) => value === 0));
      receipts.push(receipt);
    });
  }

  await runStep("early-rejection", async () => {
    const before = (await health()).chat_requests.filter((item) => item.model === rememberModel).length;
    const args = argumentsFor("invalid");
    args.evidence = [];
    const rejected = await progressCall({ apiKey: baseKey, args, token: "0" });
    assert.equal(rejected.progress.length, 0);
    assert.equal(rejected.terminal.error.code, -32602);
    assert.equal((await health()).chat_requests.filter((item) => item.model === rememberModel).length, before);
  });

  await runStep("authenticated-A-B-C", async () => {
    const sharedTeam = await createKnownEvidenceTeam("progress-AB-" + randomUUID());
    const foreignTeam = await createKnownEvidenceTeam("progress-C-" + randomUUID());
    const actors = await Promise.all([
      createKnownEvidenceCredential(sharedTeam, "progress A"),
      createKnownEvidenceCredential(sharedTeam, "progress B"),
      createKnownEvidenceCredential(foreignTeam, "progress C"),
    ]);
    const key = "equal-key-" + randomUUID();
    const results = await Promise.all(actors.map(async (actor, index) => {
      const args = argumentsFor("actor-" + index, ["", "repair-exhausted", "embedding-model"][index], index !== 2);
      args.target_space = "private";
      args.idempotency_key = key;
      const receipt = await progressCall({ apiKey: actor.apiKey, args, token: JSON.stringify("equal-token") });
      assert.equal(receipt.progress.length, [5, 3, 2][index]);
      assert.equal(receipt.terminal.result.structuredContent.processing_state, index === 0 ? "completed" : "failed");
      return receipt;
    }));
    const evidenceID = results[0].terminal.result.structuredContent.evidence[0].evidence_id;
    for (const actor of actors.slice(1)) {
      const response = await fetch(required("DENSE_MEM_USER_URL").replace(/\/$/, "") + "/mcp", {
        method: "POST", headers: { Authorization: "Bearer " + actor.apiKey, Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "trace_memory", arguments: { evidence_id: evidenceID } } }),
      });
      const body = await response.json();
      assert(body.error || body.result?.isError, "another authenticated actor must not read private Evidence");
    }
    for (const [index, receipt] of results.entries()) {
      assert(!receipt.raw.includes(receipt.args.evidence[0].content.split("[fixture:")[0].trim()), "progress cannot echo private Evidence");
      receipt.actorKey = actors[index].apiKey;
      receipts.push(receipt);
    }
  });

  for (const version of ["2025-11-25", "2026-07-28"]) {
    await runStep("disconnect-and-recover-" + version, async () => {
      await provider("hold");
      const args = argumentsFor("disconnect", "admission-hold", false);
      const controller = new AbortController();
      try {
        await assert.rejects(progressCall({ apiKey: baseKey, args, token: "0", version, signal: controller.signal,
          onProgress: async (frame) => {
            if (frame.params.message !== "Checking duplicate Evidence") return;
            await waitFor(async () => (await health()).held_admission_requests > 0, "disconnect must reach required provider work");
            controller.abort();
          } }), { name: "AbortError" });
      } finally { controller.abort(); await provider("normal"); }
      let original;
      await waitFor(() => {
        original = postgresQuery("SELECT outcome || '|' || error_code FROM remember_attempts WHERE team_id = '" + teamID +
          "'::uuid AND idempotency_key = '" + args.idempotency_key + "' ORDER BY created_at DESC LIMIT 1").trim();
        return original.startsWith("failed|") || original.startsWith("completed|");
      }, "disconnected operation must reach its retained terminal outcome");
      if (version === "2026-07-28") assert.equal(original, "failed|request_cancelled");
      const recovered = await progressCall({ apiKey: baseKey, args, token: "0", version });
      assert.equal(recovered.terminal.result.structuredContent.processing_state, "completed");
      receipts.push(recovered);
      recovered.disconnectedOutcome = original;
    });
  }

  await runStep("log-only-diagnosis", async () => {
    const window = captureSemanticAssessmentWindow({
      project: required("DENSE_MEM_E2E_COMPOSE_PROJECT"), composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since,
      directory: (process.env.DENSE_MEM_E2E_REMEMBER_FAILURE_DIR || "/tmp/dense-mem-remember-failures") + "/progress-debug",
    });
    for (const secret of [baseKey, ...receipts.map((item) => item.actorKey).filter(Boolean)]) assert(!window.includes(secret));
    const records = window.split(/\r?\n/).filter(Boolean).map(JSON.parse);
    assert(!records.some((row) => row.level === "TRACE"), "DEBUG must filter TRACE");
    for (const receipt of receipts.filter((item) => item.progress.length && !item.disconnectedOutcome)) {
      const correlated = records.filter((row) => row.correlation_id === receipt.correlationID);
      const delivered = correlated.filter((row) => row.msg === "mcp_progress_delivered");
      assert.equal(delivered.length, receipt.progress.length);
      assert(delivered.every((row) => row.level === "DEBUG" && row.team_id && row.profile_id && row.invocation_id &&
        row.request_id && row.progress_token !== undefined && row.write_budget_ms === 100 && row.duration_ms >= 0));
      assert(correlated.some((row) => row.msg === "mcp_tool_outcome"));
    }
    const failed = receipts.find((item) => item.terminal.result?.structuredContent.errors?.[0]?.code === "provider_unavailable");
    const correlated = records.filter((row) => row.correlation_id === failed.correlationID);
    const terminal = correlated.find((row) => row.msg === "remember_invocation_completed");
    assert(terminal?.level === "ERROR" && terminal.error_code === "provider_unavailable" &&
      terminal.request_hash && terminal.canonical_attempt_id && terminal.retryable === true);
    const providerRows = correlated.filter((item) => item.msg === "ai_exchange_payload");
    const providerProof = diagnoseProviderHTTPLog(providerRows.map((row) => JSON.stringify(row)).join("\n"));
    assert(providerProof.exchanges.some((item) => item.direction === "response" && item.transport_capture_state === "no_response"));
    assert(providerProof.exchanges.some((item) => item.direction === "transport_error" && item.protected_bytes > 0));
    const exchanges = new Map();
    for (const row of providerRows.filter((item) => item.direction === "request")) {
      const key = row.exchange_id + ":" + row.direction;
      const group = exchanges.get(key) || [];
      group.push(row); exchanges.set(key, group);
    }
    let requestBody;
    for (const [key, rows] of exchanges) {
      rows.sort((a, b) => a.payload_chunk.index - b.payload_chunk.index);
      if (rows.every((row) => row.payload_chunk.state === "captured")) {
        const body = reconstructDiagnosticBody(rows);
        if (key.endsWith(":request") && body.model === rememberModel) requestBody = body;
      } else assert(rows.some((row) => row.payload_chunk.state === "unavailable" || row.transport_capture_state !== "captured"));
    }
    assert(requestBody && JSON.stringify(requestBody).includes(failed.args.evidence[0].content),
      "logs alone must retain the exact admitted provider input and effective model");
    expect(correlated.some((row) => row.msg === "remember_phase_completed" && row.phase === "assessment" && row.assessor_diagnostics),
      "logs alone must retain assessment decisions, attempts, validation and deadline context");
  });
  return { status: "ok", scenarios: receipts.length, telemetry_disabled: process.env.DENSE_MEM_E2E_TELEMETRY_DISABLED === "1",
    verified_progress_sample: receipts[0].progress.map(({ params }) => ({ message: params.message, progress: params.progress })),
    diagnostics: { cause: "provider_unavailable", action: "retry the unchanged complete request/key", source: "protected console logs" },
    cancellation: receipts.filter((item) => item.disconnectedOutcome).map((item) => ({ version: item.version, original: item.disconnectedOutcome, recovered: "completed" })) };
}

export async function parseProgressStream(body, onFrame) {
  const decoder = new TextDecoder();
  let pending = "";
  let bytes = 0;
  for await (const chunk of body) {
    bytes += chunk.byteLength;
    assert(bytes <= 4 << 20, "progress response exceeds its bounded capture");
    pending += decoder.decode(chunk, { stream: true });
    let newline;
    while ((newline = pending.indexOf("\n")) >= 0) {
      const line = pending.slice(0, newline).replace(/\r$/, "");
      pending = pending.slice(newline + 1);
      if (line.startsWith("data: ")) await onFrame(JSON.parse(line.slice(6)), line.slice(6));
    }
  }
  pending += decoder.decode();
  assert.equal(pending.trim(), "", "progress stream ended with an incomplete event");
}

async function progressCall({ apiKey, args, token, accept = "application/json, text/event-stream", version = "2026-07-28", signal, onProgress }) {
  const id = randomUUID();
  const correlationID = randomUUID();
  const params = { name: "remember", arguments: args, _meta: {
    "io.modelcontextprotocol/protocolVersion": version, "io.modelcontextprotocol/clientCapabilities": {},
  } };
  let body = JSON.stringify({ jsonrpc: "2.0", id, method: "tools/call", params });
  if (token !== undefined) body = body.replace('"_meta":{', '"_meta":{"progressToken":' + token + ",");
  const response = await fetch(required("DENSE_MEM_USER_URL").replace(/\/$/, "") + "/mcp", {
    method: "POST", headers: { Authorization: "Bearer " + apiKey, Accept: accept, "Content-Type": "application/json",
      "MCP-Protocol-Version": version, "Mcp-Method": "tools/call", "Mcp-Name": "remember", "X-Correlation-ID": correlationID },
    body, signal,
  });
  const progress = [];
  let terminal;
  let raw = "";
  const frame = async (item, text) => {
    raw += text;
    if (item.method === "notifications/progress") {
      assert(!terminal, "progress arrived after the terminal response");
      assert.equal(item.params.progress, progress.length + 1);
      assert(!Object.hasOwn(item.params, "total"));
      assert(messages.includes(item.params.message));
      assert(!progress.some((previous) => previous.params.message === item.params.message));
      if (/^-?\d+$/.test(token)) assert(text.includes('"progressToken":' + token + ",") || text.includes('"progressToken":' + token + "}"));
      else assert.equal(item.params.progressToken, JSON.parse(token));
      progress.push(item);
      assert(progress.length <= 5);
      if (onProgress) await onProgress(item);
    } else {
      assert(!terminal, "more than one terminal response");
      assert.equal(item.id, id);
      terminal = item;
    }
  };
  if (response.headers.get("content-type")?.includes("text/event-stream")) await parseProgressStream(response.body, frame);
  else { const text = await response.text(); await frame(JSON.parse(text), text); }
  assert(terminal, "missing terminal response");
  assert(response.ok || terminal.error, "HTTP failure lacks its bounded JSON-RPC error");
  return { args, progress, terminal, correlationID, version, raw };
}

function assertSuccess(receipt, count) {
  assert.equal(receipt.progress.length, count);
  assert.equal(receipt.terminal.result.structuredContent.processing_state, "completed");
  assert.equal(receipt.terminal.result.isError || false, false);
}

function argumentsFor(label, fault = "", forceInsert = true) {
  return { target_space: "team_shared", evidence: [{ content: "Exact progress Evidence [fixture:" + label + "-" + randomUUID() +
    "] admitted token password secret" + (fault ? " [fixture-fault:" + fault + "]" : ""), source_type: "manual", force_insert: forceInsert }],
    idempotency_key: "progress-" + randomUUID() };
}

async function waitFor(check, message) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(message);
}

function required(name) {
  const value = process.env[name];
  assert(value, name + " is required");
  return value;
}
