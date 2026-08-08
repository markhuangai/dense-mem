#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { randomUUID } from "node:crypto";

import { nextScheduledUTCMinute } from "./team_dreaming_schedule.mjs";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const prometheusURL = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const communityRunWaitAttempts = 140;

let rpcID = 0;
const runID = `community-recall-e2e-${Date.now()}`;
const scheduledAt = nextScheduledUTCMinute(Date.now(), 4);
const runDate = scheduledAt.toISOString().slice(0, 10);
const ownerProfileID = await userProfileID();
const seeded = seedCommunityGraph(ownerProfileID);

await updateControlConfig("/config/general", [{ key: "APP_TIMEZONE", value: "UTC" }]);
await updateControlConfig("/config/community-detection", [
  { key: "COMMUNITY_DETECTION_ENABLED", value: "true" },
  { key: "COMMUNITY_DETECTION_START_TIME_LOCAL", value: scheduledAt.toISOString().slice(11, 16) },
  { key: "COMMUNITY_DETECTION_MAX_CONCURRENCY", value: "1" },
  { key: "COMMUNITY_DETECTION_JITTER_SECONDS", value: "0" },
]);

const completed = await waitForCommunityRun();
if (completed.status !== "completed" || completed.algorithm_kind !== undefined && completed.algorithm_kind !== "louvain") {
  throw new Error(`community run did not complete with Louvain: ${JSON.stringify(completed)}`);
}
const communityStatus = await controlJSON(`/teams/${teamID}/community/status`);
if (communityStatus.data?.effective_config?.enabled !== true || Number(communityStatus.data?.current_community_count ?? 0) < 1) {
  throw new Error(`community status did not expose the completed snapshot: ${JSON.stringify(communityStatus)}`);
}

const recalled = await mcpSuccess("recall_memory", {
  query: "Dense-Mem Runtime PostgreSQL",
  limit: 5,
  community_limit: 3,
  community_relationship_limit: 2,
});
assertCommunityContract(recalled);
const coveredEvidenceIDs = [...new Set(recalled.related_communities.flatMap((community) => community.relationships.flatMap((relationship) => relationship.evidence_ids ?? [])))];
if (coveredEvidenceIDs.length === 0) throw new Error("community recall returned no nested evidence IDs for suppression coverage");
const covered = await mcpSuccess("recall_memory", { query: "Dense-Mem Runtime PostgreSQL", limit: 5, community_limit: 3, known_evidence_ids: coveredEvidenceIDs });
assertKnownEvidenceSuppressed(covered, coveredEvidenceIDs);
const fullyCovered = await mcpSuccess("recall_memory", {
  query: "Dense-Mem Runtime PostgreSQL",
  limit: 5,
  community_limit: 3,
  known_evidence_ids: seeded.fragments,
});
if ((fullyCovered.related_communities ?? []).length !== 0 || (fullyCovered.related_relationships ?? []).length !== 0) {
  throw new Error("all known evidence did not suppress the community and fallback groups");
}

const disabled = await mcpSuccess("recall_memory", {
  query: "Dense-Mem Runtime PostgreSQL",
  limit: 5,
  community_limit: 0,
});
if (!Array.isArray(disabled.related_communities) || disabled.related_communities.length !== 0) {
  throw new Error("community_limit=0 did not disable community recall");
}
if ((disabled.degradations ?? []).some((item) => item?.code === "community_temporal_not_supported")) {
  throw new Error("disabled community recall reported an unrelated temporal degradation");
}

const temporal = await mcpSuccess("recall_memory", {
  query: "Dense-Mem Runtime PostgreSQL",
  limit: 5,
  community_limit: 3,
  known_at: "2000-01-01T00:00:00Z",
});
if (!Array.isArray(temporal.related_communities) || temporal.related_communities.length !== 0 ||
    !(temporal.degradations ?? []).some((item) => item?.code === "community_temporal_not_supported")) {
  throw new Error("temporal recall did not skip current-only communities with a bounded degradation");
}

