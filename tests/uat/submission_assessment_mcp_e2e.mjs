#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const prometheusURL = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const placementTimeoutSeconds = positiveIntEnv("DENSE_MEM_E2E_PLACEMENT_TIMEOUT_SECONDS", 720, 60, 1800);

let rpcID = 0;
const runID = `submission-assessment-e2e-${Date.now()}`;
const contractVersion = "dense-mem.v2.6";
const evidence = [
  "Project Aurora uses LedgerDB. Project Aurora uses Atlas.",
  "Atlas enables Relay.",
];
const verifierBefore = await prometheusValue("densemem_verifier_requests_total");

const coverageRunID = `${runID}:coverage`;
const coverageError = await mcpToolExpectError("remember", {
  idempotency_key: `${coverageRunID}:batch`,
  evidence: [
    { content: "Covered uses evidence.", source_type: "document", source: coverageRunID, source_group: coverageRunID },
    { content: "Uncovered evidence.", source_type: "document", source: coverageRunID, source_group: coverageRunID },
  ],
  relationships: [simpleRelationship("Covered uses evidence.", "coverage-rel", 0, "Covered", "uses", "evidence", "uses", "project", "product", "Covered uses evidence.")],
});
if (!coverageError.includes("missing evidence indexes: [1]")) {
  throw new Error("remember coverage rejection did not identify the missing evidence index");
}
const coverageStaged = positiveCount(postgresRow(`
  SELECT count(*) FROM knowledge_ingests
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND idempotency_key = ${sqlLiteral(`${coverageRunID}:batch`)}
`)[0]);
if (coverageStaged !== 0) {
  throw new Error("coverage-rejected remember unexpectedly staged an ingest");
}

const dynamicRunID = `${runID}:dynamic-preflight`;
const dynamicError = await mcpToolError("remember", {
  idempotency_key: dynamicRunID,
  evidence: [{
    content: "Unavailable exact references must fail before staging.",
    source_type: "document",
    source: dynamicRunID,
    source_group: dynamicRunID,
  }],
  relationships: [{
    ref: "dynamic-preflight",
    subject: { known_entity_id: "00000000-0000-4000-8000-000000000261" },
    predicate: { known_predicate_key: `${runID}:missing-predicate` },
    object: { entity: { name: "Dynamic target" } },
    polarity: "+",
    evidence_indices: [0],
  }],
});
const dynamicIssues = Array.isArray(dynamicError.data?.issues) ? dynamicError.data.issues : [];
if (!dynamicIssues.some((issue) => issue.path === "/relationships/0/subject/known_entity_id" && issue.code === "unavailable")) {
  throw new Error("dynamic exact-reference validation did not return the bounded known_entity_id issue");
}
const dynamicStaged = positiveCount(postgresRow(`
  SELECT count(*) FROM knowledge_ingests
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND idempotency_key = ${sqlLiteral(dynamicRunID)}
`)[0]);
if (dynamicStaged !== 0) {
  throw new Error("dynamic exact-reference validation unexpectedly staged an ingest");
}

seedPredicates("unrelated", 2500);

const remember = await submitRemember("baseline", {
  idempotency_key: `${runID}:batch`,
  evidence: evidence.map((content, index) => ({
    content,
    source_type: "document",
    source: `${runID}:evidence:${index}`,
    source_group: runID,
    source_key: `${runID}:source:${index}`,
    source_revision: "revision-1",
  })),
  relationships: [
    relationship("r:uses-ledger", 0, "Project Aurora", "uses", "LedgerDB", "uses", "project", "product", "Project Aurora uses LedgerDB."),
    relationship("r:uses-atlas", 0, "Project Aurora", "uses", "Atlas", "uses", "project", "product", "Project Aurora uses Atlas."),
    relationship("r:enables-relay", 1, "Atlas", "enables", "Relay", "enables", "product", "product", evidence[1]),
  ],
});
const submissionID = stringValue(remember.submission_id);
if (!submissionID) {
  throw new Error("remember did not return a submission_id");
}

const completedStatus = await waitForCompletedPlacement(submissionID, "baseline");
const completedRelationshipResults = Array.isArray(completedStatus.relationship_results) ? completedStatus.relationship_results : [];
if (completedStatus.contract_version !== contractVersion || remember.contract_version !== contractVersion) {
  throw new Error("baseline Remember receipt or status did not expose dense-mem.v2.6");
}
if (completedRelationshipResults.length !== 3 || completedRelationshipResults.some((result) => result.disposition !== "stored")) {
  throw new Error("baseline status did not expose one stored result for every submitted relationship ref");
}
const verifierAfter = await waitForStableVerifierRequests(verifierBefore + 1);
const summary = submissionSummary(submissionID);

