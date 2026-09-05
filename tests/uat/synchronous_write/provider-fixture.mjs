#!/usr/bin/env node
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const port = Number(process.env.PORT || 8787);
const dimensions = Number(process.env.DENSE_MEM_E2E_PROVIDER_DIMENSIONS || 1536);
const fault = (process.env.DENSE_MEM_E2E_PROVIDER_FAULT || "none").trim();
const timeoutDelayMs = Number(process.env.DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS || 30_000);
const correctionProviderFaultMarker = "e2e-correction-provider-fault";
const correctionProviderTimeoutMarker = "e2e-correction-provider-timeout";
const assessmentAttempts = new Map();
const embeddingCallsByFault = new Map();
let assessmentCalls = 0;
let embeddingCalls = 0;

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
    sendJSON(response, 200, { status: "ok", fault, assessment_calls: assessmentCalls, embedding_calls: embeddingCalls });
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

  const requestFault = fixtureFault(payload) || fault;
  const route = request.url?.endsWith("/embeddings") ? "embedding" : request.url?.endsWith("/chat/completions") ? "assessment" : "other";
  const routeFault = faultForRoute(requestFault, route);
  let embeddingFaultCall = 0;
  if (route === "embedding") embeddingCalls += 1;
  if (route === "embedding") {
    const embeddingFaultKey = routeFault || "none";
    embeddingFaultCall = (embeddingCallsByFault.get(embeddingFaultKey) || 0) + 1;
    embeddingCallsByFault.set(embeddingFaultKey, embeddingFaultCall);
  }
  if (routeFault === "timeout" || routeFault === "assessment-timeout" || routeFault === "embedding-timeout" || routeFault === "embedding-only-timeout" || (routeFault === "embedding-cancel" && embeddingFaultCall === 1)) {
    await new Promise((resolve) => setTimeout(resolve, timeoutDelayMs));
  }
  if (routeFault === "unavailable" || routeFault === "assessment-unavailable" || routeFault === "embedding-unavailable") {
    response.destroy();
    return;
  }

  if (request.url?.endsWith("/embeddings")) {
    const inputs = Array.isArray(payload.input) ? payload.input : [payload.input || ""];
    if (inputs.some((input) => String(input).includes(correctionProviderTimeoutMarker))) {
      await new Promise((resolve) => setTimeout(resolve, timeoutDelayMs));
    }
    if (inputs.some((input) => String(input).includes(correctionProviderFaultMarker))) {
      response.destroy();
      return;
    }
    if (routeFault === "malformed" || routeFault === "embedding-malformed" || routeFault === "embedding-model") {
      sendJSON(response, 200, { model: "fixture-wrong-model", data: [{ index: 0, embedding: [0] }] });
      return;
    }
    if (routeFault === "embedding-count") {
      sendJSON(response, 200, {
        model: payload.model || "dense-mem-e2e-embedding",
        data: inputs.slice(0, Math.max(0, inputs.length - 1)).map((input, index) => ({ index, embedding: vectorFor(input, dimensions) })),
        usage: { prompt_tokens: inputs.length, total_tokens: inputs.length },
      });
      return;
    }
    const vectorWidth = routeFault === "embedding-dimension" ? Math.max(1, dimensions - 1) : dimensions;
    const data = inputs.map((input, index) => ({ index, embedding: vectorFor(input, vectorWidth) }));
    if (routeFault === "embedding-non-finite" && data.length > 0) data[0].embedding[0] = "NaN";
    sendJSON(response, 200, {
      model: payload.model || "dense-mem-e2e-embedding",
      data,
      usage: { prompt_tokens: inputs.length, total_tokens: inputs.length },
    });
    return;
  }

  if (request.url?.endsWith("/chat/completions")) {
    assessmentCalls += 1;
    const assessmentRequest = assessmentInput(payload);
    const assessmentFault = routeFault;
    const attemptKey = assessmentRequest.request_id || "fixture";
    const attempt = (assessmentAttempts.get(attemptKey) || 0) + 1;
    assessmentAttempts.set(attemptKey, attempt);
    const repair = isRepairPayload(payload);
    if (assessmentFault === "malformed" || assessmentFault === "assessment-malformed" || assessmentFault === "repair-exhausted" || (assessmentFault === "repair" && !repair)) {
      sendJSON(response, 200, { choices: [{ message: { content: "not-json" } }] });
      return;
    }
    const assessment = fixtureChatResponse(payload, assessmentFault, attempt);
    sendJSON(response, 200, {
      choices: [{ message: { content: JSON.stringify(assessment) } }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    });
    return;
  }
  sendJSON(response, 404, { error: { message: "fixture route not found" } });
});

