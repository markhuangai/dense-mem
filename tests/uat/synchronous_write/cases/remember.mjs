import assert from "node:assert/strict";

import { assertTerminalRememberResult } from "../surface.mjs";

export const name = "remember";

export async function run({ rpc, rawRPC = rpc, expect }) {
  const selectedFault = (process.env.DENSE_MEM_E2E_PROVIDER_FAULT || "none").trim();
  const faults = selectedFault === "none" ? [
    "none",
    "multi",
    "mixed-objects",
    "mixed",
    "repair",
    "repair-exhausted",
    "security",
    "no-supported",
    "unavailable",
    "malformed",
    "timeout",
    "embedding-count",
    "embedding-model",
    "embedding-dimension",
    "embedding-non-finite",
    "embedding-timeout",
    "embedding-cancel",
    "search-generation-rotation",
  ] : [selectedFault];

  const listed = await rpc("tools/list", {});
  const tools = listed.tools || [];
  const rememberTool = tools.find((tool) => tool.name === "remember");
  expect(rememberTool, "selected E2E catalog must expose remember");
  const rememberProperties = rememberTool.outputSchema?.properties || rememberTool.output_schema?.properties || {};
  expect(!Object.hasOwn(rememberProperties, "status_tool") && !Object.hasOwn(rememberProperties, "check_after_seconds"), "selected Remember schema must not expose polling fields");
  expect(tools.some((tool) => tool.name === "get_submission_status"), "selected catalog must retain the unrelated status tool");

  const results = [];
  for (const fault of faults) {
    if (fault === "embedding-cancel") {
      results.push(await runCancellationCase({ rpc, rawRPC, expect }));
    } else if (fault === "multi") {
      results.push(await runMultiItemCase({ rpc, expect }));
    } else if (fault === "mixed-objects") {
      results.push(await runMixedObjectCase({ rpc, expect }));
    } else if (fault === "mixed") {
      results.push(await runMixedDispositionCase({ rpc, expect }));
    } else if (fault === "repair" || fault === "repair-exhausted") {
      results.push(await runRepairCase({ rpc, expect, fault }));
    } else if (fault === "security" || fault === "no-supported") {
      results.push(await runTerminalDomainCase({ rpc, expect, fault }));
    } else if (fault.startsWith("embedding-")) {
      results.push(await runEmbeddingFaultCase({ rpc, expect, fault }));
    } else if (fault === "search-generation-rotation") {
      results.push(await runSearchGenerationFenceCase({ rpc, expect }));
    } else {
      results.push(await runProviderFaultCase({ rpc, expect, fault }));
    }
  }

  if (selectedFault === "none") {
    results.push(await runConcurrentWinnerCase({ rpc, expect }));
    results.push(await runChangedHashConflictCase({ rawRPC, expect }));
    results.push(await runSupersessionFenceCase({ rpc, expect }));
    results.push(await runInputBudgetCase({ rpc, expect }));
  }
  return { mode: name, results };
}

