import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const name = "dream";

export async function run({ rpc, rawRPC, expect }) {
  await enableDreaming();
  const listed = await rpc("tools/list", {});
  const names = new Set((listed.tools || []).map((tool) => tool.name));
  expect(names.has("resolve_dream_feedback"), "dream surface must expose resolve_dream_feedback");
  expect(names.has("get_submission_status"), "dream slice must retain the legacy status tool");

  const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
  const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
  const ownerProfileID = await apiCredentialOwnerID(apiKey);
  const scenarios = makeScenarios();
  seedHypotheses(teamID, ownerProfileID, scenarios);

  const completed = await resolve(rpc, scenarios.completed, expect);
  expect(completed.status === "submitted", "completed Remember must submit the Hypothesis");
  const completedSubmissionID = requireString(completed.submission_id, "completed submission ID");
  const completedHypothesis = hypothesisRow(teamID, scenarios.completed.hypothesisID);
  expect(completedHypothesis.status === "submitted", "completed confirmation must persist submitted status");
  expect(completedHypothesis.submittedIngestID === completedSubmissionID, "completed confirmation must link the committed ingest");
  const completedFeedbackEvents = feedbackEventCount(teamID, scenarios.completed.hypothesisID);
  expect(completedFeedbackEvents === 1, "completed confirmation must persist one feedback event");
  const completedAttempt = attemptRow(teamID, scenarios.completed.idempotencyKey);
  expect(completedAttempt.outcome === "completed" && completedAttempt.count === 1, "completed confirmation must persist one terminal attempt");
  const completedEvidence = evidenceRow(teamID, completedSubmissionID);
  expect(completedEvidence.content === scenarios.completed.evidence, "Remember must persist the independent evidence exactly");
  expect(completedEvidence.hypothesisID === scenarios.completed.hypothesisID, "Remember evidence must retain bounded Hypothesis provenance");
  expect(completedEvidence.content !== scenarios.completed.statement, "Hypothesis text must not become submitted evidence");

  const replay = await resolve(rpc, scenarios.completed, expect);
  expect(replay.status === "submitted", "completed replay must retain submitted status");
  expect(replay.submission_id === completedSubmissionID, "completed replay must reuse the canonical submission ID");
  const replayHypothesis = hypothesisRow(teamID, scenarios.completed.hypothesisID);
  expect(replayHypothesis.submittedAt === completedHypothesis.submittedAt, "completed replay must retain the original submission time");
  expect(feedbackEventCount(teamID, scenarios.completed.hypothesisID) === completedFeedbackEvents, "completed replay must not append a feedback event");
  const replayAttempt = attemptRow(teamID, scenarios.completed.idempotencyKey);
  expect(replayAttempt.outcome === "completed" && replayAttempt.count === 1, "completed replay must not create a second canonical attempt");

  const changedReason = await rawRPC("tools/call", {
    name: "resolve_dream_feedback",
    arguments: {
      hypothesis_id: scenarios.completed.hypothesisID,
      decision: "confirm_true",
      reason: "A different bounded feedback reason must not replay the original mutation.",
      evidence: [{ content: scenarios.completed.evidence, source_type: "manual", source: `dream-feedback-${scenarios.completed.hypothesisID}` }],
      relationships: [dreamRelationship(scenarios.completed)],
    },
  });
  expect(String(changedReason.error?.message || "").includes("remember: conflict"), "changed Dream feedback reason must conflict under the same retry identity");
  expect(attemptRow(teamID, scenarios.completed.idempotencyKey).count === 1, "changed Dream feedback reason must not create another Remember attempt");
  expect(feedbackEventCount(teamID, scenarios.completed.hypothesisID) === completedFeedbackEvents, "changed Dream feedback reason must not append a feedback event");
  const unchangedHypothesis = hypothesisRow(teamID, scenarios.completed.hypothesisID);
  expect(unchangedHypothesis.status === completedHypothesis.status, "changed Dream feedback reason must preserve Hypothesis status");
  expect(unchangedHypothesis.submittedIngestID === completedHypothesis.submittedIngestID, "changed Dream feedback reason must preserve submitted ingest");
  expect(unchangedHypothesis.submittedAt === completedHypothesis.submittedAt, "changed Dream feedback reason must preserve submission time");

  const rejected = await resolve(rpc, scenarios.rejected, expect);
  expect(rejected.status === "proposed", "rejected Remember must leave the Hypothesis reviewable");
  requireString(rejected.submission_id, "rejected submission ID");
  expect(hypothesisRow(teamID, scenarios.rejected.hypothesisID).status === "proposed", "rejected confirmation must not advance Dream state");
  expect(attemptRow(teamID, scenarios.rejected.idempotencyKey).outcome === "rejected", "rejected confirmation must persist a rejected terminal attempt");

  const quarantined = await resolve(rpc, scenarios.quarantined, expect);
  expect(quarantined.status === "proposed", "quarantined Remember must leave the Hypothesis reviewable");
  requireString(quarantined.submission_id, "quarantined submission ID");
  expect(hypothesisRow(teamID, scenarios.quarantined.hypothesisID).status === "proposed", "quarantined confirmation must not advance Dream state");
  expect(attemptRow(teamID, scenarios.quarantined.idempotencyKey).outcome === "quarantined", "quarantined confirmation must persist a quarantined terminal attempt");

  const failedResponse = await rawRPC("tools/call", {
    name: "resolve_dream_feedback",
    arguments: resolveArguments(scenarios.failed),
  });
  expect(failedResponse.result?.isError === true, "operational Remember failure must be a structured tool error");
  const failed = failedResponse.result?.structuredContent;
  expect(failed?.processing_state === "failed", "operational Remember failure must expose terminal state");
  expect(Array.isArray(failed?.errors) && failed.errors.length > 0, "operational Remember failure must expose typed errors");
  requireString(failed?.submission_id, "failed submission ID");
  expect(hypothesisRow(teamID, scenarios.failed.hypothesisID).status === "proposed", "operational failure must not advance Dream state");
  expect(attemptRow(teamID, scenarios.failed.idempotencyKey).outcome === "failed", "operational failure must persist a failed terminal attempt");
  const failedAttemptCount = attemptRow(teamID, scenarios.failed.idempotencyKey).count;

  const noEvidence = await rawRPC("tools/call", {
    name: "resolve_dream_feedback",
    arguments: { hypothesis_id: scenarios.failed.hypothesisID, decision: "confirm_true" },
  });
  expect(String(noEvidence.error?.message || "").includes("evidence is required"), "confirmation without evidence must fail contract validation");
  expect(attemptRow(teamID, scenarios.failed.idempotencyKey).count === failedAttemptCount, "confirmation without evidence must not invoke Remember");

  const hypothesisText = await rawRPC("tools/call", {
    name: "resolve_dream_feedback",
    arguments: {
      hypothesis_id: scenarios.failed.hypothesisID,
      decision: "confirm_true",
      evidence: [{ content: scenarios.failed.statement }],
      relationships: [dreamRelationship(scenarios.failed)],
    },
  });
  expect(String(hypothesisText.error?.message || "").includes("hypothesis text cannot be submitted"), "Hypothesis text must not be accepted as evidence");
  expect(attemptRow(teamID, scenarios.failed.idempotencyKey).count === failedAttemptCount, "Hypothesis text rejection must not invoke Remember");

  return {
    mode: name,
    completed_submission_id: completedSubmissionID,
    replay_reused_submission: true,
    rejected_reviewable: true,
    quarantined_reviewable: true,
    failed_reviewable: true,
    independent_evidence_provenance: true,
  };
}

