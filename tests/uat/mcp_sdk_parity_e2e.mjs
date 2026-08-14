#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../..", import.meta.url));
const result = spawnSync("go", ["test", "./internal/mcp", "-run", "TestConformanceHarness", "-count=1"], { cwd: root, encoding: "utf8", timeout: 300_000 });
if (result.status !== 0) throw new Error(`MCP SDK parity harness failed: ${result.stderr || "redacted"}`);
console.log(JSON.stringify({ status: "ok", scenario: "mcp_sdk_parity", official_sdk: "v1.7.0", shared_registry: true, cancellation_and_errors: true }, null, 2));