async function runProviderFaultCase({ rpc, expect, fault }) {
  const args = singleItemArguments(`provider-${fault}`, fault === "none" ? "" : `[fixture-fault:${fault}]`);
  const first = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(first, expect);
  const expected = {
    none: ["completed", "current", ""],
    unavailable: ["failed", "not_required", "provider_unavailable"],
    malformed: ["failed", "not_required", "provider_response_invalid"],
    timeout: ["failed", "not_required", "provider_unavailable"],
    "assessment-unavailable": ["failed", "not_required", "provider_unavailable"],
    "assessment-malformed": ["failed", "not_required", "provider_response_invalid"],
    "assessment-timeout": ["failed", "not_required", "provider_unavailable"],
  }[fault];
  expect(expected, `unsupported provider fault ${fault}`);
  expect(first.processing_state === expected[0], `${fault} must return ${expected[0]}`);
  expect(first.search_state === expected[1], `${fault} must return ${expected[1]} search state`);
  if (expected[2]) expect(first.errors[0]?.code === expected[2], `${fault} must return ${expected[2]}`);
  expect(first.submission_id, "terminal Remember must return a submission id");
  expect(!Object.hasOwn(first, "status_tool") && !Object.hasOwn(first, "check_after_seconds"), "terminal Remember must not return polling metadata");

  const replay = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(replay, expect);
  if (fault === "none") {
    expect(stableJSON(replay) === stableJSON(first), "matching terminal replay must be byte-equivalent");
  } else {
    expect(replay.processing_state === "failed", `failed attempts must remain retryable: ${JSON.stringify({ fault, first, replay })}`);
    expect(replay.submission_id !== first.submission_id, `same-hash operational failure must execute a new attempt: ${JSON.stringify({ fault, first, replay })}`);
  }
  return { fault, processing_state: first.processing_state, submission_id: first.submission_id };
}

async function runMultiItemCase({ rpc, expect }) {
  const suffix = Date.now();
  const subject = `Dense-Mem Multi ${suffix}`;
  const database = `PostgreSQL Multi ${suffix}`;
  const protocol = `MCP Multi ${suffix}`;
  const predicate = `stores_memory_in_multi_${suffix}`;
  const args = {
    evidence: [
      { content: `${subject} stores its durable memory in ${database}. [fixture:multi-a]`, source_type: "manual" },
      { content: `${subject} exposes a stable ${protocol} contract. [fixture:multi-b]`, source_type: "manual" },
    ],
    relationships: [
      relationship("database", subject, "project", { entity: { name: database, entity_kind: "product" } }, [0, 1], predicate),
      relationship("protocol", subject, "project", { entity: { name: protocol, entity_kind: "product" } }, [1], predicate),
    ],
    idempotency_key: `synchronous-write-remember-multi-${Date.now()}`,
  };
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "completed", `mixed Entity batch must complete: ${JSON.stringify(result)}`);
  expect(result.evidence.length === 2, "multi-item batch must return every evidence disposition");
  expect(result.evidence.every((item) => item.disposition === "stored" && item.search_state === "current"), "multi-item evidence must be current");
  expect(result.relationship_results.length === 2, "multi-item batch must return every relationship disposition");
  expect(result.relationship_results.every((item) => item.disposition === "stored" && item.splits.length > 0), "multi-item relationships must be stored with splits");
  return { fault: "multi", processing_state: result.processing_state, evidence_count: result.evidence.length };
}

async function runMixedObjectCase({ rpc, expect }) {
  const suffix = Date.now();
  const subject = `Dense-Mem Mixed Objects ${suffix}`;
  const database = `PostgreSQL Mixed Objects ${suffix}`;
  const entityPredicate = `stores_memory_in_mixed_objects_entity_${suffix}`;
  const args = {
    evidence: [
      { content: `${subject} stores its durable memory in ${database}. [fixture:mixed-objects-entity]`, source_type: "manual" },
      { content: `${subject} retains a stable memory contract. [fixture:mixed-objects-value]`, source_type: "manual" },
    ],
    relationships: [
      relationship("entity-object", subject, "project", { entity: { name: database, entity_kind: "product" } }, [0], entityPredicate),
      relationship("typed-value", subject, "project", { value: { type: "string", value: "stable" } }, [1]),
    ],
    idempotency_key: `synchronous-write-remember-mixed-objects-${Date.now()}`,
  };
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "completed", `mixed Entity and typed-Value batch must complete: ${JSON.stringify(result)}`);
  expect(result.evidence.length === 2 && result.evidence.every((item) => item.disposition === "stored" && item.search_state === "current"), "mixed object evidence must be current");
  const byRef = new Map(result.relationship_results.map((item) => [item.ref, item]));
  expect(byRef.get("entity-object")?.disposition === "stored" && byRef.get("entity-object")?.splits.length > 0, "Entity-object relationship must be stored");
  expect(byRef.get("typed-value")?.disposition === "stored" && byRef.get("typed-value")?.splits.length > 0, "typed-Value relationship must be stored");
  return { fault: "mixed-objects", processing_state: result.processing_state, relationship_count: result.relationship_results.length };
}

