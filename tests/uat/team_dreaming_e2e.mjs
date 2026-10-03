#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";

import { nextScheduledUTCMinute } from "./team_dreaming_schedule.mjs";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const graphModel = "dense-mem-e2e-dream-graph";

let rpcID = 0;
const maxPollingAttempts = 60;
const ownerProfileID = await apiCredentialOwnerID();
const adverseTeam = await createAdverseEvidenceTeam();
const seeded = await seedSchedulerInputs(ownerProfileID);
const evidenceSeeded = await seedEvidenceDiscoveryInputs(ownerProfileID);
const historicalKnownAt = new Date().toISOString();
const scheduledAt = nextScheduledUTCMinute(Date.now(), 3);
const runDate = formatDate(scheduledAt);

await updateControlConfig("/config/general", [{ key: "APP_TIMEZONE", value: "UTC" }]);
await updateControlConfig("/config/dreaming", [
  { key: "DREAMING_ENABLED", value: "true" },
  { key: "DREAMING_FORCE_ENABLED", value: "true" },
  { key: "DREAMING_START_TIME_LOCAL", value: formatTime(scheduledAt) },
  { key: "DREAMING_MAX_OUTPUTS", value: "1" },
]);

const scheduledRun = await waitForScheduledRun();
assertEqual(scheduledRun.team_id, teamID, "scheduled run team");
assertEqual(scheduledRun.run_date, runDate, "scheduled run date");
assertEqual(scheduledRun.status, "completed", "scheduled run status");
assertEqual(Number(scheduledRun.input_relationships), 2, "scheduled input count");
assertEqual(Number(scheduledRun.created_dreams), 1, "scheduled created output count");
assertAtLeast(Number(scheduledRun.attempted_paths), 1, "scheduled attempted paths");
assertAtLeast(Number(scheduledRun.provider_turns), 1, "scheduled provider turns");
assertAtLeast(Number(scheduledRun.provider_proposals), 1, "scheduled provider proposals");
assertEqual(Number(scheduledRun.outcome_summary?.provider_failed ?? 0), 0, "scheduled provider failure");
await assertSystemRun(scheduledRun.run_id);

const controlDreams = await controlJSON(`/teams/${teamID}/dreams?limit=10`);
const scheduledDream = findDream(controlDreams.data?.items, scheduledRun.run_id, "control portal API");
assertEvidenceDerivedDream(scheduledDream, seeded, "control portal API");
const runDiagnostics = await waitForRunDiagnostic(scheduledRun.run_id, completeRunDiagnosticReady);
assertAtLeast(runDiagnostics.length, 1, "Dream run diagnostic capture");
const completeRunCaptures = runDiagnostics.filter((item) => item.phase === "run");
assertEqual(completeRunCaptures.length, 1, "complete Dream trace has one run capture");
assertEqual(completeRunCaptures[0].details?.phase_trace_truncated ?? false, false, "complete Dream phase trace marker");
const diagnosticPhases = new Set(runDiagnostics.map((item) => item.phase));
for (const phase of ["run", "target", "provider", "validation", "proposal", "disposition"]) {
  assertEqual(diagnosticPhases.has(phase), true, `Dream diagnostic phase ${phase}`);
}
const retainedCapture = runDiagnostics.find((item) => item.capture_state !== "expired") ?? runDiagnostics[0];
const retainedDetail = await controlResponse(`/teams/${teamID}/dreaming/runs/${scheduledRun.run_id}/diagnostics/${retainedCapture.capture_id}`);
assertEqual(retainedDetail.status, 200, "Dream diagnostic detail status");
assertEqual(retainedDetail.body?.data?.capture_id, retainedCapture.capture_id, "Dream diagnostic detail capture");
assertEqual(retainedDetail.body?.data?.run_id, scheduledRun.run_id, "Dream diagnostic detail run");
assertEqual(retainedDetail.cacheControl, "no-store", "Dream diagnostic detail cache policy");
assertEqual(retainedDetail.contentTypeOptions, "nosniff", "Dream diagnostic detail content type policy");
const crossTeamDetail = await controlResponse(`/teams/${adverseTeam.teamID}/dreaming/runs/${scheduledRun.run_id}/diagnostics/${retainedCapture.capture_id}`);
assertEqual(crossTeamDetail.status, 404, "cross-team Dream diagnostic detail");
const hypothesisID = scheduledDream.dream_id;
const statement = scheduledDream.hypothesis;
const reviewer = await createTeamCredential("Team Dreaming E2E reviewer");
const feedback = await mcpTool(reviewer.apiKey, "resolve_dream_feedback", {
  hypothesis_id: hypothesisID,
  decision: "reinforce",
  reason: "Independent same-team review confirmed this output remains useful.",
});
assertEqual(feedback.hypothesis_id, hypothesisID, "feedback hypothesis");
assertEqual(feedback.status, "reinforced", "feedback status");
await assertFeedbackActor(hypothesisID, reviewer.credentialID);
  const hypothesisDiagnostics = await controlJSON(`/teams/${teamID}/dreams/${hypothesisID}/diagnostics?limit=10`);
  assertEqual(Array.isArray(hypothesisDiagnostics.data?.items), true, "Dream hypothesis diagnostics page");
  assertEqual(hypothesisDiagnostics.data.items.some((item) => item.phase === "feedback"), true, "Dream feedback diagnostic phase");
  const crossTeamHypothesisDiagnostics = await controlResponse(`/teams/${adverseTeam.teamID}/dreams/${hypothesisID}/diagnostics?limit=10`);
  assertEqual(crossTeamHypothesisDiagnostics.status, 200, "cross-team Dream hypothesis diagnostics response");
  assertEqual(Array.isArray(crossTeamHypothesisDiagnostics.body?.data?.items), true, "cross-team Dream hypothesis diagnostics page");
  assertEqual(crossTeamHypothesisDiagnostics.body.data.items.length, 0, "cross-team Dream hypothesis diagnostics empty");

