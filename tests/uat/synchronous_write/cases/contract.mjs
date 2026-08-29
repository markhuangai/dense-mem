export const name = "contract";

import {
  TERMINAL_TOOLS,
  assertTerminalCorrectionResult,
  assertTerminalRememberResult,
} from "../surface.mjs";

export async function run({ rpc, rawRPC, expect }) {
  await enableTargetFeatureGates();
  const listed = await rpc("tools/list", {});
  const tools = listed.tools || [];
  const names = tools.map((tool) => tool.name);
  expect(names.length === TERMINAL_TOOLS.length && TERMINAL_TOOLS.every((tool) => names.includes(tool)), "v2.6.1 catalog must expose exactly ten tools");
  expect(!names.includes("get_submission_status"), "v2.6.1 catalog must remove get_submission_status");

  const rememberTool = tools.find((tool) => tool.name === "remember");
  const rememberSchema = rememberTool?.outputSchema || rememberTool?.output_schema || {};
  const rememberProperties = rememberSchema.properties || {};
  expect(JSON.stringify(rememberProperties.contract_version?.enum) === JSON.stringify(["dense-mem.v2.6.1"]), "Remember schema must identify v2.6.1");
  expect(!Object.hasOwn(rememberProperties, "status_tool") && !Object.hasOwn(rememberProperties, "check_after_seconds"), "v2.6.1 Remember schema must not expose polling fields");

  const runID = `synchronous-write-contract-${Date.now()}`;
  const sourceRaw = await rawRPC("tools/call", { name: "remember", arguments: rememberArguments(runID, "none") });
  const source = successfulToolResult(sourceRaw, expect);
  assertTerminalRememberResult(source);
  expect(source.contract_version === "dense-mem.v2.6.1", "terminal Remember must return v2.6.1");
  expect(source.processing_state === "completed", `terminal Remember must complete: ${JSON.stringify(source)}`);
  expect(source.relationship_results?.[0]?.splits?.[0]?.relationship_id, "terminal Remember must return a relationship split");
  assertTextStructuredParity(sourceRaw, expect);

  const removed = await rawRPC("tools/call", { name: "get_submission_status", arguments: { submission_id: source.submission_id } });
  expect(removed.error?.code === -32601 && !removed.result, "removed get_submission_status must return bounded method-not-found");

  const rejectedRaw = await rawRPC("tools/call", { name: "remember", arguments: rememberArguments(`${runID}-rejected`, "no-supported") });
  const rejected = structuredToolResult(rejectedRaw, expect);
  assertTerminalRememberResult(rejected);
  expect(rejected.contract_version === "dense-mem.v2.6.1" && rejected.processing_state === "rejected", "terminal rejection must preserve target state");
  expect(rejected.errors?.[0]?.code === "no_supported_memory", "terminal rejection must preserve bounded error code");
  assertTextStructuredParity(rejectedRaw, expect);

  const split = source.relationship_results[0].splits[0];
  const trace = await toolSuccess(rpc, "trace_memory", { relationship_id: split.relationship_id });
  const support = trace.evidence_supports?.[0];
  expect(support?.evidence_id && Number.isInteger(support.span_start) && Number.isInteger(support.span_end), "trace must expose correction support spans");
  const correctionRaw = await rawRPC("tools/call", {
    name: "correct_relationship",
    arguments: {
      action: "submit",
      relationship_id: split.relationship_id,
      expected_version: trace.relationship?.version,
      patch: { object_entity: { name: `${runID} successor`, entity_kind: "project" } },
      supports: [{ evidence_id: support.evidence_id, start: support.span_start, end: support.span_end }],
      reason: "The relationship object was resolved incorrectly.",
      idempotency_key: `${runID}-correction`,
    },
  });
  const correction = successfulToolResult(correctionRaw, expect);
  assertTerminalCorrectionResult(correction);
  expect(correction.contract_version === "dense-mem.v2.6.1", "direct correction must return v2.6.1");
  expect(!Object.hasOwn(correction, "status_tool") && !Object.hasOwn(correction, "check_after_seconds"), "direct correction must not return polling metadata");
  assertTextStructuredParity(correctionRaw, expect);

  // Keep the intermediate confirmation branch exercised even when this
  // disposable seed resolves the correction directly to completed.
  assertTerminalCorrectionResult({
    contract_version: "dense-mem.v2.6.1",
    submission_id: "fixture-confirmation",
    submission_kind: "relationship_correction",
    processing_state: "awaiting_confirmation",
    search_state: "pending",
    correlation_id: "fixture-confirmation-correlation",
    awaiting_confirmation: {
      confirmation_token: "fixture-token",
      expires_at: "2026-08-30T00:00:00Z",
      candidates: [
        { endpoint: "subject_entity", entity_id: "fixture-subject", entity_kind: "project", canonical_name: "Subject" },
        { endpoint: "object_entity", entity_id: "fixture-object", entity_kind: "project", canonical_name: "Object" },
      ],
    },
    errors: [],
  });

  return {
    mode: name,
    tools: names.length,
    removed_status: true,
    remember_state: source.processing_state,
    rejection_is_error: rejectedRaw.result?.isError === true,
    correction_state: correction.processing_state,
    text_structured_parity: true,
  };
}

