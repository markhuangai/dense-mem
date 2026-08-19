#!/usr/bin/env node

import { randomUUID } from "node:crypto";
import { spawnSync } from "node:child_process";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const harnessURL = requiredEnv("DENSE_MEM_E2E_OAUTH_HARNESS_URL").replace(/\/$/, "");
const mockURL = requiredEnv("DENSE_MEM_E2E_OAUTH_MOCK_URL").replace(/\/$/, "");
const issuerBase = requiredEnv("DENSE_MEM_E2E_OAUTH_ISSUER_BASE").replace(/\/$/, "");
const fixtureToken = requiredEnv("DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const oauthComposeFile = requiredEnv("DENSE_MEM_E2E_OAUTH_COMPOSE_FILE");
const logSentinel = `oauth-compat-claim-${randomUUID()}`;
const issuedTokens = [];
const requestTimeoutMs = 30_000;
let rpcID = 0;

const metadataPath = "/.well-known/oauth-protected-resource/mcp";
const metadata = await expectJSON(`${harnessURL}${metadataPath}`, {}, 200);
assert(metadata.resource === `${harnessURL}/mcp`, "metadata resource did not use the configured public URL");
assertSetEqual(metadata.authorization_servers, [
  `${issuerBase}/entra`,
  `${issuerBase}/pingone`,
  `${issuerBase}/generic/`,
], "authorization servers");
assertSetEqual(metadata.scopes_supported, [
  "memory.read", "memory.write", "ping.read", "ping.write", "generic.read", "generic.write",
], "supported scopes");
assertSetEqual(metadata.bearer_methods_supported, ["header"], "bearer methods");

const missingAuth = await requestRaw(`${harnessURL}/mcp`, { method: "POST" });
assert(missingAuth.status === 401, "harness missing-auth response was not 401");
assert(missingAuth.headers["www-authenticate"] === `Bearer resource_metadata="${harnessURL}${metadataPath}"`, "harness challenge did not use configured metadata URL");

const spoofedHost = await requestRaw(`${harnessURL}/mcp`, {
  method: "POST",
  headers: { Host: "attacker.invalid" },
});
assert(spoofedHost.status === 401, "spoofed-host request did not receive a bounded challenge");
assert(spoofedHost.headers["www-authenticate"] === `Bearer resource_metadata="${harnessURL}${metadataPath}"`, "request Host changed the challenge URL");
assert(!spoofedHost.body.includes("attacker.invalid"), "spoofed Host was reflected in the harness response");

const profileCases = [
  ["entra", { claims: { tenant: "team-a", log_sentinel: logSentinel } }, "entra"],
  ["pingone", { claims: { tenant: "team-b" } }, "pingone"],
  ["generic", { claims: { tenant: "team-c" } }, "generic"],
];
let productionFixtureJWT = "";
for (const [profile, options, expectedProfile] of profileCases) {
  const token = await issueToken(profile, options);
  if (profile === "entra") productionFixtureJWT = token;
  const response = await harnessValidation(token);
  assert(response.status === 200, `${profile} compatibility token was rejected`);
  assert(response.payload.valid === true && response.payload.profile === expectedProfile, `${profile} compatibility result was not bounded and valid`);
  assert(response.payload.scope_count === 2, `${profile} scope mapping did not produce two grants`);
  assert(response.payload.team_claim_present === true, `${profile} optional team claim was not recognized`);
  assert(!response.body.includes(token) && !response.body.includes(logSentinel), `${profile} token or arbitrary claim was reflected`);
}

const multipleAudienceToken = await issueToken("entra", {
  claims: { aud: ["unconfigured-audience", "api://dense-mem-entra"] },
});
const multipleAudienceResponse = await harnessValidation(multipleAudienceToken);
assert(multipleAudienceResponse.status === 200, "a token containing a configured secondary audience was rejected");
assert(multipleAudienceResponse.payload.profile === "entra", "multiple-audience validation selected the wrong profile");
assert(multipleAudienceResponse.payload.scope_count === 2, "multiple-audience validation changed mapped scopes");
assert(multipleAudienceResponse.payload.team_claim_present === false, "an absent optional team claim was reported as present");

for (const [label, profile, options] of [
  ["wrong issuer", "entra", { claims: { iss: `${issuerBase}/unknown` } }],
  ["trailing-slash issuer mismatch", "generic", { claims: { iss: `${issuerBase}/generic` } }],
  ["wrong audience", "entra", { claims: { aud: "wrong-audience" } }],
  ["wrong key", "entra", { key: "wrong" }],
  ["unknown kid", "entra", { kid: "unknown" }],
  ["algorithm confusion", "entra", { header_alg: "PS256", sign_alg: "RS256" }],
  ["expired", "entra", { claims: { exp: Math.floor(Date.now() / 1000) - 120 } }],
  ["not yet valid", "entra", { claims: { nbf: Math.floor(Date.now() / 1000) + 120 } }],
  ["future issued at", "entra", { claims: { iat: Math.floor(Date.now() / 1000) + 120 } }],
  ["duplicate header", "entra", { mode: "duplicate_header" }],
  ["duplicate claim", "entra", { mode: "duplicate_claim" }],
]) {
  await expectInvalid(label, await issueToken(profile, options), 401, "invalid_token");
}
for (const claim of ["iss", "aud", "sub", "exp"]) {
  await expectInvalid(`missing ${claim}`, await issueToken("entra", { omit: [claim] }), 401, "invalid_token");
}
await expectInvalid("malformed JWT", "not.a.jwt", 401, "invalid_token");

await setMockState("generic", { active_key: "secondary" });
await delay(1_100);
const rotatedGeneric = await issueToken("generic");
assert((await harnessValidation(rotatedGeneric)).status === 200, "unknown rotated key did not trigger a bounded JWKS refresh");
await setMockState("generic", { jwks_outage: true });
assert((await harnessValidation(rotatedGeneric)).status === 200, "cached known key failed during a JWKS outage");
await delay(1_100);
const unavailableUnknownKey = await issueToken("generic", { key: "future" });
await expectInvalid("unknown key during outage", unavailableUnknownKey, 503, "temporarily_unavailable");
const statsAfterFailure = await mockJSON("/fixture/stats", { profile: "generic" });
await expectInvalid("repeated unknown key during outage", unavailableUnknownKey, 503, "temporarily_unavailable");
const statsAfterRepeat = await mockJSON("/fixture/stats", { profile: "generic" });
assert(statsAfterRepeat.jwks_requests === statsAfterFailure.jwks_requests, "provider outage caused an unbounded repeat refresh");
await setMockState("generic", { jwks_outage: false });

const productionAPIKeyResponse = await productionMCP(apiKey);
assert(productionAPIKeyResponse.status === 200 && productionAPIKeyResponse.payload.result, "production /mcp stopped accepting API keys");
const productionJWTResponse = await productionMCP(productionFixtureJWT);
assert(productionJWTResponse.status === 401, "production /mcp accepted a compatibility fixture JWT");
assert(!productionJWTResponse.body.includes(productionFixtureJWT), "production /mcp reflected bearer material");
for (const path of ["/.well-known/oauth-protected-resource", metadataPath]) {
  const response = await requestRaw(`${userURL}${path}`, { method: "GET" });
  assert(response.status === 404, `production exposed dormant OAuth metadata at ${path}`);
}

assertNoOAuthMaterialPersisted(productionFixtureJWT, logSentinel);
assertNoOAuthMaterialInLogs(issuedTokens, logSentinel);

console.log(JSON.stringify({
  status: "ok",
  scenario: "oauth_provider_compatibility",
  tested_commit: requiredEnv("DENSE_MEM_E2E_COMMIT_SHA"),
  provider_profiles: 3,
  production_api_key_only: true,
  metadata_and_challenges: true,
  multiple_audiences_and_optional_team: true,
  key_rotation_and_outage: true,
  token_and_claim_leak_checks: true,
}, null, 2));

async function expectInvalid(label, token, expectedStatus, expectedCode) {
  const response = await harnessValidation(token);
  assert(response.status === expectedStatus, `${label} returned HTTP ${response.status}; expected ${expectedStatus}`);
  assert(response.payload.error === expectedCode, `${label} returned an unexpected bounded error`);
  assert(!response.body.includes(token) && !response.body.includes(logSentinel), `${label} reflected token or claim material`);
}

async function harnessValidation(token) {
  const response = await requestRaw(`${harnessURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
  });
  return { ...response, payload: parseJSON(response.body, "harness validation") };
}

async function productionMCP(token) {
  const response = await requestRaw(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method: "tools/list", params: {} }),
  });
  return { ...response, payload: parseJSON(response.body, "production MCP") };
}

async function issueToken(profile, options = {}) {
  const response = await mockJSON("/fixture/token", { profile, ...options });
  if (typeof response.token !== "string" || response.token.split(".").length !== 3) {
    throw new Error("OAuth fixture did not return a JWT");
  }
  issuedTokens.push(response.token);
  return response.token;
}

async function setMockState(profile, state) {
  await mockJSON("/fixture/state", { profile, ...state });
}

async function mockJSON(path, body) {
  return expectJSON(`${mockURL}${path}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${fixtureToken}`, Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }, 200);
}

async function expectJSON(url, options, expectedStatus) {
  const response = await requestRaw(url, options);
  if (response.status !== expectedStatus) {
    throw new Error(`OAuth fixture boundary returned HTTP ${response.status}; response body redacted`);
  }
  return parseJSON(response.body, "OAuth fixture boundary");
}

function requestRaw(rawURL, options = {}) {
  const target = new URL(rawURL);
  return new Promise((resolve, reject) => {
    const transport = target.protocol === "https:" ? httpsRequest : httpRequest;
    const request = transport({
      protocol: target.protocol,
      hostname: target.hostname,
      port: target.port,
      path: `${target.pathname}${target.search}`,
      method: options.method ?? "GET",
      headers: options.headers ?? {},
      timeout: requestTimeoutMs,
      ...(target.protocol === "https:" ? { servername: target.hostname } : {}),
    }, (response) => {
      let body = "";
      response.setEncoding("utf8");
      response.on("data", (chunk) => {
        body += chunk;
        if (body.length > 1024 * 1024) {
          request.destroy(new Error("response exceeded compatibility E2E limit"));
        }
      });
      response.on("end", () => resolve({ status: response.statusCode ?? 0, headers: response.headers, body }));
    });
    request.on("error", reject);
    request.on("timeout", () => request.destroy(new Error("OAuth compatibility request timed out")));
    if (options.body !== undefined) request.write(options.body);
    request.end();
  });
}

function assertNoOAuthMaterialPersisted(token, sentinel) {
  const result = postgresCommand(`
    SELECT count(*)
    FROM (
      SELECT metadata::text AS value FROM audit_log
      UNION ALL SELECT COALESCE(before_payload::text, '') FROM audit_log
      UNION ALL SELECT COALESCE(after_payload::text, '') FROM audit_log
    ) persisted
    WHERE value LIKE '%' || ${sqlLiteral(token)} || '%'
       OR value LIKE '%' || ${sqlLiteral(sentinel)} || '%';
  `);
  if (result.status !== 0 || Number(result.stdout.trim()) !== 0) {
    throw new Error("OAuth bearer or claim material crossed a persistence boundary");
  }
}

function assertNoOAuthMaterialInLogs(tokens, sentinel) {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile, "-f", oauthComposeFile,
    "logs", "--no-color", "server", "oauth-compat-harness", "oauth-provider-mock",
  ], { cwd: repositoryRoot(), encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
  if (result.status !== 0) throw new Error("could not inspect OAuth compatibility logs");
  const logs = `${result.stdout}\n${result.stderr}`;
  if (logs.includes(sentinel) || tokens.some((token) => logs.includes(token))) {
    throw new Error("OAuth bearer or claim material crossed a log boundary");
  }
}

function postgresCommand(sql) {
  return spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec",
    'psql -X -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "oauth-compat-e2e", sql,
  ], { cwd: repositoryRoot(), encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
}

function parseJSON(raw, label) {
  try {
    return raw ? JSON.parse(raw) : {};
  } catch {
    throw new Error(`${label} returned non-JSON content`);
  }
}

function assertSetEqual(actual, expected, label) {
  assert(Array.isArray(actual), `${label} was not an array`);
  const left = [...actual].sort();
  const right = [...expected].sort();
  assert(JSON.stringify(left) === JSON.stringify(right), `${label} did not match configured values`);
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function repositoryRoot() {
  return fileURLToPath(new URL("../..", import.meta.url));
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