if (summary.assessments !== 1 || summary.completedItems !== 2 || summary.commitOutcomes !== 2) {
  throw new Error("submission did not complete as one atomic placement run");
}
// Repeated mentions may share one grounded span and are canonically resolved
// to one entity event; this fixture still requires all five distinct entities.
if (summary.entityResolutions < 5 || summary.entityResolutions > 6 || summary.relationshipObservations !== 3 || summary.verifications !== 3) {
  throw new Error("submission assessment did not preserve every submitted entity and relationship target");
}
if (summary.reviewTasks !== 0 || summary.registrationEvents !== 1 || summary.enablesRegistrations !== 1 || summary.createdRegistrations !== 1) {
  throw new Error("submission assessment did not use the controlled terminal registration path");
}
if (summary.searchDocuments !== 5) {
  throw new Error("atomic submission commit did not create evidence and relationship search documents");
}
const baselineSourceActivations = positiveCount(postgresRow(`
  SELECT count(*)
  FROM remember_source_revision_intents
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND ingest_id = ${sqlLiteral(submissionID)}::uuid
    AND source_id IS NOT NULL
    AND source_revision_id IS NOT NULL
`)[0]);
if (baselineSourceActivations !== evidence.length) {
  throw new Error("accepted baseline did not atomically activate every staged source revision intent");
}

const mixedContent = "[remember-mixed] Aurora Mixed uses Atlas Mixed. Aurora Mixed imagines Phantom Mixed.";
const mixedRemember = await submitRemember("mixed dispositions", {
  idempotency_key: `${runID}:mixed`,
  evidence: [{ content: mixedContent, source_type: "document", source: `${runID}:mixed`, source_group: `${runID}:mixed` }],
  relationships: [
    relationshipForContent(mixedContent, "mixed-stored", 0, "Aurora Mixed", "uses", "Atlas Mixed", "uses", "project", "product", "Aurora Mixed uses Atlas Mixed"),
    relationshipForContent(mixedContent, "mixed-not-supported", 0, "Aurora Mixed", "imagines", "Phantom Mixed", "imagines", "project", "product", "Aurora Mixed imagines Phantom Mixed"),
  ],
});
const mixedStatus = await waitForCompletedPlacement(mixedRemember.submission_id, "mixed dispositions");
const mixedStored = relationshipResult(mixedStatus, "mixed-stored");
const mixedNotStored = relationshipResult(mixedStatus, "mixed-not-supported");
if (mixedStored.disposition !== "stored" || mixedStored.splits.length !== 1) {
  throw new Error("mixed submission did not expose its stored relationship result");
}
if (mixedNotStored.disposition !== "not_stored" || mixedNotStored.reason !== "not_supported_by_evidence" || mixedNotStored.splits.length !== 0) {
  throw new Error("mixed submission did not expose its explicit not_stored relationship result");
}

const allUnsupportedContent = "[remember-all-unsupported] Aurora Unsupported imagines Phantom Unsupported and predicts Mirage Unsupported.";
const allUnsupportedRemember = await submitRemember("all unsupported", {
  idempotency_key: `${runID}:all-unsupported`,
  evidence: [{ content: allUnsupportedContent, source_type: "document", source: `${runID}:all-unsupported`, source_group: `${runID}:all-unsupported` }],
  relationships: [
    relationshipForContent(allUnsupportedContent, "all-unsupported-imagines", 0, "Aurora Unsupported", "imagines", "Phantom Unsupported", "imagines", "project", "product", "Aurora Unsupported imagines Phantom Unsupported"),
    relationshipForContent(allUnsupportedContent, "all-unsupported-predicts", 0, "Aurora Unsupported", "predicts", "Mirage Unsupported", "predicts", "project", "product", "Aurora Unsupported imagines Phantom Unsupported and predicts Mirage Unsupported"),
  ],
});
const allUnsupportedStatus = await waitForSubmissionState(allUnsupportedRemember.submission_id, "rejected", "all unsupported");
assertOnlyStatusError(allUnsupportedStatus, "no_supported_memory", "all unsupported");
const allUnsupportedResults = [
  relationshipResult(allUnsupportedStatus, "all-unsupported-imagines"),
  relationshipResult(allUnsupportedStatus, "all-unsupported-predicts"),
];
if (allUnsupportedResults.some((result) => result.disposition !== "not_stored" || result.reason !== "not_supported_by_evidence" || result.splits.length !== 0)) {
  throw new Error("all-unsupported rejection did not preserve every not_stored relationship result");
}
assertNoCommittedSemanticEffects(allUnsupportedRemember.submission_id, "all unsupported", allUnsupportedResults.length);