assertContainsDream(controlDreams.data?.items, hypothesisID, statement, "control portal API");
const userDreams = await userJSON("/ui/api/dreams?limit=10");
assertEvidenceDerivedDream(assertContainsDream(userDreams.data?.items, hypothesisID, statement, "user portal API"), seeded, "user portal API");
const listOutput = await mcpTool(apiKey, "list_dreams", { limit: 10 }, true);
assertContainsDream(listOutput.dreams, hypothesisID, statement, "MCP list_dreams");
const getOutput = await mcpTool(apiKey, "get_dream", { hypothesis_id: hypothesisID }, true);
assertEvidenceDerivedDream(getOutput.hypothesis, seeded, "MCP get_dream");
const paraphraseContextRecall = await mcpTool(apiKey, "recall_memory", {
  query: "Which service keeps Dense-Mem's durable data available?",
});
assertHypothesisFromPublicRecallContext(
  paraphraseContextRecall, hypothesisID, getOutput.hypothesis.derivations,
  "recall from paraphrase-retrieved context",
);
const historicalContextRecall = await mcpTool(apiKey, "recall_memory", {
  query: "Which service keeps Dense-Mem's durable data available?",
  known_at: historicalKnownAt,
});
assertEqual((historicalContextRecall.results ?? []).length > 0, true, "historical recall retained earlier evidence context");
assertEqual(
  (historicalContextRecall.related_hypotheses ?? []).some((item) => item?.hypothesis_id === hypothesisID),
  false,
  "historical recall omitted the later-created hypothesis",
);
assertEqual(
  (historicalContextRecall.degradations ?? []).some((item) => item?.code === "related_hypotheses_temporal_not_supported"),
  true,
  "historical hypothesis omission reported its optional degradation",
);

assertEqual(postgresQuery(`
  SELECT count(*) FROM dream_cycle_runs
  WHERE team_id = ${sqlLiteral(teamID)}::uuid AND lane = 'evidence_discovery'
`), "0", "eligible standalone evidence does not start hourly discovery");
const historical = seedHistoricalEvidenceHypothesis(ownerProfileID, evidenceSeeded, seeded);
const historicalOutput = await mcpTool(apiKey, "get_dream", { hypothesis_id: historical.hypothesisID }, true);
assertEvidenceDiscoveryDream(historicalOutput.hypothesis, evidenceSeeded, "historical MCP get_dream");
const historicalControl = await controlJSON(`/teams/${teamID}/dreams?limit=20`);
assertEvidenceDiscoveryDream(findDream(historicalControl.data?.items, historical.runID, "historical control reader"), evidenceSeeded, "historical control reader");
const historicalUser = await userJSON("/ui/api/dreams?limit=20");
assertEvidenceDiscoveryDream(findDream(historicalUser.data?.items, historical.runID, "historical user reader"), evidenceSeeded, "historical user reader");
const historicalRecall = await mcpTool(apiKey, "recall_memory", { query: "PostgreSQL durable memory" });
assertEqual((historicalRecall.related_relationships ?? []).some((item) => (item.evidence_ids ?? []).includes(evidenceSeeded.targetID)), false, "historical discovery has not created an active Relationship");
const historicalList = await mcpTool(reviewer.apiKey, "list_dreams", { limit: 20 }, true);
assertContainsDream(historicalList.dreams, historical.hypothesisID, historical.statement, "same-team historical reader");
const denied = await mcpTool(reviewer.apiKey, "resolve_dream_feedback", {
  hypothesis_id: historical.hypothesisID, decision: "reinforce", reason: "A different owner cannot mutate this historical record.",
}, false, true);
assertEqual(Boolean(denied.code), true, "historical feedback returns a bounded error");
const unchanged = await mcpTool(apiKey, "get_dream", { hypothesis_id: historical.hypothesisID }, true);
assertEqual(unchanged.hypothesis.status, "proposed", "different owner did not mutate historical state");
const crossTeamList = await mcpTool(adverseTeam.apiKey, "list_dreams", { limit: 20 }, true);
assertEqual((crossTeamList.dreams ?? []).some((item) => item.hypothesis_id === historical.hypothesisID), false, "historical cross-team isolation");
const confirmed = await mcpTool(apiKey, "resolve_dream_feedback", {
  hypothesis_id: historical.hypothesisID, decision: "confirm_true",
  evidence: [{ content: "Independent evidence confirms Legacy Dense-Mem uses PostgreSQL.", source_type: "manual", source: `historical-confirmation-${historical.hypothesisID}` }],
  relationships: [{
    ref: `historical-confirmation-${historical.hypothesisID}`,
    subject: { name: "Legacy Dense-Mem", entity_kind: "project" },
    predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } },
    polarity: "+", modality: "statement", evidence_indices: [0],
  }],
});
assertEqual(confirmed.hypothesis_id, historical.hypothesisID, "historical confirmation hypothesis");
assertEqual(confirmed.status, "submitted", "historical confirmation status");
assertDreamProviderModels(scheduledRun);
await assertDreamProviderRequests();
console.log(JSON.stringify({
  status: "ok", team_id: teamID, run_id: scheduledRun.run_id, hypothesis_id: hypothesisID,
  statement, scheduled_at: scheduledAt.toISOString(),
  historical_run_id: historical.runID, historical_hypothesis_id: historical.hypothesisID,
  historical_statement: historical.statement,
  evidence_hypothesis_id: historical.hypothesisID, evidence_dream_statement: historical.statement,
  evidence_target_id: evidenceSeeded.targetID, evidence_target_content: evidenceSeeded.targetContent,
  evidence_failure_run_id: historical.failureRunID, evidence_failure_team_name: adverseTeam.teamName,
  provider_models: { graph: graphModel },
}, null, 2));

function formatDate(value) {
  return value.toISOString().slice(0, 10);
}

function formatTime(value) {
  return value.toISOString().slice(11, 16);
}

async function updateControlConfig(path, items) {
  await controlJSON(path, {
    method: "PATCH",
    body: JSON.stringify({ items }),
  }, true);
}