const isolatedTeam = await createIsolatedTeam();
const isolated = await mcpSuccessWithKey(isolatedTeam.apiKey, "recall_memory", {
  query: "Dense-Mem Runtime PostgreSQL",
  limit: 5,
  community_limit: 3,
});
if (!Array.isArray(isolated.related_communities) || isolated.related_communities.length !== 0) {
  throw new Error("community snapshot leaked across team visibility boundary");
}

const communityMetric = await waitForPrometheusMetric("sum(densemem_community_runs_total)");
const summaryMetric = await waitForPrometheusMetric("sum(densemem_community_summaries_total)");
const recallMetric = await waitForPrometheusMetric("sum(densemem_community_recall_total)");
if (communityMetric < 1 || summaryMetric < 1 || recallMetric < 1) {
  throw new Error(`community telemetry did not record the feature: ${communityMetric}/${summaryMetric}/${recallMetric}`);
}

const currentRecord = postgresQuery(`
  SELECT concat(
    count(*) FILTER (WHERE record.status = 'current'), '|',
    count(DISTINCT record.logical_community_id) FILTER (WHERE record.status = 'current'), '|',
    min(algorithm_kind), '|', min(algorithm_version)
  )
  FROM community_records AS record
  JOIN community_snapshot_runs AS run
    ON run.team_id = record.team_id AND run.run_id = record.run_id
  WHERE record.team_id = ${sqlLiteral(teamID)}::uuid
    AND run.window_key = ${sqlLiteral(runDate)}
`);
const [currentCount, logicalCount, algorithmKind, algorithmVersion] = currentRecord.split("|");
if (Number(currentCount) < 1 || Number(logicalCount) < 1 || algorithmKind !== "louvain" || algorithmVersion !== "v2") {
  throw new Error(`persisted community snapshot is incomplete: ${currentRecord}`);
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  window_key: runDate,
  community_count: Number(currentCount),
  logical_community_count: Number(logicalCount),
  algorithm: `${algorithmKind}/${algorithmVersion}`,
  nested_relationship_limit: 2,
  temporal_skip: true,
  team_isolation: true,
  telemetry: { runs: communityMetric, summaries: summaryMetric, recalls: recallMetric },
}, null, 2));

async function waitForCommunityRun() {
  let latest = null;
  for (let attempt = 0; attempt < communityRunWaitAttempts; attempt += 1) {
    const response = await controlJSON(`/teams/${teamID}/community/status`);
    latest = response.data?.latest_run ?? null;
    if (latest?.window_key === runDate && latest.status === "completed" && Number(response.data?.current_community_count ?? 0) > 0) {
      return latest;
    }
    if (latest?.window_key === runDate && ["failed", "too_large", "cancelled"].includes(latest.status)) {
      throw new Error(`community run ended with ${latest.status}: ${JSON.stringify(latest)}`);
    }
    await delay(3_000);
  }
  throw new Error(`timed out waiting for community run at ${scheduledAt.toISOString()}: ${JSON.stringify(latest)}`);
}

