#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const credentialID = requiredEnv("DENSE_MEM_E2E_CREDENTIAL_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const upgradeTeamID = requiredEnv("DENSE_MEM_E2E_UPGRADE_TEAM_ID");
const upgradeProfileID = requiredEnv("DENSE_MEM_E2E_UPGRADE_PROFILE_ID");
const upgradeCredential = requiredEnv("DENSE_MEM_E2E_UPGRADE_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const runID = `identity-cleanup-${Date.now()}`;
let rpcID = 0;

const catalog = postgresRow(`
  SELECT concat(
    (to_regclass('public.team_profiles') IS NULL)::text, '|',
    (to_regclass('public.identity_compatibility_state') IS NULL)::text, '|',
    (to_regprocedure('public.dense_mem_sync_legacy_profile_identity()') IS NULL)::text, '|',
    (to_regprocedure('public.dense_mem_sync_sso_identity()') IS NULL)::text, '|',
    NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public' AND table_name = 'credentials' AND column_name = 'legacy_profile_id'
    ), '|',
    NOT EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema = 'public' AND table_name = 'team_memberships' AND column_name = 'legacy_profile_id'
    ), '|',
    (to_regclass('public.semantic_team_refs') IS NULL)::text, '|',
    (to_regclass('public.semantic_profile_refs') IS NULL)::text, '|',
    (to_regclass('public.embedding_config') IS NULL)::text, '|',
    EXISTS (
      SELECT 1 FROM goose_db_version
      WHERE version_id = 2026081602 AND is_applied
    ), '|',
    (
      SELECT count(*) = 36
      FROM pg_constraint AS constraint_state
      WHERE constraint_state.contype = 'f'
        AND constraint_state.conrelid::regclass::text = ANY(ARRAY[
          'dream_cycle_runs', 'embedding_jobs', 'entity_correction_events', 'entity_correction_plans',
          'entity_names', 'entity_resolution_events', 'evidence_fragments', 'evidence_lifecycle_operations',
          'evidence_quarantines', 'evidence_security_events', 'evidence_security_signals',
          'evidence_source_revisions', 'evidence_sources', 'hypotheses', 'hypothesis_feedback_events',
          'knowledge_ingests', 'placement_items', 'placement_outcomes', 'placement_runs',
          'relationship_conflict_derived_evidence_tasks', 'relationship_conflict_events',
          'relationship_conflict_evidence_derivations', 'relationship_correction_submissions',
          'relationship_cross_references', 'relationship_evidence_supports', 'relationship_observations',
          'relationship_records', 'relationship_support_decision_events', 'relationship_transition_events',
          'review_tasks', 'search_documents', 'verification_events'
        ]::text[])
        AND constraint_state.confrelid = 'ownership_aliases'::regclass
        AND constraint_state.convalidated
        AND constraint_state.confdeltype = 'r'
    ), '|',
    (
      SELECT count(*) = 5
      FROM pg_constraint AS constraint_state
      WHERE constraint_state.contype = 'f'
        AND constraint_state.conrelid::regclass::text = ANY(ARRAY[
          'community_snapshot_runs', 'entity_records', 'search_projection_generations',
          'team_predicate_definitions', 'value_records'
        ]::text[])
        AND constraint_state.confrelid = 'teams'::regclass
        AND constraint_state.convalidated
        AND constraint_state.confdeltype = 'r'
    )
  );
`);
if (catalog.some((value) => value !== "true" && value !== "t")) throw new Error(`identity cleanup catalog is incomplete: ${catalog}`);

await mcpList(upgradeCredential);
const upgradeState = postgresRow(`
  SELECT concat(
    c.id::text, '|', alias.legacy_owner_id::text, '|', c.scopes::text, '|',
    (SELECT count(*) FROM ownership_aliases WHERE team_id = c.team_id), '|',
    (SELECT count(*) FROM usage_metric_buckets WHERE key_id = c.id), '|',
    (SELECT count(*) FROM user_portal_sessions WHERE key_id = c.id), '|',
    (
      SELECT count(*)
      FROM sso_sessions AS session
      JOIN team_memberships AS membership ON membership.id = session.membership_id
      WHERE membership.team_id = c.team_id AND membership.sso_provider_id IS NOT NULL
    )
  )
  FROM credentials AS c
  JOIN ownership_aliases AS alias
    ON alias.team_id = c.team_id AND alias.legacy_owner_id = c.id
  WHERE c.team_id = ${sqlLiteral(upgradeTeamID)}::uuid
    AND c.id = ${sqlLiteral(upgradeProfileID)}::uuid;
`);
if (
  upgradeState[0] !== upgradeProfileID
  || upgradeState[1] !== upgradeProfileID
  || upgradeState[2] !== "{read}"
  || Number(upgradeState[3]) < 2
  || Number(upgradeState[4]) < 1
  || Number(upgradeState[5]) < 1
  || Number(upgradeState[6]) < 1
) {
  throw new Error(`bridge upgrade did not retain its seeded identity history: ${upgradeState}`);
}

const baseline = postgresRow(`
  SELECT concat(
    c.id::text, '|', a.legacy_owner_id::text, '|', m.id::text, '|',
    m.team_admin::text, '|', c.status, '|', actor.active::text
  )
  FROM credentials c
  JOIN ownership_aliases a
    ON a.team_id = c.team_id AND a.legacy_owner_id = c.id AND a.credential_id = c.id
  JOIN team_memberships m
    ON m.actor_identity_id = c.actor_identity_id AND m.team_id = c.team_id
  JOIN actor_identities actor ON actor.id = c.actor_identity_id
  WHERE c.id = ${sqlLiteral(credentialID)}::uuid
  LIMIT 1;
`);
const [baselineCredentialID, aliasOwnerID, membershipID, teamAdmin, credentialStatus, actorActive] = baseline;
if (baselineCredentialID !== credentialID || aliasOwnerID !== credentialID || !membershipID || teamAdmin !== "true" || credentialStatus !== "active" || actorActive !== "true") {
  throw new Error("seed key did not retain a stable active credential, alias, and membership");
}

await mcpList(apiKey);
const portalCookies = await createPortalSession(apiKey);

const sameTeam = await createCredential(teamID, `${runID}-same-team`);
const otherTeam = await createTeam(`${runID}-other-team`);
const crossTeam = await createCredential(otherTeam.id, `${runID}-cross-team`);

const receipt = await mcpSuccess(apiKey, "remember", rememberInput());
const submissionID = requiredString(receipt.submission_id, "submission_id");
const ownerStatus = await mcpSuccess(apiKey, "get_submission_status", { submission_id: submissionID });
if (ownerStatus.submission_id !== submissionID) throw new Error("owner could not read its staged submission");
for (const [label, key] of [["same-team other owner", sameTeam.apiKey], ["cross-team owner", crossTeam.apiKey]]) {
  const isolated = await mcpRaw(key, "get_submission_status", { submission_id: submissionID });
  if (!isolated.error || isolated.result !== undefined || JSON.stringify(isolated).includes(submissionID)) {
    throw new Error(`${label} crossed submission ownership boundary`);
  }
}

const rotated = await controlJSON(`/control/api/teams/${teamID}/credentials/${sameTeam.id}/rotate`, {
  method: "POST",
  body: { name: `${runID}-rotated`, rate_limit: 300 },
});
const rotatedKey = requiredString(rotated.data?.api_key, "rotated api key");
if (rotated.data?.credential?.id !== sameTeam.id || rotatedKey === sameTeam.apiKey) throw new Error("rotation changed the stable credential ID or reused bearer material");
if (await mcpListStatus(sameTeam.apiKey) !== 401) throw new Error("old bearer remained active after rotation");
await mcpList(rotatedKey);

await controlJSON(`/control/api/teams/${teamID}/credentials/${sameTeam.id}`, { method: "DELETE" });
if (await mcpListStatus(rotatedKey) !== 401) throw new Error("deleted credential remained active");
const tombstone = postgresRow(`
  SELECT concat(c.status, '|', (c.revoked_at IS NOT NULL)::text, '|', count(a.legacy_owner_id)::text)
  FROM credentials c
  LEFT JOIN ownership_aliases a
    ON a.team_id = c.team_id AND a.legacy_owner_id = c.id
  WHERE c.id = ${sqlLiteral(sameTeam.id)}::uuid
  GROUP BY c.status, c.revoked_at;
`);
if (tombstone.join("|") !== "disabled|true|1") throw new Error(`deleted credential did not retain its canonical tombstone: ${tombstone}`);

for (const [objectName, statement] of [
  ["team_profiles", "INSERT INTO team_profiles (id) VALUES (gen_random_uuid())"],
  ["semantic_team_refs", `INSERT INTO semantic_team_refs (team_id) VALUES (${sqlLiteral(teamID)}::uuid)`],
  ["semantic_profile_refs", `INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(credentialID)}::uuid)`],
  ["embedding_config", "SELECT * FROM embedding_config LIMIT 1"],
]) {
  if (postgresSucceeds(statement)) {
    throw new Error(`post-cleanup ${objectName} access unexpectedly succeeded`);
  }
}

restartServer();
await waitForReady();
await mcpList(apiKey);
const session = await userJSON("/ui/api/session", { headers: { Cookie: portalCookies } });
if (session.data?.credential?.id !== credentialID || session.data?.membership?.team_id !== teamID) {
  throw new Error("portal session did not survive cleanup restart");
}

await waitForUsage(credentialID);
const retainedHistory = postgresRow(`
  SELECT concat(
    (SELECT count(*) FROM user_portal_sessions WHERE key_id = ${sqlLiteral(credentialID)}::uuid), '|',
    (SELECT count(*) FROM usage_metric_buckets WHERE key_id = ${sqlLiteral(credentialID)}::uuid)
  );
`);
if (Number(retainedHistory[0]) < 1 || Number(retainedHistory[1]) < 1) throw new Error(`credential history was not retained: ${retainedHistory}`);

console.log(JSON.stringify({
  status: "ok",
  scenario: "identity_cleanup",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  clean_catalog: true,
  direct_foreign_keys: true,
  transitional_objects_removed: true,
  bridge_seed_authentication: true,
  bridge_seed_history: true,
  stable_ids: true,
  owner_authentication: true,
  same_team_owner_isolation: true,
  cross_team_isolation: true,
  rotation_and_tombstone: true,
  legacy_write_rejected: true,
  portal_restart: true,
  retained_usage_history: true,
}, null, 2));

function rememberInput() {
  const content = "Identity cleanup uses canonical credentials.";
  const subject = "Identity cleanup";
  const predicate = "uses";
  const object = "canonical credentials";
  return {
    idempotency_key: `${runID}:batch`,
    evidence: [{ content, source_type: "document", source: `${runID}:source`, source_group: runID }],
    relationships: [{
      ref: `${runID}:relationship`,
      subject: { name: subject, entity_kind: "concept" },
      predicate: { proposed_key: predicate },
      object: { entity: { name: object, entity_kind: "concept" } },
      polarity: "+", evidence_indices: [0],
    }],
  };
}

async function createPortalSession(key) {
  const response = await fetch(`${userURL}/ui/api/session`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ remember: true }),
  });
  if (!response.ok) throw new Error(`portal session creation returned HTTP ${response.status}`);
  const setCookies = typeof response.headers.getSetCookie === "function"
    ? response.headers.getSetCookie()
    : [response.headers.get("set-cookie") ?? ""];
  const pairs = setCookies.flatMap((header) => [...header.matchAll(/(?:^|,\s*)(dense_mem_ui_(?:session|csrf))=([^;,]+)/g)].map((match) => `${match[1]}=${match[2]}`));
  if (!pairs.some((pair) => pair.startsWith("dense_mem_ui_session=")) || !pairs.some((pair) => pair.startsWith("dense_mem_ui_csrf="))) {
    throw new Error("portal session cookies missing");
  }
  return pairs.join("; ");
}