async function waitForScheduledRun() {
  let lastRuns = [];
  for (let attempt = 0; attempt < maxPollingAttempts; attempt += 1) {
    const payload = await controlJSON(`/teams/${teamID}/dreaming/runs?limit=20`);
    const runs = Array.isArray(payload.data) ? payload.data : [];
    lastRuns = runs;
    const run = runs.find((item) => (
      item?.run_date === runDate &&
      item?.status === "completed" &&
      Number(item?.input_relationships) === 2 &&
      Number(item?.created_dreams) === 1
    ));
    if (run) {
      return run;
    }
    await delay(5_000);
  }
  const finalPayload = await controlJSON(`/teams/${teamID}/dreaming/runs?limit=20`);
  const finalRuns = Array.isArray(finalPayload.data) ? finalPayload.data : [];
  lastRuns = finalRuns;
  const finalRun = finalRuns.find((item) => (
    item?.run_date === runDate &&
    item?.status === "completed" &&
    Number(item?.input_relationships) === 2 &&
    Number(item?.created_dreams) === 1
  ));
  if (finalRun) {
    return finalRun;
  }
  throw new Error(`timed out waiting for scheduled team run at ${scheduledAt.toISOString()}: ${JSON.stringify(lastRuns)}`);
}

async function waitForRunDiagnostic(runID, ready = completeRunDiagnosticReady) {
  let lastItems = [];
  for (let attempt = 0; attempt < maxPollingAttempts; attempt += 1) {
    const page = await controlJSON(`/teams/${teamID}/dreaming/runs/${runID}/diagnostics?limit=100`);
    lastItems = Array.isArray(page.data?.items) ? page.data.items : [];
    if (ready(lastItems)) return lastItems;
    await delay(1_000);
  }
  throw new Error(`timed out waiting for the persisted Dream run diagnostic: ${JSON.stringify(lastItems)}`);
}

function completeRunDiagnosticReady(items) {
  const runCapture = items.find((item) => item.phase === "run");
  const phases = new Set(items.map((item) => item.phase));
  return Boolean(runCapture) &&
    runCapture.details?.phase_trace_pending !== true &&
    runCapture.details?.phase_trace_truncated !== true &&
    ["target", "provider", "validation", "proposal", "disposition"].every((phase) => phases.has(phase));
}

async function assertSystemRun(runID) {
  const row = postgresQuery(`
    SELECT 'present', COALESCE(initiated_by_profile_id::text, '')
    FROM dream_cycle_runs
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND run_id = ${sqlLiteral(runID)}::uuid
  `);
  const [present, owner] = row.split("|");
  assertEqual(present, "present", "scheduled run row");
  assertEqual(owner, "", "scheduled run initiator");
}

function assertDreamProviderModels(graphRun) {
  const persistedGraphModel = postgresQuery(`
    SELECT provider_model
    FROM dream_cycle_runs
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND run_id = ${sqlLiteral(graphRun.run_id)}::uuid
  `);
  assertEqual(persistedGraphModel, graphModel, "persisted graph Dream provider model");

}

async function assertDreamProviderRequests() {
  const providerURL = (process.env.DENSE_MEM_E2E_PROVIDER_URL || "").replace(/\/$/, "");
  if (!providerURL) throw new Error("Dream model routing requires the deterministic provider fixture");
  const response = await fetch(`${providerURL}/health`);
  const state = await response.json();
  if (!response.ok) throw new Error(`Dream provider fixture health returned HTTP ${response.status}`);
  const graphRequests = (state.chat_requests || []).filter((request) => request.schema_name === "dense_mem_dream_generation_response");
  const evidenceRequests = (state.chat_requests || []).filter((request) => request.schema_name === "dense_mem_evidence_discovery_response");
  if (graphRequests.length === 0 || !graphRequests.every((request) => request.model === graphModel)) {
    throw new Error(`Dream graph requests used an unexpected model: ${JSON.stringify(graphRequests)}`);
  }
  assertEqual(evidenceRequests.length, 0, "retired evidence provider receives no requests");
}

async function apiCredentialOwnerID() {
  const session = await userJSON("/ui/api/session");
  const credentialID = session.data?.credential?.id;
  if (typeof credentialID !== "string" || !credentialID) {
    throw new Error(`user session did not return a credential ID: ${JSON.stringify(session)}`);
  }
  return credentialID;
}