function seedCommunityGraph(ownerProfileID) {
  const entities = ["Dense-Mem", "Runtime service", "PostgreSQL"].map(() => randomUUID());
  const relationships = [randomUUID(), randomUUID(), randomUUID()];
  const observations = [randomUUID(), randomUUID(), randomUUID()];
  const fragments = [randomUUID(), randomUUID(), randomUUID()];
  const verifications = [randomUUID(), randomUUID(), randomUUID()];
  const supports = [randomUUID(), randomUUID(), randomUUID()];
  const ingestID = randomUUID();
  const names = ["Dense-Mem", "Runtime service", "PostgreSQL"];
  const quotes = [
    "Dense-Mem uses the Runtime service for durable memory workflows.",
    "The Runtime service uses PostgreSQL for durable memory storage.",
    "PostgreSQL supports the Dense-Mem memory service.",
  ];
  const groups = ["community-e2e-dense-runtime", "community-e2e-runtime-postgres", "community-e2e-postgres-dense"];
  const inserts = relationships.map((relationshipID, index) => {
    const subject = entities[index];
    const object = entities[(index + 1) % entities.length];
    return `
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(relationshipID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid,
       ${sqlLiteral(groups[index])}, ${sqlLiteral(subject)}::uuid, 'uses', 1, ${sqlLiteral(object)}::uuid,
       'state', 'many', 'active', '+', 1, 1)`;
  }).join(",\n");
  const observationsSQL = relationships.map((relationshipID, index) => `
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(observations[index])}::uuid, ${sqlLiteral(relationshipID)}::uuid,
       ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, ${sqlLiteral(names[index])}, 'uses',
       ${sqlLiteral(names[(index + 1) % names.length])}, ${sqlLiteral(entities[index])}::uuid, 'uses', 1,
       ${sqlLiteral(entities[(index + 1) % entities.length])}::uuid,
       jsonb_build_array(jsonb_build_object('fragment_id', ${sqlLiteral(fragments[index])}, 'start', 0, 'end', char_length(${sqlLiteral(quotes[index])}))), '{}'::jsonb)`
  ).join(",\n");
  const verificationSQL = relationships.map((_, index) => `
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(verifications[index])}::uuid, ${sqlLiteral(observations[index])}::uuid,
       ${sqlLiteral(ownerProfileID)}::uuid, 'entailed', 0.98, 'compose community fixture', 'compose-e2e',
       ${sqlLiteral(`sha256:community-verification-${index}`)}, '{}'::jsonb)`
  ).join(",\n");
  const supportSQL = relationships.map((relationshipID, index) => `
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(supports[index])}::uuid, ${sqlLiteral(relationshipID)}::uuid,
       ${sqlLiteral(observations[index])}::uuid, ${sqlLiteral(verifications[index])}::uuid, ${sqlLiteral(fragments[index])}::uuid,
       ${sqlLiteral(ownerProfileID)}::uuid, ${sqlLiteral(`community-e2e-${index}`)}, 0,
       char_length(${sqlLiteral(quotes[index])}), ${sqlLiteral(quotes[index])}, 'primary', '{}'::jsonb)`
  ).join(",\n");
  const decisionSQL = relationships.map((relationshipID, index) => `
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(supports[index])}::uuid, ${sqlLiteral(relationshipID)}::uuid,
       ${sqlLiteral(ownerProfileID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, 'grant', 'compose community support', '{}'::jsonb)`
  ).join(",\n");
  postgresQuery(`
    INSERT INTO semantic_team_refs (team_id) VALUES (${sqlLiteral(teamID)}::uuid) ON CONFLICT DO NOTHING;
    INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid) ON CONFLICT DO NOTHING;
    INSERT INTO team_predicate_definitions (
      team_id, predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
      relationship_kind, current_cardinality, lifecycle_state, origin, metadata, created_at
    ) SELECT ${sqlLiteral(teamID)}::uuid, predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
      relationship_kind, current_cardinality, lifecycle_state, 'built_in', metadata, created_at
      FROM predicate_definitions WHERE predicate_key = 'uses' AND version = 1
      ON CONFLICT (team_id, predicate_key, version) DO NOTHING;
    INSERT INTO entity_records (team_id, entity_id, entity_kind) VALUES
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(entities[0])}::uuid, 'project'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(entities[1])}::uuid, 'product'),
      (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(entities[2])}::uuid, 'product');
    INSERT INTO entity_names (team_id, entity_id, owner_profile_id, display_name, normalized_name, name_kind) VALUES
      ${names.map((name, index) => `(${sqlLiteral(teamID)}::uuid, ${sqlLiteral(entities[index])}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, ${sqlLiteral(name)}, ${sqlLiteral(name.toLowerCase())}, 'canonical')`).join(",\n")};
    INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, idempotency_key, request_hash, source_summary, status, proposal, metadata, completed_at)
      VALUES (${sqlLiteral(teamID)}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, ${sqlLiteral(runID)}, ${sqlLiteral(`sha256:${runID}`)}, 'compose community fixture', 'completed', '{}'::jsonb, '{}'::jsonb, now());
    INSERT INTO evidence_fragments (team_id, fragment_id, ingest_id, owner_profile_id, evidence_index, content, content_hash, source_type, authority, source_ref) VALUES
      ${quotes.map((quote, index) => `(${sqlLiteral(teamID)}::uuid, ${sqlLiteral(fragments[index])}::uuid, ${sqlLiteral(ingestID)}::uuid, ${sqlLiteral(ownerProfileID)}::uuid, ${index}, ${sqlLiteral(quote)}, ${sqlLiteral(`sha256:community-fragment-${index}`)}, 'manual', 'primary', ${sqlLiteral(`community:${index}`)})`).join(",\n")};
    INSERT INTO relationship_records (
      team_id, relationship_id, owner_profile_id, semantic_group_key, subject_entity_id, predicate_key, predicate_version,
      object_entity_id, relationship_kind, current_cardinality, status, polarity, support_count, source_group_count
    ) VALUES ${inserts};
    INSERT INTO relationship_observations (
      team_id, observation_id, relationship_id, ingest_id, owner_profile_id, subject_ref, original_predicate, object_ref,
      subject_entity_id, predicate_key, predicate_version, object_entity_id, evidence, metadata
    ) VALUES ${observationsSQL};
    INSERT INTO verification_events (
      team_id, verification_event_id, observation_id, owner_profile_id, evidence_verdict, confidence, rationale, model, response_hash, metadata
    ) VALUES ${verificationSQL};
    INSERT INTO relationship_evidence_supports (
      team_id, support_id, relationship_id, observation_id, verification_event_id, fragment_id, owner_profile_id,
      source_group_key, span_start, span_end, quote, authority, metadata
    ) VALUES ${supportSQL};
    INSERT INTO relationship_support_decision_events (
      team_id, support_id, relationship_id, owner_profile_id, actor_profile_id, decision, reason, metadata
    ) VALUES ${decisionSQL};
  `);
  return { relationships, groups, entities, fragments };
}

