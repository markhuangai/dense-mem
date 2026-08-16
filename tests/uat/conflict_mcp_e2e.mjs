#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seededTeamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const reviewDriver = requiredEnv("DENSE_MEM_E2E_CONFLICT_REVIEW_DRIVER");
const submissionTimeoutEnv = process.env.DENSE_MEM_E2E_SUBMISSION_TIMEOUT_SECONDS
  ? "DENSE_MEM_E2E_SUBMISSION_TIMEOUT_SECONDS"
  : "DENSE_MEM_E2E_PLACEMENT_TIMEOUT_SECONDS";
const submissionTimeoutSeconds = positiveIntEnv(submissionTimeoutEnv, 180, 30, 900);
const runID = `conflict-e2e-${Date.now()}`;

let rpcID = 0;

const early = await earlyQuorumScenario(seededTeamID);
await assertRemovedPlacementTools(early.profileA.apiKey);
const authoritative = await dueUniqueAuthoritativeScenario();
const majority = await dueMajorityScenario();
const selected = await aiSelectionScenario();
const abstained = await aiAbstentionScenario();
const failedDays = await fiveFailedDaysScenario();
const provenance = await provenanceAndIsolationScenario();

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  supporter_majority_after_ttl: early.summary,
  authority_does_not_veto_majority: authoritative,
  due_majority: majority,
  ai_selection: selected,
  ai_abstention_last_write_wins: abstained,
  five_failed_days_last_write_wins: failedDays,
  supporter_provenance: provenance,
  removed_placement_tools_absent: true,
}, null, 2));

async function earlyQuorumScenario(teamID) {
  const fixture = await createConflictFixture("early-quorum", {
    teamID,
    authorityA: "primary",
    authorityB: "secondary",
  });
  for (let index = 2; index <= 5; index += 1) {
    const profile = await createCredential(fixture.teamID, `${runID} early-quorum supporter ${index}`);
    await submitPositionSupport(fixture, profile, {
      label: `early-quorum-a-${index}`,
      sourceGroup: fixture.sourceGroupA,
      authority: "primary",
    });
  }
  const open = await currentConflict(fixture.profileA.apiKey, fixture.relationshipA, "open");
  const winner = positionForRelationship(open, fixture.relationshipA);
  assert(winner.supporter_count === 5, `supporter majority count = ${winner.supporter_count}`);
  // Review runs are one-per-local-day; use the prior day for the pre-TTL probe.
  const beforeTTL = runReview(fixture, offsetTime(open.review_due_at, -86_460_000));
  assertReview(beforeTTL, "no_op", "waiting_for_review_due", "", "");
  const result = runReview(fixture, offsetTime(open.review_due_at, 1_000));
  assertReview(result, "resolve", "due_supporter_majority", fixture.positionAID, "");
  const state = conflictState(fixture.teamID, fixture.conflictID);
  assert(state.status === "resolved" && state.resolution_reason === "due_supporter_majority", `supporter majority lineage is invalid: ${JSON.stringify(state)}`);
  return {
    profileA: fixture.profileA,
    summary: { conflict_id: fixture.conflictID, preferred_position_id: fixture.positionAID, stage: result.stage },
  };
}

async function dueUniqueAuthoritativeScenario() {
  const fixture = await createConflictFixture("due-authoritative", {
    authorityA: "authoritative",
    authorityB: "primary",
    validFromA: "2026-08-01T00:00:00Z",
    validFromB: "2026-08-01T00:00:00Z",
  });
  for (let index = 2; index <= 3; index += 1) {
    const profile = await createCredential(fixture.teamID, `${runID} majority supporter ${index}`);
    await submitPositionSupport(fixture, profile, {
      label: `due-authoritative-b-${index}`,
      sourceGroup: fixture.sourceGroupB,
      authority: "primary",
      position: "b",
    });
  }
  const result = runReview(fixture, offsetTime(fixture.reviewDueAt, 1_000));
  assertReview(result, "resolve", "due_supporter_majority", fixture.positionBID, "");
  const state = conflictState(fixture.teamID, fixture.conflictID);
  assert(state.status === "resolved" && state.resolution_reason === "due_supporter_majority", `authority vetoed supporter majority: ${JSON.stringify(state)}`);
  return { conflict_id: fixture.conflictID, preferred_position_id: fixture.positionBID, stage: result.stage };
}