async function seedSchedulerInputs(ownerProfileID) {
  const subjectID = randomUUID();
  const middleID = randomUUID();
  const objectID = randomUUID();
  const firstRelationshipID = randomUUID();
  const secondRelationshipID = randomUUID();
  const ingestID = randomUUID();
  const firstFragmentID = randomUUID();
  const secondFragmentID = randomUUID();
  const firstObservationID = randomUUID();
  const secondObservationID = randomUUID();
  const firstVerificationID = randomUUID();
  const secondVerificationID = randomUUID();
  const firstSupportID = randomUUID();
  const secondSupportID = randomUUID();
  const firstQuote = "Dense-Mem uses the Runtime service to process memory requests.";
  const secondQuote = "The Runtime service uses PostgreSQL to store durable memory records.";
  const firstHash = sha256Hash(firstQuote);
  const secondHash = sha256Hash(secondQuote);
  const searchDocuments = await evidenceSearchDocumentsSQL(teamID, ownerProfileID, [
    { id: firstFragmentID, content: firstQuote, hash: firstHash },
    { id: secondFragmentID, content: secondQuote, hash: secondHash },
  ]);
  postgresQuery(`
    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds,
      allowed_object_kinds, relationship_kind, current_cardinality,
      lifecycle_state, origin, metadata, created_at
    )
    SELECT ${sqlLiteral(teamID)}::uuid, predicate_key, version, aliases,
           allowed_subject_kinds, allowed_object_kinds, relationship_kind,
           current_cardinality, lifecycle_state, 'built_in',
           metadata || jsonb_build_object('source', 'compose_uat'), created_at
    FROM predicate_definitions
    WHERE (predicate_key, version) = ('uses', 1)
    ON CONFLICT (team_id, predicate_key, version) DO NOTHING;

    INSERT INTO entity_records (team_id, entity_id, entity_kind)
    VALUES
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, 'project'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(middleID)}::uuid, 'product'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(objectID)}::uuid, 'product');

    INSERT INTO entity_names (
      team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind
    ) VALUES
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'Dense-Mem', 'dense-mem', 'canonical'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(middleID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'Runtime service', 'runtime service', 'canonical'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(objectID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'PostgreSQL', 'postgresql', 'canonical');

    INSERT INTO knowledge_ingests (
      team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
      source_summary, status, proposal, metadata, completed_at
    ) VALUES (
      ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ingestID)}::uuid,
      ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-evidence',
      'sha256:compose-e2e-evidence', 'Evidence-backed relationships for scheduled dreaming',
      'completed', '{}'::jsonb, '{}'::jsonb, now()
    );

    INSERT INTO evidence_fragments (
      team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
      content, content_hash, source_type, authority, source_ref
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstFragmentID)}::uuid,
        ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 0,
        ${sqlLiteral(firstQuote)}, ${sqlLiteral(firstHash)}, 'manual', 'primary', 'compose-e2e-first'
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondFragmentID)}::uuid,
        ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 1,
        ${sqlLiteral(secondQuote)}, ${sqlLiteral(secondHash)}, 'manual', 'primary', 'compose-e2e-second'
      );

    INSERT INTO relationship_records (
      team_id, relationship_id, owner_profile_id, semantic_group_key,
      subject_entity_id, predicate_key, predicate_version, object_entity_id,
      relationship_kind, current_cardinality, status, support_count,
      source_group_count
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstRelationshipID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-dense-runtime',
        ${sqlLiteral(subjectID)}::uuid, 'uses', 1, ${sqlLiteral(middleID)}::uuid,
        'state', 'many', 'active', 1, 1
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondRelationshipID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-runtime-postgres',
        ${sqlLiteral(middleID)}::uuid, 'uses', 1, ${sqlLiteral(objectID)}::uuid,
        'state', 'many', 'active', 1, 1
      );

    INSERT INTO relationship_observations (
      team_id, observation_id, relationship_id, ingest_id, owner_profile_id,
      subject_ref, original_predicate, object_ref, subject_entity_id,
      predicate_key, predicate_version, object_entity_id, evidence, metadata
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstObservationID)}::uuid,
        ${sqlLiteral(firstRelationshipID)}::uuid, ${sqlLiteral(ingestID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'Dense-Mem', 'uses',
        'Runtime service', ${sqlLiteral(subjectID)}::uuid, 'uses', 1,
        ${sqlLiteral(middleID)}::uuid,
        jsonb_build_array(jsonb_build_object('fragment_id', ${sqlLiteral(firstFragmentID)}, 'start', 0, 'end', char_length(${sqlLiteral(firstQuote)}))), '{}'::jsonb
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondObservationID)}::uuid,
        ${sqlLiteral(secondRelationshipID)}::uuid, ${sqlLiteral(ingestID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'Runtime service', 'uses',
        'PostgreSQL', ${sqlLiteral(middleID)}::uuid, 'uses', 1,
        ${sqlLiteral(objectID)}::uuid,
        jsonb_build_array(jsonb_build_object('fragment_id', ${sqlLiteral(secondFragmentID)}, 'start', 0, 'end', char_length(${sqlLiteral(secondQuote)}))), '{}'::jsonb
      );

    INSERT INTO verification_events (
      team_id, verification_event_id, observation_id, owner_profile_id, evidence_verdict,
      confidence, rationale, model, response_hash, metadata
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstVerificationID)}::uuid,
        ${sqlLiteral(firstObservationID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
        'entailed', 0.95, 'The exact excerpt supports the first premise.',
        'compose-e2e', 'sha256:compose-e2e-first-verification', '{}'::jsonb
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondVerificationID)}::uuid,
        ${sqlLiteral(secondObservationID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
        'entailed', 0.95, 'The exact excerpt supports the second premise.',
        'compose-e2e', 'sha256:compose-e2e-second-verification', '{}'::jsonb
      );

    INSERT INTO relationship_evidence_supports (
      team_id, support_id, relationship_id, observation_id, verification_event_id,
      fragment_id, owner_profile_id, source_group_key, span_start, span_end,
      quote, authority, metadata
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstSupportID)}::uuid,
        ${sqlLiteral(firstRelationshipID)}::uuid, ${sqlLiteral(firstObservationID)}::uuid,
        ${sqlLiteral(firstVerificationID)}::uuid, ${sqlLiteral(firstFragmentID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-runtime', 0,
        char_length(${sqlLiteral(firstQuote)}), ${sqlLiteral(firstQuote)}, 'primary', '{}'::jsonb
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondSupportID)}::uuid,
        ${sqlLiteral(secondRelationshipID)}::uuid, ${sqlLiteral(secondObservationID)}::uuid,
        ${sqlLiteral(secondVerificationID)}::uuid, ${sqlLiteral(secondFragmentID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-postgres', 0,
        char_length(${sqlLiteral(secondQuote)}), ${sqlLiteral(secondQuote)}, 'primary', '{}'::jsonb
      );

    INSERT INTO relationship_support_decision_events (
      team_id, support_id, relationship_id, owner_profile_id, actor_profile_id,
      decision, reason, metadata
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(firstSupportID)}::uuid,
        ${sqlLiteral(firstRelationshipID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'grant', 'compose e2e support', '{}'::jsonb
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondSupportID)}::uuid,
        ${sqlLiteral(secondRelationshipID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'grant', 'compose e2e support', '{}'::jsonb
      );
    ${searchDocuments}
  `);
  const indexed = postgresQuery(`
    SELECT count(*) FROM search_documents
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND source_kind = 'evidence'
      AND source_id IN (${sqlLiteral(firstFragmentID)}::uuid, ${sqlLiteral(secondFragmentID)}::uuid)
  `);
  assertEqual(indexed, "2", "scheduled premise search-document seed");
  return {
    firstRelationshipID,
    secondRelationshipID,
    firstQuote,
    secondQuote,
    subjectID,
    middleID,
    objectID,
  };
}

