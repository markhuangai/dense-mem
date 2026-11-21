import { createServer } from "node:http";
import { fixtureChatResponse } from "../../uat/synchronous_write/provider-fixture.mjs";

let partitionTimeoutsRemaining = 2;
let ontologyRateLimitRemaining = true;

const server = createServer(async (request, response) => {
  try {
    let text = "";
    for await (const chunk of request) { text += chunk; if (text.length > (16 << 20)) throw new Error("request limit"); }
    const input = text ? JSON.parse(text) : {};
    if (ontologyRateLimitRemaining && input.response_format?.json_schema?.name === "ontology_evidence_organization_v3") {
      ontologyRateLimitRemaining = false;
      response.writeHead(429, { "content-type": "application/json", "retry-after": "1" });
      response.end('{"error":"fixture ontology rate limit"}');
      return;
    }
    if (partitionTimeoutsRemaining && input.response_format?.json_schema?.name === "dense_mem_semantic_partition_response") {
      const partition = JSON.parse(input.messages.at(-1).content);
      const content = partition.items[0]?.boundary_text.replace(/⟦[^⟧]+⟧/g, "");
      if (content === "Atlas\nAtlas uses PostgreSQL for durable memory.") {
        partitionTimeoutsRemaining--;
        response.writeHead(524, { "content-type": "application/json" });
        response.end('{"error":"fixture gateway timeout"}');
        return;
      }
    }
    let body;
    if (request.url.endsWith("/embeddings")) {
      const rows = Array.isArray(input.input) ? input.input : [input.input];
      body = { data: rows.map((_, index) => ({ index, embedding: [1, ...Array(1535).fill(0)] })), usage: { prompt_tokens: rows.length, total_tokens: rows.length } };
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
