import assert from "node:assert/strict";
import { rememberWithEmbeddingRetry } from "../synchronous_write/remember_retry.mjs";

let sequence = 0;

export async function remember(base, apiKey, args) {
  const response = await rememberWithEmbeddingRetry(args, (request) => envelope(base, apiKey, "remember", request));
  return payload(response);
}

export async function callTool(base, apiKey, name, args, options) {
  const response = await envelope(base, apiKey, name, args, options);
  return { receipt: payload(response), isError: response.result.isError === true };
}

async function envelope(base, apiKey, name, args, { signal, correlationID } = {}) {
  const headers = { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" };
  if (correlationID) headers["X-Correlation-ID"] = correlationID;
  const response = await fetch(`${base}/mcp`, { method: "POST",
    headers,
    body: JSON.stringify({ jsonrpc: "2.0", id: ++sequence, method: "tools/call", params: { name, arguments: args } }), signal });
  assert(response.ok, `MCP HTTP ${response.status}`);
  return response.json();
}

function payload(value) {
  assert(!value.error, "MCP error");
  return value.result.structuredContent || JSON.parse(value.result.content.find((item) => item.type === "text").text);
}