async function seedEvidenceDiscoveryInputs(ownerProfileID, targetTeamID = teamID, targetContentOverride = "") {
  const targetID = randomUUID();
  const quarantinedID = randomUUID();
  const ingestID = randomUUID();
  const targetSecurityID = randomUUID();
  const quarantinedSecurityID = randomUUID();
  const quarantineID = randomUUID();
  const targetContent = targetContentOverride || "Dense-Mem uses PostgreSQL for durable memory in an hourly evidence target.";
  const quarantinedContent = "This quarantined evidence must never enter evidence discovery.";
  const targetContentHash = sha256Hash(targetContent);
  const quarantinedContentHash = sha256Hash(quarantinedContent);
  const searchDocuments = await evidenceSearchDocumentsSQL(targetTeamID, ownerProfileID, [
    { id: targetID, content: targetContent, hash: targetContentHash },
  ]);
  postgresQuery(`
    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds,
      allowed_object_kinds, relationship_kind, current_cardinality,
      lifecycle_state, origin, metadata, created_at
    )
    SELECT ${sqlLiteral(targetTeamID)}::uuid, predicate_key, version, aliases,
           allowed_subject_kinds, allowed_object_kinds, relationship_kind,
           current_cardinality, lifecycle_state, 'built_in',
           metadata || jsonb_build_object('source', 'compose_uat'), created_at
    FROM predicate_definitions
    WHERE (predicate_key, version) = ('uses', 1)
    ON CONFLICT (team_id, predicate_key, version) DO NOTHING;

    INSERT INTO knowledge_ingests (
      team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
      source_summary, status, proposal, metadata, completed_at
    ) VALUES (
      ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
      'hourly-evidence-discovery', 'sha256:hourly-evidence-discovery',
      'Hourly evidence-discovery UAT fixture', 'completed', '{}'::jsonb, '{}'::jsonb, now()
    );
    INSERT INTO evidence_fragments (
      team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
      content, content_hash, source_type, authority, source_ref, metadata
    ) VALUES
      (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(targetID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 0,
       ${sqlLiteral(targetContent)}, ${sqlLiteral(targetContentHash)}, 'manual', 'primary', 'hourly-evidence-target', '{}'::jsonb),
      (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(quarantinedID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 1,
       ${sqlLiteral(quarantinedContent)}, ${sqlLiteral(quarantinedContentHash)}, 'manual', 'primary', 'hourly-evidence-quarantined', '{}'::jsonb);
    INSERT INTO evidence_security_events (
      team_id, security_event_id, fragment_id, ingest_id, owner_profile_id,
      event_kind, decision, reason, metadata
    ) VALUES
      (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(targetSecurityID)}::uuid, ${sqlLiteral(targetID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
       'deterministic_scan', 'pass', 'hourly evidence target passed security', '{}'::jsonb),
      (${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(quarantinedSecurityID)}::uuid, ${sqlLiteral(quarantinedID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
       'deterministic_scan', 'quarantine', 'hourly adverse evidence is quarantined', '{}'::jsonb);
    INSERT INTO evidence_quarantines (
      team_id, quarantine_id, fragment_id, ingest_id, owner_profile_id, status, reason
    ) VALUES (
      ${sqlLiteral(targetTeamID)}::uuid, ${sqlLiteral(quarantineID)}::uuid, ${sqlLiteral(quarantinedID)}::uuid,
      ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'active', 'hourly adverse evidence fixture'
    );
    ${searchDocuments}
  `);
  const indexed = postgresQuery(`
    SELECT count(*)
    FROM search_documents
    WHERE team_id = ${sqlLiteral(targetTeamID)}::uuid
      AND source_kind = 'evidence'
      AND source_id = ${sqlLiteral(targetID)}::uuid
  `);
  assertEqual(indexed, "1", `evidence search-document seed for ${targetTeamID}`);
  const quarantinedIndexed = postgresQuery(`
    SELECT count(*) FROM search_documents
    WHERE team_id = ${sqlLiteral(targetTeamID)}::uuid
      AND source_kind = 'evidence'
      AND source_id = ${sqlLiteral(quarantinedID)}::uuid
  `);
  assertEqual(quarantinedIndexed, "0", "quarantined evidence has no search document");
  return { targetID, targetContent, ingestID };
}

