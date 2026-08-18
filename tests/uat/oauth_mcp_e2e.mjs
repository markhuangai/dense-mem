#!/usr/bin/env node

import { createHash, randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const scenario = requiredEnv("DENSE_MEM_E2E_SCENARIO");
const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seedTeamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const oauthInternalURL = requiredEnv("DENSE_MEM_E2E_OAUTH_INTERNAL_URL").replace(/\/$/, "");
const oauthMockURL = requiredEnv("DENSE_MEM_E2E_OAUTH_MOCK_URL").replace(/\/$/, "");
const oauthFixtureToken = requiredEnv("DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN");
const ssoSessionToken = requiredEnv("DENSE_MEM_E2E_SSO_SESSION_TOKEN");
const ssoCSRFToken = requiredEnv("DENSE_MEM_E2E_SSO_CSRF_TOKEN");
const resultFile = requiredEnv("DENSE_MEM_E2E_RESULT_FILE");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const runID = `oauth-mcp-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
const logSentinel = `oauth-private-token-sentinel-${randomUUID()}`;
const issuedTokens = [];
let rpcID = 0;

if (!new Set(["oauth_provider_compatibility", "mcp_oauth"]).has(scenario)) {
  throw new Error(`unsupported OAuth E2E scenario ${scenario}`);
}

const secondTeam = await createTeam(`${runID} Team B`);
const thirdTeam = await createTeam(`${runID} Team C`);
await controlJSON("/config/sso", {
  method: "PATCH",
  body: { items: [{ key: "MCP_PUBLIC_BASE_URL", value: userURL }] },
});

const providerInputs = {
  entra: providerInput({
    name: `${runID} Entra`,
    kind: "azure_ad",
    tenantID: "dense-mem-e2e-tenant",
    identityClaim: "oid",
    audience: "dense-mem-entra",
    algorithm: "RS256",
    scopeClaim: "scp",
    teamClaim: "dense_mem_team_id",
    mappings: [
      ["memory.read", ["read"]],
      ["memory.write", ["write"]],
    ],
  }),
  pingone: providerInput({
    name: `${runID} PingOne`,
    kind: "pingone",
    identityClaim: "sub",
    audience: "urn:dense-mem:pingone",
    algorithm: "PS256",
    scopeClaim: "scope",
    jwksSource: "static",
    mappings: [
      ["ping.read", ["read"]],
      ["ping.write", ["write"]],
    ],
  }),
  generic: providerInput({
    name: `${runID} Generic OIDC`,
    kind: "generic_oidc",
    identityClaim: "sub",
    audience: "https://dense-mem.example.test/mcp",
    algorithm: "ES256",
    scopeClaim: "permissions",
    teamClaim: "dense_mem_team_id",
    mappings: [
      ["generic.read", ["read"]],
      ["generic.write", ["write"]],
    ],
  }),
};

const strictProviderResponse = await controlRaw("/sso/providers", {
  method: "POST",
  body: { ...providerInputs.generic, unsupported_field: true },
});
assert(strictProviderResponse.status === 422, "provider configuration accepted an unknown field");

const providers = {};
for (const name of ["entra", "pingone", "generic"]) {
  providers[name] = (await controlJSON("/sso/providers", { method: "POST", body: providerInputs[name] })).data;
  assertUUID(providers[name]?.id, `${name} provider ID`);
}

const entraTeamAMapping = await createMapping(providers.entra.id, seedTeamID, "oauth-team-a", ["read", "write"]);
await createMapping(providers.entra.id, secondTeam.id, "oauth-team-b", ["read", "write"]);
await createMapping(providers.pingone.id, seedTeamID, "ping-team", ["read", "write"]);
await createMapping(providers.generic.id, seedTeamID, "generic-team", ["read", "write"]);

const identities = {
  entra: identityFixture(providers.entra.id, "entra-user", true),
  pingone: identityFixture(providers.pingone.id, "pingone-user", true),
  generic: identityFixture(providers.generic.id, "generic-user", true),
  noMembership: identityFixture(providers.entra.id, "oauth-no-membership", true),
  disabled: identityFixture(providers.entra.id, "oauth-disabled", false),
  suspended: identityFixture(providers.entra.id, "oauth-suspended", true),
};

const memberships = [
  membershipFixture(identities.entra, seedTeamID, ["read"], "oauth-team-a", "active"),
  membershipFixture(identities.entra, secondTeam.id, ["read", "write"], "oauth-team-b", "active"),
  membershipFixture(identities.pingone, seedTeamID, ["read", "write"], "ping-team", "active"),
  membershipFixture(identities.generic, seedTeamID, ["read", "write"], "generic-team", "active"),
  membershipFixture(identities.disabled, seedTeamID, ["read"], "oauth-disabled", "active"),
  membershipFixture(identities.suspended, seedTeamID, ["read"], "oauth-suspended", "suspended"),
];
seedOAuthPrincipals(Object.values(identities), memberships, memberships[0]);

const metadata = await publicJSON("/.well-known/oauth-protected-resource/mcp");
assert(metadata.status === 200, "unscoped RFC 9728 metadata was unavailable");
assert(metadata.payload.resource === `${userURL}/mcp`, "unscoped metadata resource did not use the configured public base URL");
assert(metadata.payload.resource_name === "Dense-Mem MCP", "metadata resource name changed");
assert(JSON.stringify(metadata.payload.bearer_methods_supported) === JSON.stringify(["header"]), "metadata bearer method changed");
for (const name of ["entra", "pingone", "generic"]) {
  assert(metadata.payload.authorization_servers?.includes(`${oauthInternalURL}/${name}`), `${name} issuer was missing from metadata`);
}
for (const scope of ["memory.read", "memory.write", "ping.read", "ping.write", "generic.read", "generic.write"]) {
  assert(metadata.payload.scopes_supported?.includes(scope), `metadata omitted external scope ${scope}`);
}

const scopedMetadata = await publicJSON(`/.well-known/oauth-protected-resource/teams/${secondTeam.id}/mcp`);
assert(scopedMetadata.status === 200, "team-scoped RFC 9728 metadata was unavailable");
assert(scopedMetadata.payload.resource === `${userURL}/teams/${secondTeam.id}/mcp`, "team metadata resource was not canonical");

await assertChallenge("/mcp", `${userURL}/.well-known/oauth-protected-resource/mcp`);
await assertChallenge(`/teams/${seedTeamID}/mcp`, `${userURL}/.well-known/oauth-protected-resource/teams/${seedTeamID}/mcp`);

const apiUnscoped = await mcpTools(apiKey, "/mcp");
assertMCPTools(apiUnscoped, { present: ["recall_memory", "remember"] });
const apiScoped = await mcpTools(apiKey, `/teams/${seedTeamID}/mcp`);
assertMCPTools(apiScoped, { present: ["recall_memory", "remember"] });
await expectMCPStatus(apiKey, `/teams/${secondTeam.id}/mcp`, 403);

const cookieOnly = await mcpRequest(undefined, "/mcp", {
  Cookie: `dense_mem_sso_session=${ssoSessionToken}; dense_mem_sso_csrf=${ssoCSRFToken}`,
});
assert(cookieOnly.status === 401, "MCP accepted a browser SSO cookie without a bearer token");

const entraTeamA = await issueToken("entra", {
  claims: { dense_mem_team_id: seedTeamID, log_sentinel: logSentinel },
});
const entraTeamATools = await mcpTools(entraTeamA, "/mcp");
assertMCPTools(entraTeamATools, { present: ["recall_memory"], absent: ["remember"] });

const entraTeamB = await issueToken("entra", { claims: { dense_mem_team_id: secondTeam.id } });
const entraTeamBTools = await mcpTools(entraTeamB, `/teams/${secondTeam.id}/mcp`);
assertMCPTools(entraTeamBTools, { present: ["recall_memory", "remember"] });

const entraMultipleAudiences = await issueToken("entra", {
  claims: { aud: ["unrelated-audience", "dense-mem-entra"], dense_mem_team_id: seedTeamID },
});
assert((await mcpTools(entraMultipleAudiences, "/mcp")).status === 200, "valid audience array was rejected");

const ambiguous = await issueToken("entra", { omit: ["dense_mem_team_id"] });
await expectMCPError(ambiguous, "/mcp", 400, "team_required");
assert((await mcpTools(ambiguous, `/teams/${seedTeamID}/mcp`)).status === 200, "scoped route did not resolve a multi-team token");

await expectMCPError(entraTeamA, `/teams/${secondTeam.id}/mcp`, 403, "FORBIDDEN");
const nonMemberClaim = await issueToken("entra", { claims: { dense_mem_team_id: thirdTeam.id } });
await expectMCPError(nonMemberClaim, `/teams/${thirdTeam.id}/mcp`, 403, "FORBIDDEN");

const pingToken = await issueToken("pingone");
assert((await mcpTools(pingToken, "/mcp")).status === 200, "PingOne PS256/static-JWKS token was rejected");
const genericToken = await issueToken("generic", { claims: { dense_mem_team_id: seedTeamID } });
assert((await mcpTools(genericToken, "/mcp")).status === 200, "generic OIDC ES256 token was rejected");

for (const test of [
  ["wrong issuer", { claims: { iss: `${oauthInternalURL}/unknown`, dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
  ["wrong audience", { claims: { aud: "wrong-audience", dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
  ["wrong key", { key: "wrong", claims: { dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
  ["unknown kid", { kid: "unknown", claims: { dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
  ["algorithm confusion", { header_alg: "PS256", sign_alg: "RS256", claims: { dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
  ["expired", { claims: { exp: Math.floor(Date.now() / 1000) - 120, dense_mem_team_id: seedTeamID } }, 401, "AUTH_EXPIRED"],
  ["not yet valid", { claims: { nbf: Math.floor(Date.now() / 1000) + 120, dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
  ["future issued at", { claims: { iat: Math.floor(Date.now() / 1000) + 120, dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
  ["duplicate header", { mode: "duplicate_header", claims: { dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
  ["duplicate claim", { mode: "duplicate_claim", claims: { dense_mem_team_id: seedTeamID } }, 401, "AUTH_INVALID"],
]) {
  const [label, options, status, code] = test;
  await expectMCPError(await issueToken("entra", options), "/mcp", status, code, label);
}

for (const claim of ["iss", "aud", "sub", "exp"]) {
  const options = { omit: [claim], claims: { dense_mem_team_id: seedTeamID } };
  await expectMCPError(await issueToken("entra", options), "/mcp", 401, "AUTH_INVALID", `missing ${claim}`);
}
await expectMCPError("not.a.jwt", "/mcp", 401, "AUTH_INVALID", "malformed JWT");

const noMembership = await issueToken("entra", { claims: { oid: identities.noMembership.subject, dense_mem_team_id: seedTeamID } });
await expectMCPError(noMembership, "/mcp", 403, "FORBIDDEN", "identity without membership");
const disabledIdentity = await issueToken("entra", { claims: { oid: identities.disabled.subject, dense_mem_team_id: seedTeamID } });
await expectMCPError(disabledIdentity, "/mcp", 403, "FORBIDDEN", "disabled identity");
const suspendedMembership = await issueToken("entra", { claims: { oid: identities.suspended.subject, dense_mem_team_id: seedTeamID } });
await expectMCPError(suspendedMembership, "/mcp", 403, "FORBIDDEN", "suspended membership");

await controlJSON(`/sso/providers/${providers.entra.id}/mappings/${entraTeamAMapping.id}`, { method: "DELETE" });
await expectMCPError(entraTeamA, "/mcp", 403, "FORBIDDEN", "revoked membership mapping");
await controlJSON(`/sso/providers/${providers.entra.id}/mappings/${entraTeamAMapping.id}`, {
  method: "PATCH",
  body: {
    team_id: seedTeamID,
    group_id: "oauth-team-a",
    group_name: "oauth-team-a",
    scopes: ["read", "write"],
    role: "member",
    enabled: true,
  },
});
assert((await mcpTools(entraTeamA, "/mcp")).status === 200, "restored membership mapping did not restore access");

await setMockState("generic", { active_key: "secondary" });
await delay(1_100);
const rotatedGeneric = await issueToken("generic", { claims: { dense_mem_team_id: seedTeamID } });
assert((await mcpTools(rotatedGeneric, "/mcp")).status === 200, "unknown rotated key did not trigger bounded JWKS refresh");
await setMockState("generic", { outage: true });
assert((await mcpTools(rotatedGeneric, "/mcp")).status === 200, "cached known key failed during a JWKS outage");
await delay(1_100);
const unavailableUnknownKey = await issueToken("generic", { key: "future", claims: { dense_mem_team_id: seedTeamID } });
await expectMCPError(unavailableUnknownKey, "/mcp", 503, "SERVICE_UNAVAILABLE", "unknown key during outage");
await setMockState("generic", { outage: false });

assertNoOAuthSecretsPersisted();
assertNoOAuthSecretsInLogs();

writeFileSync(resultFile, JSON.stringify({
  status: "ok",
  scenario,
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  second_team_id: secondTeam.id,
  provider_profiles: ["entra-rs256-discovery", "pingone-ps256-static", "generic-es256-discovery"],
  metadata_and_challenges: true,
  team_selection_and_scope_intersection: true,
  key_rotation_and_outage: true,
  api_key_compatibility: true,
  browser_cookie_rejected: true,
  token_persistence_and_log_scan: true,
}, null, 2));

console.log(JSON.stringify({
  status: "ok",
  scenario,
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  provider_profiles: 3,
  second_team_id: secondTeam.id,
}, null, 2));

function providerInput({ name, kind, tenantID = "", identityClaim, audience, algorithm, scopeClaim, teamClaim = "", jwksSource = "discovery", mappings }) {
  const profile = kind === "azure_ad" ? "entra" : kind === "pingone" ? "pingone" : "generic";
  return {
    name,
    kind,
    issuer_url: `${oauthInternalURL}/${profile}`,
    tenant_id: tenantID,
    identity_claim: identityClaim,
    client_id: `${runID}-${profile}-browser-client`,
    client_secret_env: "",
    scopes: ["openid", "profile", "email"],
    group_claims: ["groups"],
    groups_endpoint: "",
    groups_scopes: [],
    protected_resource: {
      enabled: true,
      audiences: [audience],
      jwks_source: jwksSource,
      jwks_uri: jwksSource === "static" ? `${oauthInternalURL}/${profile}/jwks` : "",
      algorithms: [algorithm],
      scope_claim: scopeClaim,
      scope_mappings: mappings.map(([external_scope, internal_scopes]) => ({ external_scope, internal_scopes })),
      team_claim: teamClaim,
    },
    enabled: true,
  };
}

function identityFixture(providerID, subject, active) {
  return { id: randomUUID(), providerID, subject, active };
}

function membershipFixture(identity, teamID, grants, groupID, status) {
  return {
    id: randomUUID(),
    ownerID: randomUUID(),
    identity,
    teamID,
    grants,
    groupID,
    status,
  };
}

function seedOAuthPrincipals(identityFixtures, membershipFixtures, initialMembership) {
  const statements = ["BEGIN", "SELECT set_config('app.tx_mode', 'system', true)"];
  for (const identity of identityFixtures) {
    statements.push(`
      INSERT INTO actor_identities (id, kind, team_id, provider, subject, display_name, active)
      VALUES (${sqlLiteral(identity.id)}::uuid, 'human', NULL, ${sqlLiteral(identity.providerID)}, ${sqlLiteral(identity.subject)}, ${sqlLiteral(`E2E ${identity.subject}`)}, ${identity.active});
      INSERT INTO sso_identities (id, provider_id, subject, external_id, email, display_name, active, last_login_at, last_entitlement_check_at)
      VALUES (${sqlLiteral(identity.id)}::uuid, ${sqlLiteral(identity.providerID)}::uuid, ${sqlLiteral(identity.subject)}, ${sqlLiteral(identity.subject)}, ${sqlLiteral(`${identity.subject}@example.test`)}, ${sqlLiteral(`E2E ${identity.subject}`)}, ${identity.active}, now(), now());
    `);
  }
  for (const membership of membershipFixtures) {
    statements.push(`
      INSERT INTO team_memberships (
        id, actor_identity_id, team_id, status, team_admin, maximum_grants,
        sso_provider_id, sso_group_id, sso_profile_name, sso_entitlement_status,
        sso_last_entitlement_checked_at, sso_last_login_at
      ) VALUES (
        ${sqlLiteral(membership.id)}::uuid, ${sqlLiteral(membership.identity.id)}::uuid,
        ${sqlLiteral(membership.teamID)}::uuid, ${sqlLiteral(membership.status)}, false,
        ARRAY[${membership.grants.map(sqlLiteral).join(",")}]::text[],
        ${sqlLiteral(membership.identity.providerID)}::uuid, ${sqlLiteral(membership.groupID)},
        ${sqlLiteral(`E2E ${membership.identity.subject}`)}, 'active', now(), now()
      );
      INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, credential_id, reason)
      VALUES (${sqlLiteral(membership.teamID)}::uuid, ${sqlLiteral(membership.ownerID)}::uuid, ${sqlLiteral(membership.identity.id)}::uuid, NULL, 'oauth_e2e');
      INSERT INTO membership_grants (membership_id, grant_name, source)
      SELECT ${sqlLiteral(membership.id)}::uuid, grant_name, 'explicit'
      FROM unnest(ARRAY[${membership.grants.map(sqlLiteral).join(",")}]::text[]) AS grant_name;
    `);
  }
  const entitlementGroups = new Map();
  for (const membership of membershipFixtures) {
    const key = `${membership.identity.providerID}:${membership.identity.subject}`;
    const existing = entitlementGroups.get(key) ?? { identity: membership.identity, groups: new Set() };
    existing.groups.add(membership.groupID);
    entitlementGroups.set(key, existing);
  }
  for (const { identity, groups } of entitlementGroups.values()) {
    statements.push(`
      INSERT INTO sso_entitlement_cache (provider_id, subject, groups, status, checked_at, expires_at, error)
      VALUES (
        ${sqlLiteral(identity.providerID)}::uuid, ${sqlLiteral(identity.subject)},
        ARRAY[${[...groups].map(sqlLiteral).join(",")}]::text[], 'active', now(), now() + interval '8 hours', ''
      );
    `);
  }
  statements.push(`
    INSERT INTO sso_sessions (
      session_hash, identity_id, provider_id, membership_id, team_id, csrf_hash,
      expires_at, created_at, last_seen_at
    ) VALUES (
      ${sqlLiteral(sha256(ssoSessionToken))}, ${sqlLiteral(initialMembership.identity.id)}::uuid,
      ${sqlLiteral(initialMembership.identity.providerID)}::uuid, ${sqlLiteral(initialMembership.id)}::uuid,
      ${sqlLiteral(initialMembership.teamID)}::uuid, ${sqlLiteral(sha256(ssoCSRFToken))},
      now() + interval '8 hours', now(), now()
    );
    COMMIT;
  `);
  const result = postgresCommand(statements.join(";\n"));
  if (result.status !== 0) throw new Error("PostgreSQL OAuth fixture seeding failed");
}

async function createTeam(name) {
  const response = await controlJSON("/teams", {
    method: "POST",
    body: { name, description: "OAuth protected-resource E2E" },
  });
  assertUUID(response.data?.id, "created team ID");
  return response.data;
}

async function createMapping(providerID, teamID, groupID, scopes) {
  const response = await controlJSON(`/sso/providers/${providerID}/mappings`, {
    method: "POST",
    body: { team_id: teamID, group_id: groupID, group_name: groupID, scopes, role: "member", enabled: true },
  });
  assertUUID(response.data?.id, "mapping ID");
  return response.data;
}

async function issueToken(profile, options = {}) {
  const response = await mockJSON("/fixture/token", { profile, ...options });
  const token = response.token;
  if (typeof token !== "string" || token.split(".").length !== 3) throw new Error("OAuth fixture did not return a JWT");
  issuedTokens.push(token);
  return token;
}

async function setMockState(profile, state) {
  await mockJSON("/fixture/state", { profile, ...state });
}

async function mockJSON(path, body) {
  const response = await fetch(`${oauthMockURL}${path}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${oauthFixtureToken}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(`OAuth fixture HTTP ${response.status}; response body redacted`);
  return payload;
}

