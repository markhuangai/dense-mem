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
  return { model: "fixture-model", dimensions: 2, input: [`${id} [fixture-fault:admission-hold]`] };
}

async function embeddingUpstream(t, { delayMS = 0, status = 200 } = {}) {
  const requests = [];
  const upstream = createServer(async (incoming, response) => {
    let body = "";
    for await (const chunk of incoming) body += chunk;
    const payload = JSON.parse(body);
    requests.push(payload);
    if (delayMS) await delay(delayMS);
    response.writeHead(status, { "content-type": "application/json" });
    response.end(JSON.stringify(status === 200
      ? { model: payload.model, data: payload.input.map((_, index) => ({ index, embedding: [1, 0] })) }
      : { error: { message: "synthetic-upstream-secret" } }));
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
  return requests;
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

test("admission holds release a two-slot embedding client and scope timing to its current window", async (t) => {
  const upstreamRequests = await embeddingUpstream(t, { delayMS: 2100 });
  const { post, health } = await fixture(t);
  const previous = await post("/v1/embeddings", { model: "fixture-model", dimensions: 2, input: ["earlier case"] });
  assert.equal(previous.status, 200);
  await previous.json();
  assert.ok((await health()).embedding_timing.events.length > 0);
  assert.equal((await post("/admission-fixture", { mode: "hold" })).status, 200);
  assert.deepEqual((await health()).embedding_timing, { events: [], dropped: 0, instrumentation_ms: 0 });
  let scheduled = 0;
  const clients = Array.from({ length: 2 }, async () => {
    const responses = [];
    while (scheduled < 4) {
      const index = scheduled++;
      responses.push(await post("/v1/embeddings", request(`admitted-${index}`)));
    }
    return responses;
  });
  await until(health, (state) => state.held_admission_requests === 2);
  assert.equal(upstreamRequests.length, 1, "current holders must wait before upstream embedding starts");
  assert.equal((await post("/admission-fixture", { mode: "normal", delay_ms: 40 })).status, 200);
  const responses = (await Promise.all(clients)).flat();
  assert.equal(responses.length, 4);
  assert.ok(responses.every((response) => response.status === 200));
  assert.equal(upstreamRequests.length, 5, "all released and queued requests must use real upstream embedding");
  for (const response of responses) assert.deepEqual((await response.json()).data[0].embedding, [1, 0]);
  const state = await health();
  const events = state.admission_timing.events;
  assert.equal(state.embedding_timing.events.length, 4);
  assert.ok(state.embedding_timing.events.every((entry) => entry.fault === "admission-hold"));
  assert.equal(state.held_admission_requests, 0);
  assert.equal(state.admission_timing.dropped, 0);
  assert.equal(events.filter((entry) => entry.event === "held").length, 2);
  assert.equal(events.filter((entry) => entry.event === "released" && entry.reason === "control").length, 2);
  const received = events.find((entry) => entry.event === "control_received" && entry.mode === "normal");
  const applied = events.find((entry) => entry.event === "control_applied" && entry.mode === "normal");
  assert.ok(applied.at_ms - received.at_ms >= 35);
  assert.equal(received.delay_ms, 40);
  assert.ok(events.every((entry) => Number.isFinite(entry.instrumentation_ms) && entry.instrumentation_ms >= 0));
  assert.equal(state.admission_timing.instrumentation_ms, events.reduce((total, entry) => total + entry.instrumentation_ms, 0));
  assert.ok(state.admission_timing.instrumentation_ms < applied.at_ms - received.at_ms,
    "provider instrumentation excludes the intentional control delay");
  assert.ok(events.every((entry, index) => index === 0 || entry.at_ms >= events[index - 1].at_ms));
  const heldCalls = events.filter((entry) => entry.event === "held").map((entry) => entry.call).sort();
  assert.equal(new Set(heldCalls).size, 2);
  assert.deepEqual(events.filter((entry) => entry.event === "released").map((entry) => entry.call).sort(), heldCalls);
});

test("admission timing records cancellation and rejects invalid diagnostic delays", async (t) => {
  const upstreamRequests = await embeddingUpstream(t);
  const { post, health } = await fixture(t);
  assert.equal((await post("/admission-fixture", { mode: "hold", delay_ms: -1 })).status, 400);
  assert.equal((await post("/admission-fixture", { mode: "hold", delay_ms: 5001 })).status, 400);
  await post("/admission-fixture", { mode: "hold" });
  const controller = new AbortController();
  const pending = post("/v1/embeddings", request("cancelled"), controller.signal).catch((error) => error);
  await until(health, (state) => state.held_admission_requests === 1);
  controller.abort();
  assert.equal((await pending).name, "AbortError");
  const state = await until(health, (value) => value.held_admission_requests === 0);
  assert.ok(state.admission_timing.events.some((entry) => entry.event === "released" && entry.reason === "client_closed"));
  await post("/admission-fixture", { mode: "normal" });
  assert.equal(upstreamRequests.length, 0, "cancelled holders must not start upstream embedding");
  assert.equal((await post("/v1/embeddings", request("recovered"))).status, 200);
  assert.equal(upstreamRequests.length, 1);
});

test("embedding timing retains real upstream failure status without upstream content", async (t) => {
  await embeddingUpstream(t, { status: 429 });
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
