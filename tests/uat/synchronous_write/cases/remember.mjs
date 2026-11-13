import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

import { assertTerminalRememberResult } from "../surface.mjs";

export const name = "remember";

const repositoryRoot = fileURLToPath(new URL("../../../..", import.meta.url));
const rememberModel = "dense-mem-synchronous-write-e2e-remember";

export async function run({ rpc, rawRPC = rpc, expect, runStep }) {
  const selectedFault = (process.env.DENSE_MEM_E2E_PROVIDER_FAULT || "none").trim();
  const providerRequestOffset = await providerFixtureRequestCount(expect);
  const faults = selectedFault === "none" ? ["none", "multi", "repair", "repair-exhausted", "security", "unavailable", "malformed", "timeout", "embedding-count", "embedding-model", "embedding-dimension", "embedding-non-finite", "embedding-timeout", "embedding-cancel"] : [selectedFault];
  const tools = (await rpc("tools/list", {})).tools || [];
  expect(tools.some((tool) => tool.name === "remember"), "catalog must expose Remember");
  expect(!tools.some((tool) => tool.name === "get_submission_status"), "catalog must remove polling");
  const results = [];
  if (selectedFault === "none") results.push(await runStep("evidence-contract", () => runEvidenceContractCase({ rawRPC, expect })));
  for (const fault of faults) {
    results.push(await runStep(fault, () => {
      if (fault === "embedding-cancel") return runCancellationCase({ rpc, rawRPC, expect });
      if (fault === "multi") return runMultiItemCase({ rpc, expect });
      if (fault === "repair" || fault === "repair-exhausted") return runRepairCase({ rpc, expect, fault });
      if (fault === "security") return runTerminalDomainCase({ rpc, expect, fault });
      if (fault.startsWith("embedding-")) return runEmbeddingFaultCase({ rpc, expect, fault });
      return runProviderFaultCase({ rpc, expect, fault });
    }));
  }
  if (selectedFault === "none") {
    for (const [label, fn] of [
      ["semantic-duplicate", () => runSemanticDuplicateCase({ expect })],
      ["budget-context", () => runBudgetContextCase({ expect })],
      ["evidence-conflict", () => runEvidenceConflictCase({ rpc, expect })],
      ["concurrent-winner", () => runConcurrentWinnerCase({ rpc, expect })],
      ["admission-capacity", () => runAdmissionCapacityCase({ rpc, rawRPC, expect })],
      ["changed-hash-conflict", () => runChangedHashConflictCase({ rawRPC, expect })],
      ["supersession-fence", () => runSupersessionFenceCase({ rpc, expect })],
      ["historical-evidence-handles", () => runHistoricalEvidenceHandlesCase({ expect })],
      ["model-routing", () => assertRememberSessionModelRouting(expect, providerRequestOffset)],
    ]) results.push(await runStep(label, fn));
  }
  return { mode: name, results };
}

async function runEvidenceContractCase({ rawRPC, expect }) {
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const providerBefore = await providerFixtureRequestCount(expect);
  for (const [label, mutate, path] of [
    ["missing", (args) => { delete args.evidence; }, "/evidence"],
    ["empty", (args) => { args.evidence = []; }, "/evidence"],
    ["oversized", (args) => { args.evidence[0].content = "x".repeat(16385); }, "/evidence/0/content"],
    ...["relationships", "entities", "predicates", "proposal"].map((field) => [field, (args) => { args[field] = []; }, `/${field}`]),
  ]) {
    const args = singleItemArguments(`invalid-${label}`, "");
    mutate(args);
    const response = await rawRPC("tools/call", { name: "remember", arguments: args });
    const error = response.error?.data;
    expect(response.error?.code === -32602 && error?.code === "invalid_input" && error.reason_code === "validation_failed", `${label} must return structured validation: ${JSON.stringify(response)}`);
    expect(error.issues?.some((issue) => issue.path === path), `${label} must identify ${path}: ${JSON.stringify(error)}`);
    expect(error.retryable === false && error.next_action === "correct_and_resubmit", `${label} must return actionable correction guidance`);
    expect(Number(postgresQuery(`SELECT count(*) FROM knowledge_ingests WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND idempotency_key = '${sqlLiteral(args.idempotency_key)}'`)) === 0, `${label} must reject before durable intake`);
  }
  expect(providerBefore === await providerFixtureRequestCount(expect), "retired fields and invalid Evidence must reject before provider work");
  const boundary = singleItemArguments("evidence-bound", "");
  boundary.evidence[0].content = "x".repeat(999);
  const accepted = terminalPayload((await rawRPC("tools/call", { name: "remember", arguments: boundary })).result);
  assertStrictTerminalRemember(accepted, expect);
  expect(accepted.processing_state === "completed", "999-rune Evidence must remain supported");
  return { fault: "evidence-contract", retired_fields_rejected: true, zero_provider_calls_for_invalid: true, bounds: true };
}

async function providerFixtureRequestCount(expect) {
  const providerURL = (process.env.DENSE_MEM_E2E_PROVIDER_URL || "").replace(/\/$/, "");
  if (!providerURL) return null;
  const response = await fetch(`${providerURL}/health`);
  const state = await response.json();
  expect(response.ok, `provider fixture health returned HTTP ${response.status}`);
  return (state.chat_requests || []).length;
}

async function assertRememberSessionModelRouting(expect, providerRequestOffset) {
  if (providerRequestOffset === null) return { skipped: "provider fixture is not available" };
  const providerURL = (process.env.DENSE_MEM_E2E_PROVIDER_URL || "").replace(/\/$/, "");
  const response = await fetch(`${providerURL}/health`);
  const state = await response.json();
  expect(response.ok, `provider fixture health returned HTTP ${response.status}`);
  const assessments = (state.chat_requests || []).slice(providerRequestOffset)
    .filter((request) => request.schema_name === "dense_mem_evidence_assessment_response");
  expect(assessments.length > 0, "Remember must make semantic-assessment provider calls");
  expect(assessments.every((request) => request.model === rememberModel), `Remember used a fallback or unexpected model: ${JSON.stringify(assessments)}`);
  expect(assessments.some((request) => request.fault === "unavailable"), "Remember provider failure must reach the configured session model");
  const persistedModels = postgresQuery(`
    SELECT string_agg(DISTINCT model, ',' ORDER BY model)
    FROM semantic_assessments
    WHERE team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
      AND btrim(model) <> ''
  `);
  expect(persistedModels === rememberModel, `persisted Remember assessment model = ${persistedModels}, want ${rememberModel}`);
  return { semantic_assessment_calls: assessments.length, model: rememberModel, persisted_model: persistedModels, no_fallback: true };
}