function seedHistoricalEvidenceHypothesis(ownerProfileID, evidence, graph) {
  const runID = randomUUID(), failureRunID = randomUUID(), hypothesisID = randomUUID(), subjectID = randomUUID();
  const statement = "Legacy Dense-Mem may use PostgreSQL.";
  const targetIdentity = sha256Hash([teamID, subjectID, "uses", `entity:${graph.objectID}`].join("\u0000"));
  postgresQuery(`
    INSERT INTO entity_records(team_id, entity_id, entity_kind)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, 'project');
    INSERT INTO entity_names(team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'Legacy Dense-Mem', 'legacy dense-mem', 'canonical');
    INSERT INTO dream_cycle_runs(team_id, run_id, space_id, space_generation, run_date, window_key, lane, status, provider_model, started_at, completed_at, evidence_targets, evaluated_evidence_targets, created_hypotheses, provider_turns, outcome_summary)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(runID)}::uuid, dense_mem_team_shared_space(${sqlLiteral(teamID)}::uuid), dense_mem_team_shared_generation(${sqlLiteral(teamID)}::uuid),
      '2026-09-04', 'hour:historical-fixture', 'evidence_discovery', 'completed', 'legacy-v2.6.3', '2026-09-04T00:00:00Z', '2026-09-04T00:01:00Z', 1, 1, 1, 1, '{"created_hypotheses":1}');
    INSERT INTO dream_cycle_runs(team_id, run_id, space_id, space_generation, run_date, window_key, lane, status, provider_model, started_at, completed_at, evidence_targets, outcome_summary, error)
    VALUES (${sqlLiteral(adverseTeam.teamID)}::uuid, ${sqlLiteral(failureRunID)}::uuid, dense_mem_team_shared_space(${sqlLiteral(adverseTeam.teamID)}::uuid), dense_mem_team_shared_generation(${sqlLiteral(adverseTeam.teamID)}::uuid),
      '2026-09-04', 'hour:historical-failure', 'evidence_discovery', 'failed', 'legacy-v2.6.3', '2026-09-04T00:00:00Z', '2026-09-04T00:01:00Z', 1, '{"provider_failed":1}', 'Historical provider failure.');
    INSERT INTO hypotheses(team_id, hypothesis_id, space_id, space_generation, created_by_profile_id, lane, status,
      statement, subject_entity_id, predicate_key, predicate_version, object_entity_id, source_refs, source_versions,
      source_owner_profile_ids, content_hash, target_identity, cycle_run_id, generator_kind, generator_version)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(hypothesisID)}::uuid, dense_mem_team_shared_space(${sqlLiteral(teamID)}::uuid), dense_mem_team_shared_generation(${sqlLiteral(teamID)}::uuid), ${sqlLiteral(ownerProfileID)}::uuid,
      'evidence_discovery', 'proposed', ${sqlLiteral(statement)}, ${sqlLiteral(subjectID)}::uuid, 'uses', 1, ${sqlLiteral(graph.objectID)}::uuid,
      '[]'::jsonb, '{}'::jsonb, ARRAY[${sqlLiteral(ownerProfileID)}::uuid], ${sqlLiteral(sha256Hash(statement))}, ${sqlLiteral(targetIdentity)}, ${sqlLiteral(runID)}::uuid, 'provider', 'legacy-v2.6.3');
    INSERT INTO hypothesis_evidence_derivation_sources(team_id, hypothesis_id, space_id, space_generation,
      evidence_id, fragment_id, source_group_key, span_start, span_end, quote, authority)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(hypothesisID)}::uuid, dense_mem_team_shared_space(${sqlLiteral(teamID)}::uuid), dense_mem_team_shared_generation(${sqlLiteral(teamID)}::uuid),
      ${sqlLiteral(evidence.targetID)}::uuid, ${sqlLiteral(evidence.targetID)}::uuid, ${sqlLiteral(`ingest:${evidence.ingestID}`)}, 0, ${evidence.targetContent.length}, ${sqlLiteral(evidence.targetContent)}, 'primary');
  `);
  return { runID, failureRunID, hypothesisID, statement };
}