function assessmentInput(payload) {
  const messages = Array.isArray(payload.messages) ? payload.messages : [];
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index]?.role !== "user" || typeof messages[index]?.content !== "string") continue;
    try {
      const parsed = JSON.parse(messages[index].content);
      if (Array.isArray(parsed.evidence)) return parsed;
    } catch {
      // Repair feedback is not an assessment request.
    }
  }
  return { request_id: "fixture", submitted_entities: [], submitted_relationships: [], evidence: [] };
}

function fixtureChatResponse(payload, requestFault = "none", attempt = 1) {
  const schemaName = payload.response_format?.json_schema?.name;
  if (schemaName === "community_summary") return fixtureCommunitySummary(payload);
  if (schemaName === "dense_mem_dream_generation_response") return fixtureDreamGeneration(payload);
  if (schemaName === "dense_mem_evidence_discovery_response") return fixtureEvidenceDiscovery(payload);
  return fixtureAssessment(assessmentInput(payload), requestFault, attempt);
}

function fixtureCommunitySummary(payload) {
  const input = structuredInput(payload, (value) => Array.isArray(value.relationships));
  const relationships = input.relationships || [];
  const facts = relationships
    .map((relationship) => [relationship.subject, relationship.predicate, relationship.object].map((value) => String(value || "").trim()).filter(Boolean).join(" "))
    .filter(Boolean);
  const supportQuotes = [];
  const seenQuoteEvidence = new Set();
  for (const relationship of relationships) {
    for (const quote of relationship.support_quotes || []) {
      const evidenceID = String(quote?.evidence_id || "").trim();
      const text = String(quote?.quote || "").trim();
      if (!evidenceID || !text || seenQuoteEvidence.has(evidenceID)) continue;
      seenQuoteEvidence.add(evidenceID);
      supportQuotes.push({ evidence_id: evidenceID, quote: text });
    }
  }
  return {
    summary: facts.length > 0 ? `${facts.join("; ")}.` : "The supplied relationships form a community.",
    top_entities: uniqueStrings(relationships.flatMap((relationship) => [relationship.subject, relationship.object])).slice(0, 5),
    top_predicates: uniqueStrings(relationships.map((relationship) => relationship.predicate)).slice(0, 5),
    admitted_relationship_ids: uniqueStrings(relationships.map((relationship) => relationship.relationship_id)),
    admitted_evidence_ids: uniqueStrings(relationships.flatMap((relationship) => relationship.evidence_ids || [])),
    admitted_support_quotes: supportQuotes,
  };
}

function fixtureDreamGeneration(payload) {
  const input = structuredInput(payload, (value) => Array.isArray(value.paths));
  const maxOutputs = Number.isSafeInteger(input.max_outputs) && input.max_outputs > 0 ? input.max_outputs : 1;
  const proposals = [];
  for (const path of input.paths || []) {
    const predicate = path.allowed_predicates?.[0];
    const evidenceRefs = (path.premises || [])
      .map((premise) => premise.evidence?.[0]?.evidence_ref)
      .filter((value) => typeof value === "string" && value.length > 0);
    if (!path.path_ref || !predicate?.predicate_ref || evidenceRefs.length !== 2) continue;
    const subject = String(path.subject?.display || "the subject").trim();
    const object = String(path.object?.display || "the object").trim();
    const label = String(predicate.label || "the supplied predicate").trim();
    proposals.push({
      path_ref: path.path_ref,
      predicate_ref: predicate.predicate_ref,
      statement: `${subject} may be related to ${object} through ${label}.`,
      rationale: "The supplied two-premise path supports evaluating this possibility.",
      what_if: "What if the proposed relationship is useful?",
      possible_outcome: "The hypothesis can be reviewed against its cited evidence.",
      likelihood: 0.7,
      confidence: 0.8,
      evidence_refs: evidenceRefs,
    });
    if (proposals.length === maxOutputs) break;
  }
  return { request_id: input.request_id || "fixture", proposals };
}

