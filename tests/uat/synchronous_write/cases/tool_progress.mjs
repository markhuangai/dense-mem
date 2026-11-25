import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { progressCall } from "./progress.mjs";
import { createKnownEvidenceTeam, createKnownEvidenceCredential } from "./remember.mjs";
import { captureSemanticAssessmentWindow } from "../../ontology_assessment_diagnostics.mjs";

export const name = "tool_progress";

export async function run({ runStep }) {
  const since = new Date().toISOString();
  const team = await createKnownEvidenceTeam("tool-progress-AB-" + randomUUID());
  const other = await createKnownEvidenceTeam("tool-progress-C-" + randomUUID());
  const actors = await Promise.all([createKnownEvidenceCredential(team, "tool progress A"),
    createKnownEvidenceCredential(team, "tool progress B"), createKnownEvidenceCredential(other, "tool progress C")]);
  const captures = [];
  const call = async (name, args, options = {}) => {
    const result = await progressCall({ apiKey: actors[0].apiKey, name, args, token: '"shared-tool-token"', ...options });
    captures.push({ name, ...result });
    return result;
  };
  const stages = (receipt, expected) => {
    for (const message of expected) assert(receipt.progress.some((frame) => frame.params.message.replace(/ \(\d+\/\d+\)$/, "") === message), message);
  };
  const success = (receipt) => {
    assert(!receipt.terminal.error && !receipt.terminal.result.isError, JSON.stringify(receipt.terminal));
    return receipt.terminal.result.structuredContent;
  };
  const seeded = success(await call("remember", { target_space: "private", idempotency_key: randomUUID(),
    evidence: [{ content: `Private tool progress source ${randomUUID()}.`, source_type: "manual", force_insert: true }] }));
  const evidenceID = seeded.evidence[0].evidence_id;

  for (const version of ["2025-11-25", "2026-07-28"]) await runStep("actual-tool-stages-" + version, async () => {
    const args = { framework: "progress", app_name: "uat", user_id: "source-user", session_id: randomUUID(), idempotency_key: randomUUID(),
      events: [{ event_id: "one", text: `Progress${randomUUID().replaceAll("-", "")} uses PostgreSQL. [fixture-fault:session-repair]` }] };
    const ingested = await call("ingest_session", args, { version });
    assert.equal(success(ingested).processing_state, "completed");
    assert.equal(success(ingested).memory_outcome, "stored");
    assert(success(ingested).events[0].evidence_ids.length > 0);
    stages(ingested, ["Validating input", "Checking session ownership", "Checking previous result", "Preparing extraction windows",
      "Extracting exact session excerpts", "Regenerating session excerpts", "Saving validated session excerpts", "Preparing intake", "Preparing search state", "Committing", "Preparing result"]);
    const replay = await call("ingest_session", args, { version });
    assert.deepEqual(success(replay), success(ingested));
    stages(replay, ["Returning saved result"]);
    assert(!replay.progress.some((frame) => frame.params.message.startsWith("Extracting")));
    const recalled = await call("recall_memory", { query: "Private tool progress source", limit: 10 }, { version });
    const recall = success(recalled);
    assert(recall.results.some((item) => item.evidence_id === evidenceID));
    stages(recalled, ["Preparing Recall policy", "Preparing query embedding", "Searching authorized Evidence", "Reading authorized Evidence", "Recording the Recall snapshot"]);
    const trace = await call("trace_memory", { evidence_id: evidenceID }, { version });
    assert.equal(success(trace).evidence.evidence_id, evidenceID);
    stages(trace, ["Validating input", "Reading the authorized Evidence trace", "Preparing result"]);
    const pack = await call("export_memory_pack", { name: "progress pack", evidence_ids: [evidenceID] }, { version });
    assert.equal(JSON.parse(success(pack).artifact_json).evidence[0].fragment_id ?? JSON.parse(success(pack).artifact_json).evidence[0].evidence_id, evidenceID);
    stages(pack, ["Validating input", "Reading Evidence for export", "Building the memory pack"]);
    const feedback = await call("submit_recall_session_feedback", { recalls: [{ recall_event_id: recall.recall_id, used: true,
      answer_supported: true, quality: "high", missing_context: false, irrelevant: false }] }, { version });
    assert.equal(success(feedback).recorded_count, 1);
    stages(feedback, ["Validating input", "Recording Recall feedback", "Preparing result"]);
    for (const options of [{ accept: "application/json", token: "0" }, { token: undefined }]) {
      const terminalOnly = await call("trace_memory", { evidence_id: evidenceID }, { version, ...options });
      assert.equal(terminalOnly.progress.length, 0);
      assert.deepEqual(success(terminalOnly), success(trace));
    }
  });

  await runStep("concurrent-A-B-C-isolation", async () => {
    const results = await Promise.all(actors.map((actor) => call("trace_memory", { evidence_id: evidenceID }, { apiKey: actor.apiKey })));
    assert.equal(success(results[0]).evidence.evidence_id, evidenceID);
    for (const result of results.slice(1)) {
      assert(result.terminal.result?.isError, "B and C must not read A's private Evidence");
      assert(!JSON.stringify(result.terminal.result).includes(evidenceID));
    }
    for (const actor of actors.slice(1)) {
      for (const [name, args] of [["export_memory_pack", { name: "denied", evidence_ids: [evidenceID] }],
        ["retract_evidence", { evidence_ids: [evidenceID], reason: "non-owner", idempotency_key: randomUUID() }]]) {
        const denied = await call(name, args, { apiKey: actor.apiKey });
        assert(denied.terminal.result?.isError, `${name} must preserve private isolation`);
      }
    }
    const invalid = await call("trace_memory", { evidence_id: "invalid" });
    assert.equal(invalid.terminal.error.code, -32602);
    assert.equal(invalid.progress.length, 0);
    const failedSession = await call("ingest_session", { framework: "progress", app_name: "uat", user_id: "source-user", session_id: randomUUID(),
      idempotency_key: randomUUID(), events: [{ event_id: "one", text: "Session extraction must fail. [fixture-fault:repair-exhausted]" }] });
    assert(failedSession.terminal.result?.isError);
    stages(failedSession, ["Regenerating session excerpts", "Recording failure result"]);
    const badFeedback = await call("submit_recall_session_feedback", { recalls: [{ recall_event_id: "missing-" + randomUUID(), used: true,
      answer_supported: true, quality: "high", missing_context: false, irrelevant: false }] });
    assert.equal(success(badFeedback).recorded_count, 0);
    assert.equal(success(badFeedback).error_code, "invalid_input");
  });

  for (const version of ["2025-11-25", "2026-07-28"]) await runStep("recall-disconnect-" + version, async () => {
    const fixtureURL = required("DENSE_MEM_E2E_PROVIDER_URL").replace(/\/$/, "");
    const mode = async (value) => {
      const response = await fetch(fixtureURL + "/admission-fixture", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode: value }) });
      assert(response.ok);
    };
    const controller = new AbortController();
    const observed = [];
    await mode("hold");
    try {
      await assert.rejects(call("recall_memory", { query: "PostgreSQL [fixture-fault:admission-hold]" }, { version, signal: controller.signal,
        onProgress: async (frame) => {
          observed.push(frame);
          if (frame.params.message !== "Preparing query embedding") return;
          const deadline = Date.now() + 5000;
          while (Date.now() < deadline) {
            const response = await fetch(fixtureURL + "/health");
            assert(response.ok);
            if ((await response.json()).held_admission_requests > 0) { controller.abort(); return; }
            await new Promise((resolve) => setTimeout(resolve, 25));
          }
          throw new Error("Recall did not reach its held query embedding");
        } }), { name: "AbortError" });
      assert(observed.some((frame) => frame.params.message === "Preparing query embedding"));
    } finally { controller.abort(); await mode("normal"); }
    const recovered = await call("recall_memory", { query: "Private tool progress source", limit: 10 }, { version });
    assert(success(recovered).results.some((item) => item.evidence_id === evidenceID));
  });

  await runStep("retraction-replay", async () => {
    const args = { evidence_ids: [evidenceID], reason: "progress completed", idempotency_key: randomUUID() };
    const first = await call("retract_evidence", args);
    assert.equal(success(first).processing_state, "completed");
    stages(first, ["Validating input", "Applying Evidence retraction", "Preparing result"]);
    assert.deepEqual(success(await call("retract_evidence", args)), success(first));
  });

  const window = captureSemanticAssessmentWindow({ project: required("DENSE_MEM_E2E_COMPOSE_PROJECT"),
    composeFile: required("DENSE_MEM_E2E_COMPOSE_FILE"), since,
    directory: (process.env.DENSE_MEM_E2E_REMEMBER_FAILURE_DIR || "/tmp/dense-mem-remember-failures") + "/tool-progress" });
  for (const actor of actors) assert(!window.includes(actor.apiKey));
  const records = window.split(/\r?\n/).filter(Boolean).map(JSON.parse);
  for (const capture of captures.filter((item) => item.progress.length)) {
    const delivered = records.filter((row) => row.correlation_id === capture.correlationID && row.msg === "mcp_progress_delivered");
    assert.equal(delivered.length, capture.progress.length);
    assert(delivered.every((row) => row.tool === capture.name && row.team_id && row.profile_id && row.write_budget_ms === 100 && row.duration_ms >= 0));
    for (const frame of capture.progress) {
      assert(!frame.params.message.includes(evidenceID));
      assert(!frame.params.message.includes("source-user"));
    }
  }
  return { tools: [...new Set(captures.map((item) => item.name))], calls: captures.length, actual_stages: true, isolated_actors: ["A", "B", "C"],
    correlations: captures.map((item) => ({ tool: item.name, correlation_id: item.correlationID, progress_count: item.progress.length })) };
}
function required(name) { assert(process.env[name], `${name} is required`); return process.env[name]; }
