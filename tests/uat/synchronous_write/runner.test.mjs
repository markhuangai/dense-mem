import assert from "node:assert/strict";
import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:http";

import { fixtureChatResponse, fixtureFault } from "./provider-fixture.mjs";
import { discoverCases } from "./runner.mjs";
import { createHash } from "node:crypto";
import { diagnoseProviderHTTPLog } from "./cases/diagnostics.mjs";

test("private HTTP diagnostics reconstruct and verify chunks without exporting bodies", () => {
  const body = '{"error":"complete private provider response"}';
  const digest = createHash("sha256").update(body).digest("hex");
  const parts = [body.slice(0, 15), body.slice(15)];
  const records = parts.map((part, index) => ({ msg: "ai_exchange_payload", exchange_id: "exchange", direction: "response", team_id: "team", run_id: "run", batch_id: "batch", status_code: 409, transport_capture_state: "captured", started_at: "2026-11-03T22:00:00Z", elapsed_ms: 12, payload_chunk: { index, count: 2, body: part, sha256: digest, state: "captured" } }));
  const encoded = records.map(JSON.stringify).join("\n");
  const result = diagnoseProviderHTTPLog(encoded);
  assert.equal(result.exchanges.length, 1);
  assert.equal(result.exchanges[0].status_code, 409);
  assert.equal(result.exchanges[0].sha256, digest);
  assert.equal(result.exchanges[0].protected_bytes, Buffer.byteLength(body));
  assert.doesNotMatch(JSON.stringify(result), /complete private provider response/);
  assert.throws(() => diagnoseProviderHTTPLog(JSON.stringify(records[0])), /chunk count mismatch/);
  const corrupted = structuredClone(records);
  corrupted[1].payload_chunk.body = "changed";
  assert.throws(() => diagnoseProviderHTTPLog(corrupted.map(JSON.stringify).join("\n")), /digest mismatch/);
});

test("case progress preserves results, errors, stdout and run correlation", async () => {
  const runner = new URL("./runner.mjs", import.meta.url).href;
  const script = `
    import assert from 'node:assert/strict';
    import { runStep } from ${JSON.stringify(runner)};
    const result = await runStep('remember/completed', async () => ({ completed: true }));
    const failure = new Error('the-original-failure');
    await assert.rejects(runStep('remember/failed', async () => { throw failure; }), error => error === failure);
    process.stdout.write(JSON.stringify(result));
  `;
  const child = spawn(process.execPath, ["--input-type=module", "-e", script], { env: {
    ...process.env, DENSE_MEM_E2E_RUN_ID: "691", DENSE_MEM_E2E_RUN_ATTEMPT: "2",
    DENSE_MEM_E2E_SCENARIO: "synchronous_write", DENSE_MEM_E2E_COMMIT_SHA: "1".repeat(40),
  } });
  const closed = once(child, "close");
  let stdout = "", stderr = "";
  child.stdout.on("data", chunk => { stdout += chunk; });
  child.stderr.on("data", chunk => { stderr += chunk; });
  const [code] = await closed;
  assert.equal(code, 0, stderr);
  assert.deepEqual(JSON.parse(stdout), { completed: true });
  const events = stderr.trim().split("\n").map(JSON.parse);
  assert.deepEqual(events.map(event => [event.case, event.outcome]), [
    ["remember/completed", "started"], ["remember/completed", "passed"],
    ["remember/failed", "started"], ["remember/failed", "failed"],
  ]);
  for (const event of events) {
    assert.equal(event.event, "e2e_case_progress");
    assert.equal(event.run_id, "691");
    assert.equal(event.run_attempt, "2");
    assert.equal(event.scenario, "synchronous_write");
    assert.equal(event.source_revision, "1".repeat(40));
    assert.ok(event.elapsed_ms >= 0);
  }
});