const partialCoverageEvidence = [
  "[remember-partial-coverage] Aurora Coverage uses Atlas Coverage.",
  "Aurora Coverage imagines Phantom Coverage.",
];
const partialCoverageRemember = await submitRemember("partial coverage rejection", {
  idempotency_key: `${runID}:partial-coverage`,
  evidence: partialCoverageEvidence.map((content, index) => ({
    content,
    source_type: "document",
    source: `${runID}:partial-coverage:${index}`,
    source_group: `${runID}:partial-coverage`,
  })),
  relationships: [
    relationshipForContent(partialCoverageEvidence[0], "partial-coverage-stored", 0, "Aurora Coverage", "uses", "Atlas Coverage", "uses", "project", "product", "Aurora Coverage uses Atlas Coverage"),
    relationshipForContent(partialCoverageEvidence[1], "partial-coverage-not-supported", 1, "Aurora Coverage", "imagines", "Phantom Coverage", "imagines", "project", "product", "Aurora Coverage imagines Phantom Coverage"),
  ],
});
const partialCoverageStatus = await waitForSubmissionState(partialCoverageRemember.submission_id, "rejected", "partial coverage rejection");
assertOnlyStatusError(partialCoverageStatus, "no_supported_memory", "partial coverage rejection");
const partialCoverageResults = [
  relationshipResult(partialCoverageStatus, "partial-coverage-stored"),
  relationshipResult(partialCoverageStatus, "partial-coverage-not-supported"),
];
if (partialCoverageResults.some((result) => result.disposition !== "not_stored" || result.reason !== "not_supported_by_evidence" || result.splits.length !== 0)) {
  throw new Error("partial-coverage rejection did not persist an all-not_stored relationship result set");
}
assertNoCommittedSemanticEffects(partialCoverageRemember.submission_id, "partial coverage rejection", partialCoverageResults.length);

const multiSplitContent = "[remember-multi-split] Aurora Multi uses and works on Atlas Multi.";
const multiSplitRemember = await submitRemember("multi split", {
  idempotency_key: `${runID}:multi-split`,
  evidence: [{ content: multiSplitContent, source_type: "document", source: `${runID}:multi-split`, source_group: `${runID}:multi-split` }],
  relationships: [relationshipForContent(
    multiSplitContent,
    "multi-split",
    0,
    "Aurora Multi",
    "uses and works on",
    "Atlas Multi",
    "uses_and_works_on",
    "project",
    "product",
    "Aurora Multi uses and works on Atlas Multi",
  )],
});
const multiSplitStatus = await waitForCompletedPlacement(multiSplitRemember.submission_id, "multi split");
const multiSplitResult = relationshipResult(multiSplitStatus, "multi-split");
if (multiSplitResult.disposition !== "stored" || multiSplitResult.splits.length !== 2) {
  throw new Error("multi-split submission did not expose two stored splits");
}
if (multiSplitResult.splits[0].split_index !== 0 || multiSplitResult.splits[1].split_index !== 1) {
  throw new Error("multi-split submission did not preserve contiguous split indexes");
}

const groundingContent = "[remember-grounding-repair] Issue #261 is blocked by the private-memory UAT conflict.";
const groundingRemember = await submitRemember("issue 261 grounding repair", {
  idempotency_key: `${runID}:grounding-repair`,
  evidence: [{ content: groundingContent, source_type: "document", source: `${runID}:grounding-repair`, source_group: `${runID}:grounding-repair` }],
  relationships: [{
    ref: "issue-261-blocked",
    subject: { name: "Issue #261" },
    predicate: { proposed_key: "blocked_by" },
    object: { value: { type: "string", value: "the private-memory UAT conflict" } },
    polarity: "+",
    evidence_indices: [0],
  }],
});
await waitForCompletedPlacement(groundingRemember.submission_id, "issue 261 grounding repair");
const groundingTurns = positiveCount(postgresRow(`
  SELECT provider_turns
  FROM placement_assessments
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND ingest_id = ${sqlLiteral(groundingRemember.submission_id)}::uuid
    AND assessment_scope = 'submission'
`)[0]);
if (groundingTurns !== 2) {
  throw new Error(`issue-261 grounding repair used ${groundingTurns} provider turns instead of one same-session repair`);
}

const ambiguousEntityName = `AmbiguousDB-${Date.now()}`;
seedAmbiguousEntities(ambiguousEntityName, "product", submissionID);
const ambiguityContent = `[remember-ambiguous-repair] ${ambiguousEntityName} uses Ambiguity Target.`;
const ambiguityRemember = await submitRemember("duplicate entity ambiguity repair", {
  idempotency_key: `${runID}:ambiguity-repair`,
  evidence: [{
    content: ambiguityContent,
    source_type: "document",
    source: `${runID}:ambiguity-repair`,
    source_group: `${runID}:ambiguity-repair`,
  }],
  relationships: [relationshipForContent(
    ambiguityContent,
    "ambiguity-repair",
    0,
    ambiguousEntityName,
    "uses",
    "Ambiguity Target",
    "uses",
    "product",
    "product",
    `${ambiguousEntityName} uses Ambiguity Target`,
  )],
});
const ambiguityStatus = await waitForSubmissionState(ambiguityRemember.submission_id, "rejected", "duplicate entity ambiguity repair");
assertOnlyStatusError(ambiguityStatus, "no_supported_memory", "duplicate entity ambiguity repair");
const ambiguityResult = relationshipResult(ambiguityStatus, "ambiguity-repair");
if (ambiguityResult.disposition !== "not_stored" || ambiguityResult.reason !== "not_supported_by_evidence" || ambiguityResult.splits.length !== 0) {
  throw new Error("duplicate-entity ambiguity repair did not reject the dependent relationship as not_stored");
}
const ambiguityTurns = positiveCount(postgresRow(`
  SELECT provider_turns
  FROM placement_assessments
  WHERE team_id = ${sqlLiteral(teamID)}::uuid
    AND ingest_id = ${sqlLiteral(ambiguityRemember.submission_id)}::uuid
    AND assessment_scope = 'submission'
`)[0]);
if (ambiguityTurns !== 2) {
  throw new Error(`duplicate-entity ambiguity repair used ${ambiguityTurns} provider turns instead of one same-session repair`);
}
assertNoCommittedSemanticEffects(ambiguityRemember.submission_id, "duplicate entity ambiguity repair", 1);