async function dueMajorityScenario() {
  const fixture = await createConflictFixture("due-majority", {
    authorityA: "primary",
    authorityB: "secondary",
  });
  const secondSupporter = await createCredential(fixture.teamID, `${runID} due-majority supporter`);
  await submitPositionSupport(fixture, secondSupporter, {
    label: "due-majority-a-2",
    sourceGroup: fixture.sourceGroupA,
    authority: "primary",
  });
  const open = await currentConflict(fixture.profileA.apiKey, fixture.relationshipA, "open");
  const winner = positionForRelationship(open, fixture.relationshipA);
  assert(winner.supporter_count === 2, `due majority supporter count = ${winner.supporter_count}`);
  const result = runReview(fixture, offsetTime(open.review_due_at, 1_000));
  assertReview(result, "resolve", "due_supporter_majority", fixture.positionAID, "");
  const state = conflictState(fixture.teamID, fixture.conflictID);
  assert(state.status === "resolved" && state.resolution_reason === "due_supporter_majority", `majority lineage is invalid: ${JSON.stringify(state)}`);
  return { conflict_id: fixture.conflictID, preferred_position_id: fixture.positionAID, stage: result.stage };
}

async function aiSelectionScenario() {
  const fixture = await createConflictFixture("ai-selection", {
    authorityA: "primary",
    authorityB: "primary",
    markerA: "[conflict-ai-select-winner]",
  });
  const result = runReview(fixture, offsetTime(fixture.reviewDueAt, 1_000));
  assertReview(result, "resolve", "overdue_ai", fixture.positionAID, "ai");
  const attempts = assessmentAttempts(fixture.teamID, fixture.conflictID);
  assert(attempts.length === 1 && attempts[0].status === "selected" && attempts[0].selected_position_id === fixture.positionAID, `AI selection attempt lineage is invalid: ${JSON.stringify(attempts)}`);
  const plan = latestResolutionPlan(fixture.teamID, fixture.conflictID);
  assert(plan.method === "ai" && plan.status === "applied" && plan.preferred_position_id === fixture.positionAID, `AI selection plan is invalid: ${JSON.stringify(plan)}`);
  assert(conflictDerivationCount(fixture.teamID, fixture.conflictID) >= 1, "AI selection did not preserve retraction derivation lineage");
  return { conflict_id: fixture.conflictID, preferred_position_id: fixture.positionAID, method: plan.method };
}

async function aiAbstentionScenario() {
  const fixture = await createConflictFixture("ai-abstention", {
    authorityA: "authoritative",
    authorityB: "secondary",
    markerA: "[conflict-ai-abstain]",
  });
  const accepted = positionAcceptedTimes(fixture.teamID, fixture.conflictID, fixture.positionAID, fixture.positionBID);
  assert(Date.parse(accepted.position_b) > Date.parse(accepted.position_a), `abstention fixture does not make the lower-authority position newer: ${JSON.stringify(accepted)}`);
  const result = runReview(fixture, offsetTime(fixture.reviewDueAt, 1_000));
  assertReview(result, "resolve", "overdue_last_write_wins", fixture.positionAID, "last_write_wins");
  const attempts = assessmentAttempts(fixture.teamID, fixture.conflictID);
  assert(attempts.length === 1 && attempts[0].status === "abstained", `abstention attempt lineage is invalid: ${JSON.stringify(attempts)}`);
  const plan = latestResolutionPlan(fixture.teamID, fixture.conflictID);
  assert(plan.method === "last_write_wins" && plan.status === "applied" && plan.preferred_position_id === fixture.positionAID, `abstention fallback plan is invalid: ${JSON.stringify(plan)}`);
  return { conflict_id: fixture.conflictID, preferred_position_id: fixture.positionAID, method: plan.method, authority_preceded_recency: true };
}

async function fiveFailedDaysScenario() {
  const fixture = await createConflictFixture("five-failed-days", {
    authorityA: "primary",
    authorityB: "secondary",
    markerA: "[conflict-ai-fail]",
  });
  const firstReviewAt = Date.parse(fixture.reviewDueAt) + 1_000;
  let finalResult;
  for (let day = 0; day < 5; day += 1) {
    finalResult = runReview(fixture, new Date(firstReviewAt + day * 25 * 60 * 60 * 1_000).toISOString());
    const attempts = assessmentAttempts(fixture.teamID, fixture.conflictID);
    assert(attempts.length === day + 1 && attempts.every((attempt) => attempt.status === "failed" && attempt.failure_class), `failed assessment day ${day + 1} lineage is invalid: ${JSON.stringify(attempts)}`);
    const state = conflictState(fixture.teamID, fixture.conflictID);
    if (day < 4) {
      assert(finalResult.outcome === "overdue" && finalResult.stage === "overdue_assessment_failed", `failed assessment day ${day + 1} resolved too early: ${JSON.stringify(finalResult)}`);
      assert(state.status === "overdue" && Object.keys(latestResolutionPlan(fixture.teamID, fixture.conflictID)).length === 0, `failed assessment day ${day + 1} changed terminal state: ${JSON.stringify(state)}`);
    }
  }
  assertReview(finalResult, "resolve", "overdue_last_write_wins", fixture.positionAID, "last_write_wins");
  const plan = latestResolutionPlan(fixture.teamID, fixture.conflictID);
  assert(plan.method === "last_write_wins" && plan.status === "applied", `fifth-day fallback plan is invalid: ${JSON.stringify(plan)}`);
  return { conflict_id: fixture.conflictID, failed_assessment_days: 5, preferred_position_id: fixture.positionAID, method: plan.method };
}

