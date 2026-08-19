import { constants, generateKeyPairSync, sign } from "node:crypto";
import { readFileSync } from "node:fs";
import { createServer } from "node:https";

const certificate = readFileSync(requiredEnv("DENSE_MEM_OAUTH_CERT"));
const privateKey = readFileSync(requiredEnv("DENSE_MEM_OAUTH_KEY"));
const issuerBase = requiredEnv("DENSE_MEM_OAUTH_ISSUER_BASE").replace(/\/$/, "");
const fixtureToken = requiredEnv("DENSE_MEM_OAUTH_FIXTURE_TOKEN");

const profiles = {
  entra: createProfile("RS256", "dense-mem-entra", { oid: "entra-user", scp: "memory.read memory.write" }),
  pingone: createProfile("PS256", "urn:dense-mem:pingone", { scope: "ping.read ping.write" }),
  generic: createProfile("ES256", "https://dense-mem.example.test/mcp", { permissions: ["generic.read", "generic.write"] }),
};

const server = createServer({ cert: certificate, key: privateKey }, async (request, response) => {
  try {
    const requestURL = new URL(request.url ?? "/", issuerBase);
    if (request.method === "GET" && requestURL.pathname === "/health") {
      return sendJSON(response, 200, { status: "ok" });
    }
    const discoveryMatch = requestURL.pathname.match(/^\/(entra|pingone|generic)\/\.well-known\/openid-configuration$/);
    if (request.method === "GET" && discoveryMatch) {
      const name = discoveryMatch[1];
      const profile = profiles[name];
      profile.discoveryRequests += 1;
      if (profile.discoveryOutage) return sendJSON(response, 503, { error: "temporarily_unavailable" });
      return sendJSON(response, 200, {
        issuer: profileIssuer(name),
        jwks_uri: `${issuerBase}/${name}/jwks`,
        authorization_endpoint: `${issuerBase}/${name}/authorize`,
        token_endpoint: `${issuerBase}/${name}/token`,
      });
    }
    const jwksMatch = requestURL.pathname.match(/^\/(entra|pingone|generic)\/jwks$/);
    if (request.method === "GET" && jwksMatch) {
      const profile = profiles[jwksMatch[1]];
      profile.jwksRequests += 1;
      if (profile.jwksOutage) return sendJSON(response, 503, { error: "temporarily_unavailable" });
      return sendJSON(response, 200, { keys: [publicJWK(profile.keys[profile.activeKey], profile.algorithm)] });
    }
    if (request.method === "POST" && requestURL.pathname === "/fixture/token") {
      if (!fixtureAuthorized(request)) return sendJSON(response, 401, { error: "unauthorized" });
      const input = await readJSON(request);
      const profile = profiles[input.profile];
      if (!profile) return sendJSON(response, 400, { error: "invalid_profile" });
      return sendJSON(response, 200, { token: issueToken(input.profile, profile, input) });
    }
    if (request.method === "POST" && requestURL.pathname === "/fixture/state") {
      if (!fixtureAuthorized(request)) return sendJSON(response, 401, { error: "unauthorized" });
      const input = await readJSON(request);
      const profile = profiles[input.profile];
      if (!profile) return sendJSON(response, 400, { error: "invalid_profile" });
      if (input.active_key !== undefined) {
        if (!profile.keys[input.active_key] || input.active_key === "wrong") {
          return sendJSON(response, 400, { error: "invalid_key" });
        }
        profile.activeKey = input.active_key;
      }
      if (input.jwks_outage !== undefined) profile.jwksOutage = input.jwks_outage === true;
      if (input.discovery_outage !== undefined) profile.discoveryOutage = input.discovery_outage === true;
      return sendJSON(response, 200, { status: "updated" });
    }
    if (request.method === "POST" && requestURL.pathname === "/fixture/stats") {
      if (!fixtureAuthorized(request)) return sendJSON(response, 401, { error: "unauthorized" });
      const input = await readJSON(request);
      const profile = profiles[input.profile];
      if (!profile) return sendJSON(response, 400, { error: "invalid_profile" });
      return sendJSON(response, 200, {
        discovery_requests: profile.discoveryRequests,
        jwks_requests: profile.jwksRequests,
      });
    }
    return sendJSON(response, 404, { error: "not_found" });
  } catch {
    return sendJSON(response, 400, { error: "invalid_request" });
  }
});