const baselineTarget = relationshipResult(completedStatus, "r:uses-ledger").splits[0];
const staleContent = "[remember-post-ack-stale] Project Aurora uses LedgerDB Next.";
const staleRelationship = relationshipForContent(
  staleContent,
  "post-ack-stale",
  0,
  "Project Aurora",
  "uses",
  "LedgerDB Next",
  "uses",
  "project",
  "product",
  "Project Aurora uses LedgerDB Next",
);
staleRelationship.predicate = { known_predicate_key: "uses" };
staleRelationship.correction_target = {
  relationship_id: baselineTarget.relationship_id,
  expected_version: baselineTarget.relationship_version,
};
const staleRemember = await submitRemember("post-ack stale input", {
  idempotency_key: `${runID}:post-ack-stale`,
  evidence: [{
    content: staleContent,
    source_type: "document",
    source: `${runID}:post-ack-stale`,
    source_group: `${runID}:post-ack-stale`,
  }],
  relationships: [staleRelationship],
});
bumpRelationshipVersion(baselineTarget.relationship_id, baselineTarget.relationship_version);
const staleStatus = await waitForSubmissionState(staleRemember.submission_id, "rejected", "post-ack stale input");
assertOnlyStatusError(staleStatus, "stale_input", "post-ack stale input");
assertNoCommittedSemanticEffects(staleRemember.submission_id, "post-ack stale input");

const providerFailureContent = "[remember-provider-fail] Provider Failure uses Target Failure.";
const providerFailureRemember = await submitRemember("provider failure", {
  idempotency_key: `${runID}:provider-failure`,
  evidence: [{
    content: providerFailureContent,
    source_type: "document",
    source: `${runID}:provider-failure`,
    source_group: `${runID}:provider-failure`,
    source_key: `${runID}:provider-failure-source`,
    source_revision: "revision-1",
  }],
  relationships: [relationshipForContent(providerFailureContent, "provider-failure", 0, "Provider Failure", "uses", "Target Failure", "uses", "project", "product", "Provider Failure uses Target Failure")],
});
await forceNextAttemptToBeTerminal(providerFailureRemember.submission_id, "provider failure");
const providerFailureStatus = await waitForSubmissionState(providerFailureRemember.submission_id, "failed", "provider failure");
assertOnlyStatusError(providerFailureStatus, "provider_unavailable", "provider failure");
assertNoCommittedSemanticEffects(providerFailureRemember.submission_id, "provider failure");

const databaseFailureContent = "[remember-database-fail] Database Failure uses Target Database.";
const databaseFailureRemember = await submitRemember("database failure", {
  idempotency_key: `${runID}:database-failure`,
  evidence: [{
    content: databaseFailureContent,
    source_type: "document",
    source: `${runID}:database-failure`,
    source_group: `${runID}:database-failure`,
  }],
  relationships: [relationshipForContent(databaseFailureContent, "database-failure", 0, "Database Failure", "uses", "Target Database", "uses", "project", "product", "Database Failure uses Target Database")],
});
const databaseFailureRunID = placementRunID(databaseFailureRemember.submission_id);
installDatabaseFailureTrigger(databaseFailureRunID);
let databaseFailureStatus;
try {
  await forceNextAttemptToBeTerminal(databaseFailureRemember.submission_id, "database failure");
  databaseFailureStatus = await waitForSubmissionState(databaseFailureRemember.submission_id, "failed", "database failure");
} finally {
  dropDatabaseFailureTrigger();
}
assertOnlyStatusError(databaseFailureStatus, "database_failure", "database failure");
assertNoCommittedSemanticEffects(databaseFailureRemember.submission_id, "database failure");