async function runBudgetContextCase({ expect }) {
  const teamID = await createKnownEvidenceTeam(`assessor-budget-${Date.now()}`);
  const actor = await createKnownEvidenceCredential(teamID, `assessor-budget-${Date.now()}`);
  const candidateContents = [];
  for (let index = 0; index < 10; index += 1) {
    const content = `Large stored candidate ${index} ${"candidate context material ".repeat(160)} [fixture:budget-candidate]`;
    candidateContents.push(content);
    const seeded = await rememberWithKey(actor.apiKey, {
      target_space: "team_shared",
      evidence: [{ content, source_type: "manual", force_insert: true }],
      idempotency_key: `assessor-budget-seed-${index}-${Date.now()}-${Math.random()}`,
    });
    assertStrictTerminalRemember(seeded, expect);
    expect(seeded.processing_state === "completed" && seeded.evidence[0]?.disposition === "stored", `large candidate seed ${index} must complete: ${JSON.stringify(seeded)}`);
  }

  const shortRequest = {
    target_space: "team_shared",
    evidence: [{ content: "Short note for bounded server-selected context.", source_type: "manual" }],
    idempotency_key: `assessor-budget-short-${Date.now()}-${Math.random()}`,
  };
  const first = await rememberWithKey(actor.apiKey, shortRequest);
  assertStrictTerminalRemember(first, expect);
  expect(first.processing_state === "completed", `short note with large candidates must remain usable: ${JSON.stringify(first)}`);
  expect(first.errors.length === 0, `optional candidate omission must not be a client input failure: ${JSON.stringify(first)}`);
  expect(first.warnings?.includes("optional duplicate candidates were omitted to fit the configured assessor budget"), `large candidate context must report bounded omission: ${JSON.stringify(first)}`);
  const storedAssessmentCount = Number(postgresQuery(`
    SELECT count(*)
    FROM semantic_assessments
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND attempt_id = '${sqlLiteral(first.submission_id)}'::uuid;
  `));
  expect(storedAssessmentCount === 1, "bounded candidate selection must persist one accepted assessment");

  const replay = await rememberWithKey(actor.apiKey, shortRequest);
  assertStrictTerminalRemember(replay, expect);
  expect(stableJSON(replay) === stableJSON(first), "bounded candidate selection replay must be byte-equivalent");
  expect(replay.submission_id === first.submission_id, "bounded candidate selection replay must reuse the committed attempt");

  return {
    fault: "assessor-budget",
    processing_state: first.processing_state,
    candidate_count: candidateContents.length,
    omitted_optional_candidates: true,
    replay_preserved: true,
  };
}

async function runEvidenceConflictCase({ rpc, expect }) {
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const actor = await createKnownEvidenceCredential(teamID, `evidence-conflict-${Date.now()}`);
  const firstArgs = singleItemArguments("cited-evidence-conflict", "[fixture:cited-evidence-conflict]");
  firstArgs.evidence.push({ content: "The same change was rejected by the reviewer. [fixture:cited-evidence-conflict]", source_type: "manual" });
  const first = await rememberWithKey(actor.apiKey, firstArgs);
  assertStrictTerminalRemember(first, expect);
  expect(first.processing_state === "completed", `cited evidence conflict must complete: ${JSON.stringify(first)}`);
  expect(first.evidence.length === 2 && first.evidence.every((item) => item.disposition === "stored"), "cited conflict must preserve both safe evidence items");

  const conflictID = postgresQuery(`
    SELECT conflict_id::text
    FROM evidence_conflict_cases
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
    ORDER BY created_at DESC, conflict_id DESC
    LIMIT 1;
  `);
  expect(conflictID, "cited conflict must create a durable case");
  const firstRecall = await mcpSuccessWithKey(actor.apiKey, "recall_memory", { query: "cited evidence change reviewer" });
  expect(firstRecall.conflicts?.some((item) => item.kind === "evidence_conflict" && item.conflict_id === conflictID), `recall must expose cited evidence conflict provenance: ${JSON.stringify(firstRecall)}`);

  const recurrenceArgs = JSON.parse(JSON.stringify(firstArgs));
  recurrenceArgs.idempotency_key = `synchronous-write-remember-cited-evidence-conflict-recurrence-${Date.now()}-${Math.random()}`;
  const recurrence = await rememberWithKey(actor.apiKey, recurrenceArgs);
  assertStrictTerminalRemember(recurrence, expect);
  expect(recurrence.processing_state === "completed", `cited evidence conflict recurrence must complete: ${JSON.stringify(recurrence)}`);
  const detail = await controlJSON(`/teams/${teamID}/evidence-conflicts/${conflictID}?event_limit=100`);
  const conflict = detail.data?.conflict;
  expect(conflict?.version === 2 && conflict.events?.length === 2, `recurrence must increment the open case once and append history: ${JSON.stringify(detail)}`);
  const preferred = conflict.positions.find((position) => position.submitted)?.position_id;

  const stale = await controlRaw(`/teams/${teamID}/evidence-conflicts/${conflictID}/resolution`, {
    method: "POST",
    body: JSON.stringify({ expected_version: 1, decision: "resolve", reason: "stale review" }),
  });
  assertControlError(stale, 409, "CONFLICT", "stale evidence conflict resolution", expect);
  const invalid = await controlRaw(`/teams/${teamID}/evidence-conflicts/${conflictID}/resolution`, {
    method: "POST",
    body: JSON.stringify({ expected_version: 2, decision: "dismiss", reason: "invalid preferred position", preferred_position_id: "not-a-uuid" }),
  });
  assertControlError(invalid, 422, "VALIDATION_ERROR", "invalid evidence conflict resolution", expect);
  const unknown = await controlRaw(`/teams/${teamID}/evidence-conflicts/${randomUUID()}/resolution`, {
    method: "POST",
    body: JSON.stringify({ expected_version: 2, decision: "dismiss", reason: "unknown case" }),
  });
  assertControlError(unknown, 404, "NOT_FOUND", "unknown evidence conflict resolution", expect);
  const unauthenticated = await controlRaw(`/teams/${teamID}/evidence-conflicts/${conflictID}/resolution`, {
    method: "POST",
    headers: { Authorization: "" },
    body: JSON.stringify({ expected_version: 2, decision: "dismiss", reason: "missing authorization" }),
  });
  assertControlError(unauthenticated, 401, "AUTH_INVALID", "unauthenticated evidence conflict resolution", expect);
  const unchanged = await controlJSON(`/teams/${teamID}/evidence-conflicts/${conflictID}?event_limit=100`);
  expect(unchanged.data?.conflict?.version === 2 && unchanged.data.conflict.events?.length === 2 && unchanged.data.conflict.positions?.length === conflict.positions.length, "adverse control requests changed evidence conflict history");

  const dismissArgs = singleItemArguments("cited-evidence-conflict-dismiss", "[fixture:cited-evidence-conflict]");
  dismissArgs.evidence.push({ content: "This cited conflict is intentionally dismissed. [fixture:cited-evidence-conflict-dismiss]", source_type: "manual" });
  const dismissSubmission = await rememberWithKey(actor.apiKey, dismissArgs);
  assertStrictTerminalRemember(dismissSubmission, expect);
  expect(dismissSubmission.processing_state === "completed", `dismiss fixture must complete: ${JSON.stringify(dismissSubmission)}`);
  const dismissConflictID = postgresQuery(`
    SELECT conflict_id::text
    FROM evidence_conflict_cases
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
    ORDER BY created_at DESC, conflict_id DESC
    LIMIT 1;
  `);
  const dismissed = await controlJSON(`/teams/${teamID}/evidence-conflicts/${dismissConflictID}/resolution`, {
    method: "POST",
    body: JSON.stringify({ expected_version: 1, decision: "dismiss", reason: "cited positions are not actionable" }),
  });
  expect(dismissed.data?.conflict?.status === "dismissed" && dismissed.data.conflict.version === 2, `control must dismiss the cited conflict: ${JSON.stringify(dismissed)}`);
  const dismissedDetail = await controlJSON(`/teams/${teamID}/evidence-conflicts/${dismissConflictID}?event_limit=100`);
  expect(dismissedDetail.data?.conflict?.events?.[0]?.action === "dismissed" && dismissedDetail.data.conflict.events[0].citation_snapshot?.length === 2, `dismissal must retain cited position history: ${JSON.stringify(dismissedDetail)}`);
  const dismissedRecall = await mcpSuccessWithKey(actor.apiKey, "recall_memory", { query: "intentionally dismissed cited conflict" });
  expect(!dismissedRecall.conflicts?.some((item) => item.kind === "evidence_conflict" && item.conflict_id === dismissConflictID), `dismissed conflicts must not appear in current recall: ${JSON.stringify(dismissedRecall)}`);

  const resolved = await controlJSON(`/teams/${teamID}/evidence-conflicts/${conflictID}/resolution`, {
    method: "POST",
    body: JSON.stringify({ expected_version: 2, decision: "resolve", reason: "reviewed opposing citations", preferred_position_id: preferred }),
  });
  expect(resolved.data?.conflict?.status === "resolved" && resolved.data.conflict.version === 3, `control must resolve the cited conflict without changing evidence: ${JSON.stringify(resolved)}`);

  const beforeSimilarity = Number(postgresQuery(`SELECT count(*) FROM evidence_conflict_cases WHERE team_id = '${sqlLiteral(teamID)}'::uuid;`));
  const similarityArgs = singleItemArguments("cited-evidence-similarity-only", "");
  similarityArgs.evidence.push({ content: "A similar statement about the same change.", source_type: "manual" });
  const similarity = await rememberWithKey(actor.apiKey, similarityArgs);
  assertStrictTerminalRemember(similarity, expect);
  expect(similarity.processing_state === "completed", `similarity-only evidence must still complete: ${JSON.stringify(similarity)}`);
  const afterSimilarity = Number(postgresQuery(`SELECT count(*) FROM evidence_conflict_cases WHERE team_id = '${sqlLiteral(teamID)}'::uuid;`));
  expect(beforeSimilarity === afterSimilarity, "similarity-only evidence must not create an evidence conflict case without assessor citations");
  const resolvedRecall = await mcpSuccessWithKey(actor.apiKey, "recall_memory", { query: "cited evidence change reviewer" });
  const recalled = resolvedRecall.conflicts?.find((item) => item.kind === "evidence_conflict" && item.conflict_id === conflictID);
  expect(recalled?.status === "resolved" && recalled.positions?.every((position) => position.occurrence_id && position.quote), `resolved recall must retain exact evidence provenance: ${JSON.stringify(resolvedRecall)}`);

  const browserFixtures = [];
  for (let index = 0; index < 4; index += 1) {
    const browserArgs = singleItemArguments(`evidence-conflict-browser-${index}`, "[fixture:cited-evidence-conflict]");
    browserArgs.evidence.push({ content: `The browser review fixture rejects this change ${index}. [fixture:cited-evidence-conflict]`, source_type: "manual" });
    const browser = await rememberWithKey(actor.apiKey, browserArgs);
    assertStrictTerminalRemember(browser, expect);
    expect(browser.processing_state === "completed", `browser evidence conflict fixture ${index} must complete: ${JSON.stringify(browser)}`);
    const browserConflictID = postgresQuery(`
      SELECT conflict_id::text
      FROM evidence_conflict_cases
      WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      ORDER BY created_at DESC, conflict_id DESC
      LIMIT 1;
    `);
    expect(browserConflictID, `browser evidence conflict fixture ${index} must create a durable case`);
    browserFixtures.push(browserConflictID);
  }
  return { fault: "cited-evidence-conflict", created: true, recurrence: true, resolved: true, dismissed: true, similarity_only_no_case: true, browser_fixtures: browserFixtures.length };
}

