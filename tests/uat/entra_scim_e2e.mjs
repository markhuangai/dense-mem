const controlURL = requiredURL("DENSE_MEM_CONTROL_URL");
const userURL = requiredURL("DENSE_MEM_USER_URL");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const entraMockURL = requiredURL("DENSE_MEM_ENTRA_MOCK_URL");

const controlHeaders = { Authorization: `Bearer ${controlToken}` };

await configureRuntimeIngress();
const provider = await createProvider();
const { connector, credential } = await createDirectoryConnector(provider.id);
await enterObserveMode(connector.id);
const scimToken = await issueSCIMToken(credential);
const user = await provisionUser(connector.id, scimToken);
await provisionManagerGroup(connector.id, scimToken, user.id);
await provisionUnassignedManagerGroup(connector.id, scimToken);
await activateDirectoryConnector(connector.id);
await createControlAdminGroup(provider.id);
await verifyConfiguredProviders(provider.id);

const userSession = await completeUserOIDCLogin(provider.id);
await verifyUserSession(userSession.session);

const controlSession = await completeControlOIDCLogin(provider.id);
await verifyControlSession(controlSession.session);
await assertLogoutCSRF(
  await completeUserOIDCLogin(provider.id),
  await completeControlOIDCLogin(provider.id),
);

await deactivateDirectoryUser(connector.id, scimToken, user.id);
await verifySessionsRevoked(userSession.session, controlSession.session);

console.log("Entra SCIM compose e2e passed.");

async function configureRuntimeIngress() {
  await controlEnvelope("/control/api/config/sso", {
    method: "PATCH",
    body: {
      items: [
        { key: "SSO_PUBLIC_BASE_URL", value: userURL },
        { key: "SCIM_PUBLIC_BASE_URL", value: "https://scim.example.test" },
        { key: "CONTROL_PUBLIC_BASE_URL", value: "https://control.example.test" },
        { key: "SSO_COOKIE_SECURE", value: "false" },
      ],
    },
  });
}

async function createProvider() {
  return controlEnvelope("/control/api/sso/providers", {
    method: "POST",
    body: {
      name: "Compose Entra SCIM",
      kind: "azure_ad",
      issuer_url: "https://entra-mock:9443",
      tenant_id: "entra-tenant-1",
      identity_claim: "oid",
      client_id: "dense-mem-compose-e2e",
      client_secret_env: "",
      scopes: ["openid", "profile", "email"],
      group_claims: ["groups"],
      groups_endpoint: "",
      groups_scopes: [],
      enabled: true,
    },
  }, 201);
}

async function createDirectoryConnector(providerID) {
  return controlEnvelope(`/control/api/sso/providers/${providerID}/directory-connector`, {
    method: "POST",
    body: {
      group_pattern: "^gAD7485(?P<team>.+?)(?P<role>Readonly|Member|Manager)Permission$",
      role_entitlements: {
        Readonly: { role: "member", scopes: ["read"] },
        Member: { role: "member", scopes: ["read", "write"] },
        Manager: { role: "manager", scopes: ["read", "write", "feedback:read"] },
      },
      max_auto_teams: 10,
    },
  }, 201);
}

async function issueSCIMToken(credential) {
  const response = await expectResponse(`${userURL}/scim/oauth/token`, {
    method: "POST",
    headers: {
      Authorization: `Basic ${Buffer.from(`${credential.oauth_client_id}:${credential.oauth_client_secret}`).toString("base64")}`,
      "Content-Type": "application/x-www-form-urlencoded",
    },
    body: "grant_type=client_credentials",
  }, 200);
  const payload = await response.json();
  requireString(payload.access_token, "SCIM OAuth access token");
  return payload.access_token;
}

async function provisionUser(connectorID, token) {
  return scimRequest(connectorID, token, "/Users", {
    method: "POST",
    body: {
      schemas: ["urn:ietf:params:scim:schemas:core:2.0:User"],
      externalId: "entra-user-1",
      userName: "alex@example.test",
      displayName: "Alex Entra",
      active: true,
      emails: [{ value: "alex@example.test", type: "work", primary: true }],
    },
  }, 201);
}

async function provisionManagerGroup(connectorID, token, userID) {
  return scimRequest(connectorID, token, "/Groups", {
    method: "POST",
    body: {
      schemas: ["urn:ietf:params:scim:schemas:core:2.0:Group"],
      externalId: "entra-research-manager",
      displayName: "gAD7485ResearchManagerPermission",
      members: [{ value: userID, type: "User" }],
    },
  }, 201);
}

async function provisionUnassignedManagerGroup(connectorID, token) {
  return scimRequest(connectorID, token, "/Groups", {
    method: "POST",
    body: {
      schemas: ["urn:ietf:params:scim:schemas:core:2.0:Group"],
      externalId: "entra-operations-manager",
      displayName: "gAD7485OperationsManagerPermission",
      members: [],
    },
  }, 201);
}