async function evidenceSearchDocumentsSQL(targetTeamID, ownerProfileID, documents) {
  const model = requiredEnv("AI_API_EMBEDDING_MODEL");
  const dimensions = Number(requiredEnv("AI_API_EMBEDDING_DIMENSIONS"));
  const response = await fetch(`${requiredEnv("AI_API_URL").replace(/\/$/, "")}/embeddings`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${requiredEnv("AI_API_KEY")}`,
      "content-type": "application/json",
    },
    body: JSON.stringify({ model, dimensions, input: documents.map((document) => document.content) }),
    signal: AbortSignal.timeout(60_000),
  });
  if (!response.ok) throw new Error(`evidence fixture embedding failed with HTTP ${response.status}`);
  const payload = await response.json();
  if (!Array.isArray(payload.data) || payload.data.length !== documents.length ||
      payload.data.some((item) => !item || !Array.isArray(item.embedding) || item.embedding.length !== dimensions ||
        item.embedding.some((value) => typeof value !== "number" || !Number.isFinite(value)))) {
    throw new Error("evidence fixture embedding response has invalid vectors");
  }
  const vectors = new Array(documents.length);
  for (const item of payload.data) {
    if (!Number.isInteger(item.index) || item.index < 0 || item.index >= documents.length ||
        vectors[item.index] !== undefined) {
      throw new Error("evidence fixture embedding response has invalid indexes");
    }
    vectors[item.index] = item.embedding;
  }
  const values = documents.map((document, index) => `(
    ${sqlLiteral(randomUUID())}::uuid, ${sqlLiteral(document.id)}::uuid,
    ${sqlLiteral(document.content)}, ${sqlLiteral(document.hash)},
    ${sqlLiteral(JSON.stringify(vectors[index]))}::vector
  )`).join(",");
  return `
    INSERT INTO search_documents (
      team_id, search_document_id, owner_profile_id, source_kind, source_id, source_version,
      document_version, embedding_contract_id, embedding_dimensions, search_state,
      document_text, document_hash, projection_format_version, metadata, embedding
    )
    SELECT ${sqlLiteral(targetTeamID)}::uuid, fixture.search_document_id, ${sqlLiteral(ownerProfileID)}::uuid,
           'evidence', fixture.source_id, 1, 1, contract.embedding_contract_id,
           contract.dimensions, 'current', fixture.document_text, fixture.document_hash,
           1, '{}'::jsonb, fixture.embedding
    FROM (VALUES ${values}) AS fixture(search_document_id, source_id, document_text, document_hash, embedding)
    CROSS JOIN (
      SELECT embedding_contract.embedding_contract_id, embedding_contract.dimensions
      FROM search_index_generations AS generation
      JOIN embedding_contracts AS embedding_contract
        ON embedding_contract.embedding_contract_id = generation.embedding_contract_id
       AND embedding_contract.dimensions = generation.embedding_dimensions
      WHERE generation.activation_state = 'active' AND embedding_contract.lifecycle_state = 'active'
      ORDER BY embedding_contract.version DESC, generation.generation DESC, generation.created_at DESC
      LIMIT 1
    ) AS contract;
  `;
}

async function createAdverseEvidenceTeam() {
  const teamName = `Historical discovery failure ${Date.now()}`;
  const team = await controlJSON("/teams", {
    method: "POST",
    body: JSON.stringify({ name: teamName, description: "historical discovery provider failure fixture" }),
  });
  const targetTeamID = team.data?.id;
  if (typeof targetTeamID !== "string" || !targetTeamID) {
    throw new Error("adverse evidence team creation did not return an id");
  }
  const credential = await controlJSON(`/teams/${targetTeamID}/credentials`, {
    method: "POST",
    body: JSON.stringify({ name: "Historical discovery failure owner", role: "member", scopes: ["read", "write"], rate_limit: 300 }),
  });
  const ownerProfileID = credential.data?.credential?.id;
  if (typeof ownerProfileID !== "string" || !ownerProfileID) {
    throw new Error("adverse evidence credential did not return an owner profile id");
  }
  const apiKey = credential.data?.api_key;
  if (typeof apiKey !== "string" || !apiKey) {
    throw new Error("adverse evidence credential did not return an API key");
  }
  return { teamID: targetTeamID, teamName, ownerProfileID, apiKey };
}

async function createTeamCredential(name) {
  const payload = await controlJSON(`/teams/${teamID}/credentials`, {
    method: "POST",
    body: JSON.stringify({
      name,
      role: "member",
      scopes: ["read", "write"],
      rate_limit: 300,
    }),
  });
  const createdCredential = payload.data?.api_key;
  const credentialID = payload.data?.credential?.id;
  if (typeof createdCredential !== "string" || !createdCredential || typeof credentialID !== "string" || !credentialID) {
    throw new Error(`team reviewer creation did not return a key and credential ID: ${JSON.stringify(payload)}`);
  }
  return { apiKey: createdCredential, credentialID };
}

async function assertFeedbackActor(hypothesisID, profileID) {
  const row = postgresQuery(`
    SELECT
      'present',
      COALESCE(h.created_by_profile_id::text, ''),
      f.actor_profile_id::text,
      f.decision
    FROM hypotheses h
    JOIN LATERAL (
      SELECT actor_profile_id, decision
      FROM hypothesis_feedback_events
      WHERE team_id = h.team_id
        AND hypothesis_id = h.hypothesis_id
      ORDER BY created_at DESC, feedback_event_id DESC
      LIMIT 1
    ) f ON true
    WHERE h.team_id = ${sqlLiteral(teamID)}::uuid
      AND h.hypothesis_id = ${sqlLiteral(hypothesisID)}::uuid
  `);
  const [present, createdBy, actor, decision] = row.split("|");
  assertEqual(present, "present", "feedback row");
  assertEqual(createdBy, "", "team hypothesis creator");
  assertEqual(actor, profileID, "feedback actor");
  assertEqual(decision, "reinforce", "feedback decision");
}

async function controlJSON(path, options = {}, retryTransport = (options.method ?? "GET") === "GET") {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  }, retryTransport);
}

async function controlResponse(path, options = {}) {
  const response = await fetchWithTransportRetry(`${controlURL}/control/api${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  });
  const text = await response.text();
  return {
    status: response.status,
    body: text ? JSON.parse(text) : {},
    cacheControl: response.headers.get("cache-control"),
    contentTypeOptions: response.headers.get("x-content-type-options"),
  };
}

async function userJSON(path) {
  return httpJSON(`${userURL}${path}`, {
    headers: {
      Authorization: `Bearer ${apiKey}`,
    },
  }, true);
}

async function mcpTool(token, name, args, retryTransport = false, expectError = false) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/call",
      params: { name, arguments: args },
    }),
  }, retryTransport);
  if (response.error) {
    if (expectError) return response.error.data ?? response.error;
    throw new Error(`MCP ${name} error: ${JSON.stringify(response.error)}`);
  }
  if (expectError && response.result?.isError !== true) throw new Error(`MCP ${name} unexpectedly succeeded`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} result missing text: ${JSON.stringify(response)}`);
  }
  return JSON.parse(text);
}

async function httpJSON(url, options, retryTransport = false) {
  const response = await fetchWithTransportRetry(url, options, retryTransport);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: ${redactHTTPBody(text)}`);
  }
  return text ? JSON.parse(text) : {};
}

async function fetchWithTransportRetry(url, options, retryTransport = true) {
  let response;
  const attempts = retryTransport ? 3 : 1;
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      response = await fetch(url, options);
      break;
    } catch (error) {
      if (attempt === attempts - 1) {
        throw error;
      }
      await delay(1_000);
    }
  }
  return response;
}

function postgresQuery(sql) {
  const scopedSQL = [
    "BEGIN",
    "SET LOCAL app.tx_mode = 'system'",
    "SET LOCAL app.current_team_id = ''",
    "SET LOCAL app.current_profile_id = ''",
    "SET LOCAL app.allowed_space_ids = ''",
    sql,
    "COMMIT",
  ].join(";\n");
  const result = spawnSync("docker", [
    "compose",
    "-p",
    composeProject,
    "-f",
    composeFile,
    "exec",
    "-T",
    "postgres",
    "sh",
    "-ec",
    'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "team-dreaming-e2e",
    scopedSQL,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`postgres query failed (${result.status}): ${result.error?.message || result.stderr || result.stdout || result.signal || "no process output"}`);
  }
  return result.stdout.trim();
}

function findDream(items, runID, label) {
  if (!Array.isArray(items)) {
    throw new Error(`${label} did not return a dream list`);
  }
  const dream = items.find((item) => (
    item?.cycle_run_id === runID &&
    typeof item.dream_id === "string" &&
    typeof item.hypothesis === "string" &&
    item.hypothesis !== ""
  ));
  if (!dream) {
    throw new Error(`${label} did not return a generated hypothesis for run ${runID}: ${JSON.stringify(items)}`);
  }
  return dream;
}