async function runSemanticDuplicateCase({ expect }) {
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const actor = await createKnownEvidenceCredential(teamID, `semantic-duplicate-${Date.now()}`);
  const canonicalArgs = singleItemArguments("semantic-duplicate-canonical", "", "private");
  canonicalArgs.evidence[0].content = "Dense-Mem stores durable memory in PostgreSQL. [fixture:semantic-duplicate-canonical]";
  const canonical = await rememberWithKey(actor.apiKey, canonicalArgs);
  assertStrictTerminalRemember(canonical, expect);
  expect(canonical.processing_state === "completed", `semantic duplicate canonical write must complete: ${JSON.stringify(canonical)}`);
  const canonicalID = canonical.evidence[0]?.evidence_id;
  expect(canonicalID, "semantic duplicate canonical write must return an evidence id");

  const forcedArgs = singleItemArguments("semantic-duplicate-force", "[fixture-fault:semantic-reuse]", "private");
  forcedArgs.evidence[0].content = "Dense-Mem retains durable memory in PostgreSQL. [fixture:semantic-duplicate-force] [fixture-fault:semantic-reuse]";
  forcedArgs.evidence[0].force_insert = true;
  const forced = await rememberWithKey(actor.apiKey, forcedArgs);
  assertStrictTerminalRemember(forced, expect);
  expect(forced.processing_state === "completed", `force_insert semantic duplicate must complete: ${JSON.stringify(forced)}`);
  expect(forced.evidence[0]?.evidence_id && forced.evidence[0].evidence_id !== canonicalID, `force_insert must create a new fragment: ${JSON.stringify(forced)}`);
  expect(forced.evidence[0]?.search_state === "current", `force_insert new evidence must be searchable: ${JSON.stringify(forced)}`);
  expect(Number(postgresQuery(`
    SELECT count(*)
    FROM evidence_fragments
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND ingest_id IN ('${sqlLiteral(canonical.submission_id)}'::uuid, '${sqlLiteral(forced.submission_id)}'::uuid);
  `)) === 2, "force_insert semantic duplicate must create a second fragment");
  expect(Number(postgresQuery(`
    SELECT count(*)
    FROM evidence_occurrences
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND ingest_id IN ('${sqlLiteral(canonical.submission_id)}'::uuid, '${sqlLiteral(forced.submission_id)}'::uuid);
  `)) === 2, "force_insert semantic duplicate must preserve both occurrences");

  const exactForcedArgs = singleItemArguments("semantic-duplicate-force-exact", "", "private");
  exactForcedArgs.evidence[0].content = canonicalArgs.evidence[0].content;
  exactForcedArgs.evidence[0].force_insert = true;
  const exactForced = await rememberWithKey(actor.apiKey, exactForcedArgs);
  assertStrictTerminalRemember(exactForced, expect);
  expect(exactForced.processing_state === "completed", `force_insert exact duplicate must complete: ${JSON.stringify(exactForced)}`);
  expect(exactForced.evidence[0]?.evidence_id === canonicalID, `force_insert exact bytes must reuse the canonical fragment: ${JSON.stringify(exactForced)}`);
  expect(exactForced.evidence[0]?.search_state === "current", `force_insert exact reuse must retain current search state: ${JSON.stringify(exactForced)}`);
  expect(Number(postgresQuery(`
    SELECT count(*)
    FROM evidence_fragments
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND ingest_id IN ('${sqlLiteral(canonical.submission_id)}'::uuid, '${sqlLiteral(exactForced.submission_id)}'::uuid);
  `)) === 1, "force_insert exact duplicate must not create a second fragment");
  expect(Number(postgresQuery(`
    SELECT count(*)
    FROM evidence_occurrences
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND ingest_id IN ('${sqlLiteral(canonical.submission_id)}'::uuid, '${sqlLiteral(exactForced.submission_id)}'::uuid);
  `)) === 2, "force_insert exact duplicate must append an occurrence");

  const reusedArgs = singleItemArguments("semantic-duplicate-reuse", "[fixture-fault:semantic-reuse]", "private");
  reusedArgs.evidence[0].content = "Dense-Mem retains durable memory in PostgreSQL. [fixture:semantic-duplicate-canonical] [fixture-fault:semantic-reuse]";
  const reused = await rememberWithKey(actor.apiKey, reusedArgs);
  assertStrictTerminalRemember(reused, expect);
  expect(reused.processing_state === "completed", `semantic duplicate reuse must complete: ${JSON.stringify(reused)}`);
  expect(reused.evidence[0]?.evidence_id === canonicalID, `semantic duplicate reuse must return the canonical evidence id: ${JSON.stringify(reused)}`);
  expect(reused.search_state === "current", `semantic duplicate reuse must report current batch search state: ${JSON.stringify(reused)}`);
  expect(reused.evidence[0]?.search_state === "current", `semantic duplicate reuse must report current evidence search state: ${JSON.stringify(reused)}`);

  const fragmentCount = postgresQuery(`
    SELECT count(*)
    FROM evidence_fragments
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND ingest_id IN ('${sqlLiteral(canonical.submission_id)}'::uuid, '${sqlLiteral(reused.submission_id)}'::uuid);
  `);
  const occurrenceCount = postgresQuery(`
    SELECT count(*)
    FROM evidence_occurrences
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND ingest_id IN ('${sqlLiteral(canonical.submission_id)}'::uuid, '${sqlLiteral(reused.submission_id)}'::uuid);
  `);
  expect(Number(fragmentCount) === 1, `semantic duplicate reuse must create one fragment: ${fragmentCount}`);
  expect(Number(occurrenceCount) === 2, `semantic duplicate reuse must create two occurrences: ${occurrenceCount}`);

  const trace = await mcpSuccessWithKey(actor.apiKey, "trace_memory", { evidence_id: canonicalID });
  const occurrence = trace.occurrences?.find((item) => item.content?.includes("retains durable memory"));
  expect(occurrence?.occurrence_id && occurrence.evidence_id === canonicalID, `semantic duplicate Trace must preserve exact occurrence provenance: ${JSON.stringify(trace)}`);
  expect(trace.evidence.content === canonicalArgs.evidence[0].content, "canonical Evidence content must remain unchanged by semantic reuse");

  const unauthorizedArgs = singleItemArguments("semantic-duplicate-unauthorized", "[fixture-fault:semantic-reuse-unauthorized]", "private");
  unauthorizedArgs.evidence[0].content = "Dense-Mem keeps durable memory in PostgreSQL. [fixture:semantic-duplicate-canonical] [fixture-fault:semantic-reuse-unauthorized]";
  const unauthorizedRaw = await rawRPCWithKey(actor.apiKey, "tools/call", { name: "remember", arguments: unauthorizedArgs });
  const unauthorized = terminalPayload(unauthorizedRaw.result);
  assertStrictTerminalRemember(unauthorized, expect);
  expect(unauthorized.processing_state === "failed", `unauthorized duplicate candidate must fail: ${JSON.stringify(unauthorized)}`);
  expect(unauthorized.errors[0]?.code === "provider_response_invalid", `unauthorized duplicate candidate must be rejected by the closed assessor contract: ${JSON.stringify(unauthorized)}`);
  expect(unauthorized.evidence.every((item) => item.disposition === "not_stored"), "unauthorized duplicate candidate must not store evidence");
  expect(Number(postgresQuery(`
    SELECT count(*)
    FROM evidence_fragments
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid
      AND ingest_id = '${sqlLiteral(unauthorized.submission_id)}'::uuid;
  `)) === 0, "unauthorized duplicate candidate must not commit a fragment");

  return {
    fault: "semantic-duplicate",
    canonical_reused: true,
    occurrences_preserved: true,
    trace_occurrence_hydrated: true,
    unauthorized_candidate_rejected: true,
  };
}