server.listen(9444, "0.0.0.0");

function createProfile(algorithm, audience, extraClaims) {
  return {
    algorithm,
    audience,
    extraClaims,
    activeKey: "primary",
    jwksOutage: false,
    discoveryOutage: false,
    discoveryRequests: 0,
    jwksRequests: 0,
    keys: {
      primary: generateSigningKey(algorithm, "primary"),
      secondary: generateSigningKey(algorithm, "secondary"),
      future: generateSigningKey(algorithm, "future"),
      wrong: generateSigningKey(algorithm, "wrong"),
    },
  };
}

function generateSigningKey(algorithm, label) {
  const pair = algorithm === "ES256"
    ? generateKeyPairSync("ec", { namedCurve: "P-256" })
    : generateKeyPairSync("rsa", { modulusLength: 2048, publicExponent: 0x10001 });
  return { ...pair, label };
}

function publicJWK(key, algorithm) {
  return {
    ...key.publicKey.export({ format: "jwk" }),
    use: "sig",
    kid: key.label,
    alg: algorithm,
  };
}

function issueToken(name, profile, input) {
  const now = Math.floor(Date.now() / 1000);
  const selectedKey = profile.keys[input.key ?? profile.activeKey];
  if (!selectedKey) throw new Error("invalid signing key");
  const header = {
    alg: input.header_alg ?? profile.algorithm,
    kid: input.kid ?? selectedKey.label,
    typ: "JWT",
    ...(input.header ?? {}),
  };
  const claims = {
    iss: profileIssuer(name),
    aud: profile.audience,
    sub: `${name}-user`,
    iat: now,
    nbf: now - 5,
    exp: now + 300,
    ...profile.extraClaims,
    ...(input.claims ?? {}),
  };
  for (const field of input.omit ?? []) delete claims[field];

  let encodedHeader = JSON.stringify(header);
  let encodedClaims = JSON.stringify(claims);
  if (input.mode === "duplicate_header") {
    encodedHeader = `${encodedHeader.slice(0, -1)},"alg":"${profile.algorithm}"}`;
  }
  if (input.mode === "duplicate_claim") {
    encodedClaims = `${encodedClaims.slice(0, -1)},"sub":"duplicate-subject"}`;
  }
  const signingInput = `${base64URL(encodedHeader)}.${base64URL(encodedClaims)}`;
  const signature = signJWT(signingInput, selectedKey.privateKey, input.sign_alg ?? profile.algorithm);
  return `${signingInput}.${signature.toString("base64url")}`;
}

function profileIssuer(name) {
  const issuer = `${issuerBase}/${name}`;
  return name === "generic" ? `${issuer}/` : issuer;
}

function signJWT(value, key, algorithm) {
  if (algorithm === "PS256") {
    return sign("sha256", Buffer.from(value), {
      key,
      padding: constants.RSA_PKCS1_PSS_PADDING,
      saltLength: 32,
    });
  }
  if (algorithm === "ES256") {
    return sign("sha256", Buffer.from(value), { key, dsaEncoding: "ieee-p1363" });
  }
  return sign("RSA-SHA256", Buffer.from(value), key);
}

function fixtureAuthorized(request) {
  return request.headers.authorization === `Bearer ${fixtureToken}`;
}

async function readJSON(request) {
  let body = "";
  for await (const chunk of request) {
    body += chunk;
    if (body.length > 64 * 1024) throw new Error("request too large");
  }
  return body ? JSON.parse(body) : {};
}

function sendJSON(response, status, payload) {
  const body = JSON.stringify(payload);
  response.writeHead(status, {
    "Content-Type": "application/json",
    "Content-Length": Buffer.byteLength(body),
    "Cache-Control": "no-store",
  });
  response.end(body);
}

function base64URL(value) {
  return Buffer.from(value).toString("base64url");
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