async function provenanceAndIsolationScenario() {
  const fixture = await createConflictFixture("supporter-provenance", {
    authorityA: "authoritative",
    authorityB: "secondary",
  });
  const profileC = await createCredential(fixture.teamID, `${runID} provenance C`);
  await submitPositionSupport(fixture, profileC, {
    label: "provenance-copied-c",
    sourceGroup: fixture.sourceGroupA,
    authority: "primary",
  });
  for (let index = 1; index <= 19; index += 1) {
    const profile = await createCredential(fixture.teamID, `${runID} provenance copied ${index}`);
    await submitPositionSupport(fixture, profile, {
      label: `provenance-copied-${index}`,
      sourceGroup: fixture.sourceGroupA,
      authority: "primary",
    });
  }

  const beforeIndependent = await currentConflict(profileC.apiKey, fixture.relationshipA, "open");
  const copiedPosition = positionForRelationship(beforeIndependent, fixture.relationshipA);
  assert(copiedPosition.supporter_count === 21, `copied supporter count = ${copiedPosition.supporter_count}`);
  const observedVersion = Number(beforeIndependent.version);
  // Reusing this observed version must succeed once and be rejected after that commit advances the case.
  await submitPositionSupport(fixture, profileC, {
    label: "provenance-independent-c",
    sourceGroup: `${runID}:provenance:independent:c`,
    authority: "primary",
    conflictContext: { conflict_id: fixture.conflictID, expected_version: observedVersion },
  });

  const afterIndependent = await currentConflict(profileC.apiKey, fixture.relationshipA, "open");
  const independentPosition = positionForRelationship(afterIndependent, fixture.relationshipA);
  assert(independentPosition.supporter_count === 21, `same profile gained an extra vote: ${JSON.stringify(independentPosition)}`);
  assert(Number(afterIndependent.version) > observedVersion, "fresh conflict_context support did not advance the conflict version");

  const semanticBeforeStale = conflictSemanticSnapshot(fixture.teamID, fixture.conflictID);
  const stale = await submitPositionSupport(fixture, profileC, {
    label: "provenance-stale-c",
    sourceGroup: `${runID}:provenance:stale:c`,
    authority: "primary",
    conflictContext: { conflict_id: fixture.conflictID, expected_version: observedVersion },
    expectedState: "rejected",
  });
  assert(stale.status.processing_state === "rejected", `stale conflict submission was not rejected: ${JSON.stringify(stale.status)}`);
  const semanticAfterStale = conflictSemanticSnapshot(fixture.teamID, fixture.conflictID);
  assert(stableJSON(semanticAfterStale) === stableJSON(semanticBeforeStale), `stale conflict submission changed semantic state: before=${JSON.stringify(semanticBeforeStale)} after=${JSON.stringify(semanticAfterStale)}`);

  const renamedA = `${runID} provenance A renamed`;
  await renameCredential(fixture.teamID, fixture.profileA.profileID, renamedA);
  const traceA = await mcpSuccess(profileC.apiKey, "trace_memory", {
    relationship_id: fixture.relationshipA,
    include_transitions: true,
  });
  const traceB = await mcpSuccess(profileC.apiKey, "trace_memory", { relationship_id: fixture.relationshipB });
  assert(stringAt(traceA, ["relationship", "relationship_id"]) === fixture.relationshipA, "same-team profile C could not trace profile A");
  assert(stringAt(traceB, ["relationship", "relationship_id"]) === fixture.relationshipB, "same-team profile C could not trace profile B");
  const recall = await waitForConflictRecall(profileC.apiKey, fixture.subjectName, fixture.conflictID);
  const traceConflict = conflictByID(traceA.conflicts, fixture.conflictID);
  const recallConflict = conflictByID(recall.conflicts, fixture.conflictID);
  const tracePosition = positionByID(traceConflict, fixture.positionAID);
  const recallPosition = positionByID(recallConflict, fixture.positionAID);
  assert(stableJSON(provenanceFields(tracePosition)) === stableJSON(provenanceFields(recallPosition)), `recall/trace supporter provenance differs: trace=${JSON.stringify(tracePosition)} recall=${JSON.stringify(recallPosition)}`);
  assert(tracePosition.supporter_count === 21 && tracePosition.supporters.length === 20 && tracePosition.supporters_truncated === true, `supporter bound is invalid: ${JSON.stringify(tracePosition)}`);
  const supporterA = tracePosition.supporters.find((supporter) => supporter.profile_id === fixture.profileA.profileID);
  const supporterC = tracePosition.supporters.find((supporter) => supporter.profile_id === profileC.profileID);
  assert(supporterA?.profile_name === renamedA && supporterA.strongest_authority === "authoritative", `current profile rename was not hydrated: ${JSON.stringify(supporterA)}`);
  assert(supporterC?.profile_id === profileC.profileID, `supporter identity was not projected: ${JSON.stringify(supporterC)}`);

  const nonOwnerRetraction = await mcpRaw(profileC.apiKey, "retract_evidence", {
    evidence_ids: [fixture.evidenceA],
    reason: "Conflict e2e verifies owner-only mutation.",
    idempotency_key: `${runID}:provenance:non-owner-retract`,
  });
  assert(nonOwnerRetraction.error && nonOwnerRetraction.result === undefined && !JSON.stringify(nonOwnerRetraction).includes(fixture.evidenceA), "same-team non-owner retraction was allowed or leaked the evidence ID");

  const foreignTeamID = await createTeam("foreign-isolation");
  const foreign = await createCredential(foreignTeamID, `${runID} foreign reader`);
  const foreignRecall = await mcpSuccess(foreign.apiKey, "recall_memory", { query: fixture.subjectName, limit: 10 });
  assert(!(foreignRecall.conflicts ?? []).some((conflict) => conflict.conflict_id === fixture.conflictID), "separate-team recall disclosed the conflict");
  const foreignTrace = await mcpRaw(foreign.apiKey, "trace_memory", { relationship_id: fixture.relationshipA });
  assert(foreignTrace.error && foreignTrace.result === undefined && !JSON.stringify(foreignTrace).includes(fixture.conflictID), "separate-team trace disclosed the conflict");

  const ownerRetraction = await mcpSuccess(fixture.profileA.apiKey, "retract_evidence", {
    evidence_ids: [fixture.evidenceA],
    reason: "Conflict e2e owner mutation control.",
    idempotency_key: `${runID}:provenance:owner-retract`,
  });
  assert(ownerRetraction.processing_state === "completed", `owner retraction did not complete: ${JSON.stringify(ownerRetraction)}`);

  return {
    conflict_id: fixture.conflictID,
    supporter_count: tracePosition.supporter_count,
    returned_supporters: tracePosition.supporters.length,
    supporters_truncated: tracePosition.supporters_truncated,
    recall_trace_parity: true,
    current_profile_name_hydrated: true,
    fresh_conflict_context_applied: true,
    stale_version_rejected_without_semantic_change: true,
    same_team_read_visibility: true,
    owner_only_mutation: true,
    separate_team_isolation: true,
  };
}