async function runMixedDispositionCase({ rpc, expect }) {
  const suffix = Date.now();
  const subject = `Dense-Mem Mixed ${suffix}`;
  const database = `PostgreSQL Mixed ${suffix}`;
  const annotation = `annotation Mixed ${suffix}`;
  const predicate = `stores_memory_in_mixed_${suffix}`;
  const args = {
    evidence: [
      { content: `${subject} stores its durable memory in ${database}. [fixture:mixed-a]`, source_type: "manual" },
      { content: `The unsupported ${annotation} is not a durable claim by ${subject}. [fixture:mixed-b]`, source_type: "manual" },
    ],
    relationships: [
      relationship("supported", subject, "project", { entity: { name: database, entity_kind: "product" } }, [0, 1], predicate),
      relationship("unsupported", subject, "project", { entity: { name: annotation, entity_kind: "concept" } }, [1], predicate),
    ],
    idempotency_key: `synchronous-write-remember-mixed-${Date.now()}`,
  };
  const marked = { ...args, evidence: args.evidence.map((item) => ({ ...item, content: `${item.content} [fixture-fault:mixed]` })) };
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: marked }));
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "completed", "mixed stored/not-stored batch must complete");
  const byRef = new Map(result.relationship_results.map((item) => [item.ref, item]));
  expect(byRef.get("supported")?.disposition === "stored", "supported relationship must be stored");
  expect(byRef.get("unsupported")?.disposition === "not_stored", "unsupported relationship must be not_stored");
  expect(byRef.get("unsupported")?.splits.length === 0, "not-stored relationship must have no splits");
  return { fault: "mixed", processing_state: result.processing_state, dispositions: [...byRef].map(([ref, item]) => [ref, item.disposition]) };
}

async function runRepairCase({ rpc, expect, fault }) {
  const args = singleItemArguments(`assessment-${fault}`, `[fixture-fault:${fault}]`);
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  const expectedState = fault === "repair" ? "completed" : "failed";
  const expectedCode = fault === "repair" ? "" : "provider_response_invalid";
  expect(result.processing_state === expectedState, `${fault} must return ${expectedState}`);
  if (expectedCode) expect(result.errors[0]?.code === expectedCode, `${fault} must return ${expectedCode}`);
  return { fault, processing_state: result.processing_state };
}

async function runTerminalDomainCase({ rpc, expect, fault }) {
  const args = singleItemArguments(`domain-${fault}`, `[fixture-fault:${fault}]`);
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  const expectedState = fault === "security" ? "quarantined" : "rejected";
  const expectedCode = fault === "security" ? "submission_quarantined" : "no_supported_memory";
  expect(result.processing_state === expectedState, `${fault} must return ${expectedState}`);
  expect(result.search_state === "not_required", `${fault} must not require search`);
  expect(result.errors[0]?.code === expectedCode, `${fault} must return ${expectedCode}: ${JSON.stringify(result)}`);
  expect(result.evidence.every((item) => item.disposition === "not_stored"), `${fault} must not store evidence`);
  expect(result.relationship_results.every((item) => item.disposition === "not_stored" && item.splits.length === 0), `${fault} must not store relationships`);
  const replay = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(replay, expect);
  expect(stableJSON(replay) === stableJSON(result), `${fault} terminal replay must be byte-equivalent`);
  expect(replay.submission_id === result.submission_id && replay.correlation_id === result.correlation_id, `${fault} terminal replay must reuse identity`);
  return { fault, processing_state: result.processing_state };
}