async function enableDreaming() {
  const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
  const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
  await httpJSON(`${controlURL}/control/api/config/dreaming`, {
    method: "PATCH",
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
    body: JSON.stringify({ items: [
      { key: "DREAMING_ENABLED", value: "true" },
      { key: "DREAMING_FORCE_ENABLED", value: "true" },
      { key: "DREAMING_START_TIME_LOCAL", value: "03:00" },
      { key: "DREAMING_MAX_OUTPUTS", value: "5" },
    ] }),
  });
}

function makeScenarios() {
  const labels = ["completed", "rejected", "quarantined", "failed"];
  const scenarios = {};
  for (const label of labels) {
    const hypothesisID = randomUUID();
    const statement = `Dream ${label}: Dense-Mem may use PostgreSQL for durable memory.`;
    const evidence = label === "rejected"
      ? `[fixture-fault:no-supported] Dense-Mem uses PostgreSQL; independent ${label} evidence.`
      : label === "quarantined"
        ? `[fixture-fault:security] Dense-Mem uses PostgreSQL; independent ${label} evidence.`
        : label === "failed"
          ? `[fixture-fault:assessment-unavailable] Independent ${label} evidence.`
          : "Independent deployment evidence confirms Dense-Mem uses PostgreSQL.";
    scenarios[label] = {
      hypothesisID,
      statement,
      evidence,
      idempotencyKey: `dream-feedback:${hypothesisID}:confirm_true`,
    };
  }
  return scenarios;
}