async function activateDirectoryConnector(connectorID) {
  const preview = await controlEnvelope(`/control/api/sso/directory/connectors/${connectorID}/preview`);
  requireString(preview.version, "directory preview version");
  assert(preview.candidates.some((candidate) => candidate.team_name === "Research" && candidate.binding_origin === "directory_created"), "preview did not include the expected directory-created Research team");

  const active = await controlEnvelope(`/control/api/sso/directory/connectors/${connectorID}/status`, {
    method: "POST",
    body: { status: "active", preview_version: preview.version },
  });
  assert(active.status === "active", "connector did not activate from the current preview");
}

async function enterObserveMode(connectorID) {
  const observing = await controlEnvelope(`/control/api/sso/directory/connectors/${connectorID}/status`, {
    method: "POST",
    body: { status: "observe", preview_version: "" },
  });
  assert(observing.status === "observe", "connector did not enter observe mode");
}

async function createControlAdminGroup(providerID) {
  const group = await controlEnvelope(`/control/api/sso/providers/${providerID}/control-admin-groups`, {
    method: "POST",
    body: { group_id: "entra-control-admins", group_name: "Entra control admins", enabled: true },
  }, 201);
  assert(group.group_id === "entra-control-admins", "control admin group was not stored");
}

async function verifyConfiguredProviders(providerID) {
  const userProviders = await envelope(`${userURL}/ui/api/sso/providers`);
  assert(userProviders.some((provider) => provider.id === providerID), "configured provider is missing from user sign-in options");

  const controlProviders = await envelope(`${controlURL}/control/auth/providers`);
  assert(controlProviders.some((provider) => provider.id === providerID), "configured provider is missing from control sign-in options");
}

async function completeUserOIDCLogin(providerID) {
  const start = await expectResponse(`${userURL}/ui/api/sso/start/${providerID}`, { redirect: "manual" }, 302);
  const authorization = rewriteMockURL(requireLocation(start, "user OIDC start"));
  const consent = await expectResponse(authorization, { redirect: "manual" }, 302);
  const callback = requireLocation(consent, "user OIDC authorization");
  assert(new URL(callback).origin === new URL(userURL).origin, "user OIDC callback did not use the configured public ingress");
  const completed = await expectResponse(callback, { redirect: "manual" }, 302);
  return {
    session: cookieFromResponse(completed, "dense_mem_sso_session"),
    csrf: cookieFromResponse(completed, "dense_mem_sso_csrf"),
  };
}

async function completeControlOIDCLogin(providerID) {
  const start = await expectResponse(`${controlURL}/control/auth/start/${providerID}`, { redirect: "manual" }, 302);
  const authorization = rewriteMockURL(requireLocation(start, "control OIDC start"));
  const consent = await expectResponse(authorization, { redirect: "manual" }, 302);
  const callback = rewriteControlCallback(requireLocation(consent, "control OIDC authorization"));
  const completed = await expectResponse(callback, { redirect: "manual" }, 302);
  return {
    session: cookieFromResponse(completed, "dense_mem_control_session"),
    csrf: cookieFromResponse(completed, "dense_mem_control_csrf"),
  };
}

async function assertLogoutCSRF(userSession, controlSession) {
  const userMissing = await expectResponse(`${userURL}/ui/api/sso/logout`, {
    method: "POST",
    headers: { Cookie: userSession.session },
  }, 403);
  assert(userMissing.status === 403, "user SSO logout without CSRF was not rejected");
  const controlMissing = await expectResponse(`${controlURL}/control/auth/logout`, {
    method: "POST",
    headers: { Cookie: controlSession.session },
  }, 403);
  assert(controlMissing.status === 403, "control logout without CSRF was not rejected");
  const userValid = await expectResponse(`${userURL}/ui/api/sso/logout`, {
    method: "POST",
    headers: { Cookie: `${userSession.session}; ${userSession.csrf}`, "X-Dense-Mem-CSRF": cookieValue(userSession.csrf) },
  }, 200);
  assert(userValid.status === 200, "user SSO logout with CSRF did not succeed");
  const controlValid = await expectResponse(`${controlURL}/control/auth/logout`, {
    method: "POST",
    headers: { Cookie: `${controlSession.session}; ${controlSession.csrf}`, "X-Dense-Mem-Control-CSRF": cookieValue(controlSession.csrf) },
  }, 204);
  assert(controlValid.status === 204, "control logout with CSRF did not succeed");
}

async function verifyUserSession(sessionCookie) {
  const session = await envelope(`${userURL}/ui/api/session`, { headers: { Cookie: sessionCookie } });
  assert(session.credential === null, "user SSO session unexpectedly exposed a direct credential");
  assert(session.team?.name === "Research", "user SSO session did not receive the directory-created Research team");
  assert(session.membership?.team_id === session.team?.id, "user SSO session membership did not match the selected team");
  assert(session.membership?.grants?.includes("read"), "user SSO session did not expose its read grant");
  assert(session.teams?.length === 1, "OIDC-only group claims granted a team that SCIM did not provision");
  assert(session.teams[0]?.team?.name === "Research", "SCIM membership was not the sole active directory entitlement source");
  assert(session.teams[0]?.membership?.team_id === session.team?.id, "SCIM team option omitted its membership");
}