async function createTeam(name) {
  const response = await controlJSON("/control/api/teams", { method: "POST", body: { name, description: "identity cleanup e2e" } });
  return { id: requiredString(response.data?.id, "team id") };
}

async function createCredential(targetTeamID, name) {
  const response = await controlJSON(`/control/api/teams/${targetTeamID}/credentials`, { method: "POST", body: { name, scopes: ["read", "write"], rate_limit: 300 } });
  return { apiKey: requiredString(response.data?.api_key, "api key"), id: requiredString(response.data?.credential?.id, "credential id") };
}

async function mcpSuccess(key, name, args) {
  const response = await mcpRaw(key, name, args);
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} returned a bounded error`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} did not return JSON content`);
  return JSON.parse(text);
}

async function mcpList(key) {
  const { status, payload } = await mcpListResponse(key);
  if (status !== 200 || payload.error || !payload.result) throw new Error("MCP tools/list returned a bounded error");
  return payload.result;
}

async function mcpListStatus(key) {
  return (await mcpListResponse(key)).status;
}

async function mcpListResponse(key) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }),
  });
  const text = await response.text();
  return { status: response.status, payload: text ? JSON.parse(text) : {} };
}

async function mcpRaw(key, name, args) {
  const response = await fetch(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }) });
  const text = await response.text();
  return text ? JSON.parse(text) : {};
}