async function assertChallenge(path, expectedMetadataURL) {
  const response = await mcpRequest(undefined, path);
  assert(response.status === 401, `${path} missing-auth response was not 401`);
  assert(response.headers.get("www-authenticate") === `Bearer resource_metadata="${expectedMetadataURL}"`, `${path} challenge did not identify its matching RFC 9728 document`);
}

async function mcpTools(token, path) {
  const response = await mcpRequest(token, path);
  const names = new Set((response.payload.result?.tools ?? []).map((tool) => tool.name));
  return { ...response, names };
}

async function expectMCPStatus(token, path, status) {
  const response = await mcpRequest(token, path);
  assert(response.status === status, `${path} returned HTTP ${response.status}; expected ${status}`);
}

async function expectMCPError(token, path, status, code, label = path) {
  const response = await mcpRequest(token, path);
  assert(response.status === status, `${label} returned HTTP ${response.status}; expected ${status}`);
  assert(response.payload.code === code, `${label} returned bounded code ${String(response.payload.code)}; expected ${code}`);
  assert(!JSON.stringify(response.payload).includes(token), `${label} reflected bearer material`);
}

function assertMCPTools(response, { present = [], absent = [] }) {
  assert(response.status === 200 && response.payload.result && !response.payload.error, `MCP tools/list returned HTTP ${response.status}`);
  for (const name of present) assert(response.names.has(name), `MCP tools/list omitted ${name}`);
  for (const name of absent) assert(!response.names.has(name), `MCP tools/list exposed ${name} beyond intersected grants`);
}

