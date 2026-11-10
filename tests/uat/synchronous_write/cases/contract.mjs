import { randomUUID } from "node:crypto";
import { TERMINAL_TOOLS, assertContractInvalidInput, assertTerminalRememberResult } from "../surface.mjs";

export const name = "contract";
const CURRENT_CONTRACT_VERSION = "dense-mem.v3.0.0";

export async function run({ rpc, expect }) {
  await enableTargetFeatureGates();
  const tools = (await rpc("tools/list", {})).tools || [];
  const names = tools.map((tool) => tool.name);
  expect(names.length === TERMINAL_TOOLS.length && TERMINAL_TOOLS.every((name) => names.includes(name)), "v3 must expose exactly seven Evidence tools");
  for (const name of ["correct_relationship", "get_submission_status", "list_dreams", "get_dream", "resolve_dream_feedback"]) {
    expect(!names.includes(name), `${name} must be absent from the catalog`);
    const raw = await rpcRaw(name, {});
    expect(raw.error?.code === -32601 && !raw.result, `${name} must return bounded method-not-found`);
  }
  const remember = tools.find((tool) => tool.name === "remember");
  expect(JSON.stringify(remember.outputSchema.properties.contract_version.enum) === JSON.stringify([CURRENT_CONTRACT_VERSION]), "v3 must advertise only its current executable contract");
  expect(!Object.hasOwn(remember.inputSchema.properties, "relationships"), "Remember must accept Evidence without graph proposals");
  const traceTool = tools.find((tool) => tool.name === "trace_memory");
  expect(traceTool.inputSchema.required.includes("evidence_id") && !Object.hasOwn(traceTool.inputSchema.properties, "relationship_id"), "Trace must use Evidence identity");
  const recallTool = tools.find((tool) => tool.name === "recall_memory");
  expect(recallTool.inputSchema.properties.topic_ids.minItems === 1 && recallTool.inputSchema.properties.topic_ids.maxItems === 10 && recallTool.inputSchema.properties.topic_ids.uniqueItems, "topic filters must advertise bounded unique IDs");
  expect(recallTool.inputSchema.properties.topic_limit.default === 3 && recallTool.inputSchema.properties.topic_limit.maximum === 10, "topic hints must advertise their bounds");
  for (const name of ["remember", "recall_memory", "trace_memory", "export_memory_pack"]) {
    const schema = tools.find((tool) => tool.name === name).outputSchema;
    expect(schema.oneOf?.some((branch) => branch.properties?.reason_code), `${name} must advertise operational errors`);
  }
  const runID = `evidence-contract-${randomUUID()}`;
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const actorA = await createControlCredential(teamID, `${runID}-A`);
  const actorB = await createControlCredential(teamID, `${runID}-B`);
  const teamC = await controlPayload("/teams", { name: `${runID}-C` });
  const actorC = await createControlCredential(requiredString(teamC.data?.id, "other team id"), `${runID}-C`);
  const call = (actor, name, args) => rawRPCWithKey(actor.apiKey, "tools/call", { name, arguments: args });
  const malformedTrace = await call(actorA, "trace_memory", { evidence_id: "not-a-uuid" });
  assertContractInvalidInput(malformedTrace, "trace.evidence_id", "malformed Evidence ID", expect);
  for (const args of [{ query: "test", topic_ids: ["bad"] }, { topic_ids: [] }, { topic_ids: [randomUUID()], topic_limit: 11 }, { query: "test", known_relationship_ids: [] }]) {
    const raw = await call(actorA, "recall_memory", args);
    expect(raw.error?.code === -32602 && raw.error.data?.retryable === false, "invalid or retired Recall fields must reject before execution");
  }
  const sourceArgs = rememberArguments(runID);
  const sourceRaw = await call(actorA, "remember", sourceArgs);
  const source = successfulToolResult(sourceRaw, expect);
  assertTerminalRememberResult(source);
  expect(source.contract_version === CURRENT_CONTRACT_VERSION && source.processing_state === "completed" && source.evidence[0]?.evidence_id, "Evidence-only Remember must complete");
  assertTextStructuredParity(sourceRaw, expect);
  const evidenceID = source.evidence[0].evidence_id;
  const replay = successfulToolResult(await call(actorA, "remember", sourceArgs), expect);
  expect(stableJSON(replay) === stableJSON(source), "matching replay must preserve the original result");
  for (const actor of [actorA, actorB]) {
    const raw = await call(actor, "trace_memory", { evidence_id: evidenceID });
    const trace = successfulToolResult(raw, expect);
    expect(trace.evidence.evidence_id === evidenceID && trace.evidence.content === sourceArgs.evidence[0].content, "shared Evidence must retain exact content for both owners");
    expect(trace.occurrences.length > 0 && trace.decisions.length > 0 && Array.isArray(trace.lifecycle) && Array.isArray(trace.omissions), "Trace must expose Evidence lineage and omissions");
    assertTextStructuredParity(raw, expect);
  }
  const limited = successfulToolResult(await call(actorA, "trace_memory", { evidence_id: evidenceID, include_evidence_content: false, include_transitions: false, max_events: 1 }), expect);
  expect(!Object.hasOwn(limited.evidence, "content") && limited.evidence.content_omitted && limited.lifecycle.length === 0, "Trace flags must suppress content and transitions");
  const truncated = successfulToolResult(await call(actorA, "trace_memory", { evidence_id: evidenceID, max_fragment_content_runes: 5 }), expect);
  expect(Array.from(truncated.evidence.content).length === 5 && truncated.truncated && truncated.omissions.length > 0, "Trace must report content truncation");
  const unknown = await call(actorA, "trace_memory", { evidence_id: randomUUID() });
  const foreign = await call(actorC, "trace_memory", { evidence_id: evidenceID });
  expect(unknown.result?.isError === true && foreign.result?.isError === true, "missing and foreign Evidence must be unavailable");
  expect(!JSON.stringify(foreign).includes(evidenceID), "foreign Trace errors must not disclose the target identity");
  for (const actor of [actorB, actorC]) {
    const denied = await call(actor, "retract_evidence", { evidence_ids: [evidenceID], reason: "non-owner", idempotency_key: `${runID}-${actor === actorB ? "B" : "C"}-retract` });
    expect(denied.result?.isError === true, "read visibility must not grant mutation ownership");
  }
  const packRaw = await call(actorA, "export_memory_pack", { name: runID, evidence_ids: [evidenceID] });
  const pack = successfulToolResult(packRaw, expect);
  const artifact = JSON.parse(pack.artifact_json);
  expect(artifact.format === "dense-mem.memory-pack.v3.0" && artifact.evidence.length === 1 && artifact.evidence[0].content === sourceArgs.evidence[0].content, "v3 export must preserve exact Evidence");
  expect(artifact.evidence[0].occurrences.length > 0 && /^[a-f0-9]{64}$/.test(pack.content_sha256) && artifact.content_sha256 === pack.content_sha256, "export must retain provenance and its canonical content hash");
  assertTextStructuredParity(packRaw, expect);
  const metadataPack = successfulToolResult(await call(actorA, "export_memory_pack", { name: runID, evidence_ids: [evidenceID], include_evidence: false }), expect);
  const metadataArtifact = JSON.parse(metadataPack.artifact_json);
  expect(!Object.hasOwn(metadataArtifact.evidence[0], "content") && metadataPack.omissions.length > 0, "content-free export must report omissions");
  for (const ids of [[], Array.from({ length: 501 }, () => randomUUID())]) {
    expect((await call(actorA, "export_memory_pack", { name: runID, evidence_ids: ids })).error?.code === -32602, "export ID bounds must fail visibly");
  }
  const mixed = await call(actorC, "export_memory_pack", { name: runID, evidence_ids: [evidenceID, randomUUID()] });
  expect(mixed.result?.isError && !JSON.stringify(mixed.result).includes(evidenceID), "foreign export must fail without disclosing the protected Evidence identity");
  const retractionArgs = { evidence_ids: [evidenceID], reason: "Evidence contract fixture completed", idempotency_key: `${runID}-owner-retract` };
  const retracted = successfulToolResult(await call(actorA, "retract_evidence", retractionArgs), expect);
  expect(retracted.retracted_evidence_ids.includes(evidenceID), "the semantic owner must retract Evidence");
  expect(stableJSON(successfulToolResult(await call(actorA, "retract_evidence", retractionArgs), expect)) === stableJSON(retracted), "retraction replay must preserve its outcome");
  const after = successfulToolResult(await call(actorA, "trace_memory", { evidence_id: evidenceID }), expect);
  expect(after.lifecycle.some((event) => event.action === "retract"), "Evidence Trace must retain retraction history");
  return { mode: name, tools: names.length, contract_version: CURRENT_CONTRACT_VERSION, exact_evidence: true, trace_export: true, owner_ABC: true, retraction: true };
}