function fixtureEvidenceDiscovery(payload) {
  const input = structuredInput(payload, (value) => Array.isArray(value.contexts) && Array.isArray(value.nodes));
  const target = input.contexts?.[0];
  const predicate = input.allowed_predicates?.[0];
  const subject = input.nodes?.[0];
  const object = [...(input.nodes || [])].reverse().find((node) => node?.ref && node.ref !== subject?.ref) || input.nodes?.[1];
  const markers = [...String(target?.boundary_text || "").matchAll(/⟦([^⟧]+)⟧/g)].map((match) => match[1]);
  if (!target?.evidence_ref || !predicate?.ref || !subject?.ref || !object?.ref || markers.length < 2) {
    return { request_id: input.request_id || "fixture", proposals: [] };
  }
  if ((input.related_hypotheses || []).some((hypothesis) => (
    hypothesis?.subject_ref === subject.ref &&
    hypothesis?.predicate === predicate.ref &&
    hypothesis?.object_ref === object.ref
  ))) {
    return { request_id: input.request_id || "fixture", proposals: [] };
  }
  return {
    request_id: input.request_id || "fixture",
    proposals: [{
      subject_ref: subject.ref,
      predicate_ref: predicate.ref,
      object_ref: object.ref,
      statement: `${subject.display} may be related to ${object.display} through ${predicate.label}.`,
      rationale: "The target evidence supports evaluating this supplied relationship.",
      what_if: "What if this relationship is useful?",
      possible_outcome: "The hypothesis can be reviewed against the target evidence.",
      likelihood: 0.7,
      confidence: 0.8,
      derivations: [{ evidence_ref: target.evidence_ref, start_ref: markers[0], end_ref: markers[markers.length - 1] }],
    }],
  };
}

function structuredInput(payload, matches) {
  const messages = Array.isArray(payload.messages) ? payload.messages : [];
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index]?.role !== "user" || typeof messages[index]?.content !== "string") continue;
    try {
      const parsed = JSON.parse(messages[index].content);
      if (matches(parsed)) return parsed;
    } catch {
      // Correction feedback is not the original structured request.
    }
  }
  return {};
}

function uniqueStrings(values) {
  return [...new Set(values.map((value) => String(value || "").trim()).filter(Boolean))];
}

