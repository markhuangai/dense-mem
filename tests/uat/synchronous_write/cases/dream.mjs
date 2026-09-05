import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const name = "dream";

export async function run({ rpc, rawRPC, expect }) {
  await enableDreaming();
  const listed = await rpc("tools/list", {});
  const names = new Set((listed.tools || []).map((tool) => tool.name));
  expect(names.has("resolve_dream_feedback"), "dream surface must expose resolve_dream_feedback");
  expect(!names.has("get_submission_status"), "dream surface must remove the legacy status tool");

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
  expect(
    changedReason.result?.isError === true &&
      changedReason.result?.structuredContent?.processing_state === "failed" &&
      changedReason.result?.structuredContent?.errors?.[0]?.code === "idempotency_conflict",
    `changed Dream feedback reason must conflict under the same retry identity: ${JSON.stringify(changedReason)}`,
  );
  expect(attemptRow(teamID, scenarios.completed.idempotencyKey).count === 1, "changed Dream feedback reason must not create another Remember attempt");
  expect(feedbackEventCount(teamID, scenarios.completed.hypothesisID) === completedFeedbackEvents, "changed Dream feedback reason must not append a feedback event");
  const unchangedHypothesis = hypothesisRow(teamID, scenarios.completed.hypothesisID);
  expect(unchangedHypothesis.status === completedHypothesis.status, "changed Dream feedback reason must preserve Hypothesis status");
  expect(unchangedHypothesis.submittedIngestID === completedHypothesis.submittedIngestID, "changed Dream feedback reason must preserve submitted ingest");
  expect(unchangedHypothesis.submittedAt === completedHypothesis.submittedAt, "changed Dream feedback reason must preserve submission time");

  const completedIngestCount = hypothesisIngestCount(teamID, scenarios.completed.hypothesisID);
  const conflicting = await rawRPC("tools/call", {
    name: "resolve_dream_feedback",
    arguments: { ...resolveArguments(scenarios.completed), decision: "confirm_false", evidence: [{ content: "Independent refuting evidence.", source_type: "manual" }] },
  });
  const conflictError = conflicting.result?.structuredContent || conflicting.error?.data;
  expect(
    (conflicting.result?.isError === true || Boolean(conflicting.error)) &&
      conflictError?.code === "invalid_input" &&
      conflictError?.reason_code === "reference_not_found" &&
      conflictError?.next_action === "refresh_state" &&
      conflictError?.retryable === false,
    `conflicting Dream confirmation must return actionable not-found guidance before Remember: ${JSON.stringify(conflicting)}`,
  );
  expect(hypothesisIngestCount(teamID, scenarios.completed.hypothesisID) === completedIngestCount, "conflicting Dream confirmation must not create a Remember ingest");
  expect(feedbackEventCount(teamID, scenarios.completed.hypothesisID) === completedFeedbackEvents, "conflicting Dream confirmation must not append a feedback event");

	const unsupported = await resolve(rpc, scenarios.unsupported, expect);
	expect(unsupported.status === "submitted", "unsupported Relationships must not block Dream submission when evidence is safe");
	expect(hypothesisRow(teamID, scenarios.unsupported.hypothesisID).status === "submitted", "unsupported Relationship warning must still submit the Dream");
	const unsupportedAttempt = attemptRow(teamID, scenarios.unsupported.idempotencyKey);
	expect(unsupportedAttempt.outcome === "completed" && unsupportedAttempt.count === 1, "unsupported Relationship warning must persist one completed attempt");

	const securityResponse = await rawRPC("tools/call", {
		name: "resolve_dream_feedback",
		arguments: resolveArguments(scenarios.security),
	});
	expect(securityResponse.result?.isError === true, "unsafe Remember must be a structured tool error");
	const security = securityResponse.result?.structuredContent;
	expect(security?.processing_state === "failed", "unsafe Remember must expose failed terminal state");
	expect(security?.errors?.[0]?.code === "submission_policy_rejected" && security?.errors?.[0]?.retryable === false, "unsafe Remember must expose non-retryable policy rejection");
	requireString(security?.submission_id, "security rejection submission ID");
	expect(hypothesisRow(teamID, scenarios.security.hypothesisID).status === "proposed", "unsafe confirmation must not advance Dream state");
	const securityAttempt = attemptRow(teamID, scenarios.security.idempotencyKey);
	expect(securityAttempt.outcome === "failed" && securityAttempt.count === 1, "unsafe confirmation must persist one failed terminal attempt");
	expect(security.errors[0]?.next_action === "resubmit_remember", "unsafe Remember must advertise resubmission rather than retrying the same request");

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
	const failedAttempt = attemptRow(teamID, scenarios.failed.idempotencyKey);
	expect(failedAttempt.outcome === "failed" && failedAttempt.count === 1, "operational failure must persist one failed terminal attempt");
  assertDreamRetryGuidance(failed, scenarios.failed, "failed", "retry_same_request", expect);
	const failedAttemptCount = failedAttempt.count;

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

  const contention = scenarios.contention;
  const contentionIngestCount = hypothesisIngestCount(teamID, contention.hypothesisID);
  const contentionFeedbackCount = feedbackEventCount(teamID, contention.hypothesisID);
  const firstContention = rawRPC("tools/call", {
    name: "resolve_dream_feedback",
    arguments: {
      ...resolveArguments(contention),
      evidence: [{ content: "[fixture-fault:assessment-timeout] Independent contention evidence.", source_type: "manual" }],
    },
  });
  await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
  const secondContention = rawRPC("tools/call", {
    name: "resolve_dream_feedback",
    arguments: {
      ...resolveArguments(contention),
      evidence: [{ content: "[fixture-fault:assessment-timeout] Independent contention evidence.", source_type: "manual" }],
    },
  });
  const contentionResponses = await Promise.all([firstContention, secondContention]);
	const busyResponse = contentionResponses.find((response) => response.result?.structuredContent?.code === "dream_confirmation_busy");
	expect(busyResponse?.result?.isError === true, "concurrent Dream confirmation must expose a structured busy error");
	expect(busyResponse.result.structuredContent.next_action === "retry_dream_feedback", "busy Dream confirmation must advertise retry_dream_feedback");
  expect(hypothesisIngestCount(teamID, contention.hypothesisID) === contentionIngestCount, "busy Dream confirmation must not create a Remember ingest");
  expect(feedbackEventCount(teamID, contention.hypothesisID) === contentionFeedbackCount, "busy Dream confirmation must not append a feedback event");
  expect(attemptRow(teamID, contention.idempotencyKey).count === 1, "concurrent Dream confirmation must persist only the admitted terminal attempt");

  return {
    mode: name,
    dream_statement: scenarios.completed.statement,
    completed_submission_id: completedSubmissionID,
    replay_reused_submission: true,
    unsupported_relationship_warning: true,
    security_rejection_reviewable: true,
    failed_reviewable: true,
    contention_busy_reviewable: true,
    independent_evidence_provenance: true,
  };
}