async function runEmbeddingFaultCase({ rpc, expect, fault }) {
  const args = singleItemArguments(`embedding-${fault}`, `[fixture-fault:${fault}]`);
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "failed", `${fault} must fail in the embedding phase`);
  const expectedCode = fault === "embedding-timeout" ? "embedding_unavailable" : "embedding_response_invalid";
  expect(result.errors[0]?.code === expectedCode, `${fault} must return ${expectedCode}: ${JSON.stringify(result)}`);
  expect(result.evidence.every((item) => item.disposition === "not_stored"), `${fault} must leave evidence not_stored`);
  return { fault, processing_state: result.processing_state, error_code: result.errors[0]?.code };
}

async function runCancellationCase({ rpc, rawRPC, expect }) {
  const args = singleItemArguments("embedding-cancel", "[fixture-fault:embedding-cancel]");
  const controller = new AbortController();
  const request = rawRPC("tools/call", { name: "remember", arguments: args }, controller.signal);
  setTimeout(() => controller.abort(), 100);
  await assert.rejects(request, (error) => error?.name === "AbortError");
  await new Promise((resolve) => setTimeout(resolve, 500));
  const retry = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(retry, expect);
  expect(retry.processing_state === "completed", "a cancelled request must be retryable with the same key");
  return { fault: "embedding-cancel", processing_state: retry.processing_state };
}

async function runSearchGenerationFenceCase({ rpc, expect }) {
  const args = singleItemArguments("search-generation-rotation", "[fixture-fault:search-generation-rotation]");
  const failed = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(failed, expect);
  expect(failed.processing_state === "failed", `search-generation rotation must fail: ${JSON.stringify(failed)}`);
  expect(failed.search_state === "not_required", "late search-generation fence must not require search");
  expect(failed.errors[0]?.code === "commit_conflict", `late search-generation fence must return commit_conflict: ${JSON.stringify(failed)}`);
  expect(failed.errors[0]?.retryable === true && failed.errors[0]?.next_action === "retry_same_request", "late search-generation fence must be retryable with the same key");
  expect(failed.evidence.every((item) => item.disposition === "not_stored"), "late search-generation fence must leave evidence not_stored");
  expect(failed.relationship_results.every((item) => item.disposition === "not_stored" && item.splits.length === 0), "late search-generation fence must leave relationships not_stored");

  const retry = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(retry, expect);
  expect(retry.processing_state === "completed", `late search-generation fence must recover on retry: ${JSON.stringify(retry)}`);
  expect(retry.submission_id !== failed.submission_id, "retry after a late fence must create a new attempt");
  return { fault: "search-generation-rotation", processing_state: failed.processing_state, retry_state: retry.processing_state };
}

async function runConcurrentWinnerCase({ rpc, expect }) {
  const args = singleItemArguments("concurrent", "[fixture:concurrent]");
  const [left, right] = await Promise.all([
    rpc("tools/call", { name: "remember", arguments: args }),
    rpc("tools/call", { name: "remember", arguments: args }),
  ]);
  const first = terminalPayload(left);
  const second = terminalPayload(right);
  assertStrictTerminalRemember(first, expect);
  assertStrictTerminalRemember(second, expect);
  expect(first.processing_state === "completed" && second.processing_state === "completed", `concurrent identical requests must both receive a terminal winner: ${JSON.stringify([first, second])}`);
  expect(stableJSON(first) === stableJSON(second), `concurrent identical requests must reuse byte-equivalent winner content: ${JSON.stringify([first, second])}`);
  return { fault: "concurrent", processing_state: first.processing_state, submission_id: first.submission_id };
}