async function enableTargetFeatureGates() {
  const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
  const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  await controlJSON(controlURL, controlToken, "/config/recall-feedback", {
    items: [
      { key: "RECALL_FEEDBACK_ENABLED", value: "true" },
      { key: "RECALL_FEEDBACK_RETENTION_DAYS", value: "30" },
    ],
  });
  await controlJSON(controlURL, controlToken, "/config/dreaming", {
    items: [
      { key: "DREAMING_ENABLED", value: "true" },
      { key: "DREAMING_FORCE_ENABLED", value: "true" },
      { key: "DREAMING_START_TIME_LOCAL", value: "03:00" },
      { key: "DREAMING_MAX_OUTPUTS", value: "5" },
    ],
  });
  await controlJSON(controlURL, controlToken, `/teams/${teamID}`, {
    config: { dreaming: { enabled: true } },
  }, "PATCH");
}

async function controlJSON(baseURL, token, path, body, method = "PATCH") {
  const response = await fetch(`${baseURL}/control/api${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) throw new Error(`control API ${path} returned HTTP ${response.status}`);
  return response.text();
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function rememberArguments(label, fault) {
  const subject = `${label} subject`;
  const object = `${label} object`;
  const marker = fault === "none" ? "fixture-contract" : `fixture-fault:${fault}`;
  return {
    idempotency_key: `${label}-remember`,
    evidence: [{ content: `${subject} uses ${object}. [${marker}]`, source_type: "manual", source: label, source_group: label }],
    relationships: [{
      ref: `${label}-relationship`,
      subject: { name: subject, entity_kind: "project" },
      predicate: { proposed_key: "uses" },
      object: { entity: { name: object, entity_kind: "project" } },
      polarity: "+",
      evidence_indices: [0],
    }],
  };
}

function successfulToolResult(raw, expect) {
  expect(!raw.error && raw.result && raw.result.isError !== true, "MCP tool call must succeed");
  const text = raw.result?.content?.[0]?.text;
  expect(typeof text === "string", "MCP tool call must include JSON text");
  return JSON.parse(text);
}

function structuredToolResult(raw, expect) {
  expect(!raw.error && raw.result?.isError === true, "target rejection must be an MCP structured error result");
  const text = raw.result?.content?.[0]?.text;
  expect(typeof text === "string" && raw.result?.structuredContent, "structured error must include text and structuredContent");
  return JSON.parse(text);
}

function assertTextStructuredParity(raw, expect) {
  const text = raw.result?.content?.[0]?.text;
  const parsed = JSON.parse(text);
  expect(stableJSON(parsed) === stableJSON(raw.result?.structuredContent), "MCP text and structuredContent must be equivalent");
}

async function toolSuccess(rpc, name, argumentsValue) {
  const raw = await rpc("tools/call", { name, arguments: argumentsValue });
  const text = raw?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} did not return JSON content`);
  return JSON.parse(text);
}

function stableJSON(value) {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}