async function resolve(rpc, scenario, expect) {
  const result = await toolSuccess(rpc, "resolve_dream_feedback", resolveArguments(scenario));
  expect(result.hypothesis_id === scenario.hypothesisID, "Dream feedback must return the requested Hypothesis");
  return result;
}

function resolveArguments(scenario) {
  return {
    hypothesis_id: scenario.hypothesisID,
    decision: "confirm_true",
    evidence: [{ content: scenario.evidence, source_type: "manual", source: `dream-feedback-${scenario.hypothesisID}` }],
    relationships: [dreamRelationship(scenario)],
  };
}

function dreamRelationship(scenario) {
  return {
    ref: `dream-${scenario.hypothesisID}-relationship`,
    subject: { name: "Dense-Mem", entity_kind: "project" },
    predicate: { proposed_key: "uses" },
    object: { entity: { name: "PostgreSQL", entity_kind: "product" } },
    polarity: "+",
    modality: "statement",
    evidence_indices: [0],
  };
}

async function toolSuccess(rpc, name, args) {
  const result = await rpc("tools/call", { name, arguments: args });
  const text = result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} did not return JSON content`);
  return JSON.parse(text);
}

async function apiCredentialOwnerID(apiKey) {
  const payload = await httpJSON(`${requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "")}/ui/api/session`, {
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  const ownerProfileID = payload.data?.credential?.id;
  if (typeof ownerProfileID !== "string" || ownerProfileID.length === 0) throw new Error("user session did not return a credential ID");
  return ownerProfileID;
}

function seedHypotheses(teamID, ownerProfileID, scenarios) {
  const values = Object.values(scenarios).map((scenario) => `(
    ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(scenario.hypothesisID)}::uuid,
    ${sqlLiteral(ownerProfileID)}::uuid, 'proposed', ${sqlLiteral(scenario.statement)},
    ${sqlLiteral(`sha256:${randomUUID().replaceAll("-", "")}`)}, '[]'::jsonb, '{}'::jsonb, ARRAY[]::uuid[]
  )`);
  postgresQuery(`
    INSERT INTO hypotheses (
      team_id, hypothesis_id, created_by_profile_id, status, statement,
      content_hash, source_refs, source_versions, source_owner_profile_ids
    ) VALUES ${values.join(",")}
  `);
}

function hypothesisRow(teamID, hypothesisID) {
  const [status, submittedIngestID, submittedAt] = postgresQuery(`
    SELECT status, COALESCE(submitted_ingest_id::text, ''), COALESCE(submitted_at::text, '')
    FROM hypotheses
    WHERE team_id = ${sqlLiteral(teamID)}::uuid AND hypothesis_id = ${sqlLiteral(hypothesisID)}::uuid
  `).split("|");
  return { status, submittedIngestID, submittedAt };
}

function feedbackEventCount(teamID, hypothesisID) {
  return Number(postgresQuery(`
    SELECT count(*)::text
    FROM hypothesis_feedback_events
    WHERE team_id = ${sqlLiteral(teamID)}::uuid AND hypothesis_id = ${sqlLiteral(hypothesisID)}::uuid
  `) || 0);
}

function attemptRow(teamID, idempotencyKey) {
  const output = postgresQuery(`
    SELECT COALESCE(outcome, ''), count(*)::text
    FROM remember_attempts
    WHERE team_id = ${sqlLiteral(teamID)}::uuid AND idempotency_key = ${sqlLiteral(idempotencyKey)}
    GROUP BY outcome ORDER BY outcome LIMIT 1
  `);
  if (!output) return { outcome: "", count: 0 };
  const [outcome, count] = output.split("|");
  return { outcome, count: Number(count) };
}

function evidenceRow(teamID, ingestID) {
  const [content, hypothesisID] = postgresQuery(`
    SELECT content, COALESCE(metadata->>'hypothesis_id', '')
    FROM evidence_fragments
    WHERE team_id = ${sqlLiteral(teamID)}::uuid AND ingest_id = ${sqlLiteral(ingestID)}::uuid
    ORDER BY evidence_index LIMIT 1
  `).split("|");
  return { content, hypothesisID };
}

function postgresQuery(sql) {
  const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
  const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
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
    "compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec",
    'psql -q -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "dream-synchronous-write-e2e", scopedSQL,
  ], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) throw new Error(`postgres query failed (${result.status})`);
  return result.stdout.trim();
}

async function httpJSON(url, options = {}) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) throw new Error(`HTTP ${response.status} request failed`);
  return text ? JSON.parse(text) : {};
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function requireString(value, label) {
  if (typeof value !== "string" || value.length === 0) throw new Error(`${label} is required`);
  return value;
}