async function createConflictFixture(label, options = {}) {
  const teamID = options.teamID ?? await createTeam(label);
  const profileA = await createCredential(teamID, `${runID} ${label} A`);
  const profileB = await createCredential(teamID, `${runID} ${label} B`);
  const subjectName = `${runID} ${label} project`;
  const objectAName = `${runID} ${label} PostgreSQL`;
  const objectBName = `${runID} ${label} GraphDB`;
  const sourceGroupA = `${runID}:${label}:source:a`;
  const sourceGroupB = `${runID}:${label}:source:b`;
  const first = await submitRelationship(profileA.apiKey, {
    label: `${label}-a`,
    subjectName,
    objectName: objectAName,
    sourceGroup: sourceGroupA,
    authority: options.authorityA ?? "primary",
    validFrom: options.validFromA,
    marker: options.markerA,
  });
  const firstTrace = await mcpSuccess(profileA.apiKey, "trace_memory", {
    relationship_id: first.relationshipID,
    include_evidence_content: true,
  });
  const subjectEntityID = stringAt(firstTrace, ["relationship", "subject_entity_id"]);
  const objectAEntityID = stringAt(firstTrace, ["relationship", "object_entity_id"]);
  assert(subjectEntityID && objectAEntityID, `first ${label} trace did not return canonical entity IDs`);

  const second = await submitRelationship(profileB.apiKey, {
    label: `${label}-b`,
    subjectName,
    objectName: objectBName,
    subjectEntityID,
    sourceGroup: sourceGroupB,
    authority: options.authorityB ?? "primary",
    validFrom: options.validFromB,
    marker: options.markerB,
  });
  const secondTrace = await mcpSuccess(profileB.apiKey, "trace_memory", {
    relationship_id: second.relationshipID,
  });
  const objectBEntityID = stringAt(secondTrace, ["relationship", "object_entity_id"]);
  assert(objectBEntityID, `second ${label} trace did not return the canonical object entity ID`);
  const conflict = await currentConflict(profileB.apiKey, second.relationshipID, "open");
  const positionA = positionForRelationship(conflict, first.relationshipID);
  const positionB = positionForRelationship(conflict, second.relationshipID);
  return {
    label,
    teamID,
    profileA,
    profileB,
    subjectName,
    objectAName,
    objectBName,
    subjectEntityID,
    objectAEntityID,
    objectBEntityID,
    sourceGroupA,
    relationshipA: first.relationshipID,
    relationshipB: second.relationshipID,
    evidenceA: first.evidenceID,
    evidenceB: second.evidenceID,
    sourceGroupB,
    conflictID: String(conflict.conflict_id),
    positionAID: String(positionA.position_id),
    positionBID: String(positionB.position_id),
    reviewDueAt: String(conflict.review_due_at),
  };
}