function rememberArguments(runID) {
  return { target_space: "team_shared", idempotency_key: `${runID}-remember`, evidence: [{ content: `${runID} records exact Evidence without factual graph proposals.`, source_type: "manual", source: runID, source_group: runID }] };
}

function rpcRaw(name, args) { return rawRPCWithKey(requiredEnv("DENSE_MEM_E2E_API_KEY"), "tools/call", { name, arguments: args }); }

async function enableTargetFeatureGates() {
  const controlURL = validatedControlURL();
  const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
  await controlJSON(controlURL, controlToken, "/config/recall-feedback", {
    items: [
      { key: "RECALL_FEEDBACK_ENABLED", value: "true" },
      { key: "RECALL_FEEDBACK_RETENTION_DAYS", value: "30" },
    ],
  });
}

function validatedControlURL() {
  return validatedEndpointURL("DENSE_MEM_CONTROL_URL");
}

function validatedUserURL() {
  return validatedEndpointURL("DENSE_MEM_USER_URL");
}

export function validatedEndpointURL(name) {
  const raw = requiredEnv(name).trim();
  let parsed;
  try {
    parsed = new URL(raw);
  } catch {
    throw new Error(`${name} must be a valid URL`);
  }
  const hostname = parsed.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  const loopback = hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1";
  const productionComposeEndpoint = process.env.DENSE_MEM_E2E_RUNTIME === "production"
    && parsed.protocol === "http:"
    && parsed.pathname === "/"
    && !parsed.search
    && !parsed.hash
    && (
      (name === "DENSE_MEM_CONTROL_URL" && hostname === "server" && parsed.port === "8090")
      || (name === "DENSE_MEM_USER_URL" && hostname === "server" && parsed.port === "8080")
    );
  if (parsed.username || parsed.password || (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && (loopback || productionComposeEndpoint)))) {
    throw new Error(`${name} must use HTTPS, loopback HTTP, or its production Compose endpoint`);
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

async function createControlCredential(teamID, name) {
  const request = {
    name,
    scopes: ["read", "write"],
    rate_limit: 300,
  };
  const payload = await controlPayload(`/teams/${teamID}/credentials`, request);
  return { apiKey: requiredString(payload.data?.api_key, "credential api key") };
}

let directRPCID = 0;

async function rawRPCWithKey(apiKey, method, params) {
  const baseURL = validatedUserURL();
  const response = await fetch(`${baseURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: `contract-${++directRPCID}`, method, params }),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`${method} request returned HTTP ${response.status}`);
  return text ? JSON.parse(text) : {};
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

function stableJSON(value) {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}
