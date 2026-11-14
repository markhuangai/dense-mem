import assert from "node:assert/strict";
import { createServer } from "node:http";
import { once } from "node:events";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import test from "node:test";
import { remember as decisionRemember } from "../decision_providers/rpc.mjs";
import { rememberReceipt, rememberWithEmbeddingRetry } from "./remember_retry.mjs";

const argumentsFor = () => ({
  target_space: "private", idempotency_key: "unchanged-key", evidence: [{ content: "Exact é Evidence.", source_type: "manual", force_insert: true }],
});
const failedReceipt = () => ({
  contract_version: "dense-mem.v2.8.0", submission_id: "attempt", correlation_id: "correlation", submission_kind: "remember",
  space_kind: "private", processing_state: "failed", search_state: "not_required",
  evidence: [{ evidence_index: 0, disposition: "not_stored", search_state: "not_required", superseded_evidence_ids: [] }],
  errors: [{ code: "request_timeout", reason_code: "embedding_timeout", message: "Required embedding timed out.",
    remediation: "Retry the unchanged request.", retryable: true, next_action: "retry_same_request",
    details: { component: "remember.embedding", server_owned: true } }],
});
const envelope = (receipt) => ({ result: { isError: receipt.processing_state === "failed", structuredContent: receipt,
  content: [{ type: "text", text: JSON.stringify(receipt) }] } });