async function rememberWithKey(apiKey, args) {
  const raw = await rawRPCWithKey(apiKey, "tools/call", { name: "remember", arguments: args });
  if (raw.error) throw new Error(`Remember returned a bounded MCP error: ${raw.error.message || "unknown"}`);
  return terminalPayload(raw.result);
}

async function mcpSuccessWithKey(apiKey, name, args) {
  const raw = await rawRPCWithKey(apiKey, "tools/call", { name, arguments: args });
  if (raw.error || raw.result?.isError === true) throw new Error(`MCP ${name} returned a bounded error`);
  return terminalPayload(raw.result);
}

export async function createKnownEvidenceTeam(label) {
  const response = await controlJSON("/teams", {
    method: "POST",
    body: JSON.stringify({ name: label, description: "known evidence isolation e2e" }),
  });
  const teamID = String(response.data?.id || "");
  if (!teamID) throw new Error("control API did not return a team ID for known-evidence isolation");
  return teamID;
}

export async function createKnownEvidenceCredential(teamID, name) {
  const response = await controlJSON(`/teams/${teamID}/credentials`, {
    method: "POST",
    body: JSON.stringify({ name, role: "member", scopes: ["read", "write"], rate_limit: 300 }),
  });
  const apiKey = String(response.data?.api_key || "");
  const profileID = String(response.data?.credential?.id || "");
  if (!apiKey || !profileID) throw new Error("control API did not return a known-evidence credential");
  return { apiKey, profileID };
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
  const args = singleItemArguments("multi", "");
  args.evidence.push({ content: `Dense-Mem exposes MCP. [fixture:multi-${randomUUID()}]`, source_type: "manual" });
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "completed" && result.evidence.length === 2 && result.evidence.every((item) => item.disposition === "stored" && item.search_state === "current"), "every exact Evidence item must commit and become searchable");
  for (const [index, item] of result.evidence.entries()) {
    const trace = terminalPayload(await rpc("tools/call", { name: "trace_memory", arguments: { evidence_id: item.evidence_id } }));
    expect(trace.evidence.content === args.evidence[index].content, "Evidence content must remain exact without graph proposals");
  }
  return { fault: "multi", processing_state: result.processing_state, evidence_count: result.evidence.length };
}