function fixtureAssessment(request, requestFault, attempt) {
  const evidenceByID = new Map([
    ...(request.evidence || []),
    ...(request.known_evidence || []),
  ].map((item) => [item.evidence_id, item]));
  const proposalRelationships = Array.isArray(request.client_proposal?.relationship_hints)
    ? request.client_proposal.relationship_hints
    : Array.isArray(request.client_proposal?.relationships)
      ? request.client_proposal.relationships
      : [];
  const proposalByRef = new Map(proposalRelationships.map((relationship) => [relationship?.ref, relationship]));
  const entities = (request.submitted_entities || []).map((entity) => {
    const selectedGrounding = requestFault === "ambiguous-pronoun"
      ? entity.groundings?.find((grounding) => grounding.surface === "It") || entity.groundings?.[0]
      : entity.groundings?.[0];
    const groundingRef = selectedGrounding?.grounding_ref ?? null;
    const group = (request.entity_candidate_groups || []).find((candidate) => candidate.grounding_ref === groundingRef);
    const candidates = (group?.candidates || []).filter((candidate) => candidate.kind === entity.kind);
    const reusable = candidates.length === 1 ? candidates[0] : null;
    const ambiguous = !groundingRef && (requestFault === "unanchored-pronoun" || requestFault === "ambiguous-pronoun");
    const result = {
      ref: entity.ref,
      grounding_ref: groundingRef,
      action: ambiguous ? "ambiguous" : reusable ? "reuse" : "create",
      candidate_entity_id: reusable?.entity_id ?? null,
    };
    if (selectedGrounding?.anchor_ref) result.anchor_ref = selectedGrounding.anchor_ref;
    return result;
  });
  const relationships = (request.submitted_relationships || []).map((relationship, index) => {
    const evidenceIDs = Array.isArray(relationship.evidence_ids) ? relationship.evidence_ids : [];
    const knownEvidenceIDs = Array.isArray(relationship.known_evidence_ids) ? relationship.known_evidence_ids : [];
    const requestedKnownEvidenceIDs = Array.isArray(proposalByRef.get(relationship.ref)?.known_evidence_ids)
      ? proposalByRef.get(relationship.ref).known_evidence_ids
      : knownEvidenceIDs;
    const unavailableKnownEvidence = requestedKnownEvidenceIDs.some((evidenceID) => !evidenceByID.has(evidenceID));
    const ranges = [...evidenceIDs, ...knownEvidenceIDs]
      .map((evidenceID) => wholeEvidenceRange(evidenceByID.get(evidenceID)))
      .filter(Boolean);
    if (requestFault === "unreferenced-known" && index > 0 && request.known_evidence?.[0]) {
      ranges.push(wholeEvidenceRange(request.known_evidence[0]));
    }
    const range = ranges[0] || wholeEvidenceRange(request.evidence?.[0]);
    const subject = (request.submitted_entities || []).find((entity) => entity.ref === relationship.subject_ref);
    const noGrounding = !subject?.groundings?.length;
    const unsupported = requestFault === "no-supported" || (requestFault === "mixed" && index > 0) ||
      unavailableKnownEvidence ||
      (requestFault === "ambiguous-pronoun") ||
      (requestFault === "unanchored-pronoun" && noGrounding);
    if (unsupported) {
      return { ref: relationship.ref, disposition: "not_supported", reason: "not_supported_by_evidence", splits: [] };
    }
    const knownPredicate = relationship.known_predicate_key || "";
    const predicateOptions = Array.isArray(request.predicate_options) ? request.predicate_options : [];
    const resolved = predicateOptions.find((option) => option.predicate_key === (knownPredicate || relationship.predicate_hint));
    return {
      ref: relationship.ref,
      disposition: "stored",
      reason: null,
      splits: [{
        split_index: 0,
        subject_ref: relationship.subject_ref,
        predicate_range: range,
        predicate_status: resolved ? "resolved" : "registration_required",
        predicate_key: resolved?.predicate_key ?? null,
        predicate_version: resolved?.version ?? null,
        predicate_registration: resolved ? null : {
          predicate_key: relationship.predicate_hint || "related_to",
          relationship_kind: "state",
          current_cardinality: "many",
        },
        object_ref: relationship.object_ref ?? null,
        object_value: relationship.object_value ?? null,
        value_range: relationship.object_value ? range : null,
        polarity: relationship.polarity,
        support_ranges: ranges.length > 0 ? ranges : [range],
        valid_from: relationship.valid_from ?? null,
        valid_to: relationship.valid_to ?? null,
      }],
    };
  });
  const securitySignals = requestFault === "security" && request.evidence?.length > 0 ? [{
    kind: "instruction_override",
    start_ref: wholeEvidenceRange(request.evidence[0]).start_ref,
    end_ref: wholeEvidenceRange(request.evidence[0]).end_ref,
  }] : [];
  const securitySignalEvidenceIDs = requestFault === "security" && request.evidence?.length > 0
    ? new Set([request.evidence[0].evidence_id])
    : new Set();
  const securityResults = (request.evidence || []).map((evidence) => ({
    evidence_id: evidence.evidence_id,
    decision: securitySignalEvidenceIDs.has(evidence.evidence_id) ? "reject" : "pass",
    signals: securitySignalEvidenceIDs.has(evidence.evidence_id) ? securitySignals : [],
  }));
  const evidenceEquivalenceResults = (request.evidence_equivalence_candidates || []).map((group) => {
    if (requestFault === "semantic-reuse") {
      const candidate = group.candidates?.find((item) => String(item.content || "").includes("semantic-duplicate-canonical")) || group.candidates?.[0];
      return {
        evidence_id: group.evidence_id,
        action: "reuse",
        candidate_evidence_id: candidate?.evidence_id || "fixture-missing-candidate",
      };
    }
    if (requestFault === "semantic-reuse-unauthorized") {
      return {
        evidence_id: group.evidence_id,
        action: "reuse",
        candidate_evidence_id: "fixture-unauthorized-candidate",
      };
    }
    return {
      evidence_id: group.evidence_id,
      action: "new",
      candidate_evidence_id: null,
    };
  });
  const citedConflict = (request.evidence || []).length >= 2 && (request.evidence || []).some((item) => String(item?.content || "").includes("[fixture:cited-evidence-conflict]"));
  const evidenceConflictResults = citedConflict ? [{
    positions: request.evidence.slice(0, 2).map((evidence) => wholeEvidenceRange(evidence)),
  }] : [];
  if (requestFault === "repair" && attempt === 1) {
    return {
      request_id: request.request_id || "fixture",
      evidence_security_results: securityResults,
      evidence_equivalence_results: evidenceEquivalenceResults,
      evidence_conflict_results: evidenceConflictResults,
      entity_results: [],
      relationship_results: relationships,
    };
  }
  return {
    request_id: request.request_id || "fixture",
    evidence_security_results: securityResults,
    evidence_equivalence_results: evidenceEquivalenceResults,
    evidence_conflict_results: evidenceConflictResults,
    entity_results: entities,
    relationship_results: relationships,
  };
}

