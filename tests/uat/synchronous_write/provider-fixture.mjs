#!/usr/bin/env node
import { createHash } from "node:crypto";
import { createServer } from "node:http";

const port = Number(process.env.PORT || 8787);
const dimensions = Number(process.env.DENSE_MEM_E2E_PROVIDER_DIMENSIONS || 1536);
const fault = (process.env.DENSE_MEM_E2E_PROVIDER_FAULT || "none").trim();
const timeoutDelayMs = Number(process.env.DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS || 30_000);

function vectorFor(text, width) {
  const output = [];
  let seed = createHash("sha256").update(String(text)).digest();
  for (let index = 0; index < width; index += 1) {
    if (index > 0 && index % seed.length === 0) {
      seed = createHash("sha256").update(seed).digest();
    }
    output.push((seed[index % seed.length] / 255) * 2 - 1);
  }
  return output;
}

function sendJSON(response, status, payload) {
  const body = JSON.stringify(payload);
  response.writeHead(status, { "content-type": "application/json", "content-length": Buffer.byteLength(body) });
  response.end(body);
}

const server = createServer(async (request, response) => {
  if (request.method === "GET" && request.url === "/health") {
    sendJSON(response, 200, { status: "ok", fault });
    return;
  }
  if (fault === "timeout") {
    await new Promise((resolve) => setTimeout(resolve, timeoutDelayMs));
  }
  if (fault === "unavailable") {
    response.destroy();
    return;
  }

  let body = "";
  for await (const chunk of request) body += chunk;
  let payload = {};
  try {
    payload = body ? JSON.parse(body) : {};
  } catch {
    sendJSON(response, 400, { error: { message: "invalid fixture request" } });
    return;
  }

  if (request.url?.endsWith("/embeddings")) {
    const inputs = Array.isArray(payload.input) ? payload.input : [payload.input || ""];
    if (fault === "malformed") {
      sendJSON(response, 200, { model: "fixture-wrong-model", data: [{ index: 0, embedding: [0] }] });
      return;
    }
    sendJSON(response, 200, {
      model: payload.model || "dense-mem-e2e-embedding",
      data: inputs.map((input, index) => ({ index, embedding: vectorFor(input, dimensions) })),
      usage: { prompt_tokens: inputs.length, total_tokens: inputs.length },
    });
    return;
  }

  if (request.url?.endsWith("/chat/completions")) {
    if (fault === "malformed") {
      sendJSON(response, 200, { choices: [{ message: { content: "not-json" } }] });
      return;
    }
    // The legacy scenario only needs a deterministic provider response. The
    // active verifier will apply its own closed-schema validation.
    sendJSON(response, 200, {
      choices: [{ message: { content: JSON.stringify({ request_id: "fixture", entity_results: [], relationship_results: [], security_signals: [] }) } }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    });
    return;
  }
  sendJSON(response, 404, { error: { message: "fixture route not found" } });
});

server.listen(port, "0.0.0.0", () => {
  process.stdout.write(`synchronous-write-provider listening on ${port}\n`);
});