async function submitPositionSupport(fixture, profile, options) {
  const positionB = options.position === "b";
  return submitRelationship(profile.apiKey, {
    label: options.label,
    subjectName: fixture.subjectName,
    objectName: positionB ? fixture.objectBName : fixture.objectAName,
    subjectEntityID: fixture.subjectEntityID,
    objectEntityID: positionB ? fixture.objectBEntityID : fixture.objectAEntityID,
    sourceGroup: options.sourceGroup,
    authority: options.authority,
    conflictContext: options.conflictContext,
    expectedState: options.expectedState,
  });
}

async function submitRelationship(apiKey, input) {
  const evidence = relationshipEvidence(input);
  const receipt = await mcpSuccess(apiKey, "remember", {
    idempotency_key: `${runID}:${input.label}`,
    evidence: [{
      content: evidence,
      source_type: "document",
      source: input.sourceGroup,
      source_group: input.sourceGroup,
      authority: input.authority,
      idempotency_key: `${runID}:${input.label}:evidence`,
    }],
    relationships: [relationshipHint(input, evidence)],
  });
  const submissionID = String(receipt.submission_id ?? "");
  assert(submissionID && receipt.status_tool === "get_submission_status", `remember receipt is invalid: ${JSON.stringify(receipt)}`);
  const status = await waitForSubmission(apiKey, submissionID);
  const expectedState = input.expectedState ?? "completed";
  assert(status.processing_state === expectedState, `submission ${input.label} state = ${status.processing_state}, want ${expectedState}: ${JSON.stringify(status)}`);
  const evidenceID = String(status.evidence?.[0]?.evidence_id ?? "");
  assert(evidenceID, `submission ${input.label} did not return evidence lineage`);
  if (expectedState !== "completed") {
    return { submissionID, evidenceID, status };
  }
  const relationshipID = postgresQuery(`
    SELECT observation.relationship_id::text
    FROM relationship_observations AS observation
    WHERE observation.team_id = ${sqlLiteral(statusTeamID(submissionID))}::uuid
      AND observation.ingest_id = ${sqlLiteral(submissionID)}::uuid
      AND observation.relationship_id IS NOT NULL
    ORDER BY observation.created_at, observation.observation_id
    LIMIT 1
  `);
  assert(relationshipID, `completed submission ${input.label} did not create a relationship observation`);
  return { submissionID, relationshipID, evidenceID, status };
}

function relationshipEvidence(input) {
  const effective = input.validFrom ? `Effective ${input.validFrom}, ` : "";
  const marker = input.marker ? ` ${input.marker}` : "";
  return `${effective}${input.subjectName} primary database is ${input.objectName}.${marker}`;
}