async function fixture(t, respond) {
  const requests = [];
  const server = createServer(async (request, response) => {
    let body = "";
    for await (const chunk of request) body += chunk;
    requests.push({ body, authorization: request.headers.authorization, at: performance.now() });
    const value = respond(requests.length, response);
    if (value !== undefined) { response.writeHead(200, { "Content-Type": "application/json" }); response.end(JSON.stringify(value)); }
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(async () => { server.closeAllConnections(); await new Promise((resolve) => server.close(resolve)); });
  return { requests, base: `http://127.0.0.1:${server.address().port}`, call: async (args, signal) => {
    const response = await fetch(`http://127.0.0.1:${server.address().port}`, { method: "POST", signal,
      headers: { Authorization: "Bearer fixed-fixture-credential", "Content-Type": "application/json" }, body: JSON.stringify(args) });
    return response.json();
  } };
}

test("caller retries one complete embedding timeout with identical arguments, key, auth and target after 500 ms", async (t) => {
  const success = { ...failedReceipt(), submission_id: "recovered", processing_state: "completed", errors: [] };
  const { requests, call } = await fixture(t, (attempt) => envelope(attempt === 1 ? failedReceipt() : success));
  const args = argumentsFor();
  const original = JSON.stringify(args);
  const attempts = [];
  const result = await rememberWithEmbeddingRetry(args, async (request) => {
    const response = await call(request);
    args.evidence[0].content = "caller mutated its object";
    return response;
  }, { onAttempt: (attempt) => attempts.push(attempt) });
  assert.equal(rememberReceipt(result).submission_id, "recovered");
  assert.equal(requests.length, 2);
  assert(requests[1].at - requests[0].at >= 500);
  assert(requests.every((request) => request.body === original && request.authorization === "Bearer fixed-fixture-credential"));
  assert.equal(attempts[0].requestHash, attempts[1].requestHash);
  assert.deepEqual(attempts.map((item) => [item.attempt, item.retrying, item.exhausted]), [[1, true, false], [2, false, false]]);
  assert.deepEqual(attempts.map((item) => rememberReceipt(item.response).submission_id), ["attempt", "recovered"]);
});

test("a repeated embedding timeout fails after two retained attempts", async (t) => {
  const { requests, call } = await fixture(t, () => envelope(failedReceipt()));
  const attempts = [];
  await assert.rejects(rememberWithEmbeddingRetry(argumentsFor(), call, { onAttempt: (attempt) => attempts.push(attempt) }), /both retained E2E attempts/);
  assert.equal(requests.length, 2);
  assert.equal(attempts[1].exhausted, true);
});

test("an unexpected embedding timeout can recover to the intended provider rejection", async (t) => {
  const rejected = failedReceipt();
  rejected.errors[0] = { ...rejected.errors[0], code: "provider_unavailable", reason_code: "provider_unavailable", details: { component: "remember.assessment" } };
  const { requests, call } = await fixture(t, (attempt) => envelope(attempt === 1 ? failedReceipt() : rejected));
  const result = await rememberWithEmbeddingRetry(argumentsFor(), call, { onAttempt: () => {} });
  assert.deepEqual(rememberReceipt(result), rejected);
  assert.equal(requests.length, 2);
});

test("deliberate failures, incomplete or ambiguous receipts and unrelated errors remain single-attempt", async (t) => {
  const variants = [
    (value) => { value.result.structuredContent.errors[0].retryable = false; },
    (value) => { value.result.structuredContent.errors[0].details.component = "remember.assessment"; },
    (value) => { value.result.structuredContent.errors[0].code = "embedding_response_invalid"; },
    (value) => { delete value.result.structuredContent.correlation_id; },
    (value) => { value.result.structuredContent.evidence = []; },
    (value) => { value.result.structuredContent.evidence[0].evidence_id = "partially-stored"; },
    (value) => { delete value.result.structuredContent.evidence[0].superseded_evidence_ids; },
    (value) => { value.result.structuredContent.evidence[0].superseded_evidence_ids = null; },
    (value) => { value.result.structuredContent.evidence[0].superseded_evidence_ids = {}; },
    (value) => { value.result.structuredContent.evidence.push(value.result.structuredContent.evidence[0]); },
    (value) => { value.error = { code: -32603, message: "ambiguous" }; },
    (value) => { value.result.isError = false; },
    (value) => { delete value.result.content; },
  ];
  for (const mutate of variants) {
    const value = envelope(failedReceipt());
    mutate(value);
    if (value.result.content) value.result.content[0].text = JSON.stringify(value.result.structuredContent);
    const { requests, call } = await fixture(t, () => value);
    await rememberWithEmbeddingRetry(argumentsFor(), call, { onAttempt: () => {} });
    assert.equal(requests.length, 1);
  }
  const contradictory = envelope(failedReceipt());
  contradictory.result.content[0].text = JSON.stringify({ ...failedReceipt(), processing_state: "completed" });
  const ambiguous = await fixture(t, () => contradictory);
  await rememberWithEmbeddingRetry(argumentsFor(), ambiguous.call, { onAttempt: () => {} });
  assert.equal(ambiguous.requests.length, 1);
  const { requests, call } = await fixture(t, () => envelope(failedReceipt()));
  const result = await rememberWithEmbeddingRetry(argumentsFor(), call, { retry: false, onAttempt: () => {} });
  assert.equal(rememberReceipt(result).processing_state, "failed");
  assert.equal(requests.length, 1);
});

test("progress caller has one owner for retry and retained attempt captures", async (t) => {
  const success = { ...failedReceipt(), submission_id: "recovered", processing_state: "completed", errors: [] };
  const { requests, base } = await fixture(t, (attempt) => ({
    jsonrpc: "2.0", id: JSON.parse(requests.at(-1).body).id,
    ...envelope(attempt === 1 ? failedReceipt() : success),
  }));
  const child = spawn(process.execPath, ["--input-type=module", "-e", `
    import { progressCall } from ${JSON.stringify(new URL("./cases/progress.mjs", import.meta.url).href)};
    import { captureRememberAttempt } from ${JSON.stringify(new URL("./remember_retry.mjs", import.meta.url).href)};
    const attempts = [];
    await progressCall({ apiKey: "fixed-fixture-credential", args: ${JSON.stringify(argumentsFor())},
      token: '"recovery"', onAttempt: (attempt) => { captureRememberAttempt(attempt); attempts.push(attempt.attempt); } });
    process.stdout.write(JSON.stringify(attempts));
  `], { env: { ...process.env, DENSE_MEM_USER_URL: base } });
  const closed = once(child, "close");
  let stdout = "", stderr = "";
  child.stdout.on("data", (chunk) => { stdout += chunk; });
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  const [code] = await closed;
  assert.equal(code, 0, stderr);
  assert.deepEqual(JSON.parse(stdout), [1, 2]);
  assert.equal(requests.length, 2);
  const records = stderr.trim().split("\n").map(JSON.parse);
  const captures = records.filter((record) => record.payload_chunk.index === 0);
  assert.deepEqual(captures.map((item) => [item.attempt, item.retrying, item.exhausted]), [[1, true, false], [2, false, false]]);
  assert.equal(captures[0].logical_request_hash, captures[1].logical_request_hash);
  for (const capture of captures) {
    assert.equal(capture.event, "e2e_remember_attempt");
    const chunks = records.filter((record) => record.attempt === capture.attempt);
    assert.deepEqual(chunks.map((record) => record.payload_chunk.index), Array.from({ length: capture.payload_chunk.count }, (_, index) => index));
    const body = chunks.map((record) => record.payload_chunk.body).join("");
    assert.equal(capture.payload_chunk.sha256, createHash("sha256").update(body).digest("hex"));
    const payload = JSON.parse(body);
    assert.deepEqual(payload.progress, []);
    assert.equal(typeof payload.raw, "string");
  }
});

test("caller cancellation and thrown transport or assertion errors never resubmit", async (t) => {
  for (const phase of ["before", "receipt", "backoff", "transport", "assertion"]) {
    const controller = new AbortController();
    const { requests, call } = await fixture(t, () => envelope(failedReceipt()));
    const attempts = [];
    if (phase === "before") controller.abort();
    const failure = new Error(phase);
    await assert.rejects(rememberWithEmbeddingRetry(argumentsFor(), async (args) => {
      const result = await call(args, controller.signal);
      if (["transport", "assertion"].includes(phase)) throw failure;
      if (phase === "receipt") controller.abort();
      return result;
    }, { signal: controller.signal, onAttempt: (attempt) => {
      attempts.push(attempt);
      if (phase === "backoff") setTimeout(() => controller.abort(), 25);
    } }), (error) => ["transport", "assertion"].includes(phase) ? error === failure : error.name === "AbortError");
    assert.equal(requests.length, phase === "before" ? 0 : 1);
    if (["transport", "assertion"].includes(phase)) assert.equal(attempts[0].response.capture_state, "interrupted");
    if (phase === "receipt") assert.equal(attempts[0].retrying, false);
  }
});


test("decision-provider caller classifies the complete envelope before unwrapping", async (t) => {
  for (const ambiguity of ["isError", "text"]) {
    const value = envelope(failedReceipt());
    if (ambiguity === "isError") value.result.isError = false;
    else value.result.content[0].text = JSON.stringify({ ...failedReceipt(), processing_state: "completed" });
    const { requests, base } = await fixture(t, () => value);
    assert.equal((await decisionRemember(base, "fixed-fixture-credential", argumentsFor())).processing_state, "failed");
    assert.equal(requests.length, 1, "the actual decision caller must not retry an ambiguous envelope");
  }
  const rejected = failedReceipt();
  rejected.errors[0].code = "provider_unavailable";
  rejected.errors[0].details.component = "remember.assessment";
  const { requests, base } = await fixture(t, (attempt) => envelope(attempt === 1 ? failedReceipt() : rejected));
  assert.deepEqual(await decisionRemember(base, "fixed-fixture-credential", argumentsFor()), rejected);
  assert.equal(requests.length, 2);
  assert.deepEqual(requests.map((request) => JSON.parse(request.body).params.arguments), [argumentsFor(), argumentsFor()]);
});
