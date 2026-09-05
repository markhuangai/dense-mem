import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";

import { assertTerminalRememberResult } from "../surface.mjs";

export const name = "remember";

const repositoryRoot = fileURLToPath(new URL("../../../..", import.meta.url));

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
  ] : [selectedFault];

  const listed = await rpc("tools/list", {});
  const tools = listed.tools || [];
  const rememberTool = tools.find((tool) => tool.name === "remember");
  expect(rememberTool, "selected E2E catalog must expose remember");
  const rememberProperties = rememberTool.outputSchema?.properties || rememberTool.output_schema?.properties || {};
  expect(!Object.hasOwn(rememberProperties, "status_tool") && !Object.hasOwn(rememberProperties, "check_after_seconds"), "selected Remember schema must not expose polling fields");
  expect(!tools.some((tool) => tool.name === "get_submission_status"), "selected catalog must remove the status tool");

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
    } else {
      results.push(await runProviderFaultCase({ rpc, expect, fault }));
    }
  }

  if (selectedFault === "none") {
    results.push(await runKnownEvidenceCase({ expect }));
    results.push(await runSemanticDuplicateCase({ expect }));
    results.push(await runBudgetContextCase({ expect }));
    results.push(await runEvidenceConflictCase({ rpc, expect }));
    results.push(await runConcurrentWinnerCase({ rpc, expect }));
    results.push(await runChangedHashConflictCase({ rawRPC, expect }));
    results.push(await runSupersessionFenceCase({ rpc, expect }));
  }
  return { mode: name, results };
}

