import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:http";
import test from "node:test";

test("session proxy preserves non-JSON errors and accounts for missing and successful usage", async () => {
  const state = { status: 429, body: "provider-response-canary", contentType: "text/plain" };
  const provider = createServer((_request, response) => {
    response.writeHead(state.status, { "Content-Type": state.contentType });
    response.end(state.body);
  });
  const reservation = createServer();
  provider.listen(0, "127.0.0.1");
  reservation.listen(0, "127.0.0.1");
  await Promise.all([once(provider, "listening"), once(reservation, "listening")]);
  const port = reservation.address().port;
  await new Promise((resolve) => reservation.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  const upstream = `http://127.0.0.1:${provider.address().port}/v1`;
  const child = spawn(process.execPath, [new URL("session_provider_proxy.mjs", import.meta.url).pathname], {
    env: { ...process.env, AI_API_URL: upstream, AI_VERIFIER_API_URL: upstream, AI_API_KEY: "credential-canary", AI_VERIFIER_API_KEY: "credential-canary", DENSE_MEM_EVAL_PROXY_PORT: String(port) },
  });
  let output = "";
  const ready = once(child.stdout, "data");
  child.stdout.on("data", (chunk) => { output += chunk; });
  child.stderr.on("data", (chunk) => { output += chunk; });
  try {
    await Promise.race([ready, once(child, "exit").then(() => { throw new Error("proxy exited before readiness"); })]);
    const call = (path) => fetch(base + path, { method: "POST", body: JSON.stringify({ prompt: "prompt-canary" }), headers: { "Content-Type": "application/json" } });
    for (const path of ["/v1/chat/completions", "/v1/embeddings"]) {
      const response = await call(path);
      assert.equal(response.status, 429);
      assert.equal(response.headers.get("content-type"), "text/plain");
      assert.equal(await response.text(), state.body);
    }
    state.status = 200;
    state.contentType = "application/json";
    state.body = JSON.stringify({ usage: { prompt_tokens: 7, completion_tokens: 2 } });
    assert.equal((await call("/v1/chat/completions")).status, 200);
    state.body = JSON.stringify({ usage: { prompt_tokens: 5 } });
    assert.equal((await call("/v1/embeddings")).status, 200);
    const metrics = await (await fetch(base + "/metrics")).json();
    assert.deepEqual(metrics, { chat_turns: 2, embedding_turns: 2, input_tokens: 7, output_tokens: 2, missing_chat_usage: 1, embedding_input_tokens: 5, embedding_usage_unavailable: 1 });
    for (const secret of ["provider-response-canary", "prompt-canary", "credential-canary"]) assert(!output.includes(secret));
  } finally {
    if (child.exitCode === null) {
      const exited = once(child, "exit");
      child.kill();
      await exited;
    }
    provider.closeAllConnections();
    await new Promise((resolve) => provider.close(resolve));
  }
});
