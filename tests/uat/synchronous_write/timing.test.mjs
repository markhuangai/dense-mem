import assert from "node:assert/strict";
import { createServer } from "node:http";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";

import { server } from "./provider-fixture.mjs";

async function fixture(t) {
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => {
    server.close(resolve);
    server.closeAllConnections();
  }));
  const url = `http://127.0.0.1:${server.address().port}`;
  const post = (path, payload, signal) => fetch(url + path, {
    method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(payload), signal,
  });
  const health = async () => (await fetch(url + "/health")).json();
  return { post, health };
}

function request(id) {
  return {
    response_format: { json_schema: { name: "dense_mem_semantic_assessment_response" } },
    messages: [{ role: "user", content: JSON.stringify({ request_id: id,
      evidence: [{ evidence_id: "evidence", content: "Atlas uses PostgreSQL. [fixture-fault:admission-hold]" }],
      submitted_entities: [], submitted_relationships: [],
    }) }],
  };
}

async function until(read, predicate) {
  const deadline = performance.now() + 2000;
  while (performance.now() < deadline) {
    const value = await read();
    if (predicate(value)) return value;
    await delay(10);
  }
  assert.fail("fixture did not reach its expected state");
}

test("admission timing distinguishes held arrivals and delayed control release over real HTTP", async (t) => {
  const { post, health } = await fixture(t);
  assert.equal((await post("/admission-fixture", { mode: "hold" })).status, 200);
  const requests = Array.from({ length: 4 }, (_, index) => post("/v1/chat/completions", request(`admitted-${index}`)));
  await until(health, (state) => state.held_admission_requests === 4);
  assert.equal((await post("/admission-fixture", { mode: "normal", delay_ms: 40 })).status, 200);
  const responses = await Promise.all(requests);
  assert.ok(responses.every((response) => response.status === 200));
  const state = await health();
  const events = state.admission_timing.events;
  assert.equal(state.held_admission_requests, 0);
  assert.equal(state.admission_timing.dropped, 0);
  assert.equal(events.filter((entry) => entry.event === "held").length, 4);
  assert.equal(events.filter((entry) => entry.event === "released" && entry.reason === "control").length, 4);
  const received = events.find((entry) => entry.event === "control_received" && entry.mode === "normal");
  const applied = events.find((entry) => entry.event === "control_applied" && entry.mode === "normal");
  assert.ok(applied.at_ms - received.at_ms >= 35);
  assert.equal(received.delay_ms, 40);
  assert.ok(events.every((entry) => Number.isFinite(entry.instrumentation_ms) && entry.instrumentation_ms >= 0));
  assert.equal(state.admission_timing.instrumentation_ms, events.reduce((total, entry) => total + entry.instrumentation_ms, 0));
  assert.ok(state.admission_timing.instrumentation_ms < applied.at_ms - received.at_ms,
    "provider instrumentation excludes the intentional control delay");
  assert.ok(events.every((entry, index) => index === 0 || entry.at_ms >= events[index - 1].at_ms));
  assert.deepEqual(events.filter((entry) => entry.event === "held").map((entry) => entry.request_id).sort(),
    ["admitted-0", "admitted-1", "admitted-2", "admitted-3"]);
});

test("admission timing records cancellation and rejects invalid diagnostic delays", async (t) => {
  const { post, health } = await fixture(t);
  assert.equal((await post("/admission-fixture", { mode: "hold", delay_ms: -1 })).status, 400);
  assert.equal((await post("/admission-fixture", { mode: "hold", delay_ms: 5001 })).status, 400);
  await post("/admission-fixture", { mode: "hold" });
  const controller = new AbortController();
  const pending = post("/v1/chat/completions", request("cancelled"), controller.signal).catch((error) => error);
  await until(health, (state) => state.held_admission_requests === 1);
  controller.abort();
  assert.equal((await pending).name, "AbortError");
  const state = await until(health, (value) => value.held_admission_requests === 0);
  assert.ok(state.admission_timing.events.some((entry) => entry.event === "released" && entry.reason === "client_closed"));
  await post("/admission-fixture", { mode: "normal" });
});

test("embedding timing retains real upstream failure status without upstream content", async (t) => {
  const upstream = createServer((_request, response) => {
    response.writeHead(429, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: { message: "synthetic-upstream-secret" } }));
  });
  await new Promise((resolve) => upstream.listen(0, "127.0.0.1", resolve));
  t.after(() => { upstream.close(); upstream.closeAllConnections(); });
  const settings = {
    DENSE_MEM_E2E_EMBEDDING_BASE_URL: `http://127.0.0.1:${upstream.address().port}/v1`,
    DENSE_MEM_E2E_EMBEDDING_API_KEY: "synthetic-upstream-secret",
    DENSE_MEM_E2E_EMBEDDING_MODEL: "fixture-model", DENSE_MEM_E2E_EMBEDDING_DIMENSIONS: "2",
    DENSE_MEM_E2E_EMBEDDING_MAX_BATCH_ITEMS: "2",
  };
  const previous = Object.fromEntries(Object.keys(settings).map((key) => [key, process.env[key]]));
  Object.assign(process.env, settings);
  t.after(() => {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
  const { post, health } = await fixture(t);
  const response = await post("/v1/embeddings", { model: "fixture-model", dimensions: 2, input: ["Atlas uses PostgreSQL."] });
  assert.equal(response.status, 429);
  const state = await health();
  const event = state.embedding_timing.events.at(-1);
  assert.equal(event.status, 429);
  assert.equal(event.item_count, 1);
  assert.ok(event.elapsed_ms >= 0);
  assert.doesNotMatch(JSON.stringify(event), /synthetic-upstream-secret|Atlas uses/);
});
