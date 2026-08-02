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
const seeded = seedSchedulerInputs(ownerProfileID);

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
assertEvidenceDerivedDream(assertContainsDream(userDreams.data?.items, hypothesisID, statement, "user portal API"), seeded, "user portal API");
const listOutput = await mcpTool(apiKey, "list_dreams", { limit: 10 });
assertContainsDream(listOutput.dreams, hypothesisID, statement, "MCP list_dreams");
const getOutput = await mcpTool(apiKey, "get_dream", { hypothesis_id: hypothesisID });
assertEvidenceDerivedDream(getOutput.hypothesis, seeded, "MCP get_dream");

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
        ${sqlLiteral(firstQuote)}, 'sha256:compose-e2e-first', 'manual', 'primary', 'compose-e2e-first'
      ),
      (
        ${sqlLiteral(teamID)}::uuid, ${sqlLiteral(secondFragmentID)}::uuid,
        ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 1,
        ${sqlLiteral(secondQuote)}, 'sha256:compose-e2e-second', 'manual', 'primary', 'compose-e2e-second'
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
  `);
  return {
    firstRelationshipID,
    secondRelationshipID,
    firstQuote,
    secondQuote,
  };
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
  let response;
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      response = await fetch(url, options);
      break;
    } catch (error) {
      if (attempt === 2) {
        throw error;
      }
      await delay(1_000);
    }
  }
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
  return dream;
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