async function mcpRequest(token, path, extraHeaders = {}) {
  const headers = { Accept: "application/json", "Content-Type": "application/json", ...extraHeaders };
  if (token !== undefined) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(`${userURL}${path}`, {
    method: "POST",
    headers,
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }),
  });
  const text = await response.text();
  let payload = {};
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    throw new Error(`MCP ${path} returned non-JSON HTTP ${response.status}`);
  }
  return { status: response.status, headers: response.headers, payload };
}

async function publicJSON(path) {
  const response = await fetch(`${userURL}${path}`, { headers: { Accept: "application/json" } });
  const text = await response.text();
  let payload = {};
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    throw new Error(`public ${path} returned non-JSON HTTP ${response.status}`);
  }
  return { status: response.status, payload };
}

async function controlJSON(path, options = {}) {
  const response = await controlRaw(path, options);
  if (response.status < 200 || response.status > 299) {
    throw new Error(`control ${path} returned HTTP ${response.status}; response body redacted`);
  }
  return response.payload;
}

async function controlRaw(path, options = {}) {
  const response = await fetch(`${controlURL}/control/api${path}`, {
    method: options.method ?? "GET",
    headers: { Authorization: `Bearer ${controlToken}`, Accept: "application/json", "Content-Type": "application/json" },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  const text = await response.text();
  let payload = {};
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    throw new Error(`control ${path} returned non-JSON HTTP ${response.status}`);
  }
  return { status: response.status, payload };
}

function assertNoOAuthSecretsPersisted() {
  for (const token of issuedTokens) {
    const result = postgresCommand(`
      SELECT count(*)
      FROM (
        SELECT protected_resource_config::text AS value FROM sso_providers
        UNION ALL SELECT metadata::text FROM audit_log
        UNION ALL SELECT COALESCE(before_payload::text, '') FROM audit_log
        UNION ALL SELECT COALESCE(after_payload::text, '') FROM audit_log
      ) persisted
      WHERE value LIKE '%' || ${sqlLiteral(token)} || '%';
    `);
    if (result.status !== 0 || Number(result.stdout.trim()) !== 0) throw new Error("OAuth bearer material crossed a persistence boundary");
  }
  const sentinel = postgresCommand(`
    SELECT count(*)
    FROM audit_log
    WHERE metadata::text LIKE '%' || ${sqlLiteral(logSentinel)} || '%'
       OR COALESCE(before_payload::text, '') LIKE '%' || ${sqlLiteral(logSentinel)} || '%'
       OR COALESCE(after_payload::text, '') LIKE '%' || ${sqlLiteral(logSentinel)} || '%';
  `);
  if (sentinel.status !== 0 || Number(sentinel.stdout.trim()) !== 0) throw new Error("OAuth claim content crossed an audit persistence boundary");
}

function assertNoOAuthSecretsInLogs() {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "logs", "--no-color", "server"], {
    cwd: repositoryRoot(), encoding: "utf8", maxBuffer: 20 * 1024 * 1024,
  });
  if (result.status !== 0) throw new Error("could not inspect server logs for OAuth bearer leakage");
  const logs = `${result.stdout}\n${result.stderr}`;
  if (logs.includes(logSentinel) || issuedTokens.some((token) => logs.includes(token))) {
    throw new Error("OAuth bearer or claim material crossed the server log boundary");
  }
}

function postgresCommand(sql) {
  return spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec",
    'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "oauth-mcp-e2e", sql,
  ], { cwd: repositoryRoot(), encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function repositoryRoot() {
  return fileURLToPath(new URL("../..", import.meta.url));
}

function assertUUID(value, label) {
  assert(typeof value === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value), `${label} missing`);
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