async function runBudgetContextCase({ expect }) {
  const teamID = await createKnownEvidenceTeam(`assessor-budget-${Date.now()}`);
  const actor = await createKnownEvidenceCredential(teamID, `assessor-budget-${Date.now()}`, "shared_only");
  const candidateContents = [];
  for (let index = 0; index < 10; index += 1) {
    const content = `Large stored candidate ${index} ${"candidate context material ".repeat(36)}`;
    candidateContents.push(content);
    const seeded = await rememberWithKey(actor.apiKey, {
      evidence: [{ content, source_type: "manual", force_insert: true }],
      relationships: [],
      idempotency_key: `assessor-budget-seed-${index}-${Date.now()}-${Math.random()}`,
    });
    assertStrictTerminalRemember(seeded, expect);
    expect(seeded.processing_state === "completed" && seeded.evidence[0]?.disposition === "stored", `large candidate seed ${index} must complete: ${JSON.stringify(seeded)}`);
  }

  const shortRequest = {
    evidence: [{ content: "Short note for bounded server-selected context.", source_type: "manual" }],
    relationships: [],
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
  const actor = await createKnownEvidenceCredential(teamID, `evidence-conflict-${Date.now()}`, "shared_only");
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
  similarityArgs.evidence.push({ content: "A similar but uncited statement about the same change.", source_type: "manual" });
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

async function runKnownEvidenceCase({ expect }) {
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const actorA = await createKnownEvidenceCredential(teamID, `known-evidence-${Date.now()}-A`, "shared_only");
  const actorAKey = actorA.apiKey;
  const actorB = await createKnownEvidenceCredential(teamID, `known-evidence-${Date.now()}-B`, "shared_only");
  const privateActor = await createKnownEvidenceCredential(teamID, `known-evidence-${Date.now()}-private`, "credential_private");
  const teamC = await createKnownEvidenceTeam(`known-evidence-${Date.now()}-C`);
  const actorC = await createKnownEvidenceCredential(teamC, `known-evidence-${Date.now()}-C`, "shared_only");

  const source = await rememberWithKey(actorAKey, singleItemArguments("known-evidence-source", "[fixture:known-evidence-source]"));
  assertStrictTerminalRemember(source, expect);
  expect(source.processing_state === "completed", `known evidence source must complete: ${JSON.stringify(source)}`);
  const knownEvidenceID = source.evidence.find((item) => item.disposition === "stored")?.evidence_id;
  expect(knownEvidenceID, "known evidence source must return a stored evidence id");
  const sourceRelationshipID = source.relationship_results[0]?.splits?.[0]?.relationship_id;
  expect(sourceRelationshipID, "known evidence source must return a relationship id");
  const sourceTrace = await mcpSuccessWithKey(actorAKey, "trace_memory", { relationship_id: sourceRelationshipID });
  const sourceOwnerProfileID = sourceTrace.relationship?.owner_profile_id;
  const subjectEntityID = sourceTrace.relationship?.subject_entity_id;
  expect(sourceOwnerProfileID && subjectEntityID, "known evidence source trace must expose owner and subject identity");
  postgresCommand(`
    INSERT INTO entity_names (
      team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind,
      space_id, space_generation
    )
    SELECT
      '${sqlLiteral(teamID)}'::uuid,
      '${sqlLiteral(subjectEntityID)}'::uuid,
      '${sqlLiteral(sourceOwnerProfileID)}'::uuid,
      'Dense Memory', 'dense memory', 'alias',
      relationship.space_id, relationship.space_generation
    FROM relationship_records AS relationship
    WHERE relationship.team_id = '${sqlLiteral(teamID)}'::uuid
      AND relationship.relationship_id = '${sqlLiteral(sourceRelationshipID)}'::uuid
    ON CONFLICT DO NOTHING;
  `);

  const canonicalGrounding = source.evidence.find((item) => item.evidence_id === knownEvidenceID);
  expect(canonicalGrounding, "canonical source evidence must be returned");
  expect(groundingSurfaceCount(teamID, source.submission_id, "Dense-Mem") >= 1, "canonical mention must be grounded at its exact span");

  const aliasArgs = singleItemArguments("known-evidence-alias", "[fixture:known-evidence-alias]");
  aliasArgs.evidence[0].content = "Dense Memory stores durable memory in PostgreSQL. [fixture:known-evidence-alias]";
  const alias = await rememberWithKey(actorB.apiKey, aliasArgs);
  assertStrictTerminalRemember(alias, expect);
  expect(alias.processing_state === "completed" && alias.relationship_results[0]?.disposition === "stored", `alias grounding must store the relationship: ${JSON.stringify(alias)}`);
  expect(groundingSurfaceCount(teamID, alias.submission_id, "Dense Memory") >= 1, "active alias must be grounded at its exact span");

  const anchoredArgs = singleItemArguments("known-evidence-anchored", "[fixture:known-evidence-anchored]");
  anchoredArgs.evidence[0].content = "It stores durable memory in PostgreSQL. [fixture:known-evidence-anchored]";
  anchoredArgs.relationships[0].known_evidence_ids = [knownEvidenceID];
  const anchored = await rememberWithKey(actorB.apiKey, anchoredArgs);
  assertStrictTerminalRemember(anchored, expect);
  expect(anchored.processing_state === "completed" && anchored.relationship_results[0]?.disposition === "stored", `anchored coreference must store the relationship: ${JSON.stringify(anchored)}`);
  expect(groundingSurfaceCount(teamID, anchored.submission_id, "It") >= 1, "anchored pronoun must be grounded at its exact span");

  const anchoredRelationshipID = anchored.relationship_results[0]?.splits?.[0]?.relationship_id;
  expect(anchoredRelationshipID, "anchored relationship must return a relationship id");
  const anchoredTrace = await mcpSuccessWithKey(actorB.apiKey, "trace_memory", { relationship_id: anchoredRelationshipID });
  const anchoredSupport = anchoredTrace.evidence_supports?.find((support) => support.evidence_id === anchored.evidence[0]?.evidence_id);
  expect(anchoredSupport, "anchored trace must expose submitted support");
  const crossOwner = postgresQuery(`
    SELECT count(*)
    FROM relationship_evidence_supports AS support
    JOIN relationship_observations AS observation
      ON observation.team_id = support.team_id
     AND observation.observation_id = support.observation_id
    WHERE observation.team_id = '${sqlLiteral(teamID)}'::uuid
      AND observation.ingest_id = '${sqlLiteral(anchored.submission_id)}'::uuid
      AND support.fragment_id = '${sqlLiteral(knownEvidenceID)}'::uuid
      AND support.owner_profile_id <> support.evidence_owner_profile_id;
  `);
  expect(Number(crossOwner) === 1, "known evidence support must retain distinct relationship and evidence owners");

  const correctionArguments = {
    action: "submit",
    relationship_id: anchoredRelationshipID,
    expected_version: anchoredTrace.relationship?.version,
    patch: { object_entity: { name: "unauthorized known-evidence successor", entity_kind: "project" } },
    supports: [{ evidence_id: anchoredSupport.evidence_id, start: anchoredSupport.span_start, end: anchoredSupport.span_end }],
    reason: "Known-evidence ownership must not grant relationship mutation authority.",
  };
  for (const [label, apiKey] of [["same-team evidence owner", actorAKey], ["separate-team actor", actorC.apiKey]]) {
    const denied = await rawRPCWithKey(apiKey, "tools/call", {
      name: "correct_relationship",
      arguments: { ...correctionArguments, idempotency_key: `known-evidence-${label}-${Date.now()}-${Math.random()}` },
    });
    assertMutationDenied(denied, anchoredRelationshipID, expect, label);
  }

  const randomUnavailable = await assertUnavailableKnownEvidence(actorB.apiKey, "known-evidence-random-unavailable", randomUUID(), expect);
  const privateSource = await rememberWithKey(privateActor.apiKey, singleItemArguments("known-evidence-private-source", "[fixture:known-evidence-private-source]"));
  assertStrictTerminalRemember(privateSource, expect);
  const privateEvidenceID = privateSource.evidence.find((item) => item.disposition === "stored")?.evidence_id;
  expect(privateEvidenceID, "private known evidence source must return a stored evidence id");
  const privateUnavailable = await assertUnavailableKnownEvidence(actorB.apiKey, "known-evidence-private-unavailable", privateEvidenceID, expect);
  const foreignSource = await rememberWithKey(actorC.apiKey, singleItemArguments("known-evidence-foreign-source", "[fixture:known-evidence-foreign-source]"));
  assertStrictTerminalRemember(foreignSource, expect);
  const foreignEvidenceID = foreignSource.evidence.find((item) => item.disposition === "stored")?.evidence_id;
  expect(foreignEvidenceID, "foreign known evidence source must return a stored evidence id");
  const foreignUnavailable = await assertUnavailableKnownEvidence(actorB.apiKey, "known-evidence-foreign-unavailable", foreignEvidenceID, expect);

  const twentyIDs = Array.from({ length: 20 }, () => randomUUID());
  const twentyArgs = singleItemArguments("known-evidence-twenty", "[fixture:known-evidence-twenty]");
  twentyArgs.relationships[0].known_evidence_ids = twentyIDs;
  const twenty = await rememberWithKey(actorB.apiKey, twentyArgs);
  assertStrictTerminalRemember(twenty, expect);
  expect(twenty.processing_state === "completed" && twenty.relationship_results[0]?.disposition === "not_stored", `20 known evidence IDs must be accepted and remain unsupported when unavailable: ${JSON.stringify(twenty)}`);

  const twentyOneArgs = singleItemArguments("known-evidence-twenty-one", "[fixture:known-evidence-twenty-one]");
  twentyOneArgs.relationships[0].known_evidence_ids = [...twentyIDs, randomUUID()];
  const twentyOne = await rawRPCWithKey(actorB.apiKey, "tools/call", { name: "remember", arguments: twentyOneArgs });
  expect(twentyOne.error?.code === -32602, "21 known evidence IDs must fail contract validation");
  const twentyOneIssues = twentyOne.error?.data?.issues ?? [];
  expect(twentyOneIssues.some((issue) => issue.message.includes("exceeds maximum item count of 20")), "21-ID validation must report the maximum item count");
  expect(twentyOneIssues.some((issue) => issue.path.includes("known_evidence_ids")), "21-ID validation must identify the known_evidence_ids bound");

  const unreferencedArgs = {
    evidence: [{
      content: "Dense-Mem stores durable memory in PostgreSQL. Unreferenced Candidate stores durable memory in PostgreSQL. [fixture-fault:unreferenced-known]",
      source_type: "manual",
    }],
    relationships: [
      relationship("known-evidence-allowlisted", "Dense-Mem", "project", { value: { type: "string", value: "PostgreSQL" } }, [0]),
      relationship("known-evidence-unreferenced", "Unreferenced Candidate", "project", { value: { type: "string", value: "PostgreSQL" } }, [0]),
    ],
    idempotency_key: `synchronous-write-remember-known-evidence-unreferenced-${Date.now()}-${Math.random()}`,
  };
  unreferencedArgs.relationships[0].known_evidence_ids = [knownEvidenceID];
  const unreferencedRaw = await rawRPCWithKey(actorB.apiKey, "tools/call", { name: "remember", arguments: unreferencedArgs });
  const unreferenced = terminalPayload(unreferencedRaw.result);
  assertStrictTerminalRemember(unreferenced, expect);
  expect(unreferenced.processing_state === "failed" && unreferenced.errors[0]?.code === "provider_response_invalid", "unreferenced known evidence support must be rejected by the closed contract");
  expect(unreferenced.evidence.every((item) => item.disposition === "not_stored"), "unreferenced known evidence rejection must not commit submitted evidence");

  for (const [label, content, marker] of [
    ["unanchored-pronoun", "It stores durable memory in PostgreSQL.", "unanchored-pronoun"],
    ["ambiguous-pronoun", "Dense-Mem and Dense Memory are mentioned. It stores durable memory in PostgreSQL.", "ambiguous-pronoun"],
  ]) {
    const pronounArgs = singleItemArguments(`known-evidence-${label}`, `[fixture-fault:${marker}]`);
    pronounArgs.evidence[0].content = `${content} [fixture-fault:${marker}]`;
    const pronoun = await rememberWithKey(actorB.apiKey, pronounArgs);
    assertStrictTerminalRemember(pronoun, expect);
    expect(pronoun.processing_state === "completed", `${label} must complete with a warning: ${JSON.stringify(pronoun)}`);
    expect(pronoun.relationship_results[0]?.disposition === "not_stored" && pronoun.relationship_results[0]?.splits.length === 0, `${label} must not create relationship support`);
    expect(pronoun.evidence.every((item) => item.disposition === "stored"), `${label} must still store submitted evidence`);
    expect(groundingSurfaceCount(teamID, pronoun.submission_id, "It") === 0, `${label} must not persist a pronoun entity grounding`);
  }

  return {
    fault: "known-evidence",
    processing_state: anchored.processing_state,
    canonical_grounding: true,
    alias_grounding: true,
    anchored_coreference: true,
    cross_owner_support: true,
    mutation_isolation: true,
    unavailable_ids: [randomUnavailable, privateUnavailable, foreignUnavailable],
    id_boundaries: true,
    unreferenced_candidate_rejected: true,
    unanchored_and_ambiguous_rejected: true,
  };
}

async function runSemanticDuplicateCase({ expect }) {
  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const actor = await createKnownEvidenceCredential(teamID, `semantic-duplicate-${Date.now()}`, "credential_private");
  const canonicalArgs = singleItemArguments("semantic-duplicate-canonical", "");
  canonicalArgs.evidence[0].content = "Dense-Mem stores durable memory in PostgreSQL. [fixture:semantic-duplicate-canonical]";
  const canonical = await rememberWithKey(actor.apiKey, canonicalArgs);
  assertStrictTerminalRemember(canonical, expect);
  expect(canonical.processing_state === "completed", `semantic duplicate canonical write must complete: ${JSON.stringify(canonical)}`);
  const canonicalID = canonical.evidence[0]?.evidence_id;
  expect(canonicalID, "semantic duplicate canonical write must return an evidence id");

  const forcedArgs = singleItemArguments("semantic-duplicate-force", "[fixture-fault:semantic-reuse]");
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

  const exactForcedArgs = singleItemArguments("semantic-duplicate-force-exact", "");
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

  const reusedArgs = singleItemArguments("semantic-duplicate-reuse", "[fixture-fault:semantic-reuse]");
  reusedArgs.evidence[0].content = "Dense-Mem retains durable memory in PostgreSQL. [fixture:semantic-duplicate-canonical] [fixture-fault:semantic-reuse]";
  const reused = await rememberWithKey(actor.apiKey, reusedArgs);
  assertStrictTerminalRemember(reused, expect);
  expect(reused.processing_state === "completed", `semantic duplicate reuse must complete: ${JSON.stringify(reused)}`);
  expect(reused.evidence[0]?.evidence_id === canonicalID, `semantic duplicate reuse must return the canonical evidence id: ${JSON.stringify(reused)}`);
  expect(reused.search_state === "current", `semantic duplicate reuse must report current batch search state: ${JSON.stringify(reused)}`);
  expect(reused.evidence[0]?.search_state === "current", `semantic duplicate reuse must report current evidence search state: ${JSON.stringify(reused)}`);
  expect(reused.relationship_results[0]?.disposition === "stored", `semantic duplicate reuse must preserve relationship storage: ${JSON.stringify(reused)}`);

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

  const relationshipID = reused.relationship_results[0]?.splits?.[0]?.relationship_id;
  expect(relationshipID, "semantic duplicate reuse must return a relationship id");
  const trace = await mcpSuccessWithKey(actor.apiKey, "trace_memory", { relationship_id: relationshipID });
  const support = trace.evidence_supports?.find((item) => item.evidence_id === canonicalID && item.occurrence_id && item.quote?.includes("retains durable memory"));
  expect(support, `semantic duplicate trace must expose occurrence provenance: ${JSON.stringify(trace)}`);
  const traceEvidence = trace.evidence?.find((item) => item.occurrence_id === support.occurrence_id);
  expect(traceEvidence, `semantic duplicate trace must hydrate the supporting occurrence: ${JSON.stringify(trace)}`);
  expect(traceEvidence.evidence_id === canonicalID && traceEvidence.content.includes("retains durable memory"), `semantic duplicate trace must pair canonical identity with occurrence content: ${JSON.stringify({ support, traceEvidence })}`);
  expect(support.quote === traceEvidence.content, `semantic duplicate trace quote must match occurrence content: ${JSON.stringify({ support, traceEvidence })}`);

  const unauthorizedArgs = singleItemArguments("semantic-duplicate-unauthorized", "[fixture-fault:semantic-reuse-unauthorized]");
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

async function assertUnavailableKnownEvidence(apiKey, label, evidenceID, expect) {
  const args = singleItemArguments(label, `[fixture:${label}]`);
  args.relationships[0].known_evidence_ids = [evidenceID];
  const result = await rememberWithKey(apiKey, args);
  assertStrictTerminalRemember(result, expect);
  expect(result.processing_state === "completed", `${label} must complete with a warning: ${JSON.stringify(result)}`);
  expect(result.relationship_results[0]?.disposition === "not_stored" && result.relationship_results[0]?.splits.length === 0, `${label} must not store a relationship`);
  expect(result.evidence.every((item) => item.disposition === "stored"), `${label} must store submitted evidence`);
  return { label, state: result.processing_state };
}

function assertMutationDenied(raw, relationshipID, expect, label) {
  expect(!raw.error && raw.result?.isError === true, `${label} mutation must return a structured denial`);
  const denied = terminalPayload(raw.result);
  expect(denied.processing_state === "failed", `${label} mutation must fail`);
  expect(denied.errors?.[0]?.code === "entity_not_found", `${label} mutation must use bounded not-found classification: ${JSON.stringify(denied)}`);
  expect(!JSON.stringify(denied).includes(relationshipID), `${label} mutation must not disclose the relationship ID`);
}

async function createKnownEvidenceTeam(label) {
  const response = await controlJSON("/teams", {
    method: "POST",
    body: JSON.stringify({ name: label, description: "known evidence isolation e2e" }),
  });
  const teamID = String(response.data?.id || "");
  if (!teamID) throw new Error("control API did not return a team ID for known-evidence isolation");
  return teamID;
}

async function createKnownEvidenceCredential(teamID, name, memoryBinding) {
  const response = await controlJSON(`/teams/${teamID}/credentials`, {
    method: "POST",
    body: JSON.stringify({ name, role: "member", scopes: ["read", "write"], rate_limit: 300, memory_binding: memoryBinding }),
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
  const assessorTurns = Number(postgresQuery(`
    SELECT assessor_turns
    FROM remember_attempts
    WHERE team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
      AND attempt_id = '${sqlLiteral(result.submission_id)}'::uuid
  `));
  const expectedTurns = fault === "repair" ? 2 : 3;
  expect(assessorTurns === expectedTurns, `${fault} must retain ${expectedTurns} assessor turns`);
  return { fault, processing_state: result.processing_state, assessor_turns: assessorTurns };
}

async function runTerminalDomainCase({ rpc, expect, fault }) {
  const args = fault === "security"
    ? multiEvidenceSecurityArguments(`domain-${fault}`)
    : singleItemArguments(`domain-${fault}`, `[fixture-fault:${fault}]`);
  const result = terminalPayload(await rpc("tools/call", { name: "remember", arguments: args }));
  assertStrictTerminalRemember(result, expect);
  const expectedState = fault === "security" ? "failed" : "completed";
  const expectedCode = fault === "security" ? "submission_policy_rejected" : "";
  expect(result.processing_state === expectedState, `${fault} must return ${expectedState}`);
  if (fault === "security") {
    expect(result.search_state === "not_required", `${fault} must not require search`);
    expect(result.errors[0]?.code === expectedCode, `${fault} must return ${expectedCode}: ${JSON.stringify(result)}`);
    expect(result.evidence.length === 2, `${fault} must return every submitted evidence disposition`);
    expect(result.evidence.every((item) => item.disposition === "not_stored"), `${fault} must not store evidence`);
    expect(result.relationship_results.every((item) => item.disposition === "not_stored" && item.splits.length === 0), `${fault} must not store relationships`);
    const zeroWriteCounts = postgresQuery(`
      SELECT count(*) FROM knowledge_ingests
      WHERE team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
        AND ingest_id = '${sqlLiteral(result.submission_id)}'::uuid
      UNION ALL
      SELECT count(*) FROM evidence_fragments
      WHERE team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
        AND ingest_id = '${sqlLiteral(result.submission_id)}'::uuid
      UNION ALL
      SELECT count(*) FROM semantic_assessments
      WHERE team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
        AND attempt_id = '${sqlLiteral(result.submission_id)}'::uuid
      UNION ALL
      SELECT count(*) FROM relationship_observations
      WHERE team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
        AND ingest_id = '${sqlLiteral(result.submission_id)}'::uuid
      UNION ALL
      SELECT count(*)
      FROM search_documents AS document
      WHERE document.team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
        AND document.source_id IN (
          SELECT fragment_id FROM evidence_fragments
          WHERE team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
            AND ingest_id = '${sqlLiteral(result.submission_id)}'::uuid
          UNION ALL
          SELECT observation.relationship_id
          FROM relationship_observations AS observation
          WHERE observation.team_id = '${sqlLiteral(requiredEnv("DENSE_MEM_E2E_TEAM_ID"))}'::uuid
            AND observation.ingest_id = '${sqlLiteral(result.submission_id)}'::uuid
        );
    `).split(/\r?\n/).filter(Boolean).map(Number);
    expect(zeroWriteCounts.length === 5 && zeroWriteCounts.every((count) => count === 0), `${fault} must not create semantic or search rows: ${zeroWriteCounts.join(",")}`);
  } else {
    expect(result.search_state === "current", `${fault} must index safe evidence`);
    expect(result.errors.length === 0, `${fault} must not return a batch error`);
    expect(result.evidence.every((item) => item.disposition === "stored"), `${fault} must store safe evidence`);
    expect(result.relationship_results.every((item) => item.disposition === "not_stored" && item.splits.length === 0), `${fault} must return unsupported relationship warnings`);
  }
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
  const conflict = response.result?.structuredContent;
  expect(response.result?.isError === true && conflict?.errors?.[0]?.code === "idempotency_conflict", "changed request hash must return a structured public conflict");
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

function singleItemArguments(label, marker) {
  const content = `Dense-Mem stores durable memory in PostgreSQL. [fixture:${label}]${marker ? ` ${marker}` : ""}`;
  return {
    evidence: [{ content, source_type: "manual" }],
    relationships: [relationship("durable-store", "Dense-Mem", "project", { value: { type: "string", value: "PostgreSQL" } }, [0])],
    idempotency_key: `synchronous-write-remember-${label}-${Date.now()}-${Math.random()}`,
  };
}

function multiEvidenceSecurityArguments(label) {
  const args = singleItemArguments(label, "[fixture-fault:security]");
  args.evidence.push({
    content: "A second safe evidence item must not be partially committed.",
    source_type: "manual",
  });
  args.relationships[0].evidence_indices = [0, 1];
  return args;
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
  if (Object.hasOwn(result, "warnings")) {
    expect(Array.isArray(result.warnings) && result.warnings.length <= 20 && result.warnings.every((warning) => typeof warning === "string" && warning.length <= 512), "terminal Remember warnings must be bounded");
  }
  for (const evidence of result.evidence) {
    expect(typeof evidence.disposition === "string" && Number.isInteger(evidence.evidence_index), "terminal evidence must have disposition and index");
    expect(Array.isArray(evidence.superseded_evidence_ids) && typeof evidence.search_state === "string", "terminal evidence must have supersession and search state");
  }
  for (const relationship of result.relationship_results) {
    expect(typeof relationship.ref === "string" && typeof relationship.disposition === "string" && Array.isArray(relationship.splits), "terminal relationship result must have ref, disposition, and splits");
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
}

function groundingSurfaceCount(teamID, submissionID, surface) {
  return Number(postgresQuery(`
    SELECT count(*)
    FROM entity_resolution_events AS event
    JOIN evidence_fragments AS fragment
      ON fragment.team_id = event.team_id
     AND fragment.fragment_id = event.fragment_id
    WHERE event.team_id = '${sqlLiteral(teamID)}'::uuid
      AND event.ingest_id = '${sqlLiteral(submissionID)}'::uuid
      AND event.span_start IS NOT NULL
      AND event.span_end IS NOT NULL
      AND substring(fragment.content FROM event.span_start + 1 FOR event.span_end - event.span_start) = '${sqlLiteral(surface)}';
  `));
}

function postgresQuery(sql) {
  if (!/^(SELECT|WITH)\b/i.test(sql.trim())) {
    throw new Error("known-evidence PostgreSQL query helper only permits read queries");
  }
  return postgresExec(sql, "synchronous-write-remember-query").trim();
}

function postgresCommand(sql) {
  if (!/^INSERT\s+INTO\s+entity_names\b/i.test(sql.trim())) {
    throw new Error("known-evidence PostgreSQL mutation helper only permits alias setup");
  }
  postgresExec(sql, "synchronous-write-remember-command");
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
