#!/usr/bin/env node
import { createServer } from "node:http";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";
import { forwardEmbedding } from "../embedding_upstream.mjs";
import { handleDecisionFixture } from "../decision_providers/protocol.mjs";

const port = Number(process.env.PORT || 8787);
const fault = (process.env.DENSE_MEM_E2E_PROVIDER_FAULT || "none").trim();
const timeoutDelayMs = Number(process.env.DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS || 35_000);
const assessmentAttempts = new Map();
const embeddingCallsByFault = new Map();
let ontologyMode = "normal";
let ontologyFailuresRemaining = 0;
let activeOntologyRequests = 0;
let maxActiveOntologyRequests = 0;
const heldOntologyRequests = new Set();
let holdAdmissionRequests = false;
const heldAdmissionRequests = new Set();
const admissionTiming = { events: [], dropped: 0, instrumentation_ms: 0 };
const embeddingTiming = { events: [], dropped: 0, instrumentation_ms: 0 };
let assessmentCalls = 0;
let embeddingCalls = 0;
const chatRequests = [];

function recordTiming(timing, event, fields = {}) {
  const started = performance.now();
  if (timing.events.length === 128) {
    timing.events.shift();
    timing.dropped += 1;
  }
  const entry = { event, at_ms: started, ...fields };
  timing.events.push(entry);
  entry.instrumentation_ms = performance.now() - started;
  timing.instrumentation_ms += entry.instrumentation_ms;
  return entry;
}

function sendJSON(response, status, payload) {
  const body = JSON.stringify(payload);
  response.writeHead(status, { "content-type": "application/json", "content-length": Buffer.byteLength(body) });
  response.end(body);
}

