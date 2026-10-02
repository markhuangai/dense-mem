import assert from "node:assert/strict";
import { createServer } from "node:http";
import { test } from "node:test";
import { GitHubApi } from "../../.github/scripts/pr-image-cleanup.cjs";

async function fixture(handler, run, options = {}) {
  const server = createServer(handler);
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const api = new GitHubApi({ apiUrl: `http://127.0.0.1:${server.address().port}`, token: "fixture-secret", repository: "fixture/repository", ...options });
  try {
    await run(api);
  } finally {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
}

function json(response, status, body, headers = {}) {
  response.writeHead(status, { "Content-Type": "application/json", ...headers });
  response.end(JSON.stringify(body));
}

test("cleanup API recovers from a real dropped connection", async () => {
  let requests = 0;
  await fixture((request, response) => {
    requests += 1;
    if (requests === 1) return request.socket.destroy();
    json(response, 200, [{ id: 42 }]);
  }, async (api) => {
    assert.deepEqual(await api.versions("repository"), [{ id: 42 }]);
  });
  assert.equal(requests, 2);
});

test("cleanup API reports bounded connection failure without credentials", async () => {
  let requests = 0;
  await fixture((request) => { requests += 1; request.socket.destroy(); }, async (api) => {
    await assert.rejects(api.versions("repository"), (error) => {
      assert.equal(error.attempts, 3);
      assert.equal(error.transport_code, "UND_ERR_SOCKET");
      assert.match(error.message, /GET \/orgs\/fixture\/packages\/container\/repository\/versions/);
      assert.doesNotMatch(error.message, /fixture-secret/);
      return true;
    });
  });
  assert.equal(requests, 3);
});

test("cleanup API recovers transient HTTP errors and respects long Retry-After", async () => {
  for (const status of [429, 500, 502, 503, 504]) {
    let requests = 0;
    await fixture((_request, response) => {
      requests += 1;
      json(response, requests === 1 ? status : 200, [], { "Retry-After": "0" });
    }, async (api) => assert.deepEqual(await api.request("/versions"), []));
    assert.equal(requests, 2);
  }
  let requests = 0;
  await fixture((_request, response) => {
    requests += 1;
    json(response, 429, { message: "fixture-secret" }, { "Retry-After": "31" });
  }, async (api) => {
    await assert.rejects(api.request("/versions"), (error) => {
      assert.equal(error.status, 429);
      assert.match(error.message, /Retry-After exceeds/);
      assert.doesNotMatch(error.message, /fixture-secret/);
      return true;
    });
  });
  assert.equal(requests, 1);
});

test("cleanup API fails authorization immediately and preserves HTTP status", async () => {
  for (const status of [401, 403, 404]) {
    let requests = 0;
    await fixture((_request, response) => { requests += 1; json(response, status, { message: "fixture-secret" }); }, async (api) => {
      await assert.rejects(api.request("/versions"), (error) => error.status === status && error.attempts === 1 && !error.message.includes("fixture-secret"));
    });
    assert.equal(requests, 1);
  }
});

test("cleanup API bounds stalled requests and does not retry caller cancellation", async () => {
  let requests = 0;
  await fixture(() => { requests += 1; }, async (api) => {
    await assert.rejects(api.request("/stalled"), (error) => error.transport_code === "TIMEOUT" && error.attempts === 3);
  }, { requestTimeoutMilliseconds: 30 });
  assert.equal(requests, 3);
  requests = 0;
  await fixture(() => { requests += 1; }, async (api) => {
    const controller = new AbortController();
    setTimeout(() => controller.abort(), 30);
    await assert.rejects(api.request("/cancelled", { signal: controller.signal }), (error) => error.transport_code === "ABORTED" && error.attempts === 1);
  });
  assert.equal(requests, 1);
});

test("cleanup API paginates and retries only the same deleted version", async () => {
  const paths = [];
  let deleted = false;
  await fixture((request, response) => {
    paths.push(`${request.method} ${request.url}`);
    if (request.method === "DELETE") {
      if (!deleted) { deleted = true; return request.socket.destroy(); }
      return json(response, 404, { message: "already deleted" });
    }
    const page = new URL(request.url, "http://fixture").searchParams.get("page");
    json(response, 200, page === "1" ? Array.from({ length: 100 }, (_, id) => ({ id })) : [{ id: 100 }]);
  }, async (api) => {
    assert.equal((await api.versions("repository")).length, 101);
    await assert.rejects(api.deleteVersion("repository", 42), (error) => error.status === 404);
  });
  assert.equal(deleted, true);
  assert.deepEqual(paths.filter((path) => path.startsWith("DELETE")), [
    "DELETE /orgs/fixture/packages/container/repository/versions/42",
    "DELETE /orgs/fixture/packages/container/repository/versions/42",
  ]);
});

test("cleanup API preserves diagnostics when cancelled during retry backoff", async () => {
  let requests = 0;
  const controller = new AbortController();
  await fixture((_request, response) => {
    requests += 1;
    json(response, 503, {});
    setTimeout(() => controller.abort(), 200);
  }, async (api) => {
    await assert.rejects(api.request("/backoff", { signal: controller.signal }), (error) => {
      assert.equal(error.attempts, 1);
      assert.equal(error.transport_code, "ABORTED");
      assert.match(error.message, /GET \/backoff.*retry delay after 1 attempt\(s\).*transport=ABORTED/);
      assert.doesNotMatch(error.message, /fixture-secret/);
      return true;
    });
  });
  assert.equal(requests, 1);
});
