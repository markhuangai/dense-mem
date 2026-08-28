export const name = "correction";

const providerFaultMarker = "e2e-correction-provider-fault";
const providerTimeoutMarker = "e2e-correction-provider-timeout";
const sourcePlacementTimeoutSeconds = Number(process.env.DENSE_MEM_E2E_PLACEMENT_TIMEOUT_SECONDS || 240);

export async function run({ rpc, rawRPC, expect }) {
  const listed = await rpc("tools/list", {});
  const names = new Set((listed.tools || []).map((tool) => tool.name));
  for (const tool of ["remember", "correct_relationship", "get_submission_status", "trace_memory"]) {
    expect(names.has(tool), `correction surface must expose ${tool}`);
  }

  const runID = `synchronous-write-correction-${Date.now()}`;
  const successfulSource = await createSource(rpc, expect, `${runID}-success`);
  const successfulCorrection = await correct(rpc, successfulSource, `${runID}-correct`);
  const successfulStatus = await toolSuccess(rpc, "get_submission_status", { submission_id: successfulCorrection.submission_id });
  const successorID = String(successfulStatus.correction_result?.successor_relationship_id || "");
  expect(successfulStatus.processing_state === "completed", "successful correction status must be completed");
  expect(successfulStatus.search_state === "current", "successful correction search state must be current");
  expect(successorID && successorID !== successfulSource.relationshipID, "successful correction must return a distinct successor");

  const superseded = await toolSuccess(rpc, "trace_memory", { relationship_id: successfulSource.relationshipID });
  const successor = await toolSuccess(rpc, "trace_memory", { relationship_id: successorID });
  expect(superseded.relationship?.relationship_status === "superseded", "successful correction must supersede the original relationship");
  expect(successor.relationship?.relationship_status === "active", "successful correction must activate the successor relationship");

  const failedSource = await createSource(rpc, expect, `${runID}-failure`);
  const failed = await toolRaw(rawRPC, "correct_relationship", correctionInput(failedSource, `${providerFaultMarker}-${runID}`));
  expect(failed.error && failed.result === undefined, "provider failure must return a bounded MCP error");
  expect(String(failed.error.message || "").includes("embedding_unavailable"), "provider failure must retain its bounded embedding classification");

  const preserved = await toolSuccess(rpc, "trace_memory", { relationship_id: failedSource.relationshipID });
  expect(preserved.relationship?.relationship_status === "active", "provider failure must preserve the original active relationship");

  const timeoutSource = await createSource(rpc, expect, `${runID}-timeout`);
  const timedOut = await toolRaw(rawRPC, "correct_relationship", correctionInput(timeoutSource, `${providerTimeoutMarker}-${runID}`));
  expect(timedOut.error && timedOut.result === undefined, "provider timeout must return a bounded MCP error");
  expect(String(timedOut.error.message || "").includes("embedding_timeout"), "provider timeout must retain its bounded embedding classification");
  const timeoutPreserved = await toolSuccess(rpc, "trace_memory", { relationship_id: timeoutSource.relationshipID });
  expect(timeoutPreserved.relationship?.relationship_status === "active", "provider timeout must preserve the original active relationship");

  return { mode: name, processing_state: successfulStatus.processing_state, provider_failure_preserved: true, provider_timeout_preserved: true };
}

async function createSource(rpc, expect, label) {
  const subject = `${label} subject`;
  const object = `${label} original project`;
  const evidence = `${subject} uses ${object}.`;
  const receipt = await toolSuccess(rpc, "remember", {
    idempotency_key: `${label}-remember`,
    evidence: [{ content: evidence, source_type: "manual", source: label, source_group: label }],
    relationships: [{
      ref: `${label}-relationship`,
      subject: { name: subject, entity_kind: "project" },
      predicate: { proposed_key: "uses" },
      object: { entity: { name: object, entity_kind: "project" } },
      polarity: "+",
      evidence_indices: [0],
    }],
  });
  const status = await waitForRemember(rpc, receipt.submission_id);
  const split = status.relationship_results?.[0]?.splits?.[0];
  expect(split?.relationship_id, "remember must create a relationship before correction");
  const trace = await toolSuccess(rpc, "trace_memory", { relationship_id: split.relationship_id });
  const support = trace.evidence_supports?.[0];
  expect(trace.relationship?.relationship_status === "active", "remember source relationship must be active");
  expect(Number.isInteger(trace.relationship?.version) && trace.relationship.version > 0, "remember source relationship must expose its version");
  expect(support?.evidence_id && Number.isInteger(support.span_start) && Number.isInteger(support.span_end), "trace must expose correction support spans");
  return {
    relationshipID: split.relationship_id,
    version: trace.relationship.version,
    support: { evidence_id: support.evidence_id, start: support.span_start, end: support.span_end },
  };
}

async function correct(rpc, source, target) {
  return toolSuccess(rpc, "correct_relationship", correctionInput(source, target));
}

function correctionInput(source, target) {
  return {
    action: "submit",
    relationship_id: source.relationshipID,
    expected_version: source.version,
    patch: { object_entity: { name: target, entity_kind: "project" } },
    supports: [source.support],
    reason: "The relationship object was resolved incorrectly.",
    idempotency_key: `${target}-correction`,
  };
}

async function waitForRemember(rpc, submissionID) {
  let last = {};
  const attempts = Math.ceil((sourcePlacementTimeoutSeconds * 1000) / 2000);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const status = await toolSuccess(rpc, "get_submission_status", { submission_id: submissionID });
    last = status;
    if (status.processing_state === "completed") return status;
    if (["rejected", "failed", "quarantined"].includes(status.processing_state) || status.search_state === "failed") {
      throw new Error(`remember source reached ${status.processing_state}/${status.search_state}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 2000));
  }
  throw new Error(`timed out waiting for source relationship completion (${last.processing_state || "unknown"}/${last.search_state || "unknown"})`);
}

async function toolSuccess(rpc, name, args) {
  const result = await rpc("tools/call", { name, arguments: args });
  const text = result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} did not return JSON content`);
  return JSON.parse(text);
}

async function toolRaw(rawRPC, name, args) {
  return rawRPC("tools/call", { name, arguments: args });
}