const server = createServer(async (request, response) => {
  if (request.method === "GET" && request.url === "/health") {
    sendJSON(response, 200, { status: "ok", fault, assessment_calls: assessmentCalls, embedding_calls: embeddingCalls, chat_requests: chatRequests, active_ontology_requests: activeOntologyRequests, max_active_ontology_requests: maxActiveOntologyRequests, held_admission_requests: heldAdmissionRequests.size, admission_timing: admissionTiming, embedding_timing: embeddingTiming });
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

  if (request.method === "POST" && request.url === "/admission-fixture") {
    const delayMS = payload.delay_ms ?? 0;
    if (!["normal", "hold"].includes(payload.mode) || !Number.isSafeInteger(delayMS) || delayMS < 0 || delayMS > 5000) {
      sendJSON(response, 400, { error: "invalid admission fixture mode" });
      return;
    }
    if (payload.mode === "hold") {
      admissionTiming.events.length = 0;
      admissionTiming.dropped = 0;
      admissionTiming.instrumentation_ms = 0;
      embeddingTiming.events.length = 0;
      embeddingTiming.dropped = 0;
      embeddingTiming.instrumentation_ms = 0;
    }
    recordTiming(admissionTiming, "control_received", { mode: payload.mode, delay_ms: delayMS });
    if (delayMS) await new Promise((resolve) => setTimeout(resolve, delayMS));
    holdAdmissionRequests = payload.mode === "hold";
    if (!holdAdmissionRequests) for (const release of heldAdmissionRequests) release();
    recordTiming(admissionTiming, "control_applied", { mode: payload.mode, held: heldAdmissionRequests.size });
    sendJSON(response, 200, { mode: payload.mode });
    return;
  }

  if (handleDecisionFixture(request, response, payload)) return;

  if (request.method === "POST" && request.url === "/ontology-fixture") {
    if (!["normal", "matching", "ambiguous", "fail-three-then-success", "hold"].includes(payload.mode)) {
      sendJSON(response, 400, { error: "invalid ontology fixture mode" });
      return;
    }
    ontologyMode = payload.mode;
    if (payload.mode !== "hold") for (const release of heldOntologyRequests) release();
    if (payload.mode === "hold") maxActiveOntologyRequests = activeOntologyRequests;
    ontologyFailuresRemaining = payload.mode === "fail-three-then-success" ? 3 : 0;
    sendJSON(response, 200, { mode: ontologyMode });
    return;
  }

  const route = request.url?.endsWith("/embeddings") ? "embedding" : request.url?.endsWith("/chat/completions") ? "assessment" : "other";
  const requestFault = fixtureFault(payload, route) || fault;
  const routeFault = faultForRoute(requestFault, route);
  if (route === "assessment" && chatRequests.length < 2048) {
    chatRequests.push({
      model: typeof payload.model === "string" ? payload.model : "",
      schema_name: typeof payload.response_format?.json_schema?.name === "string" ? payload.response_format.json_schema.name : "",
      fault: routeFault,
      generated_fields: Object.keys(payload.response_format?.json_schema?.schema?.properties || {}),
      source_kinds: payload.response_format?.json_schema?.name === "ontology_evidence_organization_v3"
        ? [...new Set((assessmentInput(payload).items || []).map((item) => item.kind))] : [],
    });
  }
  let embeddingFaultCall = 0;
  if (route === "embedding") embeddingCalls += 1;
  if (route === "embedding") {
    const embeddingFaultKey = routeFault || "none";
    embeddingFaultCall = (embeddingCallsByFault.get(embeddingFaultKey) || 0) + 1;
    embeddingCallsByFault.set(embeddingFaultKey, embeddingFaultCall);
  }
  if (routeFault === "timeout" || routeFault === "assessment-timeout" || routeFault === "embedding-timeout" || routeFault === "embedding-only-timeout" || (routeFault === "embedding-cancel" && embeddingFaultCall === 1) || (routeFault === "embedding-session-cancel" && embeddingFaultCall === 1)) {
    await new Promise((resolve) => setTimeout(resolve, timeoutDelayMs));
  }
  if (routeFault === "unavailable" || routeFault === "assessment-unavailable" || routeFault === "embedding-unavailable") {
    response.destroy();
    return;
  }
  if (routeFault === "status-429") {
    sendJSON(response, 429, { error: { message: "fixture rate limited", code: "rate_limit" } });
    return;
  }
  if (routeFault === "status-500") {
    sendJSON(response, 500, { error: { message: "fixture provider failure", code: "provider_failure" } });
    return;
  }
  if (routeFault === "embedding-status-409") {
    sendJSON(response, 409, { error: { message: "fixture embedding conflict", code: "fixture_embedding_conflict" } });
    return;
  }

  if (request.url?.endsWith("/embeddings")) {
    const inputs = Array.isArray(payload.input) ? payload.input : [payload.input || ""];
    if (inputs.every((input) => String(input).includes("[fixture-ranking:ontology]"))) {
      const embedding = Array.from({ length: payload.dimensions }, (_, index) => Number(index === 0));
      sendJSON(response, 200, { model: payload.model, data: inputs.map((_, index) => ({ index, embedding })) });
      return;
    }
    if (routeFault === "malformed" || routeFault === "embedding-malformed" || routeFault === "embedding-model") {
      sendJSON(response, 200, { model: "fixture-wrong-model", data: [{ index: 0, embedding: [0] }] });
      return;
    }
    if (holdAdmissionRequests && routeFault === "admission-hold") {
      await new Promise((resolve) => {
        const release = (reason = "control") => {
          clearTimeout(timer);
          response.off("close", closed);
          heldAdmissionRequests.delete(release);
          recordTiming(admissionTiming, "released", { call: embeddingFaultCall, reason, held: heldAdmissionRequests.size });
          resolve();
        };
        const closed = () => release("client_closed");
        const timer = setTimeout(() => release("hold_deadline"), 30_000);
        heldAdmissionRequests.add(release);
        recordTiming(admissionTiming, "held", { call: embeddingFaultCall, held: heldAdmissionRequests.size });
        response.once("close", closed);
      });
      if (response.destroyed) return;
    }
    const embeddingStarted = performance.now();
    const upstream = await forwardEmbedding(payload, response);
    const timing = recordTiming(embeddingTiming, "upstream_completed", { fault: routeFault, call: embeddingFaultCall, item_count: inputs.length,
      elapsed_ms: performance.now() - embeddingStarted, status: upstream.status, client_closed: response.destroyed });
    if (upstream.status !== 200 || response.destroyed) process.stdout.write(JSON.stringify({ type: "fixture_embedding_timing", ...timing }) + "\n");
    if (upstream.status !== 200) {
      sendJSON(response, upstream.status, upstream.body);
      return;
    }
    const result = upstream.body;
    if (routeFault === "embedding-count" && Array.isArray(result.data)) {
      result.data = result.data.slice(0, Math.max(0, inputs.length - 1));
    }
    if (routeFault === "embedding-dimension" && Array.isArray(result.data?.[0]?.embedding)) {
      result.data[0].embedding = result.data[0].embedding.slice(0, -1);
    }
    if (routeFault === "embedding-non-finite" && Array.isArray(result.data?.[0]?.embedding)) {
      result.data[0].embedding[0] = "NaN";
    }
    if (routeFault === "embedding-no-usage") delete result.usage;
    sendJSON(response, 200, result);
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
    let assessment;
    if (payload.response_format?.json_schema?.name === "ontology_evidence_organization_v3") {
      activeOntologyRequests += 1;
      maxActiveOntologyRequests = Math.max(maxActiveOntologyRequests, activeOntologyRequests);
      try {
        if (ontologyMode === "hold") await new Promise((resolve) => {
          const release = () => { clearTimeout(timer); heldOntologyRequests.delete(release); resolve(); };
          const timer = setTimeout(release, 30000);
          heldOntologyRequests.add(release);
        });
        assessment = fixtureChatResponse(payload, assessmentFault, attempt);
      } finally { activeOntologyRequests -= 1; }
    } else assessment = fixtureChatResponse(payload, assessmentFault, attempt);
    const properties = payload.response_format?.json_schema?.schema?.properties;
    if (properties && !Object.hasOwn(properties, "evidence_security_results")) delete assessment.evidence_security_results;
    if (properties && !Object.hasOwn(properties, "evidence_equivalence_results")) delete assessment.evidence_equivalence_results;
    sendJSON(response, 200, {
      choices: [{ message: { content: JSON.stringify(assessment) } }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    });
    return;
  }
  sendJSON(response, 404, { error: { message: "fixture route not found" } });
});

function fixtureSessionExtraction(input) {
  const core = input.window.core;
  const segments = [input.window.before, ...core, input.window.after].filter(Boolean);
  const result = { request_id: input.request_id, overflow: false, coverage: core.map((item) => item.ref), excerpts: [], security_signals: [] };
  const byEvent = new Map();
  for (const segment of segments) {
    const list = byEvent.get(segment.event_index) || [];
    list.push(segment);
    byEvent.set(segment.event_index, list);
  }
  for (const parts of byEvent.values()) {
    parts.sort((left, right) => left.start - right.start);
    const text = parts.map((part) => part.text).join("");
    const offset = parts[0].start;
    const runes = Array.from(text);
    if (text.includes("[fixture:session-overlap-whitespace]") && offset === 0 && runes.length === 1000) {
      for (const [start, end] of [[0, 900], [800, 1000]]) {
        if (!core.some((part) => part.event_index === parts[0].event_index && part.start < end && part.end > start)) continue;
        result.excerpts.push({ event_index: parts[0].event_index, start, end, content: runes.slice(start, end).join("") });
      }
      continue;
    }
    for (const match of text.matchAll(/([\p{L}\d]+) (?:also )?uses ([\p{L}\d]+)\.(?: \[fixture-fault:[a-z0-9_-]+\])?/gu)) {
      const start = offset + Array.from(text.slice(0, match.index)).length;
      const end = start + Array.from(match[0]).length;
      if (!core.some((part) => part.event_index === parts[0].event_index && part.start < end && part.end > start)) continue;
      result.excerpts.push({ event_index: parts[0].event_index, start, end, content: match[0] });
    }
  }
  return result;
}

function assessmentInput(payload) {
  const messages = Array.isArray(payload.messages) ? payload.messages : [];
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index]?.role !== "user" || typeof messages[index]?.content !== "string") continue;
    try {
      const parsed = JSON.parse(messages[index].content);
      if (Array.isArray(parsed.evidence) || (Array.isArray(parsed.items) && (Array.isArray(parsed.pairs) || parsed.policy_version))) return parsed;
    } catch {
      // Repair feedback is not an assessment request.
    }
  }
  return { request_id: "fixture", evidence: [] };
}