const overflowBefore = await waitForStableVerifierRequests(verifierAfter);
const overflowSubmission = overflowFixture();
seedPredicates("overflow", overflowSubmission.relationships.length, "overflow");
const overflowRemember = await submitRemember("predicate overflow", overflowSubmission);
const overflowSubmissionID = stringValue(overflowRemember.submission_id);
if (!overflowSubmissionID) {
  throw new Error("predicate overflow remember did not return a submission_id");
}
const overflowStatus = await waitForFailedSubmission(overflowSubmissionID);
const overflowErrors = Array.isArray(overflowStatus.errors) ? overflowStatus.errors : [];
if (!overflowErrors.some((item) => stringValue(item.code) === "input_budget_exceeded")) {
  throw new Error("predicate overflow status did not expose its bounded terminal failure");
}
const overflowProviderState = postgresRow(`
  SELECT
    (SELECT count(*) FROM placement_runs
     WHERE team_id = ${sqlLiteral(teamID)}::uuid
       AND ingest_id = ${sqlLiteral(overflowSubmissionID)}::uuid
       AND assessor_attempt_id IS NOT NULL),
    (SELECT count(*) FROM placement_assessments
     WHERE team_id = ${sqlLiteral(teamID)}::uuid
       AND ingest_id = ${sqlLiteral(overflowSubmissionID)}::uuid);
`);
const overflowAttempts = positiveCount(overflowProviderState[0]);
const overflowAssessments = positiveCount(overflowProviderState[1]);
if (overflowAttempts !== 0 || overflowAssessments !== 0) {
  throw new Error("predicate overflow unexpectedly called the verifier");
}
const overflowAfter = await waitForStableVerifierRequests(overflowBefore);
if (overflowAfter !== overflowBefore) {
  throw new Error("predicate overflow unexpectedly called the verifier");
}
const terminalFailures = await waitForPrometheusValueSelector(
  "densemem_assessor_terminal_failures_total",
  'stage="predicate_options_overflow"',
  1,
);
if (terminalFailures < 1) {
  throw new Error("predicate overflow did not record the bounded terminal metric");
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_id: submissionID,
  verifier_requests_before: verifierBefore,
  verifier_requests_after_baseline: verifierAfter,
  verifier_requests_before_overflow: overflowBefore,
  overflow_assessor_attempts: overflowAttempts,
  overflow_assessments: overflowAssessments,
  mixed_dispositions: [mixedStored.disposition, mixedNotStored.disposition],
  all_unsupported_dispositions: allUnsupportedResults.map((result) => result.disposition),
  partial_coverage_dispositions: partialCoverageResults.map((result) => result.disposition),
  multi_split_count: multiSplitResult.splits.length,
  grounding_repair_turns: groundingTurns,
  ambiguity_repair_turns: ambiguityTurns,
  stale_input_state: staleStatus.processing_state,
  provider_failure_state: providerFailureStatus.processing_state,
  database_failure_state: databaseFailureStatus.processing_state,
  assessments: summary.assessments,
  completed_items: summary.completedItems,
  relationship_observations: summary.relationshipObservations,
  predicate_registration_events: summary.registrationEvents,
  baseline_source_activations: baselineSourceActivations,
}, null, 2));

function relationship(ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText) {
  return relationshipForContent(evidence[evidenceIndex], ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText);
}

function simpleRelationship(content, ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText) {
  return relationshipForContent(
    content,
    ref,
    evidenceIndex,
    subject,
    predicateSurface,
    object,
    proposedKey,
    subjectKind,
    objectKind,
    supportText,
  );
}

function relationshipForContent(content, ref, evidenceIndex, subject, predicateSurface, object, proposedKey, subjectKind, objectKind, supportText) {
  return {
    ref,
    subject: {
      name: subject,
      entity_kind: subjectKind,
    },
    predicate: {
      proposed_key: proposedKey,
    },
    object: {
      entity: {
        name: object,
        entity_kind: objectKind,
      },
    },
    polarity: "+",
    evidence_indices: [evidenceIndex],
  };
}

function seedPredicates(prefix, count, keyPrefix = `${runID}:${prefix}`) {
  postgresRow(`
    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds,
      allowed_object_kinds, relationship_kind, current_cardinality,
      lifecycle_state, origin, metadata
    )
    SELECT ${sqlLiteral(teamID)}::uuid,
           ${sqlLiteral(`${keyPrefix}_`)} || series::text,
           1,
           ARRAY[]::text[],
           ARRAY['project','product','organization','concept','other']::text[],
           ARRAY['project','product','organization','concept','other']::text[],
           'state', 'many', 'active', 'built_in', '{}'::jsonb
    FROM generate_series(0, ${count - 1}) AS series;
    SELECT count(*) FROM team_predicate_definitions
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND predicate_key LIKE ${sqlLiteral(`${keyPrefix}_%`)};
  `);
}

function seedAmbiguousEntities(name, kind, ownerSubmissionID) {
  postgresRow(`
    BEGIN;
    SET LOCAL app.tx_mode = 'system';
    WITH owner AS (
      SELECT owner_profile_id
      FROM knowledge_ingests
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND ingest_id = ${sqlLiteral(ownerSubmissionID)}::uuid
    ), created AS (
      INSERT INTO entity_records (team_id, entity_id, entity_kind)
      SELECT ${sqlLiteral(teamID)}::uuid, gen_random_uuid(), ${sqlLiteral(kind)}
      FROM generate_series(1, 2)
      RETURNING entity_id
    )
    INSERT INTO entity_names (
      team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind
    )
    SELECT ${sqlLiteral(teamID)}::uuid, created.entity_id, owner.owner_profile_id,
           ${sqlLiteral(name)}, lower(${sqlLiteral(name)}), 'canonical'
    FROM created CROSS JOIN owner;
    COMMIT;
  `);
  const count = positiveCount(postgresRow(`
    SELECT count(*)
    FROM entity_names
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND normalized_name = lower(${sqlLiteral(name)})
      AND valid_to IS NULL;
  `)[0]);
  if (count !== 2) {
    throw new Error(`ambiguous Entity fixture created ${count} candidates instead of two`);
  }
}