function assertContainsDream(items, hypothesisID, expectedStatement, label) {
  if (!Array.isArray(items)) {
    throw new Error(`${label} did not return a dream list`);
  }
  const dream = items.find((item) => item?.dream_id === hypothesisID || item?.hypothesis_id === hypothesisID);
  if (!dream || (dream.hypothesis !== expectedStatement && dream.statement !== expectedStatement)) {
    throw new Error(`${label} did not return team-owned hypothesis ${hypothesisID}: ${JSON.stringify(items)}`);
  }
  return dream;
}

function assertRelatedHypothesis(recall, hypothesisID, label) {
  if (!Array.isArray(recall?.related_hypotheses) ||
      !recall.related_hypotheses.some((item) => item?.hypothesis_id === hypothesisID)) {
    throw new Error(`${label} did not return hypothesis ${hypothesisID}: ${JSON.stringify(recall?.related_hypotheses)}`);
  }
}

function assertHypothesisFromPublicRecallContext(recall, hypothesisID, derivations, label) {
  const evidenceIDs = new Set((recall?.results ?? []).map((item) => item?.evidence_id).filter(Boolean));
  const citedQuotes = new Set((derivations ?? []).map((item) => item?.quote).filter(Boolean));
  const relationshipIDs = new Set();
  const entityIDs = new Set();
  const addRelationship = (relationship) => {
    if (relationship?.relationship_id) relationshipIDs.add(relationship.relationship_id);
    for (const id of relationship?.equivalent_relationship_ids ?? []) relationshipIDs.add(id);
    for (const id of relationship?.evidence_ids ?? []) evidenceIDs.add(id);
    if (relationship?.subject?.entity_id) entityIDs.add(relationship.subject.entity_id);
    if (relationship?.object?.entity_id) entityIDs.add(relationship.object.entity_id);
  };

  for (const relationship of recall?.related_relationships ?? []) addRelationship(relationship);
  for (const path of recall?.related_communities ?? []) {
    for (const id of path?.evidence_ids ?? []) evidenceIDs.add(id);
    for (const entity of path?.top_entities ?? []) {
      if (entity?.entity_id) entityIDs.add(entity.entity_id);
    }
    for (const relationship of path?.relationships ?? []) addRelationship(relationship);
  }

  const hypothesis = (recall?.related_hypotheses ?? []).find((item) => (
    item?.hypothesis_id === hypothesisID && (
      [...citedQuotes].some((quote) => (recall?.results ?? []).some((result) => result?.context?.includes(quote))) ||
      (item?.source_evidence_ids ?? []).some((id) => evidenceIDs.has(id)) ||
      (item?.source_relationship_ids ?? []).some((id) => relationshipIDs.has(id)) ||
      [item?.subject_entity_id, item?.object_entity_id].some((id) => entityIDs.has(id))
    )
  ));
  if (!hypothesis) {
    throw new Error(`${label} returned no hypothesis grounded in public recall context: ${JSON.stringify({
      results: recall?.results,
      related_relationships: recall?.related_relationships,
      related_communities: recall?.related_communities,
      related_hypotheses: recall?.related_hypotheses,
    })}`);
  }
}

function assertEvidenceDerivedDream(dream, seededInputs, label) {
  const derivations = dream?.derivations;
  if (!Array.isArray(derivations) || derivations.length !== 2) {
    throw new Error(`${label} did not return exactly two cited premise excerpts: ${JSON.stringify(dream)}`);
  }
  const byPosition = new Map(derivations.map((derivation) => [Number(derivation?.premise_position), derivation]));
  const first = byPosition.get(1);
  const second = byPosition.get(2);
  if (!first || !second) {
    throw new Error(`${label} did not return premise positions 1 and 2: ${JSON.stringify(derivations)}`);
  }
  assertEqual(first.relationship_id, seededInputs.firstRelationshipID, `${label} first derivation relationship`);
  assertEqual(second.relationship_id, seededInputs.secondRelationshipID, `${label} second derivation relationship`);
  assertEqual(first.quote, seededInputs.firstQuote, `${label} first derivation quote`);
  assertEqual(second.quote, seededInputs.secondQuote, `${label} second derivation quote`);
  if (!first.source_group_key || !second.source_group_key) {
    throw new Error(`${label} derivations omitted source groups: ${JSON.stringify(derivations)}`);
  }
}

function assertEvidenceDiscoveryDream(dream, seededInputs, label) {
  assertEqual(dream?.lane, "evidence_discovery", `${label} lane`);
  const evidenceIDs = Array.isArray(dream?.source_evidence_ids) ? dream.source_evidence_ids : [];
  if (!evidenceIDs.includes(seededInputs.targetID)) {
    throw new Error(`${label} omitted the target evidence ID: ${JSON.stringify(dream)}`);
  }
  const derivations = dream?.evidence_derivations;
  if (!Array.isArray(derivations) || derivations.length !== 1) {
    throw new Error(`${label} did not return one evidence derivation: ${JSON.stringify(dream)}`);
  }
  assertEqual(derivations[0].evidence_id, seededInputs.targetID, `${label} evidence ID`);
  assertEqual(derivations[0].quote, seededInputs.targetContent, `${label} exact quote`);
  assertEqual(Number(derivations[0].span_start), 0, `${label} span start`);
  assertEqual(Number(derivations[0].span_end), seededInputs.targetContent.length, `${label} span end`);
}

function assertEqual(actual, expected, label) {
  if (actual !== expected) {
    throw new Error(`${label} = ${JSON.stringify(actual)}; want ${JSON.stringify(expected)}`);
  }
}

function assertAtLeast(actual, minimum, label) {
  if (!Number.isFinite(actual) || actual < minimum) {
    throw new Error(`${label} = ${JSON.stringify(actual)}; want at least ${minimum}`);
  }
}

function sqlLiteral(value) {
  return `'${String(value).replace(/'/g, "''")}'`;
}

function sha256Hash(value) {
  return createHash("sha256").update(value).digest("hex");
}

function redactHTTPBody(text) {
  return text.replace(/"api_key"\s*:\s*"[^"]*"/g, "\"api_key\":\"<redacted>\"");
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
