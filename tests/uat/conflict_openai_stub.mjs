#!/usr/bin/env node

import http from "node:http";

const port = positiveInteger(process.env.DENSE_MEM_CONFLICT_STUB_PORT ?? "8081", "DENSE_MEM_CONFLICT_STUB_PORT");
const host = process.env.DENSE_MEM_CONFLICT_STUB_HOST ?? "0.0.0.0";

const server = http.createServer(async (request, response) => {
  try {
    if (request.method === "GET" && request.url === "/health") {
      return sendJSON(response, 200, { status: "ok" });
    }
    if (request.method !== "POST") {
      return sendJSON(response, 405, { error: { message: "method not allowed" } });
    }
    const payload = await readJSON(request);
    if (request.url === "/v1/embeddings") {
      return sendJSON(response, 200, embeddingResponse(payload));
    }
    if (request.url !== "/v1/chat/completions") {
      return sendJSON(response, 404, { error: { message: "not found" } });
    }

    const schemaName = payload?.response_format?.json_schema?.name;
    const providerInput = initialProviderInput(payload?.messages);
    if (schemaName === "dense_mem_semantic_assessment_response") {
      return sendChat(response, semanticAssessmentResponse(providerInput));
    }
    if (schemaName === "dense_mem_conflict_assessment_response") {
      const contents = (providerInput.evidence ?? []).map((item) => String(item?.content ?? ""));
      if (contents.some((content) => content.includes("[conflict-ai-fail]"))) {
        return sendJSON(response, 503, { error: { message: "deterministic conflict provider failure" } });
      }
      return sendChat(response, conflictAssessmentResponse(providerInput));
    }
    return sendJSON(response, 400, { error: { message: "unsupported response schema" } });
  } catch (error) {
    const message = error instanceof Error ? error.message : "invalid request";
    return sendJSON(response, 400, { error: { message } });
  }
});

server.listen(port, host);

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}

function embeddingResponse(payload) {
  const input = Array.isArray(payload?.input) ? payload.input : [];
  const dimensions = positiveInteger(payload?.dimensions, "embedding dimensions");
  return {
    object: "list",
    model: String(payload?.model ?? "dense-mem-conflict-e2e-embedding"),
    data: input.map((text, index) => ({
      object: "embedding",
      index,
      embedding: deterministicVector(String(text), dimensions),
    })),
    usage: { prompt_tokens: Math.max(1, input.length), total_tokens: Math.max(1, input.length) },
  };
}

function deterministicVector(text, dimensions) {
  let seed = 2166136261;
  for (const rune of text) {
    seed ^= rune.codePointAt(0);
    seed = Math.imul(seed, 16777619) >>> 0;
  }
  return Array.from({ length: dimensions }, (_, index) => (((seed + index * 2654435761) >>> 0) % 2001 - 1000) / 1000);
}