async function runChangedHashConflictCase({ rawRPC, expect }) {
  const key = `synchronous-write-remember-conflict-${Date.now()}`;
  const firstArgs = singleItemArguments("conflict", "[fixture:conflict-a]");
  firstArgs.idempotency_key = key;
  const firstResponse = await rawRPC("tools/call", { name: "remember", arguments: firstArgs });
  const first = terminalPayload(firstResponse.result);
  assertStrictTerminalRemember(first, expect);
  const secondArgs = singleItemArguments("conflict", "[fixture:conflict-b]");
  secondArgs.idempotency_key = key;
  const response = await rawRPC("tools/call", { name: "remember", arguments: secondArgs });
  expect(response.error && response.result === undefined, "changed request hash must return a bounded public conflict");
  return { fault: "changed-hash", conflict: true };
}

async function runSupersessionFenceCase({ rpc, expect }) {
  const targetArgs = singleItemArguments("supersession-target", "[fixture:supersession-target]");
  const target = terminalPayload(await rpc("tools/call", { name: "remember", arguments: targetArgs }));
  assertStrictTerminalRemember(target, expect);
  expect(target.processing_state === "completed", "supersession target must complete");
  const targetEvidenceID = target.evidence.find((item) => item.evidence_id)?.evidence_id;
  expect(targetEvidenceID, "supersession target must return an evidence id");

  const replacementArgs = singleItemArguments("supersession-replacement", "[fixture:supersession-replacement]");
  replacementArgs.evidence[0].supersedes_evidence_ids = [targetEvidenceID];
  const replacement = terminalPayload(await rpc("tools/call", { name: "remember", arguments: replacementArgs }));
  assertStrictTerminalRemember(replacement, expect);
  expect(replacement.processing_state === "completed", "supersession replacement must complete");
  expect(replacement.evidence[0].superseded_evidence_ids.length === 1 && replacement.evidence[0].superseded_evidence_ids[0] === targetEvidenceID, "supersession replacement must return its committed target evidence id");

  const staleArgs = singleItemArguments("supersession-stale", "[fixture:supersession-stale]");
  staleArgs.evidence[0].supersedes_evidence_ids = [targetEvidenceID];
  const stale = terminalPayload(await rpc("tools/call", { name: "remember", arguments: staleArgs }));
  assertStrictTerminalRemember(stale, expect);
  expect(stale.processing_state === "rejected", `stale supersession must reject: ${JSON.stringify(stale)}`);
  expect(stale.search_state === "not_required", "stale supersession must not require search");
  expect(stale.errors[0]?.code === "stale_input", `stale supersession must return stale_input: ${JSON.stringify(stale)}`);
  expect(stale.evidence.every((item) => item.disposition === "not_stored"), "stale supersession must not store evidence");

  const lateTargetArgs = singleItemArguments("supersession-late-target", "[fixture:supersession-late-target]");
  const lateTarget = terminalPayload(await rpc("tools/call", { name: "remember", arguments: lateTargetArgs }));
  assertStrictTerminalRemember(lateTarget, expect);
  expect(lateTarget.processing_state === "completed", "late supersession target must complete");
  const lateTargetEvidenceID = lateTarget.evidence.find((item) => item.evidence_id)?.evidence_id;
  expect(lateTargetEvidenceID, "late supersession target must return an evidence id");

  const late = singleItemArguments("supersession-late", "[fixture-fault:supersession-rotation]");
  late.evidence[0].supersedes_evidence_ids = [lateTargetEvidenceID];
  const lateResult = terminalPayload(await rpc("tools/call", { name: "remember", arguments: late }));
  assertStrictTerminalRemember(lateResult, expect);
  expect(lateResult.processing_state === "rejected", `late supersession must reject: ${JSON.stringify(lateResult)}`);
  expect(lateResult.errors[0]?.code === "stale_input", `late supersession must return stale_input: ${JSON.stringify(lateResult)}`);
  expect(lateResult.evidence.every((item) => item.disposition === "not_stored"), "late supersession must not store evidence");
  return { fault: "supersession-stale", processing_state: stale.processing_state, late_processing_state: lateResult.processing_state };
}