async function createIsolatedTeam() {
  const team = await controlJSON("/teams", { method: "POST", body: JSON.stringify({ name: `${runID} isolated`, description: "community isolation check" }) });
  const id = team.data?.id;
  if (typeof id !== "string" || !id) throw new Error("isolated team creation did not return an id");
  const profile = await controlJSON(`/teams/${id}/profiles`, { method: "POST", body: JSON.stringify({ name: `${runID} isolated key`, scopes: ["read", "write"], rate_limit: 300 }) });
  const isolatedAPIKey = profile.data?.api_key;
  if (typeof isolatedAPIKey !== "string" || !isolatedAPIKey) throw new Error("isolated team profile did not return an API key");
  return { teamID: id, apiKey: isolatedAPIKey };
}

function assertCommunityContract(payload) {
  const communities = payload.related_communities;
  if (!Array.isArray(communities) || communities.length < 1) throw new Error(`recall did not return a community: ${JSON.stringify(payload)}`);
  const required = ["community_id", "logical_community_id", "rank", "summary", "top_entities", "top_predicates", "entity_count", "relationship_count", "relationships", "relationships_truncated"];
  for (const community of communities) {
    for (const key of required) if (!Object.hasOwn(community, key)) throw new Error(`community omitted ${key}`);
    if (!Array.isArray(community.top_entities) || community.top_entities.length > 5 || !Array.isArray(community.top_predicates) || community.top_predicates.length > 5) throw new Error("community top fields exceeded bounds");
    if (!Array.isArray(community.relationships) || community.relationships.length > 2 || community.relationships.length > 0 && !community.relationships[0].relationship_id) throw new Error("community nested relationship contract is invalid");
    if (Object.hasOwn(community, "evidence_ids") || Object.hasOwn(community, "related_relationships")) throw new Error("community leaked the transitional public path shape");
  }
  const nestedIDs = new Set(communities.flatMap((community) => community.relationships.map((relationship) => relationship.relationship_id)));
  for (const relationship of payload.related_relationships ?? []) if (nestedIDs.has(relationship.relationship_id)) throw new Error("direct and community relationship groups overlapped");
  const serialized = JSON.stringify(payload);
  for (const field of ["provider_model", "source_fingerprint", "summary_input_hash"]) if (serialized.includes(field)) throw new Error(`public recall leaked ${field}`);
}