async function runRepairCase({ rpc, expect, fault }) {
  const args = singleItemArguments(`assessment-${fault}`, `[fixture-fault:${fault}]`);
  const startedAt = Date.now();
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  const durationMS = Date.now() - startedAt;
  assertStrictTerminalRemember(result, expect);
  const expectedState = fault === "repair" ? "completed" : "failed";
  const expectedCode = fault === "repair" ? "" : "provider_response_invalid";
  expect(result.processing_state === expectedState, `${fault} must return ${expectedState}`);
  if (expectedCode) expect(result.errors[0]?.code === expectedCode, `${fault} must return ${expectedCode}`);
  const assessorTurns = Number(postgresQuery(`
    SELECT assessor_turns
    FROM remember_attempts
    WHERE team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
      AND attempt_id = '${sqlLiteral(result.submission_id)}'::uuid
  `));
  const expectedTurns = fault === "repair" ? 2 : 3;
  expect(assessorTurns === expectedTurns, `${fault} must retain ${expectedTurns} assessor turns`);
  if (fault === "repair") {
    expect(durationMS <= 60_000, `terminal Remember repair exceeded the 60-second target: ${durationMS} ms`);
  }
  return {
    fault,
    processing_state: result.processing_state,
    assessor_turns: assessorTurns,
    ...(fault === "repair" ? { duration_ms: durationMS, terminal_target_ms: 60_000 } : {}),
  };
}

async function runTerminalDomainCase({ rpc, expect, fault }) {
  const args = multiEvidenceSecurityArguments(`domain-${fault}`);
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "failed" && result.search_state === "not_required" && result.errors[0]?.code === "submission_policy_rejected", "one rejected Evidence must reject the complete batch");
  expect(result.evidence.length === 2 && result.evidence.every((item) => item.disposition === "not_stored"), "policy rejection must not partially commit Evidence");
  expect(rememberCanonicalCounts(requiredEnv("DENSE_MEM_E2E_TEAM_ID"), result.submission_id).every((count) => count === 0), "policy rejection must commit zero canonical state");
  const replay = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  expect(stableJSON(replay) === stableJSON(result), "terminal policy rejection replay must retain its identity and outcome");
  return { fault, processing_state: result.processing_state };
}

async function runEmbeddingFaultCase({ rpc, expect, fault }) {
  const args = singleItemArguments(`embedding-${fault}`, `[fixture-fault:${fault}]`);
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "failed", `${fault} must fail in the embedding phase`);
  const expectedCode = fault === "embedding-timeout" ? "request_timeout" : "embedding_response_invalid";
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
  let retry;
  for (let attempt = 0; attempt < 3; attempt += 1) {
    retry = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
    assertStrictTerminalRemember(retry, expect);
    if (retry.processing_state === "completed") break;
    expect(retry.errors[0]?.retryable === true, `a cancelled request returned a non-retryable failure: ${JSON.stringify(retry)}`);
  }
  expect(retry?.processing_state === "completed", `a cancelled request must be retryable with the same key: ${JSON.stringify(retry)}`);
  return { fault: "embedding-cancel", processing_state: retry.processing_state };
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

async function runAdmissionCapacityCase({ rpc, rawRPC, expect }) {
  const fixtureURL = "http://synchronous-write-provider:8787";
  const timing = { stages: [], instrumentation_ms: 0 };
  const started = performance.now();
  const mark = (stage, fields = {}) => {
    const measured = performance.now();
    timing.stages.push({ stage, elapsed_ms: measured - started, ...fields });
    timing.instrumentation_ms += performance.now() - measured;
  };
  const setMode = async (mode) => {
    mark("control_started", { mode });
    const response = await fetch(`${fixtureURL}/admission-fixture`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ mode }),
    });
    expect(response.ok, `admission fixture control failed: ${response.status}`);
    mark("control_completed", { mode });
  };
  const suffix = randomUUID().replaceAll("-", "");
  const requests = Array.from({ length: 5 }, (_, index) => {
    const subject = `Admission specimen ${suffix} ${index}`;
    const value = `capacity state ${index}`;
    return {
      target_space: "team_shared",
      evidence: [{ content: `${subject} has ${value}. [fixture-fault:admission-hold]`, source_type: "manual" }],
      idempotency_key: `admission-${suffix}-${index}`,
    };
  });
  const controllers = requests.slice(0, 4).map(() => new AbortController());
  const pending = [];
  expect(rememberAdmissionLockCount() === 0, "admission fixture must begin without Remember lock holders");
  await setMode("hold");
  try {
    for (const [index, args] of requests.slice(0, 4).entries()) {
      mark("request_started", { index });
      pending.push(rawRPC("tools/call", { name: "remember", arguments: args }, controllers[index].signal)
        .then((response) => { mark("request_settled", { index }); return { response }; },
          (error) => { mark("request_settled", { index, transport_error: true }); return { error }; }));
    }
    let held = 0;
    let admitted = 0;
    timing.native_admission = { checks: 0, query_ms: 0, last_count: 0 };
    const deadline = Date.now() + 20_000;
    while (Date.now() < deadline) {
      const health = await fetch(`${fixtureURL}/health`);
      expect(health.ok, "admission fixture health failed");
      held = (await health.json()).held_admission_requests;
      const queryStarted = performance.now();
      admitted = rememberAdmissionLockCount();
      timing.native_admission.checks += 1;
      timing.native_admission.query_ms += performance.now() - queryStarted;
      timing.native_admission.last_count = admitted;
      if (admitted === 4 && held > 0) break;
      await delay(50);
    }
    expect(admitted === 4 && held > 0, `four Remember calls must hold native admission while the provider is blocked: ${admitted} admitted, ${held} provider holders`);
    mark("four_admitted_requests_observed", { held_provider_requests: held });
    mark("excess_request_started");
    const rejected = await rawRPC("tools/call", { name: "remember", arguments: requests[4] });
    mark("excess_request_completed");
    expect(rejected.result?.isError === true, "excess Remember must report a terminal error");
    const busy = rejected.result.structuredContent;
    assertStrictTerminalRemember(busy, expect);
    const failure = busy.errors[0];
    expect(failure.code === "database_failure" && failure.reason_code === "idempotency_lock_busy", "busy admission must retain its precise bounded reason");
    expect(failure.retryable === true && failure.next_action === "retry_same_request", "busy admission must preserve unchanged-request retry");
    expect(failure.message.includes("Reduce concurrent requests") && failure.remediation.includes("same idempotency_key"), "busy admission must explain capacity recovery");
    await setMode("normal");
    mark("canonical_check_started");
    const counts = rememberCanonicalCounts(requiredEnv("DENSE_MEM_E2E_TEAM_ID"), busy.submission_id);
    mark("canonical_check_completed");
    expect(counts.every((count) => count === 0), "rejected admission must commit no canonical state");
    for (const outcome of await Promise.all(pending)) {
      if (outcome.error) throw outcome.error;
      const result = terminalPayload(outcome.response.result);
      assertStrictTerminalRemember(result, expect);
      expect(result.processing_state === "completed", "admitted concurrent Remember must complete after release");
    }
    const recovered = terminalPayload(await rpc("tools/call", { name: "remember", arguments: requests[4] }));
    mark("capacity_recovered");
    assertStrictTerminalRemember(recovered, expect);
    expect(recovered.processing_state === "completed", "the rejected request must complete after capacity is released");
    const replay = terminalPayload(await rpc("tools/call", { name: "remember", arguments: requests[4] }));
    mark("replay_completed");
    expect(stableJSON(replay) === stableJSON(recovered), "capacity recovery must retain exact terminal replay");
    return { fault: "admission-capacity", admitted: 4, rejected: failure.reason_code, recovered: recovered.processing_state, timing };
  } finally {
    for (const controller of controllers) controller.abort();
    await setMode("normal");
    await Promise.all(pending);
    try {
      const health = await fetch(`${fixtureURL}/health`, { signal: AbortSignal.timeout(2000) });
      if (!health.ok) throw new Error(`fixture health returned ${health.status}`);
      const state = await health.json();
      timing.provider = state.admission_timing;
      timing.embedding = state.embedding_timing;
    } catch (error) {
      timing.capture_error = error.message;
    }
    mark("cleanup_completed");
    process.stdout.write(JSON.stringify({ type: "admission_capacity_timing", ...timing }) + "\n");
  }
}