async function verifyControlSession(sessionCookie) {
  const session = await envelope(`${controlURL}/control/api/session`, { headers: { Cookie: sessionCookie } });
  assert(session.auth_method === "sso", "control session did not authenticate through SSO");
}

async function deactivateDirectoryUser(connectorID, token, userID) {
  await scimRequest(connectorID, token, `/Users/${userID}`, {
    method: "PATCH",
    body: {
      schemas: ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
      Operations: [{ op: "Replace", path: "active", value: false }],
    },
  }, 200);
}

async function verifySessionsRevoked(userSessionCookie, controlSessionCookie) {
  const [userResponse, controlResponse] = await Promise.all([
    fetch(`${userURL}/ui/api/session`, { headers: { Cookie: userSessionCookie } }),
    fetch(`${controlURL}/control/api/session`, { headers: { Cookie: controlSessionCookie } }),
  ]);
  assert(userResponse.status >= 400, "SCIM deactivation did not invalidate the user SSO session");
  assert(controlResponse.status >= 400, "SCIM deactivation did not invalidate the control SSO session");
}

async function controlEnvelope(path, options = {}, expectedStatus = 200) {
  return envelope(`${controlURL}${path}`, {
    ...options,
    headers: { ...controlHeaders, ...(options.headers ?? {}) },
  }, expectedStatus);
}

async function scimRequest(connectorID, token, path, options = {}, expectedStatus = 200) {
  return jsonResponse(`${userURL}/scim/v2/${connectorID}${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/scim+json",
      ...(options.headers ?? {}),
    },
  }, expectedStatus);
}

async function envelope(url, options = {}, expectedStatus = 200) {
  const payload = await jsonResponse(url, options, expectedStatus);
  if (!payload || typeof payload !== "object" || !("data" in payload)) {
    throw new Error(`expected a data envelope from ${safeURL(url)}`);
  }
  return payload.data;
}

async function jsonResponse(url, options = {}, expectedStatus = 200) {
  const response = await expectResponse(url, options, expectedStatus);
  try {
    return await response.json();
  } catch {
    throw new Error(`expected JSON from ${safeURL(url)}`);
  }
}

async function expectResponse(url, options, expectedStatus) {
  const headers = new Headers(options.headers ?? {});
  let body = options.body;
  if (body !== undefined && typeof body !== "string") {
    headers.set("Content-Type", headers.get("Content-Type") ?? "application/json");
    body = JSON.stringify(body);
  }
  const response = await fetch(url, { ...options, headers, body });
  const expected = Array.isArray(expectedStatus) ? expectedStatus : [expectedStatus];
  if (!expected.includes(response.status)) {
    throw new Error(`${options.method ?? "GET"} ${safeURL(url)} returned ${response.status}`);
  }
  return response;
}

function rewriteMockURL(location) {
  const destination = new URL(location);
  assert(destination.hostname === "entra-mock" && destination.port === "9443", "OIDC start did not target the isolated Entra mock");
  const publicMock = new URL(entraMockURL);
  destination.protocol = publicMock.protocol;
  destination.host = publicMock.host;
  return destination.toString();
}

function rewriteControlCallback(location) {
  const destination = new URL(location);
  assert(destination.origin === "https://control.example.test", "control OIDC callback did not use the configured HTTPS ingress");
  const localControl = new URL(controlURL);
  destination.protocol = localControl.protocol;
  destination.host = localControl.host;
  return destination.toString();
}

function requireLocation(response, label) {
  const location = response.headers.get("location");
  if (!location) {
    throw new Error(`${label} did not return a redirect`);
  }
  return location;
}

function cookieFromResponse(response, name) {
  const values = typeof response.headers.getSetCookie === "function"
    ? response.headers.getSetCookie()
    : [response.headers.get("set-cookie") ?? ""];
  const prefix = `${name}=`;
  const cookie = values.find((value) => value.startsWith(prefix));
  if (!cookie) {
    throw new Error(`${name} was not set after OIDC callback`);
  }
  const separator = cookie.indexOf(";");
  return separator === -1 ? cookie : cookie.slice(0, separator);
}

function cookieValue(cookie) {
  const separator = cookie.indexOf("=");
  return separator === -1 ? "" : cookie.slice(separator + 1);
}

function safeURL(value) {
  const url = new URL(value);
  return `${url.origin}${url.pathname}`;
}

function requiredEnv(name) {
  const value = process.env[name]?.trim();
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function requiredURL(name) {
  const value = requiredEnv(name).replace(/\/$/, "");
  const url = new URL(value);
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error(`${name} must be an HTTP URL`);
  }
  return value;
}

function requireString(value, label) {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${label} is missing`);
  }
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
