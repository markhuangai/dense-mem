#!/usr/bin/env node

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
let rpcID = 0;

const legacy = await rpc("initialize", { protocolVersion: "2025-11-25" }, { "MCP-Protocol-Version": "2025-11-25" });
if (legacy.error || legacy.result?.protocolVersion !== "2025-11-25") throw new Error("2025-11-25 initialize negotiation failed");
const discover = await rpc("server/discover", { _meta: { "io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": {} } }, { "MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "server/discover" });
if (discover.error || !discover.result?.supportedVersions?.includes("2026-07-28")) throw new Error("2026-07-28 server/discover negotiation failed");
const list = await rpc("tools/list", {}, { "MCP-Protocol-Version": "2025-11-25" });
if (list.error || !list.result?.tools?.some((tool) => tool.name === "remember")) throw new Error("SDK tools/list did not expose the shared registry");
const unknownVersion = await rawRPC("initialize", { protocolVersion: "2099-01-01" }, { "MCP-Protocol-Version": "2099-01-01" });
if (unknownVersion.status !== 400) throw new Error("unknown protocol version was not rejected");
const invalidContent = await rawRPC("tools/list", {}, { "MCP-Protocol-Version": "2025-11-25", "Content-Type": "text/plain" });
if (![400, 415].includes(invalidContent.status)) throw new Error("invalid content type was not rejected");
const controller = new AbortController();
const pending = fetch(`${userURL}/mcp`, { method: "POST", signal: controller.signal, headers: { Authorization: `Bearer ${apiKey}`, "Content-Type": "application/json", Accept: "application/json", "MCP-Protocol-Version": "2025-11-25" }, body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }) });
controller.abort();
try { await pending; } catch (error) { if (error.name !== "AbortError") throw error; }
console.log(JSON.stringify({ status: "ok", scenario: "mcp_sdk_transport", protocol_2025: true, protocol_2026: true, unknown_version_rejected: true, malformed_content_rejected: true, cancellation_observed: true }, null, 2));

async function rpc(method, params, headers) { const response = await rawRPC(method, params, headers); if (!response.body) return {}; return JSON.parse(response.body); }
async function rawRPC(method, params, headers = {}) { const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json", ...headers }, body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }) }); return { status: response.status, body: await response.text() }; }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