async function runChangedHashConflictCase({ rawRPC, expect }) {
  const key = `synchronous-write-remember-conflict-${Date.now()}`;
  const firstArgs = singleItemArguments("conflict", "[fixture:conflict-a]");
  firstArgs.idempotency_key = key;
  const firstResponse = await rawRPC("tools/call", { name: "remember", arguments: firstArgs });
  const first = terminalPayload(firstResponse.result);
  assertStrictTerminalRemember(first, expect);
  expect(first.processing_state === "completed", "conflict setup must complete");
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const originalCounts = rememberCanonicalCounts(teamID, first.submission_id);
  const secondArgs = singleItemArguments("conflict", "[fixture:conflict-b]");
  secondArgs.idempotency_key = key;
  const response = await rawRPC("tools/call", { name: "remember", arguments: secondArgs });
  const conflict = response.result?.structuredContent;
  expect(response.result?.isError === true && conflict?.errors?.[0]?.code === "idempotency_conflict", "changed request hash must return a structured public conflict");
  assertStrictTerminalRemember(conflict, expect);
  expect(stableJSON(terminalPayload(response.result)) === stableJSON(conflict), "conflict text and structured result must match");
  const error = conflict.errors[0];
  expect(error.retryable === false && error.next_action === "resubmit_remember" && error.remediation.includes("new idempotency_key"), "conflict must require an unchanged replay or a deliberately new key");
  expect(conflict.evidence.length === secondArgs.evidence.length, "conflict must cover every submitted item");
  for (const item of conflict.evidence) {
    expect(item.disposition === "not_stored" && item.reason === "idempotency_conflict", "conflict items must retain their actual cause");
    expect(item.message === error.message && item.remediation === error.remediation, "conflict item guidance must match the terminal error");
    expect(!item.evidence_id && (!item.splits || item.splits.length === 0), "conflict must not return stored Evidence");
  }
  expect(rememberCanonicalCounts(teamID, conflict.submission_id).every((count) => count === 0), "conflict must apply zero canonical writes");
  const replay = terminalPayload((await rawRPC("tools/call", { name: "remember", arguments: firstArgs })).result);
  expect(stableJSON(replay) === stableJSON(first), "original request replay must retain its authoritative result after a conflict");
  expect(stableJSON(rememberCanonicalCounts(teamID, first.submission_id)) === stableJSON(originalCounts), "conflict and original replay must preserve canonical state");
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
  expect(stale.processing_state === "failed", `stale supersession must fail: ${JSON.stringify(stale)}`);
  expect(stale.search_state === "not_required", "stale supersession must not require search");
  expect(stale.errors[0]?.code === "stale_input", `stale supersession must return stale_input: ${JSON.stringify(stale)}`);
  expect(stale.evidence.every((item) => item.disposition === "not_stored"), "stale supersession must not store evidence");

  return { fault: "supersession-stale", processing_state: stale.processing_state };
}

