import http from "node:http";

const port = Number.parseInt(process.env.E2E_EMBEDDING_STUB_PORT ?? "8081", 10);

const server = http.createServer(async (request, response) => {
  if (request.method === "GET" && request.url === "/health") {
    response.writeHead(200, { "content-type": "text/plain" });
    response.end("ok");
    return;
  }

  if (request.method !== "POST" || !["/embeddings", "/v1/embeddings"].includes(request.url ?? "")) {
    response.writeHead(404, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: { message: "not found" } }));
    return;
  }

  try {
    const body = JSON.parse(await readBody(request));
    const inputs = Array.isArray(body.input) ? body.input : [body.input];
    const dimensions = Number.isInteger(body.dimensions) && body.dimensions > 0 ? body.dimensions : 1536;
    const model = typeof body.model === "string" && body.model.length > 0 ? body.model : "e2e-embedding-stub";
    const promptTokens = inputs.reduce((total, input) => total + Math.floor(String(input).length / 4) + 1, 0);

    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({
      object: "list",
      model,
      data: inputs.map((input, index) => ({
        object: "embedding",
        index,
        embedding: deterministicVector(String(input), dimensions),
      })),
      usage: {
        prompt_tokens: promptTokens,
        total_tokens: promptTokens,
      },
    }));
  } catch (error) {
    response.writeHead(400, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: { message: error instanceof Error ? error.message : "bad request" } }));
  }
});

server.listen(port, "0.0.0.0", () => {
  console.log(`e2e embedding stub listening on ${port}`);
});

function readBody(request) {
  return new Promise((resolve, reject) => {
    let body = "";
    request.setEncoding("utf8");
    request.on("data", (chunk) => {
      body += chunk;
    });
    request.on("end", () => resolve(body));
    request.on("error", reject);
  });
}

function deterministicVector(input, dimensions) {
  const seed = hash(input);
  return Array.from({ length: dimensions }, (_, index) => {
    const value = (seed + index * 2654435761) % 1000;
    return value / 500 - 1;
  });
}

function hash(input) {
  let value = 2166136261;
  for (let index = 0; index < input.length; index += 1) {
    value ^= input.charCodeAt(index);
    value = Math.imul(value, 16777619) >>> 0;
  }
  return value;
}