test("an interrupted nested fault identifies its active step before completion", { timeout: 10000 }, async () => {
  let received;
  const requestReceived = new Promise(resolve => { received = resolve; });
  const server = createServer(() => received());
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const runner = new URL("./runner.mjs", import.meta.url).href;
  const child = spawn(process.execPath, ["--input-type=module", "-e", `
    import { runStep } from ${JSON.stringify(runner)};
    await runStep('remember/assessment', () =>
      runStep('remember/assessment-repair', () => fetch('http://127.0.0.1:${server.address().port}')));
  `]);
  const closed = once(child, "close");
  let stderr = "";
  let started;
  const progressStarted = new Promise(resolve => { started = resolve; });
  child.stderr.on("data", chunk => {
    stderr += chunk;
    if (stderr.endsWith("\n") && stderr.includes('"case":"remember/assessment-repair"')) started();
  });
  try {
    await Promise.race([
      Promise.all([requestReceived, progressStarted]),
      closed.then(result => { throw new Error(`case exited before blocking: ${JSON.stringify(result)} ${stderr}`); }),
    ]);
    const events = stderr.trim().split("\n").map(JSON.parse);
    assert.deepEqual(events.map(event => event.case), ["remember/assessment", "remember/assessment-repair"]);
    assert.match(stderr, /"outcome":"started"/);
    assert.doesNotMatch(stderr, /"outcome":"(?:passed|failed)"/);
    child.kill("SIGTERM");
    const [code, signal] = await closed;
    assert.equal(code, null);
    assert.equal(signal, "SIGTERM");
    assert.equal(stderr.trim().split("\n").length, 2);
  } finally {
    child.kill("SIGKILL");
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
});

test("provider fixture routes topic organization with locked classifications and pair constraints", () => {
  const request = structuredRequest("ontology_evidence_organization_v3", {
    request_id: "organization-fixture", definitions: [{ ref: "locked-topic", kind: "topic" }],
    items: [{ ref: "evidence-a", kind: "evidence", locked_definition_ref: "locked-topic" }, { ref: "evidence-b", kind: "evidence", text: "Atlas uses Redis." }],
    pairs: [{ ref: "separated", required_relation: "distinct" }, { ref: "joined", required_relation: "equivalent" }, { ref: "unconstrained", required_relation: "" }],
  });
  const response = fixtureChatResponse(request);
  assert.equal(response.request_id, "organization-fixture");
  assert.deepEqual(Object.keys(response).sort(), ["definitions", "equivalence", "items", "request_id"]);
  assert.equal(response.items[0].definition_ref, "locked-topic");
  assert.equal(response.definitions[0].kind, "topic");
  assert.deepEqual(response.equivalence.map((pair) => pair.relation), ["distinct", "equivalent", "distinct"]);
});

test("synchronous-write cases are sorted and filterable", async () => {
  const directory = join(tmpdir(), "dense-mem-synchronous-write-" + Date.now() + "-" + process.pid);
  await mkdir(join(directory, "nested"), { recursive: true });
  await writeFile(join(directory, "zeta.mjs"), "export const name = 'zeta'; export const run = () => ({});\\n");
  await writeFile(join(directory, "alpha.mjs"), "export const name = 'alpha'; export const run = () => ({});\\n");
  await writeFile(join(directory, "README.md"), "ignored\\n");
  try {
    const all = await discoverCases(directory, "");
    assert.deepEqual(all.map((url) => url.split("/").pop()), ["alpha.mjs", "zeta.mjs"]);
    const filtered = await discoverCases(directory, "zeta");
    assert.deepEqual(filtered.map((url) => url.split("/").pop()), ["zeta.mjs"]);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("shared scenario runner owns synchronous-write diagnostics coverage", async () => {
  const scenario = await readFile(new URL("../../../scripts/e2e-scenario.sh", import.meta.url), "utf8");
  assert.match(scenario, /run_node_case tests\/uat\/synchronous_write\/runner\.mjs/);
  assert.match(scenario, /synchronous_write\) specs=\("tests-compose\/remember-attempts\.spec\.ts"\)/);
  assert.match(scenario, /DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE/);
  assert.doesNotMatch(scenario, /parse_json_dream_statement/);
});

test("provider fixture retains bounded timeout and fault behavior", async () => {
  const fixture = await readFile(new URL("./provider-fixture.mjs", import.meta.url), "utf8");
  assert.match(fixture, /timeoutDelayMs/);
  assert.match(fixture, /embeddingCallsByFault/);
  assert.match(fixture, /routeFault === "embedding-cancel" && embeddingFaultCall === 1/);
});

test("provider fixture emits complete Evidence assessment and invalid allowlist variants", () => {
  const request = structuredRequest("dense_mem_evidence_assessment_response", {
    request_id: "assessment-invalid-request",
    evidence: [{ evidence_id: "evidence-1", content: "Dense-Mem uses PostgreSQL.", boundary_text: "⟦fixture-start⟧Dense-Mem uses PostgreSQL.⟦fixture-end⟧" }],
  });
  const response = fixtureChatResponse(request);
  assert.deepEqual(Object.keys(response).sort(), ["evidence_conflict_results", "evidence_equivalence_results", "evidence_security_results", "request_id"]);
  assert.deepEqual(response.evidence_security_results, [{ evidence_id: "evidence-1", decision: "pass", signals: [] }]);
  assert.equal(fixtureChatResponse(request, "assessment-invalid").evidence_security_results[0].evidence_id, "provider-forged-ref");
  assert.equal(fixtureChatResponse(request, "assessment-duplicate").evidence_security_results.length, 2);
  assert.equal(fixtureChatResponse(request, "assessment-missing").evidence_security_results.length, 0);
});

test("partition fixture faults survive boundary annotations and exercise complete responses", () => {
  const requestFor = (fault, separator = "\r\n\r\n ") => structuredRequest("dense_mem_semantic_partition_response", {
    request_id: "partition",
    policy_version: "fixture",
    items: [{ item_id: "item:0", boundary_text: `⟦p0⟧First meaning.${separator}⟦p1⟧Second meaning [⟦p2⟧fixture-⟦p3⟧fault:⟦p4⟧${fault}]⟦p5⟧` }],
  });
  for (const fault of ["partition-split", "partition-missing", "partition-unknown", "partition-repair"]) {
    const request = requestFor(fault);
    assert.equal(fixtureFault(request, "assessment"), fault);
    const response = fixtureChatResponse(request, fixtureFault(request, "assessment"));
    if (fault === "partition-split") assert.deepEqual(response.items[0].end_refs, ["p1", "p5"]);
    else if (fault === "partition-missing") assert.deepEqual(response.items, []);
    else assert.deepEqual(response.items[0].end_refs, ["invented"]);
  }
  const repaired = requestFor("partition-repair");
  assert.deepEqual(fixtureChatResponse(repaired, fixtureFault(repaired, "assessment"), 2).items[0].end_refs, ["p5"]);
  for (const separator of ["\u2028", "\u2029"]) {
    const request = requestFor("partition-split", separator);
    assert.deepEqual(fixtureChatResponse(request, fixtureFault(request, "assessment")).items[0].end_refs, ["p1", "p5"]);
  }
});

test("provider fixture scopes faults to the active Evidence, topic or session input", () => {
  const staleContext = structuredRequest("dense_mem_evidence_assessment_response", { evidence: [{ content: "[fixture-fault:security] stale" }] });
  staleContext.messages.push({ role: "user", content: JSON.stringify({ evidence: [{ content: "Current evidence." }], known_evidence: [{ content: "[fixture-fault:security] recalled evidence" }] }) });
  assert.equal(fixtureFault(staleContext, "assessment"), "");
  for (const [schema, input] of [
    ["dense_mem_evidence_assessment_response", { evidence: [{ content: "Current [fixture-fault:timeout]." }] }],
    ["ontology_evidence_organization_v3", { items: [{ text: "Current [fixture-fault:timeout]." }] }],
    ["session_evidence_excerpts_v2", { window: { core: [{ text: "Current [fixture-fault:timeout]." }] } }],
  ]) assert.equal(fixtureFault(structuredRequest(schema, input), "assessment"), "timeout");
  assert.equal(fixtureFault({ input: ["Current embedding input [fixture-fault:embedding-count]."] }, "embedding"), "embedding-count");
});

test("provider fixture emits exact current-event excerpts with original rune offsets", () => {
  const text = "🧭  Ari uses Go. [fixture-fault:embedding-malformed]";
  const result = fixtureChatResponse(structuredRequest("session_evidence_excerpts_v2", {
    request_id: "session", window: { core: [{ event_index: 0, ref: "segment", start: 0, end: Array.from(text).length, text }] },
  }));
  assert.deepEqual(result.excerpts, [{ event_index: 0, start: 3, end: Array.from(text).length, content: "Ari uses Go. [fixture-fault:embedding-malformed]" }]);
  const empty = fixtureChatResponse(structuredRequest("session_evidence_excerpts_v2", { request_id: "empty", window: { core: [{ event_index: 0, ref: "empty", start: 0, end: 7, text: "Thanks." }] } }));
  assert.deepEqual(empty.excerpts, []);
  const overlappingText = "Ari uses Go. [fixture:session-overlap-whitespace]".padEnd(999, "é") + "\n";
  const overlapping = fixtureChatResponse(structuredRequest("session_evidence_excerpts_v2", {
    request_id: "overlap", window: { core: [{ event_index: 0, ref: "current", start: 0, end: 1000, text: overlappingText }] },
  }));
  assert.deepEqual(overlapping.excerpts, [[0, 900], [800, 1000]].map(([start, end]) => ({
    event_index: 0, start, end, content: Array.from(overlappingText).slice(start, end).join(""),
  })));
});

function structuredRequest(schemaName, input) {
  return {
    messages: [{ role: "user", content: JSON.stringify(input) }],
    response_format: { json_schema: { name: schemaName } },
  };
}

test("synchronous-write provider remains a project-scoped Compose helper", async () => {
  const compose = await readFile(new URL("../../../scripts/e2e-stack.yml", import.meta.url), "utf8");
  const controller = await readFile(new URL("../../../scripts/e2e-host-controller.sh", import.meta.url), "utf8");
  const stack = await readFile(new URL("../../../scripts/e2e-host-controller-stack.sh", import.meta.url), "utf8");
  const processor = await readFile(new URL("../../../cmd/internal/serverapp/remember_processor_integration_test.go", import.meta.url), "utf8");
  assert.match(compose, /synchronous-write-provider-files/);
  assert.match(compose, /profiles: \[synchronous_write, verifier\]/);
  assert.match(stack, /provider-fixture\.mjs/);
  assert.match(stack, /DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS/);
  assert.match(controller, /go -C cmd\/e2e run \. --root \/workspace/);
  assert.match(stack, /--scenario synchronous_write_primitives/);
  assert.doesNotMatch(stack, /--capability repository,service,server/);
  assert.match(stack, /--scenario synchronous_write_primitives --timeout 20m --total-timeout 25m/);
  assert.match(processor, /func TestEvidenceOnlyPostgresRetiredRememberReceipts/);
});

test("legacy local Compose fragments are no longer imported", async () => {
  const entries = await readdir(new URL("../../../scripts", import.meta.url));
  assert.equal(entries.some((entry) => entry.startsWith("e2e-compose")), false);
});

test("remember case covers semantic duplicate reuse and unauthorized candidates", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  const fixture = await readFile(new URL("./provider-fixture.mjs", import.meta.url), "utf8");
  assert.match(remember, /runSemanticDuplicateCase/);
  assert.match(remember, /semantic-reuse/);
  assert.match(remember, /occurrences_preserved/);
  assert.match(fixture, /semantic-reuse-unauthorized/);
});

test("remember case covers bounded assessor context selection and replay", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  assert.match(remember, /runBudgetContextCase/);
  assert.match(remember, /optional duplicate candidates were omitted/);
  assert.match(remember, /bounded candidate selection replay must be byte-equivalent/);
  assert.match(remember, /semantic_assessments/);
});

test("remember case covers cited evidence conflict creation, recurrence, resolution, dismissal, and similarity-only rejection", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  const fixture = await readFile(new URL("./provider-fixture.mjs", import.meta.url), "utf8");
  assert.match(remember, /runEvidenceConflictCase/);
  assert.match(remember, /cited-evidence-conflict/);
  assert.match(remember, /similarity-only evidence must not create/);
  assert.match(fixture, /evidenceConflictResults/);
});

test("remember PostgreSQL fixtures use the consolidated production runner handoff", async () => {
  const remember = await readFile(new URL("./cases/remember.mjs", import.meta.url), "utf8");
  const scenario = await readFile(new URL("../../../scripts/e2e-scenario.sh", import.meta.url), "utf8");
  const controller = await readFile(new URL("../../../scripts/e2e-host-controller-runtime.sh", import.meta.url), "utf8");
  assert.match(remember, /fileURLToPath\(new URL\("\.\.\/\.\.\/\.\.\/\.\.", import\.meta\.url\)\)/);
  assert.match(remember, /DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE/);
  assert.match(remember, /SET LOCAL app\.tx_mode = 'system'/);
  assert.match(remember, /only permits read queries/);
  assert.match(remember, /psql -X -q -v ON_ERROR_STOP=1/);
  assert.match(remember, /Remember PostgreSQL fixture failed \(\$\{result\.status\}\):/);
  assert.match(remember, /requiredEnv\("DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE"\)/);
  assert.match(scenario, /export DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE=/);
  assert.match(scenario, /specs\+=\("tests-compose\/compose-evidence-conflict\.spec\.ts"\)/);
  assert.match(controller, /DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE=\/ci\/helper-compose\.yml/);
});