function assertKnownEvidenceSuppressed(payload, knownEvidenceIDs) {
  const known = new Set(knownEvidenceIDs);
  for (const community of payload.related_communities ?? []) {
    for (const relationship of community.relationships ?? []) {
      if ((relationship.evidence_ids ?? []).some((evidenceID) => known.has(evidenceID))) {
        throw new Error("known evidence was returned in a community relationship");
      }
    }
  }
  for (const relationship of payload.related_relationships ?? []) {
    if ((relationship.evidence_ids ?? []).some((evidenceID) => known.has(evidenceID))) {
      throw new Error("known evidence was returned in a direct relationship");
    }
  }
}

async function updateControlConfig(path, items) { await controlJSON(path, { method: "PATCH", body: JSON.stringify({ items }) }); }
async function userProfileID() {
  const response = await httpJSON(`${userURL}/ui/api/session`, { headers: { Authorization: `Bearer ${apiKey}` } });
  const id = response.data?.key?.id;
  if (typeof id !== "string" || !id) throw new Error("user session did not return a profile id");
  return id;
}
async function mcpSuccess(name, args) { return mcpSuccessWithKey(apiKey, name, args); }
async function mcpSuccessWithKey(key, name, args) {
  const response = await httpJSON(`${userURL}/mcp`, { method: "POST", headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/call", params: { name, arguments: args } }) });
  if (response.error || response.result === undefined) throw new Error(`MCP ${name} returned a bounded error`);
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} did not return JSON content`);
  return JSON.parse(text);
}
async function controlJSON(path, options = {}) { return httpJSON(`${controlURL}/control/api${path}`, { ...options, headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json", ...(options.headers ?? {}) } }); }
async function prometheusValue(query) {
  const url = new URL("/api/v1/query", `${prometheusURL}/`); url.searchParams.set("query", query);
  const response = await httpJSON(url.toString(), { method: "GET" });
  return Number(response.data?.result?.[0]?.value?.[1] ?? 0);
}
async function waitForPrometheusMetric(query) {
  for (let attempt = 0; attempt < 12; attempt += 1) {
    const value = await prometheusValue(query);
    if (value > 0) return value;
    await delay(1_000);
  }
  return prometheusValue(query);
}
async function httpJSON(url, options) {
  const response = await fetch(url, options); const text = await response.text();
  if (!response.ok) throw new Error(`HTTP ${response.status} ${url}: ${redactHTTPBody(text)}`);
  return text ? JSON.parse(text) : {};
}
function postgresQuery(sql) {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"', "community-recall-e2e", sql], { cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8" });
  if (result.status !== 0) throw new Error(`postgres query failed (${result.status}): ${result.stderr || result.stdout}`);
  return result.stdout.trim();
}
function sqlLiteral(value) { return `'${String(value).replace(/'/g, "''")}'`; }
function redactHTTPBody(text) { return text.replace(/"api_key"\s*:\s*"[^"]*"/g, '"api_key":"<redacted>"'); }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
