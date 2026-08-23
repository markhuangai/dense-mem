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
    const conversation = providerConversation(payload?.messages);
    const providerInput = conversation.input;
    if (schemaName === "dense_mem_semantic_assessment_response") {
      if (evidenceContains(providerInput, "[remember-provider-fail]")) {
        await delay(1_000);
        return sendJSON(response, 503, { error: { message: "deterministic remember provider failure" } });
      }
      if (evidenceContains(providerInput, "[remember-database-fail]") || evidenceContains(providerInput, "[remember-post-ack-stale]")) {
        await delay(2_000);
      }
      return sendChat(response, semanticAssessmentResponse(providerInput, conversation.repairTurn));
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

function semanticAssessmentResponse(input, repairTurn) {
  const candidateGroups = Array.isArray(input.entity_candidate_groups) ? input.entity_candidate_groups : [];
  const predicateOptions = Array.isArray(input.predicate_options) ? input.predicate_options : [];
  const submittedEntities = Array.isArray(input.submitted_entities) ? input.submitted_entities : [];
  const submittedRelationships = Array.isArray(input.submitted_relationships) ? input.submitted_relationships : [];
  const groundingRepair = evidenceContains(input, "[remember-grounding-repair]");
  const mixed = evidenceContains(input, "[remember-mixed]");
  const multiSplit = evidenceContains(input, "[remember-multi-split]");

  const entityResults = submittedEntities.map((entity, index) => {
    const grounding = Array.isArray(entity.groundings) ? entity.groundings[0] : undefined;
    const group = candidateGroups.find((candidate) => candidate?.grounding_ref === grounding?.grounding_ref);
    const compatible = (group?.candidates ?? []).filter((candidate) => candidate?.kind === entity.kind);
    const reusable = compatible.length === 1 && !group?.candidate_context_truncated;
    const knownEntityID = String(entity.known_entity_id ?? "");
    const exact = knownEntityID !== "";
    return {
      ref: entity.ref,
      grounding_ref: groundingRepair && !repairTurn && index === 0 ? "stale-grounding-ref" : grounding?.grounding_ref ?? null,
      action: grounding ? (exact || reusable ? "reuse" : compatible.length === 0 ? "create" : "ambiguous") : "ambiguous",
      candidate_entity_id: exact ? knownEntityID : reusable ? compatible[0].entity_id : null,
    };
  });

  const relationshipResults = submittedRelationships.map((relationship) => {
    if (mixed && String(relationship.ref).includes("not-supported")) {
      return {
        ref: relationship.ref,
        disposition: "not_supported",
        reason: "not_supported_by_evidence",
        splits: [],
      };
    }
    const hint = findRef(input.client_proposal, relationship.ref);
    const proposedKey = String(relationship.known_predicate_key ?? hint?.predicate?.known_predicate_key ?? hint?.predicate?.proposed_key ?? relationship.predicate_hint ?? "");
    const splitSpecs = multiSplit && String(relationship.ref).includes("multi-split")
      ? [{ key: "uses", surface: "uses" }, { key: "works_on", surface: "works on" }]
      : [{ key: proposedKey, surface: String(relationship.predicate_hint ?? proposedKey).replaceAll("_", " ") }];
    return {
      ref: relationship.ref,
      disposition: "stored",
      reason: null,
      splits: splitSpecs.map((spec, splitIndex) => semanticAssessmentSplit(
        input,
        relationship,
        predicateOptions,
        splitIndex,
        spec.key,
        spec.surface,
      )),
    };
  });

  return {
    request_id: input.request_id,
    security_signals: [],
    entity_results: entityResults,
    relationship_results: relationshipResults,
  };
}

function semanticAssessmentSplit(input, relationship, predicateOptions, splitIndex, predicateKey, predicateSurface) {
  const evidenceID = relationship.evidence_ids?.[0];
  const evidence = (input.evidence ?? []).find((item) => item?.evidence_id === evidenceID);
  if (!evidence) {
    throw new Error(`no evidence for submitted relationship ${relationship.ref}`);
  }
  const predicate = predicateOptions.find((option) => option?.predicate_key === predicateKey);
  if (relationship.known_predicate_key && !predicate) {
    throw new Error(`exact predicate option is absent for submitted relationship ${relationship.ref}`);
  }
  const supportRange = groundedOffsetRange(evidence, 0, Array.from(String(evidence.content ?? "")).length);
  let valueRange = null;
  if (relationship.object_value) {
    const valueText = String(relationship.object_value.display ?? relationship.object_value.canonical_value ?? "");
    valueRange = groundedTextRange(evidence, valueText);
  }
  return {
    split_index: splitIndex,
    subject_ref: relationship.subject_ref,
    predicate_range: groundedTextRange(evidence, predicateSurface),
    predicate_status: predicate ? "resolved" : "registration_required",
    predicate_key: predicate?.predicate_key ?? null,
    predicate_version: predicate?.version ?? null,
    predicate_registration: predicate ? null : {
      predicate_key: predicateKey,
      relationship_kind: "state",
      current_cardinality: "many",
    },
    object_ref: relationship.object_ref ?? null,
    object_value: relationship.object_value ?? null,
    value_range: valueRange,
    polarity: relationship.polarity,
    support_ranges: [supportRange],
    valid_from: nullableString(relationship.valid_from),
    valid_to: nullableString(relationship.valid_to),
  };
}

function groundedTextRange(evidence, text) {
  const contentRunes = Array.from(String(evidence?.content ?? ""));
  const textRunes = Array.from(String(text));
  const lowerContent = contentRunes.map((value) => value.toLocaleLowerCase());
  const lowerText = textRunes.map((value) => value.toLocaleLowerCase());
  for (let start = 0; start <= lowerContent.length - lowerText.length; start += 1) {
    if (lowerText.every((value, index) => lowerContent[start + index] === value)) {
      return groundedOffsetRange(evidence, start, start + lowerText.length);
    }
  }
  throw new Error(`grounding text ${JSON.stringify(text)} is absent`);
}

function groundedOffsetRange(evidence, start, end) {
  const refs = [...String(evidence?.boundary_text ?? "").matchAll(/⟦([^⟧]+)⟧/gu)].map((match) => match[1]);
  if (!refs[start] || !refs[end] || end <= start) {
    throw new Error("evidence boundary references are incomplete");
  }
  return { evidence_id: evidence.evidence_id, start_ref: refs[start], end_ref: refs[end] };
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

function providerConversation(messages) {
  if (!Array.isArray(messages)) {
    throw new Error("messages must be an array");
  }
  let initial = null;
  const repairs = [];
  for (const message of messages) {
    if (message?.role !== "user" || typeof message.content !== "string") {
      continue;
    }
    try {
      const value = JSON.parse(message.content);
      if (!value || typeof value !== "object") {
        continue;
      }
      if (Object.hasOwn(value, "validation_errors")) {
        repairs.push(value);
      } else if (initial === null) {
        initial = value;
      }
    } catch {
      // The initial structured payload is the only user message that must parse.
    }
  }
  if (initial === null) {
    throw new Error("initial provider payload is missing");
  }
  const refreshed = repairs.at(-1)?.refreshed_candidate_context;
  const input = refreshed && typeof refreshed === "object"
    ? {
        ...initial,
        entity_candidate_groups: refreshed.entity_candidate_groups ?? initial.entity_candidate_groups,
        predicate_options: refreshed.predicate_options ?? initial.predicate_options,
      }
    : initial;
  return { input, repairTurn: repairs.length > 0 };
}

function evidenceContains(input, marker) {
  return (input.evidence ?? []).some((item) => String(item?.content ?? "").includes(marker));
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

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