async function runHistoricalEvidenceHandlesCase({ expect }) {
  const label = `historical-evidence-${randomUUID()}`;
  const teamID = await createKnownEvidenceTeam(label);
  const owner = await createKnownEvidenceCredential(teamID, `${label}-A`);
  const peer = await createKnownEvidenceCredential(teamID, `${label}-B`);
  const foreignTeam = await createKnownEvidenceTeam(`${label}-foreign`);
  const foreign = await createKnownEvidenceCredential(foreignTeam, `${label}-C`);
  const target = await rememberWithKey(owner.apiKey, singleItemArguments("historical-target", "", "private"));
  assertStrictTerminalRemember(target, expect);
  expect(target.processing_state === "completed", "historical handle target must complete");
  const canonical = target.evidence[0].evidence_id;
  const alias = randomUUID(), historical = randomUUID(), mapping = randomUUID();
  const fixture = postgresExec(`
    INSERT INTO evidence_occurrences(team_id,occurrence_id,canonical_fragment_id,canonical_owner_profile_id,ingest_id,owner_profile_id,
      space_id,space_generation,evidence_index,content,content_hash,source_type,authority,source_ref,source_id,source_revision_id,labels,metadata)
    SELECT team_id,'${alias}'::uuid,canonical_fragment_id,canonical_owner_profile_id,ingest_id,owner_profile_id,
      space_id,space_generation,1,content,content_hash,source_type,authority,source_ref,source_id,source_revision_id,labels,metadata
    FROM evidence_occurrences WHERE team_id='${sqlLiteral(teamID)}'::uuid AND occurrence_id='${sqlLiteral(canonical)}'::uuid AND owner_profile_id='${sqlLiteral(owner.profileID)}'::uuid
    RETURNING occurrence_id;
    INSERT INTO evidence_fragments(team_id,fragment_id,ingest_id,owner_profile_id,space_id,space_generation,evidence_index,
      content,content_hash,source_type,authority,source_ref,source_id,source_revision_id,labels,metadata)
    SELECT team_id,'${alias}'::uuid,ingest_id,owner_profile_id,space_id,space_generation,1,
      content,content_hash,source_type,authority,source_ref,source_id,source_revision_id,labels,metadata
    FROM evidence_fragments WHERE team_id='${sqlLiteral(teamID)}'::uuid AND fragment_id='${sqlLiteral(canonical)}'::uuid AND owner_profile_id='${sqlLiteral(owner.profileID)}'::uuid
    RETURNING fragment_id;
    INSERT INTO evidence_exact_aliases(team_id,alias_fragment_id,alias_owner_profile_id,canonical_fragment_id,canonical_owner_profile_id,space_id,space_generation)
    SELECT team_id,'${alias}'::uuid,owner_profile_id,fragment_id,owner_profile_id,space_id,space_generation
    FROM evidence_fragments WHERE team_id='${sqlLiteral(teamID)}'::uuid AND fragment_id='${sqlLiteral(canonical)}'::uuid AND owner_profile_id='${sqlLiteral(owner.profileID)}'::uuid
    RETURNING alias_fragment_id;
    INSERT INTO private_memory_migration_records(record_id,team_id,source_table,source_key,source_space_id,source_generation,
      original_membership_ids,destination_space_id,destination_generation,destination_key,destination_membership_id,supporting_copy,sealed,completed,projection_rebuilt)
    SELECT '${mapping}'::uuid,fragment.team_id,'evidence_fragments',jsonb_build_array(fragment.team_id::text,'${historical}'),space.id,space.generation,
      ARRAY[space.owner_membership_id],space.id,space.generation,jsonb_build_array(fragment.team_id::text,fragment.fragment_id::text),space.owner_membership_id,false,false,true,true
    FROM evidence_fragments fragment JOIN memory_spaces space ON space.team_id=fragment.team_id AND space.id=fragment.space_id
    WHERE fragment.team_id='${sqlLiteral(teamID)}'::uuid AND fragment.fragment_id='${sqlLiteral(canonical)}'::uuid AND fragment.owner_profile_id='${sqlLiteral(owner.profileID)}'::uuid
    RETURNING record_id;
  `, "historical-evidence-handles-fixture").trim().split(/\r?\n/);
  expect(JSON.stringify(fixture) === JSON.stringify([alias, alias, alias, mapping]), "historical fixtures must create every intended row");

  const exportArgs = { name: label, evidence_ids: [canonical, alias] };
  const pack = await mcpSuccessWithKey(owner.apiKey, "export_memory_pack", exportArgs);
  const artifact = JSON.parse(pack.artifact_json);
  expect(artifact.evidence.length === 1 && artifact.evidence[0].evidence_id === canonical, "authorized aliases must export one canonical Evidence item");
  expect(artifact.evidence[0].occurrences.length === 2, "canonical export must retain both historical occurrences");
  for (const [name, actor] of [["B", peer], ["C", foreign]]) {
    const deniedExport = await rawRPCWithKey(actor.apiKey, "tools/call", { name: "export_memory_pack", arguments: exportArgs });
    expect(deniedExport.result?.isError === true && !JSON.stringify(deniedExport.result).includes(canonical), `${name} must not export another owner's canonical or alias IDs`);
    const correction = singleItemArguments(`historical-denied-${name}`, "", "private");
    correction.evidence[0].supersedes_evidence_ids = [historical];
    const denied = await rememberWithKey(actor.apiKey, correction);
    assertStrictTerminalRemember(denied, expect);
    expect(denied.processing_state === "failed" && denied.evidence.every((item) => item.disposition === "not_stored"), `${name} must not supersede a mapped private Evidence target`);
  }
  const aliasRetraction = await mcpSuccessWithKey(owner.apiKey, "retract_evidence", { evidence_ids: [alias], reason: "Historical alias retraction", idempotency_key: `${label}-alias-retraction` });
  expect(aliasRetraction.processing_state === "completed", "owner must retract a historical exact alias");
  for (const evidenceID of [canonical, alias]) {
    const traced = await mcpSuccessWithKey(owner.apiKey, "trace_memory", { evidence_id: evidenceID });
    expect(traced.lifecycle.some((event) => event.target_evidence_id === alias && event.action === "retract"), "Trace must retain the historical alias retraction");
  }
  const retractedPack = JSON.parse((await mcpSuccessWithKey(owner.apiKey, "export_memory_pack", exportArgs)).artifact_json);
  expect(retractedPack.evidence.length === 1 && retractedPack.evidence[0].lifecycle.some((event) => event.target_evidence_id === alias && event.action === "retract"), "canonical export must retain alias lifecycle history");
  const excluded = await mcpSuccessWithKey(owner.apiKey, "trace_memory", { evidence_id: alias, include_transitions: false });
  expect(excluded.lifecycle.length === 0 && excluded.omissions.includes("lifecycle_excluded_by_request"), "transition exclusion must remain explicit for historical alias events");
  const boundedTrace = await mcpSuccessWithKey(owner.apiKey, "trace_memory", { evidence_id: alias, max_events: 1 });
  expect(["occurrences", "source_revisions", "decisions", "lifecycle", "search_documents"].reduce((count, field) => count + boundedTrace[field].length, 0) <= 1 && boundedTrace.truncated, "alias history must honor the total event budget");
  for (const [name, actor] of [["B", peer], ["C", foreign]]) {
    const deniedTrace = await rawRPCWithKey(actor.apiKey, "tools/call", { name: "trace_memory", arguments: { evidence_id: alias } });
    expect(deniedTrace.result?.isError === true && !JSON.stringify(deniedTrace.result).includes(alias), `${name} must not read private alias lifecycle history`);
  }
  const correction = singleItemArguments("historical-owner-replacement", "", "private");
  correction.evidence[0].supersedes_evidence_ids = [historical];
  const replaced = await rememberWithKey(owner.apiKey, correction);
  assertStrictTerminalRemember(replaced, expect);
  expect(replaced.processing_state === "completed" && JSON.stringify(replaced.evidence[0].superseded_evidence_ids) === JSON.stringify([canonical]), "owner supersession must resolve the historical handle to its authorized destination");
  const replay = await rememberWithKey(owner.apiKey, correction);
  expect(JSON.stringify(replay) === JSON.stringify(replaced), "historical-handle replay must preserve the original request identity and outcome");
  for (const fence of ["generation", "sealed", "purged"]) {
    const unavailable = randomUUID(), record = randomUUID();
    const targetID = replaced.evidence[0].evidence_id;
    const seeded = postgresExec(`
      INSERT INTO private_memory_migration_records(record_id,team_id,source_table,source_key,source_space_id,source_generation,
        original_membership_ids,destination_space_id,destination_generation,destination_key,destination_membership_id,supporting_copy,sealed,completed,projection_rebuilt,purged_at)
      SELECT '${record}'::uuid,fragment.team_id,'evidence_fragments',jsonb_build_array(fragment.team_id::text,'${unavailable}'),space.id,space.generation,
        ARRAY[space.owner_membership_id],space.id,space.generation+${fence === "generation" ? 1 : 0},jsonb_build_array(fragment.team_id::text,fragment.fragment_id::text),space.owner_membership_id,false,${fence === "sealed"},true,true,${fence === "purged" ? "now()" : "NULL"}
      FROM evidence_fragments fragment JOIN memory_spaces space ON space.team_id=fragment.team_id AND space.id=fragment.space_id
      WHERE fragment.team_id='${sqlLiteral(teamID)}'::uuid AND fragment.fragment_id='${sqlLiteral(targetID)}'::uuid AND fragment.owner_profile_id='${sqlLiteral(owner.profileID)}'::uuid
      RETURNING record_id;
    `, `historical-evidence-${fence}`).trim();
    expect(seeded === record, `${fence} fixture must insert its intended mapping`);
    const request = singleItemArguments(`historical-${fence}`, "", "private");
    request.evidence[0].supersedes_evidence_ids = [unavailable];
    const denied = await rememberWithKey(owner.apiKey, request);
    assertStrictTerminalRemember(denied, expect);
    expect(denied.processing_state === "failed" && denied.evidence.every((item) => item.disposition === "not_stored"), `${fence} mapping must never revive an unavailable historical handle`);
  }
  return { fault: "historical-evidence-handles", canonical_export: true, private_supersession: true, isolated_owners: ["A", "B", "C"], replay_preserved: true, generation_sealing_purge_fences: true };
}

