import assert from "node:assert/strict";
import http from "node:http";
import { fileURLToPath } from "node:url";
import test from "node:test";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";

test("proxy forwards stdio MCP requests to Dense-Mem Streamable HTTP", async () => {
  const requests = [];
  const server = http.createServer(async (req, res) => {
    if (req.method === "GET") {
      res.writeHead(405).end();
      return;
    }

    assert.equal(req.headers.authorization, "Bearer dm_live_test");

    const body = await readBody(req);
    const rpc = JSON.parse(body);
    requests.push(rpc.method);

    if (!("id" in rpc)) {
      res.writeHead(202).end();
      return;
    }

    const result = resultFor(rpc);

    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ jsonrpc: "2.0", id: rpc.id, result }));
  });

  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();

  const transport = new StdioClientTransport({
    command: process.execPath,
    args: ["bin/dense-mem-mcp-proxy.js"],
    cwd: fileURLToPath(new URL("..", import.meta.url)),
    env: {
      DENSE_MEM_MCP_URL: `http://127.0.0.1:${port}/mcp`,
      DENSE_MEM_API_KEY: "dm_live_test",
    },
    stderr: "pipe",
  });

  let stderr = "";
  transport.stderr?.on("data", (chunk) => {
    stderr += chunk;
  });
  const client = new Client({ name: "dense-mem-proxy-test", version: "0.0.0" });

  try {
    await withTimeout(client.connect(transport), 15000, () => stderr);
    const { tools } = await withTimeout(client.listTools(), 15000, () => stderr);
    assert.equal(tools[0].name, "remember");

    assert.deepEqual(requests, ["initialize", "notifications/initialized", "tools/list"]);
    assert.doesNotMatch(stderr, /dm_live_test/);
  } finally {
    await client.close();
    await new Promise((resolve) => server.close(resolve));
  }
});

async function readBody(req) {
  let body = "";
  for await (const chunk of req) {
    body += chunk;
  }
  return body;
}

function resultFor(rpc) {
  if (rpc.method === "tools/list") {
    return {
      tools: [{ name: "remember", description: "Remember", inputSchema: { type: "object" } }],
    };
  }

  return {
    protocolVersion: rpc.params?.protocolVersion ?? "2025-11-25",
    capabilities: { tools: {} },
    serverInfo: { name: "mock-dense-mem", version: "0.0.0" },
  };
}

function withTimeout(promise, timeoutMs, getDiagnostic) {
  let timeout;
  const timeoutPromise = new Promise((_, reject) => {
    timeout = setTimeout(() => {
      reject(new Error(`Timed out waiting for MCP proxy response. stderr: ${getDiagnostic()}`));
    }, timeoutMs);
  });

  return Promise.race([promise, timeoutPromise]).finally(() => clearTimeout(timeout));
}
