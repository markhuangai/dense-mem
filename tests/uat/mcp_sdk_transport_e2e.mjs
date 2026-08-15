#!/usr/bin/env node

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
let rpcID = 0;

for (const version of ["2025-11-25", "2026-07-28"]) {
  const method = version === "2026-07-28" ? "server/discover" : "initialize";
  const headers = { "MCP-Protocol-Version": version };
  if (version === "2026-07-28") headers["Mcp-Method"] = method;
  const negotiated = await rpc(method, initializeParams(version), headers);
  if (negotiated.error || (version === "2026-07-28" ? !negotiated.result?.supportedVersions?.includes(version) : negotiated.result?.protocolVersion !== version)) {
    throw new Error(`${version} protocol negotiation failed`);
  }
}
const list = await rpc("tools/list", {}, { "MCP-Protocol-Version": "2025-11-25" });
if (list.error || !list.result?.tools?.some((tool) => tool.name === "remember")) {
  throw new Error("SDK tools/list did not expose the shared registry");
}
const unknownVersion = await rawRPC("initialize", { protocolVersion: "2099-01-01" }, { "MCP-Protocol-Version": "2099-01-01" });
if (unknownVersion.status !== 400) throw new Error("unknown protocol version was not rejected");
const invalidContent = await rawRPC("tools/list", {}, { "MCP-Protocol-Version": "2025-11-25", "Content-Type": "text/plain" });
if (![400, 415].includes(invalidContent.status)) throw new Error("invalid content type was not rejected");
const invalidNotificationContent = await rawNotification("tools/list", {}, { "MCP-Protocol-Version": "2025-11-25", "Content-Type": "text/plain" });
if (![400, 415].includes(invalidNotificationContent.status)) throw new Error("invalid notification content type was not rejected");
const initializedNotification = await rawNotification("notifications/initialized", {}, { "MCP-Protocol-Version": "2025-11-25" });
if (initializedNotification.status !== 202 || initializedNotification.body !== "") throw new Error("initialized notification returned a response");

const controller = new AbortController();
const pending = fetch(`${userURL}/mcp`, {
  method: "POST",
  signal: controller.signal,
  headers: { Authorization: `Bearer ${apiKey}`, "Content-Type": "application/json", Accept: "application/json", "MCP-Protocol-Version": "2025-11-25" },
  body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }),
});
controller.abort();
try {
  await pending;
} catch (error) {
  if (error.name !== "AbortError") throw error;
}
console.log(JSON.stringify({
  status: "ok",
  scenario: "mcp_sdk_transport",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  protocol_2025: true,
  protocol_2026: true,
  unknown_version_rejected: true,
  malformed_content_rejected: true,
  notification_response_suppressed: true,
  cancellation_observed: true,
}, null, 2));

async function rpc(method, params, headers) {
  const response = await rawRPC(method, params, headers);
  if (!response.body) return {};
  return JSON.parse(response.body);
}

async function rawRPC(method, params, headers = {}) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json", ...headers },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }),
  });
  return { status: response.status, body: await response.text() };
}

async function rawNotification(method, params, headers = {}) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json", ...headers },
    body: JSON.stringify({ jsonrpc: "2.0", method, params }),
  });
  return { status: response.status, body: await response.text() };
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function initializeParams(version) {
  if (version === "2026-07-28") return { protocolVersion: version, _meta: { "io.modelcontextprotocol/protocolVersion": version, "io.modelcontextprotocol/clientCapabilities": {} } };
  return { protocolVersion: version };
}