function semanticAssessmentResponse(input) {
  const candidateGroups = Array.isArray(input.entity_candidate_groups) ? input.entity_candidate_groups : [];
  const predicateOptions = Array.isArray(input.predicate_options) ? input.predicate_options : [];
  const submittedEntities = Array.isArray(input.submitted_entities) ? input.submitted_entities : [];
  const submittedRelationships = Array.isArray(input.submitted_relationships) ? input.submitted_relationships : [];

  const entityResults = submittedEntities.map((entity) => {
    const group = candidateGroups.find((candidate) => (
      candidate?.evidence_id === entity.evidence_id
      && candidate?.start === entity.start
      && candidate?.end === entity.end
    ));
    const compatible = (group?.candidates ?? []).filter((candidate) => candidate?.kind === entity.kind);
    const reusable = compatible.length === 1 && !group?.candidate_context_truncated;
    return {
      ref: entity.ref,
      surface: entity.surface,
      kind: entity.kind,
      evidence_id: entity.evidence_id,
      start: entity.start,
      end: entity.end,
      action: reusable ? "reuse" : compatible.length === 0 ? "create" : "ambiguous",
      candidate_entity_id: reusable ? compatible[0].entity_id : null,
      confidence: 0.99,
      rationale: reusable ? "The submitted candidate is an exact compatible match." : "The submitted span is evidence grounded.",
    };
  });

  const relationshipResults = submittedRelationships.map((relationship) => {
    const hint = findRef(input.client_proposal, relationship.ref);
    const proposedKey = String(hint?.predicate?.proposed_key ?? "");
    const predicate = predicateOptions.find((option) => option?.predicate_key === proposedKey)
      ?? predicateOptions.find((option) => option?.predicate_key === "primary_database");
    if (!predicate) {
      throw new Error(`no predicate option for submitted relationship ${relationship.ref}`);
    }
    const validFrom = nullableString(hint?.valid_from);
    const validTo = nullableString(hint?.valid_to);
    return {
      ref: relationship.ref,
      subject_ref: relationship.subject_ref,
      original_predicate: relationship.original_predicate,
      predicate_status: "resolved",
      predicate_key: predicate.predicate_key,
      predicate_version: predicate.version,
      object_ref: relationship.object_ref ?? null,
      object_value: relationship.object_value ?? null,
      polarity: relationship.polarity,
      modality: relationship.modality,
      evidence: relationship.evidence,
      valid_from: validFrom,
      valid_to: validTo,
      scope_status: "absent",
      scope_key: null,
      evidence_verdict: "entailed",
      temporal_verdict: validFrom || validTo ? "entailed" : "absent",
      confidence: 0.99,
      rationale: "The submitted evidence directly supports the relationship.",
    };
  });

  return {
    request_id: input.request_id,
    security_signals: [],
    entity_results: entityResults,
    relationship_results: relationshipResults,
  };
}

function conflictAssessmentResponse(input) {
  const evidence = Array.isArray(input.evidence) ? input.evidence : [];
  const selected = evidence.find((item) => String(item?.content ?? "").includes("[conflict-ai-select-winner]"));
  if (selected) {
    return {
      decision: "select",
      position_id: selected.position_id,
      confidence: 0.99,
      rationale: "The deterministic fixture marks this position for selection.",
    };
  }
  return {
    decision: "abstain",
    position_id: null,
    confidence: 0,
    rationale: "The deterministic fixture requires abstention.",
  };
}

function initialProviderInput(messages) {
  if (!Array.isArray(messages)) {
    throw new Error("messages must be an array");
  }
  for (const message of messages) {
    if (message?.role !== "user" || typeof message.content !== "string") {
      continue;
    }
    try {
      const value = JSON.parse(message.content);
      if (value && typeof value === "object" && !Object.hasOwn(value, "validation_errors")) {
        return value;
      }
    } catch {
      // The initial structured payload is the only user message that must parse.
    }
  }
  throw new Error("initial provider payload is missing");
}

function findRef(value, ref) {
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = findRef(item, ref);
      if (found) return found;
    }
    return null;
  }
  if (!value || typeof value !== "object") return null;
  if (value.ref === ref) return value;
  for (const child of Object.values(value)) {
    const found = findRef(child, ref);
    if (found) return found;
  }
  return null;
}

function sendChat(response, content) {
  sendJSON(response, 200, {
    choices: [{ message: { content: JSON.stringify(content) } }],
    usage: { prompt_tokens: 10, completion_tokens: 10, total_tokens: 20 },
  });
}

async function readJSON(request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > 4 * 1024 * 1024) throw new Error("request exceeds 4 MiB");
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

function sendJSON(response, status, body) {
  response.writeHead(status, { "Content-Type": "application/json" });
  response.end(JSON.stringify(body));
}

function positiveInteger(value, name) {
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed <= 0) throw new Error(`${name} must be a positive integer`);
  return parsed;
}

function nullableString(value) {
  return typeof value === "string" && value ? value : null;
}