function fixtureChatResponse(payload, requestFault = "none", attempt = 1) {
  const schemaName = payload.response_format?.json_schema?.name;
  if (schemaName === "dense_mem_semantic_partition_response") {
    const input = structuredInput(payload, (value) => Array.isArray(value.items) && value.policy_version);
    const result = { request_id: input.request_id, items: input.items.map((item) => {
      const refs = [...item.boundary_text.matchAll(/⟦([^⟧]+)⟧/g)].map((match) => match[1]);
      const breakRef = [...item.boundary_text.matchAll(/[\r\n]+⟦([^⟧]+)⟧/g)].map((match) => match[1]).find((ref) => ref !== refs.at(-1));
      return { item_id: item.item_id, end_refs: requestFault === "partition-split" && breakRef ? [breakRef, refs.at(-1)] : [refs.at(-1)] };
    }) };
    if (requestFault === "partition-missing") result.items = [];
    if (requestFault === "partition-unknown") result.items[0].end_refs = ["invented"];
    if (requestFault === "partition-repair" && attempt === 1) result.items[0].end_refs = ["invented"];
    return result;
  }
  if (schemaName === "session_evidence_excerpts_v2") return fixtureSessionExtraction(structuredInput(payload, (input) => input.window));
  if (schemaName === "ontology_evidence_organization_v3") return fixtureOntologyOrganization(payload);
  if (schemaName !== "dense_mem_evidence_assessment_response") throw new Error(`unsupported fixture schema ${schemaName}`);
  const assessment = fixtureAssessment(assessmentInput(payload), requestFault);
  if (requestFault === "routed-field-repair" || requestFault === "routed-field-exhausted") {
    const repair = structuredInput(payload, (input) => Array.isArray(input.validation_errors));
    const actionable = repair.validation_errors?.some((error) => error.field === "request_id" && error.message === "is required");
    if (requestFault === "routed-field-exhausted" || !actionable) delete assessment.request_id;
  }
  if (requestFault === "assessment-invalid") assessment.evidence_security_results[0].evidence_id = "provider-forged-ref";
  if (requestFault === "assessment-duplicate") assessment.evidence_security_results.push(structuredClone(assessment.evidence_security_results[0]));
  if (requestFault === "assessment-missing") assessment.evidence_security_results = [];
  if (requestFault === "assessment-unknown") assessment.retired_field = [];
  return assessment;
}