function assertDreamRetryGuidance(result, scenario, label, expectedAction = "retry_dream_feedback", expect) {
  const error = result?.errors?.[0];
  expect(error?.next_action === expectedAction, `${label} Dream terminal result must advertise ${expectedAction}`);
  if (expectedAction === "retry_same_request") {
    expect(typeof error?.remediation === "string" && error.remediation.includes("same idempotency_key"), `${label} Dream terminal result must advertise same-key remediation`);
    return;
  }
  expect(typeof error?.remediation === "string" && error.remediation.includes("resolve_dream_feedback"), `${label} Dream terminal result must name resolve_dream_feedback in remediation`);
  expect(error.remediation.includes("idempotency_key"), `${label} Dream terminal result must name idempotency_key in remediation`);
  expect(error.remediation.includes(scenario.hypothesisID), `${label} Dream terminal result must use the canonical Hypothesis ID in remediation`);
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
  const labels = ["completed", "unsupported", "security", "failed", "contention"];
  const scenarios = {};
  for (const label of labels) {
    const hypothesisID = randomUUID();
    const statement = `Dream ${label}: Dense-Mem may use PostgreSQL for durable memory.`;
    const evidence = label === "unsupported"
      ? `[fixture-fault:no-supported] Dense-Mem uses PostgreSQL; independent ${label} evidence.`
      : label === "security"
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

function hypothesisIngestCount(teamID, hypothesisID) {
  return Number(postgresQuery(`
    SELECT count(DISTINCT ingest_id)::text
    FROM evidence_fragments
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND metadata->>'hypothesis_id' = ${sqlLiteral(hypothesisID)}
  `) || 0);
}

function attemptRow(teamID, idempotencyKey) {
  const count = Number(postgresQuery(`
    SELECT count(*)::text
    FROM remember_attempts
    WHERE team_id = ${sqlLiteral(teamID)}::uuid AND idempotency_key = ${sqlLiteral(idempotencyKey)}
  `) || 0);
  if (count === 0) return { outcome: "", count };
  const outcome = postgresQuery(`
    SELECT COALESCE(outcome, '')
    FROM remember_attempts
    WHERE team_id = ${sqlLiteral(teamID)}::uuid AND idempotency_key = ${sqlLiteral(idempotencyKey)}
    ORDER BY created_at DESC, attempt_id DESC LIMIT 1
  `) || "";
  return { outcome, count };
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
