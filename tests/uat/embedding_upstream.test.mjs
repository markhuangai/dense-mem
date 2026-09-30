import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { createServer } from "node:http";
import { test } from "node:test";

import { forwardEmbedding } from "./embedding_upstream.mjs";

const settings = [
  "DENSE_MEM_E2E_EMBEDDING_BASE_URL",
  "DENSE_MEM_E2E_EMBEDDING_API_KEY",
  "DENSE_MEM_E2E_EMBEDDING_MODEL",
  "DENSE_MEM_E2E_EMBEDDING_DIMENSIONS",
  "DENSE_MEM_E2E_EMBEDDING_MAX_BATCH_ITEMS",
];

function configure(url) {
  const previous = Object.fromEntries(settings.map((name) => [name, process.env[name]]));
  Object.assign(process.env, {
    DENSE_MEM_E2E_EMBEDDING_BASE_URL: url,
    DENSE_MEM_E2E_EMBEDDING_API_KEY: "private-fixture-token",
    DENSE_MEM_E2E_EMBEDDING_MODEL: "@cf/baai/bge-m3",
    DENSE_MEM_E2E_EMBEDDING_DIMENSIONS: "1024",
    DENSE_MEM_E2E_EMBEDDING_MAX_BATCH_ITEMS: "2",
  });
  return () => {
    for (const [name, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  };
}

function request(input) {
  return { model: "@cf/baai/bge-m3", dimensions: 1024, input };
}

test("embedding fixture forwards real HTTP response and provider usage", async () => {
  let received;
  const server = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    received = { authorization: req.headers.authorization, body: JSON.parse(body) };
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify({ data: [{ embedding: [0.25] }, { embedding: [0.5] }], usage: { prompt_tokens: 7, total_tokens: 7 } }));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const restore = configure(`http://127.0.0.1:${server.address().port}/v1`);
  try {
    const forwarded = await forwardEmbedding(request(["first", "second"]), new EventEmitter());
    assert.equal(forwarded.status, 200);
    assert.deepEqual(forwarded.body.data.map((item) => item.embedding), [[0.25], [0.5]]);
    assert.deepEqual(forwarded.body.usage, { prompt_tokens: 7, total_tokens: 7 });
    assert.equal(received.authorization, "Bearer private-fixture-token");
    assert.deepEqual(received.body, { model: "@cf/baai/bge-m3", input: ["first", "second"] });
  } finally {
    restore();
    server.close();
  }
});

test("embedding fixture rejects oversized input before forwarding", async () => {
  const restore = configure("http://127.0.0.1:1/v1");
  try {
    const result = await forwardEmbedding(request(["one", "two", "three"]), new EventEmitter());
    assert.equal(result.status, 400);
  } finally {
    restore();
  }
});

test("embedding fixture does not expose upstream error bodies or credentials", async () => {
  const server = createServer((_req, res) => {
    res.writeHead(429, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { message: "private-fixture-token" } }));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const restore = configure(`http://127.0.0.1:${server.address().port}/v1`);
  try {
    const result = await forwardEmbedding(request(["one"]), new EventEmitter());
    assert.equal(result.status, 429);
    assert.doesNotMatch(JSON.stringify(result.body), /private-fixture-token/);
  } finally {
    restore();
    server.close();
  }
});