async function controlJSON(path, options = {}) {
  const response = await fetch(`${controlURL}${path}`, { method: options.method ?? "GET", headers: { Authorization: `Bearer ${controlToken}`, Accept: "application/json", "Content-Type": "application/json" }, body: options.body ? JSON.stringify(options.body) : undefined });
  const text = await response.text();
  if (!response.ok) throw new Error(`control HTTP ${response.status} response body redacted`);
  return text ? JSON.parse(text) : {};
}

async function userJSON(path, options = {}) {
  const response = await fetch(`${userURL}${path}`, { headers: { Accept: "application/json", ...(options.headers ?? {}) } });
  const text = await response.text();
  if (!response.ok) throw new Error(`user HTTP ${response.status} response body redacted`);
  return text ? JSON.parse(text) : {};
}

function restartServer() {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "restart", "server"], { cwd: repositoryRoot(), encoding: "utf8" });
  if (result.status !== 0) throw new Error("server restart failed");
}

async function waitForReady() {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${userURL}/ready`);
      if (response.ok) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("server did not become ready after restart");
}

async function waitForUsage(keyID) {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const count = Number(postgresRow(`SELECT count(*) FROM usage_metric_buckets WHERE key_id = ${sqlLiteral(keyID)}::uuid;`)[0]);
    if (count > 0) return;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("usage history was not persisted");
}

function postgresRow(sql) {
  const result = postgresCommand(sql);
  if (result.status !== 0) throw new Error("postgres identity fixture failed");
  return result.stdout.trim().split("|");
}

function postgresSucceeds(sql) {
  return postgresCommand(sql).status === 0;
}

function postgresCommand(sql) {
  return spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|" -c "$1"', "identity-cleanup-e2e", sql], { cwd: repositoryRoot(), encoding: "utf8" });
}

function repositoryRoot() { return fileURLToPath(new URL("../..", import.meta.url)); }
function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }
function requiredString(value, field) { if (typeof value !== "string" || !value.trim()) throw new Error(`${field} missing`); return value; }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