function overflowFixture() {
  const overflowEvidence = [];
  const relationships = [];
  for (let evidenceIndex = 0; evidenceIndex < Math.ceil(101 / 6); evidenceIndex += 1) {
    const clauses = [];
    const indexes = [];
    for (let slot = 0; slot < 6; slot += 1) {
      const index = evidenceIndex * 6 + slot;
      if (index >= 101) {
        break;
      }
      clauses.push(`A${index} x B${index}`);
      indexes.push(index);
    }
    const content = `${clauses.join(". ")}.`;
    overflowEvidence.push({
      content,
      source_type: "document",
      source: `${runID}:overflow:evidence:${evidenceIndex}`,
      source_group: `${runID}:overflow`,
    });
    for (const index of indexes) {
      relationships.push(relationshipForContent(
        content,
        `overflow-rel-${index}`,
        evidenceIndex,
        `A${index}`,
        "x",
        `B${index}`,
        `overflow_${index}`,
        "project",
        "product",
        `A${index} x B${index}`,
      ));
    }
  }
  return { idempotency_key: `${runID}:overflow:batch`, evidence: overflowEvidence, relationships };
}

async function submitRemember(label, submission) {
  const result = await mcpTool("remember", submission);
  if (result.contract_version !== contractVersion) {
    throw new Error(`${label} Remember receipt did not expose ${contractVersion}`);
  }
  if (!stringValue(result.submission_id)) {
    throw new Error(`${label} Remember receipt did not include a submission_id`);
  }
  return result;
}

function relationshipResult(status, ref) {
  const results = Array.isArray(status.relationship_results) ? status.relationship_results : [];
  const result = results.find((item) => item?.ref === ref);
  if (!result || !Array.isArray(result.splits)) {
    throw new Error(`submission status omitted relationship result ${ref}`);
  }
  return result;
}

function assertOnlyStatusError(status, code, label) {
  const errors = Array.isArray(status?.errors) ? status.errors : [];
  if (errors.length !== 1 || stringValue(errors[0]?.code) !== code) {
    throw new Error(`${label} status did not expose exactly ${code}: ${JSON.stringify(errors)}`);
  }
}

async function waitForCompletedPlacement(submissionID, label = "submission") {
  return waitForSubmissionState(submissionID, "completed", label);
}

async function waitForFailedSubmission(submissionID) {
  return waitForSubmissionState(submissionID, "failed", "predicate overflow");
}

