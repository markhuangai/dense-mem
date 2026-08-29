#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../..", import.meta.url));
const result = spawnSync("go", ["test", "./internal/mcp", "-run", "TestConformanceHarness", "-count=1"], { cwd: root, encoding: "utf8", timeout: 300_000 });
if (result.status !== 0) throw new Error(`MCP SDK parity harness failed: ${result.stderr || "redacted"}`);

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
let rpcID = 0;
for (const version of ["2025-11-25", "2026-07-28"]) {
  const method = version === "2026-07-28" ? "server/discover" : "initialize";
  const headers = { "MCP-Protocol-Version": version };
  if (version === "2026-07-28") headers["Mcp-Method"] = method;
  const negotiated = await rpc(method, initializeParams(version), headers);
  if (negotiated.error || (version === "2026-07-28" ? !negotiated.result?.supportedVersions?.includes(version) : negotiated.result?.protocolVersion !== version)) {
    throw new Error(`live SDK protocol negotiation failed for ${version}`);
  }
}
const listed = await rpc("tools/list", {}, { "MCP-Protocol-Version": "2025-11-25" });
if (listed.error || !listed.result?.tools?.some((tool) => tool.name === "remember")) {
  throw new Error("live SDK tools/list did not expose the shared registry");
}
const hasStatusTool = listed.result.tools.some((tool) => tool.name === "get_submission_status");
const bounded = await rpc("tools/call", {
  name: hasStatusTool ? "get_submission_status" : "remember",
  arguments: hasStatusTool ? { submission_id: "00000000-0000-0000-0000-000000000000" } : {},
}, { "MCP-Protocol-Version": "2025-11-25" });
if (!bounded.error || typeof bounded.error.message !== "string" || bounded.error.message.length > 512) {
  throw new Error("live SDK error mapping was not bounded");
}
console.log(JSON.stringify({
  status: "ok",
  scenario: "mcp_sdk_parity",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  official_sdk: "v1.7.0",
  shared_registry: true,
  local_differential_harness: true,
  live_public_boundary: true,
  protocol_2025: true,
  protocol_2026: true,
  bounded_errors: true,
}, null, 2));

async function rpc(method, params, headers = {}) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json", ...headers },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`MCP HTTP ${response.status} response body redacted`);
  return text ? JSON.parse(text) : {};
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
