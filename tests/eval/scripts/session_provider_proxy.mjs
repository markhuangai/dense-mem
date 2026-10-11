import { createServer } from "node:http";
import assert from "node:assert/strict";

const chatBase = required("AI_VERIFIER_API_URL", process.env.AI_API_URL);
const chatKey = required("AI_VERIFIER_API_KEY", process.env.AI_API_KEY);
const embeddingBase = required("AI_API_URL");
const embeddingKey = required("AI_API_KEY");
for (const base of [chatBase, embeddingBase]) {
  const url = new URL(base);
  assert(["http:", "https:"].includes(url.protocol));
  assert(!/fixture|synchronous-write-provider|conflict-provider/.test(url.hostname), "quality gate requires real providers");
}
const usage = { chat_turns: 0, embedding_turns: 0, input_tokens: 0, output_tokens: 0, missing_chat_usage: 0, embedding_input_tokens: 0, embedding_usage_unavailable: 0 };
const server = createServer(async (request, response) => {
  if (request.url === "/metrics") return send(response, 200, JSON.stringify(usage));
  const chat = request.url === "/v1/chat/completions";
  const embedding = request.url === "/v1/embeddings";
  if (!chat && !embedding) return send(response, 404, "{}");
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  const controller = new AbortController();
  response.on("close", () => { if (!response.writableEnded) controller.abort(); });
  try {
    const upstream = await fetch(`${(chat ? chatBase : embeddingBase).replace(/\/$/, "")}/${chat ? "chat/completions" : "embeddings"}`, { method: "POST", headers: { Authorization: `Bearer ${chat ? chatKey : embeddingKey}`, "Content-Type": "application/json" }, body: Buffer.concat(chunks), signal: controller.signal });
    const body = await upstream.text();
    usage[chat ? "chat_turns" : "embedding_turns"]++;
    const tokens = JSON.parse(body).usage;
    if (chat && Number.isFinite(tokens?.prompt_tokens) && Number.isFinite(tokens?.completion_tokens)) {
      usage.input_tokens += tokens.prompt_tokens;
      usage.output_tokens += tokens.completion_tokens;
    } else if (chat) usage.missing_chat_usage++;
    else if (Number.isFinite(tokens?.prompt_tokens)) usage.embedding_input_tokens += tokens.prompt_tokens;
    else usage.embedding_usage_unavailable++;
    send(response, upstream.status, body);
  } catch (error) {
    if (!controller.signal.aborted) send(response, 502, JSON.stringify({ error: { message: "evaluation provider request failed" } }));
  }
});
server.listen(Number(process.env.DENSE_MEM_EVAL_PROXY_PORT || 8788), "0.0.0.0", () => console.log("session evaluation provider proxy ready"));
function required(name, fallback) { const value = process.env[name] || fallback; assert(value, `${name} is required`); return value; }
function send(response, status, body) { response.writeHead(status, { "Content-Type": "application/json" }); response.end(body); }
