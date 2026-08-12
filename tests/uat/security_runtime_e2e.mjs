#!/usr/bin/env node

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const telemetryURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const telemetryToken = requiredEnv("DENSE_MEM_E2E_TELEMETRY_TOKEN");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");

const malformed = await rawMCP("{");
assert(malformed.status === 200, `malformed JSON-RPC returned HTTP ${malformed.status}`);
assert(malformed.body.includes("parse error"), "malformed JSON-RPC did not return the bounded parse error");
assert(!malformed.body.includes(apiKey), "malformed JSON-RPC response echoed the credential");

const invalidEnvelope = await rawMCP(JSON.stringify({ jsonrpc: "1.0", id: { object: true }, method: "tools/list" }));
assert(invalidEnvelope.status === 200, `invalid JSON-RPC envelope returned HTTP ${invalidEnvelope.status}`);
assert(invalidEnvelope.body.includes("invalid request"), "invalid JSON-RPC envelope did not return the bounded invalid-request error");

const notification = await rawMCP(JSON.stringify({ jsonrpc: "2.0", method: "initialize", params: { protocolVersion: "2025-11-25" } }));
assert(notification.status === 202 || notification.status === 204 || notification.body === "", "JSON-RPC notification unexpectedly returned a response body");

const unknownTool = await fetch(`${userURL}/mcp`, {
  method: "POST",
  headers: { Authorization: `Bearer ${apiKey}`, "Content-Type": "application/json" },
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
  notification_without_response: true,
  bounded_unknown_tool_error: true,
  telemetry_token_required: true,
}, null, 2));

async function rawMCP(body) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, "Content-Type": "application/json" },
    body,
  });
  return { status: response.status, body: await response.text() };
}

function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function assert(condition, message) { if (!condition) throw new Error(message); }
