#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");

let rpcID = 0;
const scheduledAt = nextScheduledUTCMinute();
const runDate = formatDate(scheduledAt);
const ownerProfileID = await userProfileID();
seedSchedulerInputs(ownerProfileID);

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
await assertSystemRun(scheduledRun.run_id);

const controlDreams = await controlJSON(`/teams/${teamID}/dreams?limit=10`);
const scheduledDream = findDream(controlDreams.data?.items, scheduledRun.run_id, "control portal API");
const hypothesisID = scheduledDream.dream_id;
const statement = scheduledDream.hypothesis;
const reviewer = await createTeamProfile("Team Dreaming E2E reviewer");
const feedback = await mcpTool(reviewer.apiKey, "resolve_dream_feedback", {
  hypothesis_id: hypothesisID,
  decision: "reinforce",
  reason: "Independent same-team review confirmed this output remains useful.",
});
assertEqual(feedback.hypothesis_id, hypothesisID, "feedback hypothesis");
assertEqual(feedback.status, "reinforced", "feedback status");
await assertFeedbackActor(hypothesisID, reviewer.profileID);

assertContainsDream(controlDreams.data?.items, hypothesisID, statement, "control portal API");
const userDreams = await userJSON("/ui/api/dreams?limit=10");
assertContainsDream(userDreams.data?.items, hypothesisID, statement, "user portal API");
const listOutput = await mcpTool(apiKey, "list_dreams", { limit: 10 });
assertContainsDream(listOutput.dreams, hypothesisID, statement, "MCP list_dreams");

console.log(JSON.stringify({
  status: "ok",
  team_id: teamID,
  run_id: scheduledRun.run_id,
  hypothesis_id: hypothesisID,
  statement,
  scheduled_at: scheduledAt.toISOString(),
}, null, 2));

function nextScheduledUTCMinute() {
  const target = new Date(Date.now() + 4 * 60_000);
  target.setUTCSeconds(0, 0);
  return target;
}

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
  });
}