function singleItemArguments(label, marker, targetSpace = "team_shared") {
  const content = `Dense-Mem stores durable memory in PostgreSQL. [fixture:${label}]${marker ? ` ${marker}` : ""}`;
  return {
    target_space: targetSpace,
    evidence: [{ content, source_type: "manual" }],
    idempotency_key: `synchronous-write-remember-${label}-${Date.now()}-${Math.random()}`,
  };
}

function multiEvidenceSecurityArguments(label) {
  const args = singleItemArguments(label, "[fixture-fault:security]");
  args.evidence.push({
    content: "A second safe evidence item must not be partially committed.",
    source_type: "manual",
  });
  return args;
}

function terminalPayload(result) {
  return result?.content?.[0]?.text ? JSON.parse(result.content[0].text) : result;
}

function assertStrictTerminalRemember(result, expect) {
  assertTerminalRememberResult(result);
  expect(result.contract_version === "dense-mem.v2.8.0", "terminal Remember must include contract_version");
  expect(typeof result.submission_id === "string" && result.submission_id.length > 0, "terminal Remember must include a submission id");
  expect(result.submission_kind === "remember", "terminal Remember must include remember submission_kind");
  expect(typeof result.correlation_id === "string" && result.correlation_id.length > 0, "terminal Remember must include correlation_id");
  expect(Array.isArray(result.errors), "terminal Remember must include errors array");
  if (Object.hasOwn(result, "warnings")) {
    expect(Array.isArray(result.warnings) && result.warnings.length <= 20 && result.warnings.every((warning) => typeof warning === "string" && warning.length <= 512), "terminal Remember warnings must be bounded");
  }
  for (const evidence of result.evidence) {
    expect(typeof evidence.disposition === "string" && Number.isInteger(evidence.evidence_index), "terminal evidence must have disposition and index");
    expect(Array.isArray(evidence.superseded_evidence_ids) && typeof evidence.search_state === "string", "terminal evidence must have supersession and search state");
  }
  for (const error of result.errors) {
    expect(typeof error.code === "string" && typeof error.message === "string" && typeof error.retryable === "boolean" && typeof error.next_action === "string" && typeof error.remediation === "string", "terminal error must have complete bounded guidance");
    if (error.code === "input_budget_exceeded") {
      expect(typeof error.reason_code === "string" && error.reason_code.length > 0 && error.details && typeof error.details === "object" && !Array.isArray(error.details), "input budget errors must identify their owning component");
      expect(typeof error.details.server_owned === "boolean" || typeof error.details.client_controlled === "boolean", "input budget errors must identify ownership");
    }
  }
}

function stableJSON(value) {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

let knownEvidenceRPCID = 0;

async function rawRPCWithKey(apiKey, method, params) {
  const baseURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
  const response = await fetch(`${baseURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: `known-evidence-${++knownEvidenceRPCID}`, method, params }),
  });
  const body = await response.text();
  if (!response.ok) throw new Error(`MCP ${method} request failed with HTTP ${response.status}`);
  return body ? JSON.parse(body) : {};
}

async function controlJSON(path, options = {}) {
  const result = await controlRaw(path, options);
  if (!result.response.ok) throw new Error(`control API ${path} returned HTTP ${result.response.status}`);
  return result.payload;
}

async function controlRaw(path, options = {}) {
  const baseURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
  const token = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
  const response = await fetch(`${baseURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const body = await response.text();
  return { response, payload: body ? JSON.parse(body) : {}, text: body };
}

function assertControlError(result, status, code, label, expect) {
  expect(result.response.status === status, `${label} returned HTTP ${result.response.status}; expected ${status}`);
  expect(result.payload.code === code, `${label} returned code ${String(result.payload.code)}; expected ${code}`);
  expect(typeof result.payload.message === "string" && result.payload.message.length > 0 && result.payload.message.length < 256, `${label} returned an unbounded error message`);
  if (status === 409) {
    expect(result.payload.reason_code === "state_conflict" && result.payload.next_action === "refresh_state" && result.payload.retryable === false &&
      typeof result.payload.remediation === "string" && result.payload.remediation.includes("Refresh"),
    `${label} must expose stale-state refresh guidance: ${JSON.stringify(result.payload)}`);
  }
}

export function postgresQuery(sql) {
  if (!/^(SELECT|WITH)\b/i.test(sql.trim())) {
    throw new Error("known-evidence PostgreSQL query helper only permits read queries");
  }
  return postgresExec(sql, "synchronous-write-remember-query").trim();
}

function rememberAdmissionLockCount() {
  // Provider slots can be fewer than admission slots, so observe the native Remember lock holders.
  return Number(postgresQuery(`
    SELECT count(*) FROM pg_locks AS held
    JOIN pg_stat_activity AS owner ON owner.pid = held.pid
    WHERE held.locktype = 'advisory' AND held.granted AND held.mode = 'ExclusiveLock'
      AND owner.datname = current_database()
      AND owner.query = 'SELECT pg_try_advisory_lock(hashtextextended($1, $2))';
  `));
}

export function rememberCanonicalCounts(teamID, submissionID) {
  return postgresQuery(`
    SELECT count(*) FROM knowledge_ingests
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND ingest_id = '${sqlLiteral(submissionID)}'::uuid
    UNION ALL SELECT count(*) FROM evidence_fragments
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND ingest_id = '${sqlLiteral(submissionID)}'::uuid
    UNION ALL SELECT count(*) FROM semantic_assessments
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND attempt_id = '${sqlLiteral(submissionID)}'::uuid
    UNION ALL SELECT count(*) FROM search_documents
    WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND source_id IN (
      SELECT fragment_id FROM evidence_fragments WHERE team_id = '${sqlLiteral(teamID)}'::uuid AND ingest_id = '${sqlLiteral(submissionID)}'::uuid
    );
  `).split(/\r?\n/).filter(Boolean).map(Number);
}

function postgresExec(sql, label) {
  const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
  const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
  const composeOverlay = requiredEnv("DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE");
  const scopedSQL = [
    "BEGIN",
    "SET LOCAL app.tx_mode = 'system'",
    "SET LOCAL app.current_team_id = ''",
    "SET LOCAL app.current_profile_id = ''",
    "SET LOCAL app.allowed_space_ids = ''",
    sql,
    "COMMIT",
  ].join(";\n");
  const composeArgs = ["compose", "-p", composeProject, "-f", composeFile];
  composeArgs.push("-f", composeOverlay);
  composeArgs.push(
    "exec", "-T", "postgres", "sh", "-ec",
    'psql -X -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    label,
    scopedSQL,
  );
  const result = spawnSync("docker", [
    ...composeArgs,
  ], { cwd: repositoryRoot, encoding: "utf8" });
  if (result.status !== 0) {
    throw new Error(`Remember PostgreSQL fixture failed (${result.status}): ${(result.stderr || "").trim()}`);
  }
  return result.stdout;
}

function sqlLiteral(value) {
  return String(value).replaceAll("'", "''");
}