function relationshipHint(input, evidence) {
  const predicateSurface = "primary database";
  const subjectStart = evidence.indexOf(input.subjectName);
  const predicateStart = evidence.indexOf(predicateSurface, subjectStart);
  const objectStart = evidence.indexOf(input.objectName, predicateStart);
  assert(subjectStart >= 0 && predicateStart >= 0 && objectStart >= 0, `could not derive spans for ${input.label}`);
  const runeOffset = (offset) => Array.from(evidence.slice(0, offset)).length;
  return {
    ref: `${input.label}:relationship`,
    subject: {
      name: input.subjectName,
      entity_kind: "project",
      ...(input.subjectEntityID ? { known_entity_id: input.subjectEntityID } : {}),
      span: { evidence_index: 0, start: runeOffset(subjectStart), end: runeOffset(subjectStart + input.subjectName.length) },
    },
    predicate: {
      proposed_key: "primary_database",
      surface: predicateSurface,
      span: { evidence_index: 0, start: runeOffset(predicateStart), end: runeOffset(predicateStart + predicateSurface.length) },
    },
    object: { entity: {
      name: input.objectName,
      entity_kind: "product",
      ...(input.objectEntityID ? { known_entity_id: input.objectEntityID } : {}),
      span: { evidence_index: 0, start: runeOffset(objectStart), end: runeOffset(objectStart + input.objectName.length) },
    } },
    polarity: "+",
    modality: "statement",
    ...(input.validFrom ? { valid_from: input.validFrom } : {}),
    ...(input.conflictContext ? { conflict_context: input.conflictContext } : {}),
    supports: [{ evidence_index: 0, start: 0, end: Array.from(evidence).length }],
  };
}

async function waitForSubmission(apiKey, submissionID) {
  const attempts = Math.ceil((submissionTimeoutSeconds * 1_000) / 250);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const status = await mcpSuccess(apiKey, "get_submission_status", { submission_id: submissionID });
    if (["completed", "rejected", "failed", "quarantined"].includes(status.processing_state)) return status;
    await delay(250);
  }
  throw new Error(`timed out waiting for submission ${submissionID} after ${submissionTimeoutSeconds}s`);
}

async function currentConflict(apiKey, relationshipID, status) {
  for (let attempt = 0; attempt < 240; attempt += 1) {
    const trace = await mcpSuccess(apiKey, "trace_memory", { relationship_id: relationshipID });
    const conflict = (trace.conflicts ?? []).find((item) => item.status === status && (item.positions ?? []).some((position) => (position.relationship_ids ?? []).includes(relationshipID)));
    if (conflict) return conflict;
    await delay(250);
  }
  throw new Error(`timed out waiting for ${status} conflict for relationship ${relationshipID}`);
}

async function waitForConflictRecall(apiKey, query, conflictID) {
  for (let attempt = 0; attempt < 240; attempt += 1) {
    const recall = await mcpSuccess(apiKey, "recall_memory", { query, limit: 10, relationship_limit: 10 });
    if ((recall.conflicts ?? []).some((conflict) => conflict.conflict_id === conflictID)) return recall;
    await delay(250);
  }
  throw new Error(`timed out waiting for recall conflict ${conflictID}`);
}

function runReview(fixture, now) {
  const result = spawnSync(reviewDriver, [
    "--team-id", fixture.teamID,
    "--conflict-id", fixture.conflictID,
    "--now", now,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    env: process.env,
    encoding: "utf8",
    timeout: 60_000,
  });
  if (result.status !== 0) {
    const details = [result.error?.message, result.stderr || result.stdout].filter(Boolean).join(": ");
    throw new Error(`conflict review driver failed (${result.status}): ${redactText(details)}`);
  }
  const line = result.stdout.trim().split(/\r?\n/).findLast((item) => item.trim().startsWith("{"));
  if (!line) throw new Error(`conflict review driver returned no JSON: ${redactText(result.stdout)}`);
  return JSON.parse(line);
}

function assertReview(result, outcome, stage, preferredPositionID, method) {
  assert(result.outcome === outcome, `review outcome = ${result.outcome}, want ${outcome}: ${JSON.stringify(result)}`);
  assert(result.stage === stage, `review stage = ${result.stage}, want ${stage}: ${JSON.stringify(result)}`);
  assert(result.preferred_position_id === preferredPositionID, `review preferred position = ${result.preferred_position_id}, want ${preferredPositionID}`);
  assert(result.resolution_method === method, `review method = ${result.resolution_method}, want ${method}`);
}

function conflictState(teamID, conflictID) {
  return postgresJSON(`
    SELECT json_build_object(
      'status', conflict.status,
      'version', conflict.version,
      'preferred_position_id', COALESCE(conflict.preferred_position_id::text, ''),
      'resolution_reason', conflict.resolution_reason
    )::text
    FROM relationship_conflict_cases AS conflict
    WHERE conflict.team_id = ${sqlLiteral(teamID)}::uuid
      AND conflict.conflict_id = ${sqlLiteral(conflictID)}::uuid
  `);
}