async function waitForScheduledRun() {
  let lastRuns = [];
  for (let attempt = 0; attempt < 85; attempt += 1) {
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
  throw new Error(`timed out waiting for scheduled team run at ${scheduledAt.toISOString()}: ${JSON.stringify(lastRuns)}`);
}

async function assertSystemRun(runID) {
  const owner = postgresQuery(`
    SELECT COALESCE(initiated_by_profile_id::text, '')
    FROM dream_cycle_runs
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND run_id = ${sqlLiteral(runID)}::uuid
  `);
  assertEqual(owner, "", "scheduled run initiator");
}

async function userProfileID() {
  const session = await userJSON("/ui/api/session");
  const profileID = session.data?.key?.id;
  if (typeof profileID !== "string" || !profileID) {
    throw new Error(`user session did not return a profile ID: ${JSON.stringify(session)}`);
  }
  return profileID;
}

function seedSchedulerInputs(ownerProfileID) {
  const subjectID = randomUUID();
  const usesObjectID = randomUUID();
  const worksOnSubjectID = randomUUID();
  const worksOnObjectID = randomUUID();
  const usesRelationshipID = randomUUID();
  const worksOnRelationshipID = randomUUID();
  const candidateIngestID = randomUUID();
  const candidateObservationID = randomUUID();
  postgresQuery(`
    INSERT INTO semantic_team_refs (team_id)
    VALUES (${sqlLiteral(teamID)}::uuid)
    ON CONFLICT (team_id) DO NOTHING;

    INSERT INTO semantic_profile_refs (team_id, profile_id)
    VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid)
    ON CONFLICT (team_id, profile_id) DO NOTHING;

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
    WHERE (predicate_key, version) IN (('uses', 1), ('works_on', 1))
    ON CONFLICT (team_id, predicate_key, version) DO NOTHING;

    INSERT INTO entity_records (team_id, entity_id, entity_kind)
    VALUES
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(subjectID)}::uuid, 'project'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(usesObjectID)}::uuid, 'product'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(worksOnSubjectID)}::uuid, 'project'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(worksOnObjectID)}::uuid, 'project');

    INSERT INTO knowledge_ingests (
      team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
      source_summary, status, proposal, metadata, completed_at
    ) VALUES (
      ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(candidateIngestID)}::uuid,
      ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-candidate',
      'sha256:compose-e2e-candidate', 'Candidate relationship for scheduled dreaming',
      'completed', '{}'::jsonb, '{}'::jsonb, now()
    );

    INSERT INTO relationship_records (
      team_id, relationship_id, owner_profile_id, semantic_group_key,
      subject_entity_id, predicate_key, predicate_version, object_entity_id,
      relationship_kind, current_cardinality, status, support_count,
      source_group_count
    ) VALUES
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(usesRelationshipID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-uses',
        ${sqlLiteral(subjectID)}::uuid, 'uses', 1, ${sqlLiteral(usesObjectID)}::uuid,
        'state', 'many', 'pending_evidence', 0, 0
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(worksOnRelationshipID)}::uuid,
        ${sqlLiteral(ownerProfileID)}::uuid, 'compose-e2e-works-on',
        ${sqlLiteral(worksOnSubjectID)}::uuid, 'works_on', 1, ${sqlLiteral(worksOnObjectID)}::uuid,
        'state', 'many', 'active', 1, 1
      );

    INSERT INTO relationship_observations (
      team_id, observation_id, relationship_id, ingest_id, owner_profile_id,
      subject_ref, original_predicate, object_ref, subject_entity_id,
      predicate_key, predicate_version, object_entity_id, evidence, metadata
    ) VALUES (
      ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(candidateObservationID)}::uuid,
      ${sqlLiteral(usesRelationshipID)}::uuid, ${sqlLiteral(candidateIngestID)}::uuid,
      ${sqlLiteral(ownerProfileID)}::uuid, 'Compose E2E project', 'uses',
      'Compose E2E product', ${sqlLiteral(subjectID)}::uuid, 'uses', 1,
      ${sqlLiteral(usesObjectID)}::uuid, '[]'::jsonb, '{}'::jsonb
    );

    INSERT INTO verification_events (
      team_id, observation_id, owner_profile_id, evidence_verdict,
      confidence, rationale, model, response_hash, metadata
    ) VALUES (
      ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(candidateObservationID)}::uuid,
      ${sqlLiteral(ownerProfileID)}::uuid, 'insufficient', 0.4,
      'The scheduled candidate requires independent evidence.',
      'compose-e2e', 'sha256:compose-e2e-verification', '{}'::jsonb
    );
  `);
}

async function createTeamProfile(name) {
  const payload = await controlJSON(`/teams/${teamID}/profiles`, {
    method: "POST",
    body: JSON.stringify({
      name,
      role: "member",
      scopes: ["read", "write"],
      rate_limit: 300,
    }),
  });
  const createdAPIKey = payload.data?.api_key;
  const profileID = payload.data?.key?.id;
  if (typeof createdAPIKey !== "string" || !createdAPIKey || typeof profileID !== "string" || !profileID) {
    throw new Error(`team reviewer creation did not return a key and profile ID: ${JSON.stringify(payload)}`);
  }
  return { apiKey: createdAPIKey, profileID };
}

async function assertFeedbackActor(hypothesisID, profileID) {
  const row = postgresQuery(`
    SELECT
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
  const [createdBy, actor, decision] = row.split("|");
  assertEqual(createdBy, "", "team hypothesis creator");
  assertEqual(actor, profileID, "feedback actor");
  assertEqual(decision, "reinforce", "feedback decision");
}

async function controlJSON(path, options = {}) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  });
}

async function userJSON(path) {
  return httpJSON(`${userURL}${path}`, {
    headers: {
      Authorization: `Bearer ${apiKey}`,
    },
  });
}

async function mcpTool(token, name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
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
    throw new Error(`MCP ${name} error: ${JSON.stringify(response.error)}`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} result missing text: ${JSON.stringify(response)}`);
  }
  return JSON.parse(text);
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: ${redactHTTPBody(text)}`);
  }
  return text ? JSON.parse(text) : {};
}

function postgresQuery(sql) {
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
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "team-dreaming-e2e",
    sql,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`postgres query failed (${result.status}): ${result.stderr || result.stdout}`);
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
}

function assertEqual(actual, expected, label) {
  if (actual !== expected) {
    throw new Error(`${label} = ${JSON.stringify(actual)}; want ${JSON.stringify(expected)}`);
  }
}

function sqlLiteral(value) {
  return `'${String(value).replace(/'/g, "''")}'`;
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
