import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:http";
import test from "node:test";

for (const failures of [[], ["reset"], ["reset", "resume"]]) {
  test(`ontology UAT restores settings after failures: ${failures.join(",") || "original only"}`, async () => {
    const calls = [];
    const original = { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] };
    const server = createServer(async (request, response) => {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const body = chunks.length ? JSON.parse(Buffer.concat(chunks)) : null;
      let status = 200;
      let data = {};
      if (request.url === "/control/api/config/ontology-maintenance") {
        if (request.method === "GET") data = original;
        else if (body.items[0].value === "false") calls.push("restore");
      } else if (request.url === "/control/api/ontology/status") data = { paused: true, counts: {} };
      else if (request.url === "/control/api/ontology/resume") {
        calls.push("resume");
        if (failures.includes("resume")) status = 503;
      } else if (request.url === "/ontology-fixture") {
        calls.push("reset");
        if (failures.includes("reset")) status = 503;
      } else if (request.url.endsWith("/credentials")) status = 500;
      response.writeHead(status, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ data }));
    });
    server.listen(0, "127.0.0.1");
    await once(server, "listening");
    const base = `http://127.0.0.1:${server.address().port}`;
    let child;
    try {
      child = spawn(process.execPath, [new URL("ontology_organization_e2e.mjs", import.meta.url).pathname], {
        env: { ...process.env, DENSE_MEM_USER_URL: base, DENSE_MEM_CONTROL_URL: base, DENSE_MEM_E2E_PROVIDER_URL: base,
          DENSE_MEM_CONTROL_TOKEN: "fixture-control", DENSE_MEM_E2E_API_KEY: "fixture-key", DENSE_MEM_E2E_TEAM_ID: "fixture-team" },
      });
      let stderr = "";
      child.stderr.on("data", (chunk) => { stderr += chunk; });
      assert.equal((await once(child, "exit"))[0], 1);
      assert.deepEqual(calls, ["reset", "resume", "restore"]);
      assert.match(stderr, /credentials returned 500/);
      if (failures.includes("reset")) assert.match(stderr, /503/);
    } finally {
      if (child?.exitCode === null) { child.kill(); await once(child, "exit"); }
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    }
  });
}
