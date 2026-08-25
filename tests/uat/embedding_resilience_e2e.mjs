#!/usr/bin/env node

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const proxyURL = requiredEnv("DENSE_MEM_E2E_EMBEDDING_PROXY_URL").replace(/\/$/, "");

let rpcID = 0;
const runID = `embedding-resilience-e2e-${Date.now()}`;
const rejectedMarker = "embed-reject!";

const content = [
  `Subject 0 uses Store 0. ${runID} good evidence alpha uses the durable store.`,
  `Subject 1 uses Store 1. ${runID} embedding fixture token ${rejectedMarker}.`,
  `Subject 2 uses Store 2. ${runID} good evidence beta uses the durable store.`,
];
const remember = await mcpTool("remember", {
  idempotency_key: `${runID}:batch`,
  evidence: content.map((value, index) => ({
    content: value,
    source_type: "document",
    source: `${runID}:${index}`,
    source_group: runID,
  })),
  relationships: content.map((value, index) => {
    const subject = `Subject ${index}`;
    const predicate = "uses";
    const object = `Store ${index}`;
    return {
      ref: `${runID}:relationship:${index}`,
      subject: { name: subject, entity_kind: "concept" },
      predicate: { proposed_key: predicate },
      object: { entity: { name: object, entity_kind: "concept" } },
      polarity: "+",
      evidence_indices: [index],
    };
  }),
});
const stats = await proxyJSON("/stats");
if (stats.mode !== "input_rejected" || stats.input_rejection_failures < 1 || stats.forwarded < 1) {
  throw new Error(`embedding proxy did not observe the bounded batch call: ${JSON.stringify(stats)}`);
}
if (!Array.isArray(remember.errors) || remember.errors.length === 0) {
  throw new Error(`embedding failure was not returned in the originating Remember result: ${JSON.stringify(remember)}`);
}
if (JSON.stringify(remember).includes(rejectedMarker) || JSON.stringify(remember).includes("payload_too_large")) {
  throw new Error("Remember result exposed provider input details");
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_id: remember.submission_id ?? "",
  provider_requests: stats.requests,
  input_rejection_failures: stats.input_rejection_failures,
  forwarded_requests: stats.forwarded,
  inline_embedding_failure: true,
}, null, 2));

async function mcpTool(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }),
  });
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} returned a bounded error`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} returned no JSON content`);
  return JSON.parse(text);
}

async function proxyJSON(path) {
  return httpJSON(`${proxyURL}${path}`, { method: "GET" });
}

async function httpJSON(url, options) {
  const response = await fetch(url, { ...options, signal: options?.signal ?? AbortSignal.timeout(30_000) });
  const text = await response.text();
  if (!response.ok) throw new Error(`HTTP ${response.status} ${new URL(url).pathname}`);
  return text ? JSON.parse(text) : {};
}

function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
