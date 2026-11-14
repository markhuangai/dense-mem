import { createServer } from "node:http";
import { fixtureChatResponse } from "../../uat/synchronous_write/provider-fixture.mjs";

const server = createServer(async (request, response) => {
  try {
    let text = "";
    for await (const chunk of request) { text += chunk; if (text.length > (16 << 20)) throw new Error("request limit"); }
    const input = text ? JSON.parse(text) : {};
    let body;
    if (request.url.endsWith("/embeddings")) {
      const rows = Array.isArray(input.input) ? input.input : [input.input];
      body = { data: rows.map((_, index) => ({ index, embedding: [1, ...Array(1023).fill(0)] })), usage: { prompt_tokens: rows.length, total_tokens: rows.length } };
    } else if (input.response_format?.json_schema?.name === "public_rag_answer_v1") {
      const context = [...input.messages].reverse().map((message) => { try { return JSON.parse(message.content); } catch { return null; } }).find((value) => value?.memory_calls);
      const hits = context.memory_calls[0].result.results;
      const hit = hits.find((item) => context.question.includes("coordination") ? item.context.includes("Redis") : item.context.includes("PostgreSQL")) || hits[0];
      const action = { action: "answer", query: "", topic_id: "", evidence_id: "", answer: context.question.includes("coordination") ? "Redis" : "PostgreSQL", citations: hit ? [{ evidence_id: hit.evidence_id, quote: hit.context }] : [] };
      if (context.policy === "multi_turn" && context.memory_calls.length === 1 && hit) { action.action = "trace"; action.evidence_id = hit.evidence_id; action.answer = ""; action.citations = []; }
      body = { choices: [{ message: { content: JSON.stringify(action) }, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } };
    } else {
      const result = fixtureChatResponse(input);
      const properties = input.response_format?.json_schema?.schema?.properties;
      if (properties && !Object.hasOwn(properties, "evidence_security_results")) delete result.evidence_security_results;
      if (properties && !Object.hasOwn(properties, "evidence_equivalence_results")) delete result.evidence_equivalence_results;
      body = { choices: [{ message: { content: JSON.stringify(result) }, finish_reason: "stop" }], usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 } };
    }
    response.writeHead(200, { "content-type": "application/json" }); response.end(JSON.stringify(body));
  } catch {
    response.writeHead(400, { "content-type": "application/json" }); response.end('{"error":"fixture request failed"}');
  }
});
server.listen(8787, "0.0.0.0");