async function runInputBudgetCase({ rpc, expect }) {
  const evidenceCount = 20;
  const relationships = Array.from({ length: 140 }, (_, index) => relationship(
    `budget-${index}`,
    "Budget subject",
    "project",
    { entity: { name: `Budget object ${index}`, entity_kind: "concept" } },
    [index % evidenceCount],
    "uses",
  ));
  const evidence = Array.from({ length: evidenceCount }, (_, evidenceIndex) => ({
    content: [
      `Dense-Mem input budget evidence ${evidenceIndex}.`,
      "Budget subject.",
      ...relationships
        .filter((_, relationshipIndex) => relationshipIndex % evidenceCount === evidenceIndex)
        .map((item) => `${item.object.entity.name}.`),
    ].join(" "),
    source_type: "manual",
  }));
  const args = {
    evidence,
    relationships,
    idempotency_key: `synchronous-write-remember-input-budget-${Date.now()}-${Math.random()}`,
  };
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "failed", `input budget must fail: ${JSON.stringify(result)}`);
  expect(result.search_state === "not_required", "input budget must not require search");
  expect(result.errors[0]?.code === "input_budget_exceeded", `input budget must return input_budget_exceeded: ${JSON.stringify(result)}`);
  expect(result.evidence.every((item) => item.disposition === "not_stored"), "input budget must not store evidence");
  expect(result.relationship_results.every((item) => item.disposition === "not_stored" && item.splits.length === 0), "input budget must not store relationships");
  return { fault: "input-budget", processing_state: result.processing_state };
}

function singleItemArguments(label, marker) {
  const content = `Dense-Mem stores durable memory in PostgreSQL. [fixture:${label}]${marker ? ` ${marker}` : ""}`;
  return {
    evidence: [{ content, source_type: "manual" }],
    relationships: [relationship("durable-store", "Dense-Mem", "project", { value: { type: "string", value: "PostgreSQL" } }, [0])],
    idempotency_key: `synchronous-write-remember-${label}-${Date.now()}-${Math.random()}`,
  };
}

function relationship(ref, subjectName, subjectKind, object, evidenceIndices, predicateKey = "stores_memory_in") {
  return {
    ref,
    subject: { name: subjectName, entity_kind: subjectKind },
    predicate: { proposed_key: predicateKey },
    object,
    polarity: "+",
    evidence_indices: evidenceIndices,
  };
}

function terminalPayload(result) {
  return result?.content?.[0]?.text ? JSON.parse(result.content[0].text) : result;
}

function assertStrictTerminalRemember(result, expect) {
  assertTerminalRememberResult(result);
  expect(typeof result.contract_version === "string" && result.contract_version.length > 0, "terminal Remember must include contract_version");
  expect(typeof result.submission_id === "string" && result.submission_id.length > 0, "terminal Remember must include a submission id");
  expect(result.submission_kind === "remember", "terminal Remember must include remember submission_kind");
  expect(typeof result.correlation_id === "string" && result.correlation_id.length > 0, "terminal Remember must include correlation_id");
  expect(Array.isArray(result.errors), "terminal Remember must include errors array");
  for (const evidence of result.evidence) {
    expect(typeof evidence.disposition === "string" && Number.isInteger(evidence.evidence_index), "terminal evidence must have disposition and index");
    expect(Array.isArray(evidence.superseded_evidence_ids) && typeof evidence.search_state === "string", "terminal evidence must have supersession and search state");
  }
  for (const relationship of result.relationship_results) {
    expect(typeof relationship.ref === "string" && typeof relationship.disposition === "string" && Array.isArray(relationship.splits), "terminal relationship result must have ref, disposition, and splits");
  }
  for (const error of result.errors) {
    expect(typeof error.code === "string" && typeof error.message === "string" && typeof error.retryable === "boolean" && typeof error.next_action === "string" && typeof error.remediation === "string", "terminal error must have complete bounded guidance");
  }
}

function stableJSON(value) {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}