async function waitForSubmissionState(submissionID, expectedState, label) {
  const attempts = Math.ceil((placementTimeoutSeconds * 1000) / 2_000);
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const placement = await mcpTool("get_submission_status", { submission_id: submissionID });
    if (placement.contract_version !== contractVersion) {
      throw new Error(`${label} status did not expose ${contractVersion}`);
    }
    const state = stringValue(placement.processing_state);
    if (state === expectedState) {
      return placement;
    }
    if (["completed", "rejected", "failed", "quarantined"].includes(state)) {
      throw new Error(`${label} reached unexpected terminal state ${state}; expected ${expectedState}`);
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for ${label} to reach ${expectedState}`);
}

async function forceNextAttemptToBeTerminal(submissionID, label) {
  for (let attempt = 0; attempt < 240; attempt += 1) {
    const row = postgresRow(`
      SELECT status, attempts, max_attempts
      FROM placement_runs
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND ingest_id = ${sqlLiteral(submissionID)}::uuid
    `);
    const status = stringValue(row[0]);
    const observedAttempts = Number(row[1] ?? -1);
    if (["queued", "guarded"].includes(status) && Number.isInteger(observedAttempts) && observedAttempts >= 1) {
      const updated = postgresRow(`
        WITH updated AS (
          UPDATE placement_runs
          SET max_attempts = attempts + 1, available_at = now(), updated_at = now()
          WHERE team_id = ${sqlLiteral(teamID)}::uuid
            AND ingest_id = ${sqlLiteral(submissionID)}::uuid
            AND status IN ('queued', 'guarded')
            AND attempts = ${observedAttempts}
          RETURNING max_attempts
        )
        SELECT max_attempts FROM updated
      `);
      if (positiveCount(updated[0]) === observedAttempts + 1) {
        return;
      }
    }
    if (["completed", "rejected", "failed", "quarantined"].includes(status)) {
      throw new Error(`${label} reached ${status} before retry fault injection completed`);
    }
    await delay(250);
  }
  throw new Error(`timed out waiting for ${label} to requeue`);
}

function placementRunID(submissionID) {
  const runIDValue = stringValue(postgresRow(`
    SELECT placement_run_id
    FROM placement_runs
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND ingest_id = ${sqlLiteral(submissionID)}::uuid
  `)[0]);
  if (!runIDValue) {
    throw new Error("placement run ID is missing");
  }
  return runIDValue;
}

function bumpRelationshipVersion(relationshipID, expectedVersion) {
  const nextVersion = positiveCount(postgresRow(`
    WITH updated AS (
      UPDATE relationship_records
      SET version = version + 1, updated_at = now()
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND relationship_id = ${sqlLiteral(relationshipID)}::uuid
        AND version = ${Number(expectedVersion)}
      RETURNING version
    )
    SELECT version FROM updated
  `)[0]);
  if (nextVersion !== Number(expectedVersion) + 1) {
    throw new Error("post-ack correction target race did not advance the exact relationship version");
  }
}

function installDatabaseFailureTrigger(runIDValue) {
  postgresRow(`
    DROP TRIGGER IF EXISTS dense_mem_e2e_submission_result_failure ON submission_relationship_results;
    DROP FUNCTION IF EXISTS dense_mem_e2e_submission_result_failure();
    CREATE FUNCTION dense_mem_e2e_submission_result_failure() RETURNS trigger AS $dense_mem_e2e$
    BEGIN
      IF NEW.team_id = ${sqlLiteral(teamID)}::uuid
         AND NEW.placement_run_id = ${sqlLiteral(runIDValue)}::uuid THEN
        RAISE EXCEPTION 'deterministic submission result persistence failure' USING ERRCODE = 'XX000';
      END IF;
      RETURN NEW;
    END;
    $dense_mem_e2e$ LANGUAGE plpgsql;
    CREATE TRIGGER dense_mem_e2e_submission_result_failure
      BEFORE INSERT ON submission_relationship_results
      FOR EACH ROW EXECUTE FUNCTION dense_mem_e2e_submission_result_failure();
  `);
}

function dropDatabaseFailureTrigger() {
  postgresRow(`
    DROP TRIGGER IF EXISTS dense_mem_e2e_submission_result_failure ON submission_relationship_results;
    DROP FUNCTION IF EXISTS dense_mem_e2e_submission_result_failure();
  `);
}

function assertNoCommittedSemanticEffects(submissionID, label, expectedRelationshipResults = 0) {
  const summary = submissionSummary(submissionID);
  const row = postgresRow(`
    WITH documents AS (
      SELECT document.search_document_id
      FROM search_documents AS document
      WHERE document.team_id = ${sqlLiteral(teamID)}::uuid
        AND document.source_kind = 'evidence'
        AND document.source_id IN (
          SELECT fragment_id FROM evidence_fragments
          WHERE team_id = ${sqlLiteral(teamID)}::uuid
            AND ingest_id = ${sqlLiteral(submissionID)}::uuid
        )
    )
    SELECT
      (SELECT count(*) FROM embedding_jobs WHERE team_id = ${sqlLiteral(teamID)}::uuid AND search_document_id IN (SELECT search_document_id FROM documents)),
      (SELECT count(*) FROM remember_source_revision_intents
       WHERE team_id = ${sqlLiteral(teamID)}::uuid
         AND ingest_id = ${sqlLiteral(submissionID)}::uuid
         AND (source_id IS NOT NULL OR source_revision_id IS NOT NULL)),
      (SELECT count(*) FROM submission_relationship_results
       WHERE team_id = ${sqlLiteral(teamID)}::uuid
         AND ingest_id = ${sqlLiteral(submissionID)}::uuid)
  `);
  const semanticCounts = [
    summary.completedItems,
    summary.commitOutcomes,
    summary.entityResolutions,
    summary.relationshipObservations,
    summary.verifications,
    summary.searchDocuments,
    positiveCount(row[0]),
    positiveCount(row[1]),
  ];
  const relationshipResults = positiveCount(row[2]);
  if (semanticCounts.some((count) => count !== 0) || relationshipResults !== expectedRelationshipResults) {
    throw new Error(`${label} committed unexpected semantic, source, result, search, or embedding effects`);
  }
}

async function waitForStableVerifierRequests(minimum) {
  let previous = -1;
  let stableIntervals = 0;
  let observed = -1;
  for (let attempt = 0; attempt < 60; attempt += 1) {
    observed = await prometheusValue("densemem_verifier_requests_total");
    if (observed >= minimum && observed === previous) {
      stableIntervals += 1;
    } else {
      stableIntervals = 0;
    }
    if (stableIntervals >= 3) {
      return observed;
    }
    previous = observed;
    await delay(2_000);
  }
  throw new Error(`verifier request metric did not become stable (minimum ${minimum}, last observed ${observed})`);
}

function submissionSummary(submissionID) {
  const runIDLiteral = sqlLiteral(submissionID);
  const row = postgresRow(`
    WITH run AS (
      SELECT placement_run_id
      FROM placement_runs
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND ingest_id = ${runIDLiteral}::uuid
    ), assessment AS (
      SELECT assessment_id
      FROM placement_assessments
      WHERE team_id = ${sqlLiteral(teamID)}::uuid
        AND ingest_id = ${runIDLiteral}::uuid
        AND assessment_scope = 'submission'
        AND placement_item_id IS NULL
    )
    SELECT
      (SELECT count(*) FROM assessment) AS assessments,
      (SELECT count(*) FROM placement_items WHERE team_id = ${sqlLiteral(teamID)}::uuid AND ingest_id = ${runIDLiteral}::uuid AND status = 'completed') AS completed_items,
      (SELECT count(*) FROM placement_outcomes WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM run) AND outcome_kind = 'submission_assessment_commit') AS commit_outcomes,
      (SELECT count(*) FROM entity_resolution_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND assessment_id = (SELECT assessment_id FROM assessment)) AS entity_resolutions,
      (SELECT count(*)
       FROM relationship_observations AS observation
       JOIN verification_events AS verification
         ON verification.team_id = observation.team_id
        AND verification.observation_id = observation.observation_id
       WHERE observation.team_id = ${sqlLiteral(teamID)}::uuid
         AND verification.assessment_id = (SELECT assessment_id FROM assessment)) AS relationship_observations,
      (SELECT count(*) FROM verification_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND assessment_id = (SELECT assessment_id FROM assessment)) AS verifications,
      (SELECT count(*) FROM review_tasks AS task JOIN placement_items AS item ON item.team_id = task.team_id AND item.placement_item_id = task.placement_item_id WHERE task.team_id = ${sqlLiteral(teamID)}::uuid AND item.ingest_id = ${runIDLiteral}::uuid) AS review_tasks,
      (SELECT count(*) FROM predicate_registration_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM run)) AS registration_events,
      (SELECT count(*) FROM predicate_registration_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM run) AND predicate_key = 'enables') AS enables_registrations,
      (SELECT count(*) FROM predicate_registration_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM run) AND registration_action = 'created') AS created_registrations,
      (SELECT count(*)
       FROM search_documents AS document
       WHERE document.team_id = ${sqlLiteral(teamID)}::uuid
         AND (
           (document.source_kind = 'evidence' AND document.source_id IN (
             SELECT item.fragment_id
             FROM placement_items AS item
             WHERE item.team_id = ${sqlLiteral(teamID)}::uuid
               AND item.placement_run_id = (SELECT placement_run_id FROM run)
           ))
           OR
           (document.source_kind = 'relationship' AND document.source_id IN (
             SELECT observation.relationship_id
             FROM relationship_observations AS observation
             JOIN verification_events AS verification
               ON verification.team_id = observation.team_id
              AND verification.observation_id = observation.observation_id
             WHERE observation.team_id = ${sqlLiteral(teamID)}::uuid
               AND verification.assessment_id = (SELECT assessment_id FROM assessment)
           ))
         )) AS search_documents;
  `);
  return {
    assessments: positiveCount(row[0]),
    completedItems: positiveCount(row[1]),
    commitOutcomes: positiveCount(row[2]),
    entityResolutions: positiveCount(row[3]),
    relationshipObservations: positiveCount(row[4]),
    verifications: positiveCount(row[5]),
    reviewTasks: positiveCount(row[6]),
    registrationEvents: positiveCount(row[7]),
    enablesRegistrations: positiveCount(row[8]),
    createdRegistrations: positiveCount(row[9]),
    searchDocuments: positiveCount(row[10]),
  };
}

async function mcpTool(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${apiKey}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/call",
      params: { name, arguments: args },
    }),
  });
  if (response.error) {
    throw new Error(`MCP ${name} returned a bounded error`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} did not return JSON content`);
  }
  return JSON.parse(text);
}