function assessmentAttempts(teamID, conflictID) {
  return postgresJSON(`
    SELECT COALESCE(json_agg(item ORDER BY local_assessment_date), '[]'::json)::text
    FROM (
      SELECT assessment.local_assessment_date,
             json_build_object(
               'assessment_attempt_id', assessment.assessment_attempt_id::text,
               'status', assessment.status,
               'selected_position_id', COALESCE(assessment.selected_position_id::text, ''),
               'failure_class', assessment.failure_class,
               'local_assessment_date', assessment.local_assessment_date
             ) AS item
      FROM relationship_conflict_ai_assessment_attempts AS assessment
      WHERE assessment.team_id = ${sqlLiteral(teamID)}::uuid
        AND assessment.conflict_id = ${sqlLiteral(conflictID)}::uuid
    ) AS attempts
  `);
}

function latestResolutionPlan(teamID, conflictID) {
  return postgresJSON(`
    SELECT COALESCE((
      SELECT json_build_object(
        'method', plan.method,
        'status', plan.status,
        'preferred_position_id', plan.preferred_position_id::text,
        'assessment_attempt_id', COALESCE(plan.assessment_attempt_id::text, '')
      )::text
      FROM relationship_conflict_resolution_plans AS plan
      WHERE plan.team_id = ${sqlLiteral(teamID)}::uuid
        AND plan.conflict_id = ${sqlLiteral(conflictID)}::uuid
      ORDER BY plan.created_at DESC, plan.resolution_plan_id DESC
      LIMIT 1
    ), '{}')
  `);
}

function conflictDerivationCount(teamID, conflictID) {
  return Number(postgresQuery(`
    SELECT count(*)
    FROM relationship_conflict_evidence_derivations
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND conflict_id = ${sqlLiteral(conflictID)}::uuid
  `));
}

function positionAcceptedTimes(teamID, conflictID, positionAID, positionBID) {
  return postgresJSON(`
    SELECT json_build_object(
      'position_a', max(member.accepted_at) FILTER (WHERE member.position_id = ${sqlLiteral(positionAID)}::uuid),
      'position_b', max(member.accepted_at) FILTER (WHERE member.position_id = ${sqlLiteral(positionBID)}::uuid)
    )::text
    FROM relationship_conflict_position_members AS member
    WHERE member.team_id = ${sqlLiteral(teamID)}::uuid
      AND member.conflict_id = ${sqlLiteral(conflictID)}::uuid
      AND member.active
  `);
}

function conflictSemanticSnapshot(teamID, conflictID) {
  return postgresJSON(`
    SELECT json_build_object(
      'version', conflict.version,
      'active_positions', (
        SELECT count(*) FROM relationship_conflict_positions AS position
        WHERE position.team_id = conflict.team_id AND position.conflict_id = conflict.conflict_id AND position.active
      ),
      'active_members', (
        SELECT count(*) FROM relationship_conflict_position_members AS member
        WHERE member.team_id = conflict.team_id AND member.conflict_id = conflict.conflict_id AND member.active
      ),
      'member_relationships', (
        SELECT count(DISTINCT member.relationship_id) FROM relationship_conflict_position_members AS member
        WHERE member.team_id = conflict.team_id AND member.conflict_id = conflict.conflict_id AND member.active
      ),
      'member_supports', (
        SELECT count(DISTINCT member.support_id) FROM relationship_conflict_position_members AS member
        WHERE member.team_id = conflict.team_id AND member.conflict_id = conflict.conflict_id AND member.active
      )
    )::text
    FROM relationship_conflict_cases AS conflict
    WHERE conflict.team_id = ${sqlLiteral(teamID)}::uuid
      AND conflict.conflict_id = ${sqlLiteral(conflictID)}::uuid
  `);
}

function postgresJSON(sql) {
  const raw = postgresQuery(sql);
  if (!raw) throw new Error("PostgreSQL lineage query returned no JSON");
  return JSON.parse(raw);
}

function postgresQuery(sql) {
  const normalized = sql.trim();
  if (!/^(SELECT|WITH)\b/i.test(normalized)) throw new Error("conflict e2e PostgreSQL helper permits read-only queries only");
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile,
    "exec", "-T", "postgres", "sh", "-ec",
    'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "conflict-e2e", sql,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) throw new Error(`PostgreSQL read failed (${result.status}): ${redactText(result.stderr || result.stdout)}`);
  return result.stdout.trim();
}

