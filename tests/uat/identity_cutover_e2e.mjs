#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const profileID = requiredEnv("DENSE_MEM_E2E_PROFILE_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const runID = `identity-cutover-${Date.now()}`;
let rpcID = 0;

const baseline = postgresRow(`
  SELECT concat(
    p.id::text, '|', COALESCE(c.id::text, ''), '|',
    COALESCE(a.legacy_owner_id::text, ''), '|',
    COALESCE(m.id::text, ''), '|', COALESCE(m.team_admin::text, 'false')
  )
  FROM team_profiles p
  LEFT JOIN credentials c ON c.legacy_profile_id = p.id
  LEFT JOIN ownership_aliases a ON a.team_id = p.team_id AND a.legacy_owner_id = p.id
  LEFT JOIN team_memberships m ON m.legacy_profile_id = p.id
  WHERE p.id = ${sqlLiteral(profileID)}::uuid
  LIMIT 1;
`);
const [stableProfileID, credentialID, aliasOwnerID, membershipID, teamAdmin] = baseline;
if (stableProfileID !== profileID || credentialID !== profileID || aliasOwnerID !== profileID || !membershipID || teamAdmin !== "true") {
  throw new Error("legacy profile did not retain stable credential, alias, and membership IDs");
}

const listed = await mcpList(apiKey);
if (!listed.tools?.some((tool) => tool.name === "remember")) throw new Error("canonical credential could not authenticate against MCP");

const sameTeam = await createProfile(teamID, `${runID}-same-team`);
const otherTeam = await createTeam(`${runID}-other-team`);
const crossTeam = await createProfile(otherTeam.id, `${runID}-cross-team`);

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

postgresQuery(`
  SELECT set_config('app.tx_mode', 'system', true);
  UPDATE team_profiles
  SET scopes = ARRAY['read']
  WHERE id = ${sqlLiteral(profileID)}::uuid;
`);
const reconciled = postgresRow(`
  SELECT concat(
    c.scopes::text, '|', m.maximum_grants::text, '|',
    (SELECT count(*) FROM membership_grants g WHERE g.membership_id = m.id AND g.grant_name = 'read'), '|',
    (SELECT count(*) FROM membership_grants g WHERE g.membership_id = m.id AND g.grant_name = 'write')
  )
  FROM credentials c
  JOIN team_memberships m ON m.actor_identity_id = c.actor_identity_id AND m.team_id = c.team_id
  WHERE c.id = ${sqlLiteral(profileID)}::uuid;
`);
const [credentialScopes, membershipMaximum, readGrant, writeGrant] = reconciled;
if (credentialScopes !== "{read}" || membershipMaximum !== "{read}" || readGrant !== "1" || writeGrant !== "0") {
  throw new Error(`legacy write did not reconcile canonical grants: ${reconciled}`);
}

console.log(JSON.stringify({
  status: "ok",
  scenario: "identity_cutover",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  stable_ids: true,
  manager_backfill: true,
  owner_authentication: true,
  same_team_owner_isolation: true,
  cross_team_isolation: true,
  legacy_write_reconciled: true,
}, null, 2));

function rememberInput() {
  const content = "Identity bridge uses credentials.";
  const subject = "Identity bridge";
  const predicate = "uses";
  const object = "credentials";
  const subjectStart = 0;
  const predicateStart = subject.length + 1;
  const objectStart = predicateStart + predicate.length + 1;
  return {
    evidence: [{ content, source_type: "document", source: `${runID}:source`, source_group: runID, idempotency_key: `${runID}:evidence` }],
    relationships: [{
      ref: `${runID}:relationship`,
      subject: { name: subject, entity_kind: "concept", span: { evidence_index: 0, start: subjectStart, end: subjectStart + subject.length } },
      predicate: { proposed_key: predicate, surface: predicate, span: { evidence_index: 0, start: predicateStart, end: predicateStart + predicate.length } },
      object: { entity: { name: object, entity_kind: "concept", span: { evidence_index: 0, start: objectStart, end: objectStart + object.length } } },
      polarity: "+", modality: "statement", supports: [{ evidence_index: 0, start: 0, end: Array.from(content).length }],
    }],
  };
}

async function createTeam(name) {
  const response = await controlJSON("/control/api/teams", { method: "POST", body: { name, description: "identity cutover e2e" } });
  const id = requiredString(response.data?.id, "team id");
  return { id };
}

async function createProfile(targetTeamID, name) {
  const response = await controlJSON(`/control/api/teams/${targetTeamID}/profiles`, { method: "POST", body: { name, scopes: ["read", "write"], rate_limit: 300 } });
  return { apiKey: requiredString(response.data?.api_key, "api key"), id: requiredString(response.data?.key?.id, "profile id") };
}

async function mcpSuccess(key, name, args) {
  const response = await mcpRaw(key, name, args);
  if (response.error || response.result === undefined) {
    throw new Error(`MCP ${name} returned a bounded error: ${response.error?.code ?? "missing"} ${response.error?.message ?? ""}`.trim());
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} did not return JSON content`);
  return JSON.parse(text);
}

async function mcpList(key) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }),
  });
  const text = await response.text();
  const payload = text ? JSON.parse(text) : {};
  if (!response.ok || payload.error || !payload.result) throw new Error("MCP tools/list returned a bounded error");
  return payload.result;
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

function postgresQuery(sql) { return postgresRow(sql).join("|"); }
function postgresRow(sql) {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|" -c "$1"', "identity-cutover-e2e", sql], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) throw new Error("postgres identity fixture failed");
  return result.stdout.trim().split("|");
}
function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }
function requiredString(value, field) { if (typeof value !== "string" || !value.trim()) throw new Error(`${field} missing`); return value; }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