async function mcpToolExpectError(name, args) {
  const error = await mcpToolError(name, args);
  return error.message;
}

async function mcpToolError(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${apiKey}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/call",
      params: { name, arguments: args },
    }),
  });
  if (!response.error || response.result !== undefined) {
    throw new Error(`MCP ${name} unexpectedly succeeded`);
  }
  const message = response.error?.message;
  if (typeof message !== "string") {
    throw new Error(`MCP ${name} returned an unrecognized bounded error`);
  }
  return response.error;
}

async function prometheusValue(metric) {
  return prometheusValueSelector(metric, `team_id="${teamID}"`);
}

async function prometheusValueSelector(metric, selector) {
  const url = new URL("/api/v1/query", `${prometheusURL}/`);
  url.searchParams.set("query", `sum(${metric}{${selector}})`);
  const response = await httpJSON(url.toString(), { method: "GET" });
  const value = response.data?.result?.[0]?.value?.[1];
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) {
    throw new Error(`Prometheus returned a non-numeric ${metric}`);
  }
  return parsed;
}

async function waitForPrometheusValueSelector(metric, selector, minimum) {
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const value = await prometheusValueSelector(metric, selector);
    if (value >= minimum) {
      return value;
    }
    await delay(2_000);
  }
  return prometheusValueSelector(metric, selector);
}

function postgresRow(sql) {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile,
    "exec", "-T", "postgres", "sh", "-ec",
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|" -c "$1"',
    "submission-assessment-e2e", sql,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`postgres summary query failed (${result.status})`);
  }
  return result.stdout.trim().split("|");
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function positiveCount(value) {
  const parsed = Number(value ?? 0);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new Error("postgres summary contained an invalid count");
  }
  return parsed;
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: response body redacted`);
  }
  return text ? JSON.parse(text) : {};
}

function stringValue(value) {
  return typeof value === "string" ? value.trim() : "";
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function positiveIntEnv(name, fallback, minimum, maximum) {
  const raw = process.env[name];
  if (!raw) {
    return fallback;
  }
  const parsed = Number(raw);
  if (!Number.isInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw new Error(`${name} must be an integer between ${minimum} and ${maximum}`);
  }
  return parsed;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