async function assertRemovedPlacementTools(apiKey) {
  const listed = await rpc(apiKey, "tools/list", {});
  assert(!listed.error && listed.result, "tools/list failed in conflict e2e");
  const names = new Set((listed.result.tools ?? []).map((tool) => tool.name));
  for (const name of ["get_memory_placement", "resolve_memory_placement"]) {
    assert(!names.has(name), `removed placement tool ${name} is listed`);
    const response = await rpc(apiKey, "tools/call", { name, arguments: {} });
    assert(response.error?.code === -32601 && response.result === undefined, `removed placement tool ${name} is callable`);
  }
}

async function createTeam(label) {
  const response = await controlJSON("/teams", {
    method: "POST",
    body: JSON.stringify({ name: `${runID} ${label} team`, description: "isolated conflict e2e fixture" }),
  });
  const teamID = String(response.data?.id ?? "");
  assert(teamID, `control API did not create team ${label}`);
  return teamID;
}

async function createCredential(teamID, name) {
  const response = await controlJSON(`/teams/${teamID}/credentials`, {
    method: "POST",
    body: JSON.stringify({ name, role: "member", scopes: ["read", "write"], rate_limit: 300 }),
  });
  const apiKey = String(response.data?.api_key ?? "");
  const profileID = String(response.data?.credential?.id ?? "");
  assert(apiKey && profileID, "control API credential response omitted key material or credential ID");
  return { apiKey, profileID, name };
}

async function renameCredential(teamID, profileID, name) {
  const response = await controlJSON(`/teams/${teamID}/credentials/${profileID}`, {
    method: "PATCH",
    body: JSON.stringify({ name }),
  });
  assert(response.data?.id === profileID && response.data?.name === name, "control API did not rename the credential");
}

async function mcpSuccess(apiKey, name, args) {
  const response = await mcpRaw(apiKey, name, args);
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} returned an error: ${redactText(JSON.stringify(response.error ?? {}))}`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} result omitted JSON content`);
  return JSON.parse(text);
}

async function mcpRaw(apiKey, name, args) {
  return rpc(apiKey, "tools/call", { name, arguments: args });
}

async function rpc(apiKey, method, params) {
  return httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }),
  });
}

async function controlJSON(path, options) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
  });
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) throw new Error(`HTTP ${response.status} ${url}: ${redactText(text)}`);
  return text ? JSON.parse(text) : {};
}

function positionForRelationship(conflict, relationshipID) {
  const position = (conflict.positions ?? []).find((item) => (item.relationship_ids ?? []).includes(relationshipID));
  if (!position) throw new Error(`conflict ${conflict.conflict_id} omitted relationship ${relationshipID}`);
  return position;
}

function positionByID(conflict, positionID) {
  const position = (conflict?.positions ?? []).find((item) => item.position_id === positionID);
  if (!position) throw new Error(`conflict ${conflict?.conflict_id ?? "unknown"} omitted position ${positionID}`);
  return position;
}

function conflictByID(conflicts, conflictID) {
  const conflict = (conflicts ?? []).find((item) => item.conflict_id === conflictID);
  if (!conflict) throw new Error(`output omitted conflict ${conflictID}`);
  return conflict;
}

function provenanceFields(position) {
  return {
    supporter_count: position.supporter_count,
    supporters_truncated: position.supporters_truncated,
    supporters: position.supporters,
  };
}

function statusTeamID(submissionID) {
  // The globally unique ingest ID is the only unscoped fixture key; later lineage reads include its resolved team.
  const value = postgresQuery(`
    SELECT team_id::text
    FROM knowledge_ingests
    WHERE ingest_id = ${sqlLiteral(submissionID)}::uuid
    LIMIT 1
  `);
  assert(value, `submission ${submissionID} has no durable team`);
  return value;
}

function offsetTime(value, milliseconds) {
  const parsed = Date.parse(value);
  assert(Number.isFinite(parsed), `invalid RFC3339 time ${value}`);
  return new Date(parsed + milliseconds).toISOString();
}

function stableJSON(value) {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function stringAt(value, path) {
  let current = value;
  for (const key of path) {
    if (!current || typeof current !== "object") return "";
    current = current[key];
  }
  return typeof current === "string" ? current : "";
}

function redactText(value) {
  return String(value)
    .replace(/"api_key"\s*:\s*"[^"]*"/g, '"api_key":"<redacted>"')
    .replace(/postgres:\/\/[^@\s]+@/g, "postgres://<redacted>@")
    .slice(0, 2_000);
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function positiveIntEnv(name, fallback, minimum, maximum) {
  const raw = process.env[name];
  if (!raw) return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < minimum || value > maximum) throw new Error(`${name} must be an integer between ${minimum} and ${maximum}`);
  return value;
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}
