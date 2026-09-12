import assert from "node:assert/strict";
import test from "node:test";

import { run } from "../src/run.js";

test("proxy help exits without requiring authorization", async () => {
  const stdio = captureStdio();

  const status = await run(["--help"], {}, stdio);

  assert.equal(status, 0);
  assert.match(stdio.stdoutText(), /Usage:/);
  assert.equal(stdio.stderrText(), "");
});

test("proxy reports configuration failures without starting the gateway", async () => {
  const stdio = captureStdio();

  const status = await run([], {}, stdio);

  assert.equal(status, 1);
  assert.match(stdio.stderrText(), /^Configuration error:/);
});

test("proxy reports gateway failures and redacts authorization", async () => {
  const stdio = captureStdio();
  let gatewayOptions;

  const status = await run(
    ["--url", "http://127.0.0.1:1/mcp", "--api-key", "dm_live_gateway_failure"],
    {},
    stdio,
    async (options) => {
      gatewayOptions = options;
      throw new Error("gateway refused Authorization: Bearer dm_live_gateway_failure");
    },
  );

  assert.equal(status, 1);
  assert.equal(gatewayOptions.streamableHttpUrl, "http://127.0.0.1:1/mcp");
  assert.equal(gatewayOptions.headers.Authorization, "Bearer dm_live_gateway_failure");
  assert.match(stdio.stderrText(), /^Dense-Mem MCP proxy error:/);
  assert.doesNotMatch(stdio.stderrText(), /dm_live_gateway_failure/);
});

function captureStdio() {
  let stdout = "";
  let stderr = "";
  return {
    stdout: { write(chunk) { stdout += chunk; } },
    stderr: { write(chunk) { stderr += chunk; } },
    stdoutText: () => stdout,
    stderrText: () => stderr,
  };
}
