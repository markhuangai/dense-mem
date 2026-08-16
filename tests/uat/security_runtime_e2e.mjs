#!/usr/bin/env node

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const telemetryURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const telemetryToken = requiredEnv("DENSE_MEM_E2E_TELEMETRY_TOKEN");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
let rpcID = 100;

const malformed = await rawMCP("{");
assert(malformed.status === 400, `malformed JSON-RPC returned HTTP ${malformed.status}`);
assert(malformed.body.includes("malformed payload"), "malformed JSON-RPC did not return a bounded payload error");
assert(malformed.body.length < 4096, "malformed JSON-RPC response exceeded the bounded error size");
assert(!malformed.body.includes(apiKey), "malformed JSON-RPC response echoed the credential");

const invalidEnvelope = await rawMCP(JSON.stringify({ jsonrpc: "1.0", id: { object: true }, method: "tools/list" }));
assert(invalidEnvelope.status === 400, `invalid JSON-RPC envelope returned HTTP ${invalidEnvelope.status}`);
assert(invalidEnvelope.body.includes("malformed payload"), "invalid JSON-RPC envelope did not return a bounded payload error");
assert(invalidEnvelope.body.length < 4096, "invalid JSON-RPC envelope response exceeded the bounded error size");
assert(!invalidEnvelope.body.includes(apiKey), "invalid JSON-RPC envelope response echoed the credential");

for (const protocolVersion of ["2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"]) {
  const initialized = await mcpRPC("initialize", { protocolVersion });
  assert(initialized.response.status === 200, `${protocolVersion} initialize returned HTTP ${initialized.response.status}`);
  assert(initialized.payload?.result?.protocolVersion === protocolVersion, `${protocolVersion} was not negotiated`);

  const notification = await rawMCP(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }));
  assert(notification.status === 202 && notification.body === "", "initialized notification returned a response body");

  const listed = await mcpRPC("tools/list", {});
  assert(listed.response.status === 200, `${protocolVersion} tools/list returned HTTP ${listed.response.status}`);
  assert(Array.isArray(listed.payload?.result?.tools) && listed.payload.result.tools.length > 0, `${protocolVersion} tools/list was empty`);
}

const future = await rawMCP(
  JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "initialize", params: { protocolVersion: "2099-01-01" } }),
  { "MCP-Protocol-Version": "2099-01-01" },
);
assert(future.status === 400, `unknown protocol returned HTTP ${future.status}`);

const unknownTool = await fetch(`${userURL}/mcp`, {
  method: "POST",
  headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
  body: JSON.stringify({ jsonrpc: "2.0", id: 4, method: "tools/call", params: { name: "x".repeat(10000), arguments: {} } }),
});
const unknownToolBody = await unknownTool.text();
assert(unknownTool.status === 200 && unknownToolBody.includes("-32601"), "unknown MCP tool was not rejected");
assert(unknownToolBody.length < 4096, "unknown MCP tool response exceeded the bounded error size");

const missingToken = await fetch(`${telemetryURL}/metrics`);
assert(missingToken.status === 401, `telemetry scrape without token returned ${missingToken.status}`);
const wrongToken = await fetch(`${telemetryURL}/metrics`, { headers: { Authorization: "Bearer wrong-token" } });
assert(wrongToken.status === 401, `telemetry scrape with wrong token returned ${wrongToken.status}`);
const validToken = await fetch(`${telemetryURL}/metrics`, { headers: { Authorization: `Bearer ${telemetryToken}` } });
assert(validToken.status === 200, `telemetry scrape with configured token returned ${validToken.status}`);

console.log(JSON.stringify({
  status: "ok",
  bounded_json_rpc_errors: true,
  compatible_protocol_versions: true,
  notification_without_response: true,
  bounded_unknown_tool_error: true,
  telemetry_token_required: true,
}, null, 2));

async function rawMCP(body, headers = {}) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json", ...headers },
    body,
  });
  return { status: response.status, body: await response.text() };
}

async function mcpRPC(method, params) {
  const response = await rawMCP(JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }));
  return { response, payload: JSON.parse(response.body) };
}

function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function assert(condition, message) { if (!condition) throw new Error(message); }