function fixtureOntologyOrganization(payload) {
  const input = structuredInput(payload, (value) => Array.isArray(value.items) && Array.isArray(value.pairs));
  if (ontologyFailuresRemaining > 0) {
    ontologyFailuresRemaining -= 1;
    return { request_id: input.request_id, definitions: [], items: [], equivalence: [] };
  }
  if (ontologyMode === "ambiguous") {
    return {
      request_id: input.request_id, definitions: [],
      items: input.items.map((item) => item.locked_definition_ref
        ? { ref: item.ref, status: "classified", definition_ref: item.locked_definition_ref, reason: "" }
        : { ref: item.ref, status: "ambiguous", definition_ref: "", reason: "classification_uncertain" }),
      equivalence: input.pairs.map((pair) => ({ ref: pair.ref, relation: pair.required_relation || "ambiguous" })),
    };
  }
  const definitions = [];
  const created = new Map();
  const items = input.items.map((item) => {
    const topicKey = /redis|keydb/i.test(JSON.stringify(item)) ? "memory-coordination" : "memory-storage";
    const candidateKey = ontologyMode === "matching"
      ? (/\bheliostat\b/i.test(item.text) ? "suntracker" : /\babout\b/i.test(item.text) ? "about" : /\band\b/i.test(item.text) ? "conjunctioncatalog" : "grammarnoise")
      : topicKey;
    let ref = item.locked_definition_ref || item.preferred_definition_ref || input.definitions.find((definition) => definition.kind === "topic" && definition.key === candidateKey)?.ref || created.get(topicKey);
    if (!ref) {
      ref = `new-definition-${definitions.length}`;
      created.set(topicKey, ref);
      definitions.push({ ref, kind: "topic", key: topicKey, label: topicKey.replaceAll("-", " "), description: "Synthetic organization vocabulary", aliases: [topicKey === "memory-storage" ? "database platform" : "coordination platform"], parent_ref: "" });
    }
    return { ref: item.ref, status: "classified", definition_ref: ref, reason: "" };
  });
  const sourceItems = new Map(input.items.map((item) => [item.ref, item]));
  const equivalentFixtureTexts = new Set(["Atlas stores PostgreSQL data.", "Atlas uses PostgreSQL for data storage."]);
  return { request_id: input.request_id, definitions, items, equivalence: input.pairs.map((pair) => {
    const left = sourceItems.get(pair.left_ref);
    const right = sourceItems.get(pair.right_ref);
    const equivalent = equivalentFixtureTexts.has(left?.text) && equivalentFixtureTexts.has(right?.text);
    return { ref: pair.ref, relation: pair.required_relation || (equivalent ? "equivalent" : "distinct") };
  }) };
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

function fixtureAssessment(request, requestFault) {
  const securityResults = (request.evidence || []).map((evidence) => ({
    evidence_id: evidence.evidence_id,
    decision: requestFault === "security" ? "reject" : "pass",
    signals: requestFault === "security" ? [{
      kind: "instruction_override", start_ref: wholeEvidenceRange(evidence).start_ref, end_ref: wholeEvidenceRange(evidence).end_ref,
    }] : [],
  }));
  const evidenceEquivalenceResults = (request.evidence_equivalence_candidates || []).map((group) => {
    if (requestFault === "semantic-reuse") {
      const candidate = group.candidates?.find((item) => String(item.content || "").includes("semantic-duplicate-canonical")) || group.candidates?.[0];
      return { evidence_id: group.evidence_id, action: "reuse", candidate_evidence_id: candidate?.evidence_id || "fixture-missing-candidate" };
    }
    if (requestFault === "semantic-reuse-unauthorized") return { evidence_id: group.evidence_id, action: "reuse", candidate_evidence_id: "fixture-unauthorized-candidate" };
    return { evidence_id: group.evidence_id, action: "new", candidate_evidence_id: null };
  });
  const citedConflict = (request.evidence || []).length >= 2 && request.evidence.some((item) => String(item?.content || "").includes("[fixture:cited-evidence-conflict]"));
  const evidenceConflictResults = citedConflict ? [{ positions: request.evidence.slice(0, 2).map(wholeEvidenceRange) }] : [];
  return { request_id: request.request_id || "fixture", evidence_security_results: securityResults, evidence_equivalence_results: evidenceEquivalenceResults, evidence_conflict_results: evidenceConflictResults };
}

function wholeEvidenceRange(evidence) {
  const boundaryText = String(evidence?.boundary_text || "");
  const refs = [...boundaryText.matchAll(/⟦([^⟧]+)⟧/g)].map((match) => match[1]);
  return { evidence_id: evidence?.evidence_id || "evidence:0", start_ref: refs[0] || "fixture-start", end_ref: refs.at(-1) || "fixture-end" };
}

export function fixtureFault(payload, route) {
  if (route === "embedding") return faultMarker(Array.isArray(payload.input) ? payload.input : [payload.input]);
  if (route !== "assessment") return "";
  const schemaName = payload.response_format?.json_schema?.name;
  if (schemaName === "dense_mem_semantic_partition_response") {
    const input = structuredInput(payload, (value) => Array.isArray(value.items) && value.policy_version);
    const fault = faultMarker(input.items.map((item) => item.boundary_text));
    return fault.startsWith("partition-") ? fault : "";
  }
  if (schemaName === "session_evidence_excerpts_v2") {
    const input = structuredInput(payload, (value) => value.window);
    return faultMarker([input.window?.before, ...(input.window?.core || []), input.window?.after].filter(Boolean).map((segment) => segment.text));
  }
  if (schemaName === "ontology_evidence_organization_v3") {
    const input = structuredInput(payload, (value) => Array.isArray(value.items));
    return faultMarker((input.items || []).map((item) => item.text));
  }
  return faultMarker((assessmentInput(payload).evidence || []).flatMap((item) => [item?.content, item?.boundary_text]));
}

function faultMarker(inputs) {
  for (const input of inputs) {
    const match = String(input || "").match(/\[fixture-fault:([a-z0-9_-]+)\]/i);
    if (match) return match[1];
  }
  return "";
}

function faultForRoute(value, route) {
  const normalized = String(value || "").trim().toLowerCase();
  if (route === "embedding" && (
    normalized === "unavailable" || normalized === "malformed" || normalized === "timeout" || normalized.startsWith("assessment-") ||
    normalized === "repair" || normalized === "repair-exhausted" ||
    normalized === "security" ||
    normalized === "status-429" || normalized === "status-500"
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

export { fixtureChatResponse, server };