function wholeEvidenceRange(evidence) {
  const boundaryText = String(evidence?.boundary_text || "");
  const refs = [...boundaryText.matchAll(/⟦([^⟧]+)⟧/g)].map((match) => match[1]);
  return { evidence_id: evidence?.evidence_id || "evidence:0", start_ref: refs[0] || "fixture-start", end_ref: refs.at(-1) || "fixture-end" };
}

function fixtureFault(payload) {
  const messages = Array.isArray(payload.messages) ? payload.messages : [];
  const structuredInputs = [];
  for (const message of messages) {
    if (message?.role !== "user" || typeof message.content !== "string") continue;
    try {
      structuredInputs.push(JSON.parse(message.content));
    } catch {
      // Correction feedback is not a provider request.
    }
  }
  const input = assessmentInput(payload);
  const evidence = Array.isArray(input.evidence) ? input.evidence : [];
  const embeddingInputs = Array.isArray(payload.input) ? payload.input : [payload.input];
  const structuredContent = structuredInputs.flatMap((item) => [
    ...(Array.isArray(item?.contexts) ? item.contexts.map((context) => context?.content) : []),
    ...(Array.isArray(item?.evidence) ? item.evidence.map((item) => item?.content) : []),
  ]);
  const serialized = [...evidence.map((item) => String(item?.content || "")), ...structuredContent.map((item) => String(item || "")), ...embeddingInputs.map((item) => String(item || ""))].join("\n");
  const match = serialized.match(/\[fixture-fault:([a-z0-9_-]+)\]/i);
  return match?.[1] || "";
}

function faultForRoute(value, route) {
  const normalized = String(value || "").trim().toLowerCase();
  if (route === "embedding" && (
    normalized === "unavailable" || normalized === "malformed" || normalized === "timeout" || normalized.startsWith("assessment-") ||
    normalized === "repair" || normalized === "repair-exhausted" || normalized === "security" || normalized === "no-supported" ||
    normalized === "mixed"
  )) return "";
  if (route === "assessment" && (normalized.startsWith("embedding-") || normalized === "embedding-only-timeout")) return "";
  return normalized;
}

function isRepairPayload(payload) {
  return (Array.isArray(payload.messages) ? payload.messages : []).some((message) => {
    if (message?.role !== "user" || typeof message.content !== "string") return false;
    try {
      const parsed = JSON.parse(message.content);
      return Array.isArray(parsed.validation_errors) && typeof parsed.instruction === "string";
    } catch {
      return false;
    }
  });
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  server.listen(port, "0.0.0.0", () => {
    process.stdout.write(`synchronous-write-provider listening on ${port}\n`);
  });
}

export { fixtureChatResponse };
