import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:http";
import { mkdtemp, readFile, readdir, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { diagnoseRememberFailure } from "./synchronous_write/cases/diagnostics.mjs";

test("temporary response diagnosis distinguishes discarded upstream details without publishing bodies", () => {
  for (const [body, expected] of [
    [JSON.stringify({ error: { message: "embedding upstream returned status 409" } }), "fixture_discarded_upstream_error_body"],
    [JSON.stringify({ error: { code: "fixture_embedding_conflict" } }), "deterministic_fixture_conflict"],
    [JSON.stringify({ error: { code: "private-response-canary", message: "private-response-canary" } }), "provider_error_details_in_temporary_capture"],
    ["{private-response-canary", "response_not_json"],
    ["", "response_body_unavailable"],
  ]) {
    const summary = diagnoseRememberFailure({ diagnostics: { provider_exchanges: [{ status_code: 409, response_body: body }] } });
    assert.equal(summary.provider_exchanges[0].response_analysis, expected);
    assert.doesNotMatch(JSON.stringify(summary), /private-response-canary|response_body"/);
  }
});

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

for (const failedTeam of ["fixture-team", "foreign-team"]) test(`ontology UAT privately captures ${failedTeam} failure before restoration and exports only its bounded summary`, async () => {
  const directory = await mkdtemp(join(tmpdir(), "ontology-remember-diagnostics-"));
  const calls = [];
  const attemptID = "67ea64c5-2baf-4a42-8cae-766d37c2b334";
  const correlationID = "a476694b-1998-4be7-9a20-51289b9a7c93";
  const detail = { team_id: failedTeam, attempt_id: attemptID, correlation_id: correlationID, outcome: "failed", error_code: "embedding_unavailable",
    failed_phase: "embedding", duration_ms: 2035, diagnostics: { provider_exchanges: [{ diagnostic_id: "exchange-1", component: "embedding",
      status_code: 409, capture_state: "captured", response_body: "protected-private-fixture-response" }] } };
  const server = createServer(async (request, response) => {
    request.resume();
    calls.push(request.url);
    let data = {};
    if (request.url === "/health") data = { chat_requests: [] };
    else if (request.url === "/control/api/config/ontology-maintenance") data = { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false" }] };
    else if (request.url === "/control/api/ontology/status") data = { paused: true, counts: {} };
    else if (request.url === "/control/api/teams") data = { id: "foreign-team" };
    else if (request.url.endsWith("/credentials")) data = { api_key: request.url.includes("foreign-team") ? "foreign-key" : "fixture-key", credential: { memory_access: { team_shared: { write: true } } } };
    else if (request.url.startsWith(`/control/api/remember-attempts?team_id=${failedTeam}&`)) data = [{ attempt_id: attemptID, correlation_id: correlationID }];
    else if (request.url === `/control/api/teams/${failedTeam}/remember-attempts/${attemptID}`) data = detail;
    response.writeHead(200, { "Content-Type": "application/json" });
    if (request.url === "/mcp") {
      const failed = failedTeam === "fixture-team" || request.headers.authorization === "Bearer foreign-key";
      const terminal = failed ? { submission_id: "unrelated-public-submission", processing_state: "failed", correlation_id: correlationID,
        errors: [{ code: "embedding_unavailable", reason_code: "embedding_request_rejected" }] } : {
        processing_state: "completed", evidence: [{ disposition: "stored", evidence_id: `source-${calls.length}` }], relationship_results: [],
      };
      response.end(JSON.stringify({ result: { isError: failed, content: [{ type: "text", text: JSON.stringify(terminal) }] } }));
    }
    else response.end(JSON.stringify(request.url === "/health" ? data : { data }));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const base = `http://127.0.0.1:${server.address().port}`;
  let child;
  try {
    child = spawn(process.execPath, [new URL("ontology_organization_e2e.mjs", import.meta.url).pathname], {
      env: { ...process.env, DENSE_MEM_USER_URL: base, DENSE_MEM_CONTROL_URL: base, DENSE_MEM_E2E_PROVIDER_URL: base,
        DENSE_MEM_CONTROL_TOKEN: "fixture-control", DENSE_MEM_E2E_API_KEY: "fixture-key", DENSE_MEM_E2E_TEAM_ID: "fixture-team",
        DENSE_MEM_E2E_REMEMBER_FAILURE_DIR: directory },
    });
    let stderr = "";
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    assert.equal((await once(child, "exit"))[0], 1);
    assert.deepEqual((await readdir(directory)).sort(), [`${attemptID}.private.json`, `${attemptID}.summary.json`], stderr);
    assert.deepEqual(JSON.parse(await readFile(join(directory, `${attemptID}.private.json`), "utf8")), detail);
    assert.equal((await stat(join(directory, `${attemptID}.private.json`))).mode & 0o777, 0o600);
    const summary = await readFile(join(directory, `${attemptID}.summary.json`), "utf8");
    assert.doesNotMatch(summary, /protected-private-fixture-response|response_body|request_body/);
    assert.equal(JSON.parse(summary).team_id, failedTeam);
    const workflow = await readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8");
    assert.doesNotMatch(workflow, /remember-failures/);
    assert.ok(calls.indexOf(`/control/api/teams/${failedTeam}/remember-attempts/${attemptID}`) < calls.indexOf("/ontology-fixture"));
    assert.match(stderr, /remember_failure_diagnostics/);
    assert.match(stderr, /"status_code":409/);
    assert.doesNotMatch(stderr, /protected-private-fixture-response/);
  } finally {
    if (child?.exitCode === null) { child.kill(); await once(child, "exit"); }
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
    await rm(directory, { recursive: true });
    await assert.rejects(stat(directory), { code: "ENOENT" });
  }
});
