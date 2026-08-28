#!/usr/bin/env node
import { createHash } from "node:crypto";
import { createServer } from "node:http";

const port = Number(process.env.PORT || 8787);
const dimensions = Number(process.env.DENSE_MEM_E2E_PROVIDER_DIMENSIONS || 1536);
const fault = (process.env.DENSE_MEM_E2E_PROVIDER_FAULT || "none").trim();
const writeSlice = (process.env.DENSE_MEM_E2E_WRITE_SLICE || "").trim();
const timeoutDelayMs = Number(process.env.DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS || 30_000);
const correctionProviderFaultMarker = "e2e-correction-provider-fault";

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

function correctionAssessmentResponse(payload) {
  const messages = Array.isArray(payload.messages) ? payload.messages : [];
  let request;
  for (const message of messages) {
    if (message?.role !== "user" || typeof message.content !== "string") continue;
    try {
      const candidate = JSON.parse(message.content);
      if (Array.isArray(candidate.submitted_entities) && Array.isArray(candidate.submitted_relationships)) {
        request = candidate;
        break;
      }
    } catch {
      continue;
    }
  }
  if (!request || request.submitted_entities.length === 0 || request.submitted_relationships.length === 0) return null;

  const evidenceByID = new Map((request.evidence || []).map((item) => [item.evidence_id, item]));
  const predicates = Array.isArray(request.predicate_options) ? request.predicate_options : [];
  const entityResults = request.submitted_entities.map((entity) => {
    const grounding = Array.isArray(entity.groundings) ? entity.groundings[0] : null;
    if (!entity?.ref || !grounding?.grounding_ref) return null;
    const knownID = entity.known_entity_id || null;
    return {
      ref: entity.ref,
      grounding_ref: grounding.grounding_ref,
      action: knownID ? "reuse" : "create",
      candidate_entity_id: knownID,
    };
  });
  if (entityResults.some((item) => item === null)) return null;

  const relationshipResults = request.submitted_relationships.map((relationship) => {
    const evidence = evidenceByID.get((relationship.evidence_ids || [])[0]);
    const range = fullEvidenceRange(evidence);
    const predicate = predicates.find((item) => item.predicate_key === (relationship.known_predicate_key || relationship.predicate_hint)) || predicates[0];
    if (!relationship?.ref || !relationship?.subject_ref || !predicate || !range) return null;
    return {
      ref: relationship.ref,
      disposition: "stored",
      reason: null,
      splits: [{
        split_index: 0,
        subject_ref: relationship.subject_ref,
        predicate_range: range,
        predicate_status: "resolved",
        predicate_key: predicate.predicate_key,
        predicate_version: predicate.version,
        predicate_registration: null,
        object_ref: relationship.object_ref || null,
        object_value: relationship.object_value || null,
        value_range: null,
        polarity: relationship.polarity,
        support_ranges: [range],
        valid_from: relationship.valid_from || null,
        valid_to: relationship.valid_to || null,
      }],
    };
  });
  if (relationshipResults.some((item) => item === null)) return null;
  return {
    request_id: request.request_id,
    security_signals: [],
    entity_results: entityResults,
    relationship_results: relationshipResults,
  };
}

function fullEvidenceRange(evidence) {
  const refs = [...String(evidence?.boundary_text || "").matchAll(/⟦([^⟧]+)⟧/g)].map((match) => match[1]);
  if (!evidence?.evidence_id || refs.length < 2) return null;
  return { evidence_id: evidence.evidence_id, start_ref: refs[0], end_ref: refs.at(-1) };
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
    if (writeSlice === "correction" && inputs.some((input) => String(input).includes(correctionProviderFaultMarker))) {
      response.destroy();
      return;
    }
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
    const correctionResponse = writeSlice === "correction" ? correctionAssessmentResponse(payload) : null;
    sendJSON(response, 200, {
      choices: [{ message: { content: JSON.stringify(correctionResponse || { request_id: "fixture", entity_results: [], relationship_results: [], security_signals: [] }) } }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    });
    return;
  }
  sendJSON(response, 404, { error: { message: "fixture route not found" } });
});

server.listen(port, "0.0.0.0", () => {
  process.stdout.write(`synchronous-write-provider listening on ${port}\n`);
});
