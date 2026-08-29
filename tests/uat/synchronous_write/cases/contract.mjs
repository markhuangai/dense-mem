import { randomUUID } from "node:crypto";
import {
  TERMINAL_TOOLS,
  assertTerminalCorrectionResult,
  assertTerminalRememberResult,
} from "../surface.mjs";

export const name = "contract";

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

  const runID = `synchronous-write-contract-${randomUUID()}`;
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
  const ownership = await assertOwnershipIsolation({ runID, source, split, trace, support, expect });

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

  const staleRaw = await rawRPC("tools/call", {
    name: "correct_relationship",
    arguments: {
      action: "submit",
      relationship_id: split.relationship_id,
      expected_version: trace.relationship?.version,
      patch: { object_entity: { name: `${runID} stale successor`, entity_kind: "project" } },
      supports: [{ evidence_id: support.evidence_id, start: support.span_start, end: support.span_end }],
      reason: "The relationship version is intentionally stale.",
      idempotency_key: `${runID}-stale-correction`,
    },
  });
  const stale = structuredToolResult(staleRaw, expect);
  assertTerminalCorrectionResult(stale);
  expect(stale.contract_version === "dense-mem.v2.6.1" && stale.processing_state === "rejected", "stale correction must return a terminal rejection");
  expect(stale.errors?.[0]?.code === "relationship_version_stale", `stale correction must preserve version classification: ${JSON.stringify(stale)}`);
  assertTextStructuredParity(staleRaw, expect);

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
    ownership_isolation: ownership,
    text_structured_parity: true,
  };
}

async function enableTargetFeatureGates() {
  const controlURL = validatedControlURL();
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

function validatedControlURL() {
  const raw = requiredEnv("DENSE_MEM_CONTROL_URL").trim();
  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error("DENSE_MEM_CONTROL_URL must be a valid URL");
  }
  const hostname = parsed.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  const loopback = hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1";
  if (parsed.username || parsed.password || (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && loopback))) {
    throw new Error("DENSE_MEM_CONTROL_URL must use HTTPS or loopback HTTP");
  }
  return parsed.toString().replace(/\/$/, "");
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

async function controlPayload(path, body, method = "POST") {
  const baseURL = validatedControlURL();
  const token = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
  const response = await fetch(`${baseURL}/control/api${path}`, {
    method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) throw new Error(`control API ${path} returned HTTP ${response.status}`);
  const text = await response.text();
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(`control API ${path} returned invalid JSON`);
  }
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function requiredString(value, field) {
  if (typeof value !== "string" || !value.trim()) throw new Error(`${field} missing`);
  return value;
}

async function assertOwnershipIsolation({ runID, source, split, trace, support, expect }) {
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const sameTeamCredential = await createControlCredential(teamID, `${runID}-same-team-owner`);
  const otherTeam = await controlPayload("/teams", {
    name: `${runID} other team`,
    description: "contract ownership isolation",
  });
  const otherTeamID = requiredString(otherTeam.data?.id, "other team id");
  const crossTeamCredential = await createControlCredential(otherTeamID, `${runID}-cross-team-owner`);
  const correction = correctionArguments(runID, split.relationship_id, trace.relationship?.version, support, "ownership");

  const sameTeamRaw = await rawRPCWithKey(sameTeamCredential.apiKey, "tools/call", { name: "correct_relationship", arguments: correction });
  const crossTeamRaw = await rawRPCWithKey(crossTeamCredential.apiKey, "tools/call", { name: "correct_relationship", arguments: correction });
  const sameTeamDenied = assertOwnershipDenied(sameTeamRaw, source, split.relationship_id, expect, "same-team non-owner");
  const crossTeamDenied = assertOwnershipDenied(crossTeamRaw, source, split.relationship_id, expect, "cross-team actor");

  for (const [label, credential] of [["same-team", sameTeamCredential], ["cross-team", crossTeamCredential]]) {
    const statusRaw = await rawRPCWithKey(credential.apiKey, "tools/call", {
      name: "get_submission_status",
      arguments: { submission_id: source.submission_id },
    });
    expect(statusRaw.error?.code === -32601 && !statusRaw.result, `${label} actor must not obtain the removed correction status tool`);
  }
  return {
    same_team_rejected: sameTeamDenied.processing_state,
    cross_team_rejected: crossTeamDenied.processing_state,
    status_lookup_removed: true,
  };
}

async function createControlCredential(teamID, name) {
  const payload = await controlPayload(`/teams/${teamID}/credentials`, {
    name,
    scopes: ["read", "write"],
    rate_limit: 300,
  });
  return { apiKey: requiredString(payload.data?.api_key, "credential api key") };
}

function correctionArguments(runID, relationshipID, expectedVersion, support, suffix) {
  return {
    action: "submit",
    relationship_id: relationshipID,
    expected_version: expectedVersion,
    patch: { object_entity: { name: `${runID} ${suffix} successor`, entity_kind: "project" } },
    supports: [{ evidence_id: support.evidence_id, start: support.span_start, end: support.span_end }],
    reason: `Ownership isolation ${suffix} correction must be denied.`,
    idempotency_key: `${runID}-${suffix}-ownership-correction`,
  };
}

function assertOwnershipDenied(raw, source, relationshipID, expect, label) {
  const denied = structuredToolResult(raw, expect);
  assertTerminalCorrectionResult(denied);
  expect(denied.processing_state === "failed", `${label} correction must fail without a target mutation`);
  expect(denied.errors?.[0]?.code === "entity_not_found", `${label} correction must use bounded not-found classification: ${JSON.stringify(denied)}`);
  expect(!denied.correction_result && !denied.awaiting_confirmation, `${label} correction must not expose a result or confirmation`);
  expect(denied.submission_id !== source.submission_id, `${label} correction must not expose the owner's submission ID`);
  expect(!JSON.stringify(denied).includes(relationshipID), `${label} correction must not expose the owner's relationship ID`);
  assertTextStructuredParity(raw, expect);
  return denied;
}

let directRPCID = 0;

async function rawRPCWithKey(apiKey, method, params) {
  const baseURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
  const response = await fetch(`${baseURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: `contract-${++directRPCID}`, method, params }),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`${method} request returned HTTP ${response.status}`);
  return text ? JSON.parse(text) : {};
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
