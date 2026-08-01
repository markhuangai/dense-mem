import { createSign, generateKeyPairSync } from "node:crypto";
import { readFileSync } from "node:fs";
import { createServer } from "node:https";

const issuer = requiredEnv("DENSE_MEM_ENTRA_ISSUER").replace(/\/$/, "");
const certificate = readFileSync(requiredEnv("DENSE_MEM_ENTRA_CERT"));
const privateKey = readFileSync(requiredEnv("DENSE_MEM_ENTRA_KEY"));
const { privateKey: signingKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
const jwk = publicKey.export({ format: "jwk" });
const codes = new Map();
let codeSequence = 0;

createServer({ cert: certificate, key: privateKey }, (request, response) => {
  const url = new URL(request.url ?? "/", issuer);
  if (request.method === "GET" && url.pathname === "/.well-known/openid-configuration") {
    return sendJSON(response, 200, {
      issuer,
      authorization_endpoint: `${issuer}/authorize`,
      token_endpoint: `${issuer}/token`,
      jwks_uri: `${issuer}/jwks`,
      userinfo_endpoint: `${issuer}/userinfo`,
      response_types_supported: ["code"],
      subject_types_supported: ["public"],
      id_token_signing_alg_values_supported: ["RS256"],
    });
  }
  if (request.method === "GET" && url.pathname === "/jwks") {
    return sendJSON(response, 200, { keys: [{ ...jwk, use: "sig", kid: "entra-mock-key", alg: "RS256" }] });
  }
  if (request.method === "GET" && url.pathname === "/authorize") {
    const redirectURI = url.searchParams.get("redirect_uri");
    const state = url.searchParams.get("state");
    const clientID = url.searchParams.get("client_id");
    if (!redirectURI || !state || !clientID) {
      return sendJSON(response, 400, { error: "invalid_request" });
    }
    const code = `entra-mock-code-${++codeSequence}`;
    codes.set(code, {
      clientID,
      nonce: url.searchParams.get("nonce") ?? "",
    });
    const callback = new URL(redirectURI);
    callback.searchParams.set("code", code);
    callback.searchParams.set("state", state);
    response.writeHead(302, { Location: callback.toString() });
    return response.end();
  }
  if (request.method === "POST" && url.pathname === "/token") {
    return readBody(request).then((body) => {
      const code = new URLSearchParams(body).get("code") ?? "";
      const issued = codes.get(code);
      if (!issued) {
        return sendJSON(response, 400, { error: "invalid_grant" });
      }
      codes.delete(code);
      return sendJSON(response, 200, {
        access_token: "entra-mock-access-token",
        token_type: "Bearer",
        expires_in: 3600,
        id_token: signedIDToken(issued.clientID, issued.nonce),
      });
    }).catch(() => sendJSON(response, 400, { error: "invalid_request" }));
  }
  if (request.method === "GET" && url.pathname === "/userinfo") {
    return sendJSON(response, 200, identityClaims(""));
  }
  return sendJSON(response, 404, { error: "not_found" });
}).listen(9443, "0.0.0.0");

function signedIDToken(clientID, nonce) {
  const now = Math.floor(Date.now() / 1000);
  const header = base64URL(JSON.stringify({ alg: "RS256", kid: "entra-mock-key", typ: "JWT" }));
  const payload = base64URL(JSON.stringify({
    ...identityClaims(nonce),
    iss: issuer,
    aud: clientID,
    iat: now,
    exp: now + 3600,
  }));
  const input = `${header}.${payload}`;
  const signer = createSign("RSA-SHA256");
  signer.update(input);
  signer.end();
  return `${input}.${signer.sign(signingKey).toString("base64url")}`;
}

function identityClaims(nonce) {
  return {
    sub: "entra-user-1",
    oid: "entra-user-1",
    tid: "entra-tenant-1",
    email: "alex@example.test",
    preferred_username: "alex@example.test",
    name: "Alex Entra",
    nonce,
    groups: ["entra-research-manager", "entra-operations-manager", "entra-control-admins"],
  };
}

function base64URL(value) {
  return Buffer.from(value).toString("base64url");
}

function readBody(request) {
  return new Promise((resolve, reject) => {
    let body = "";
    request.setEncoding("utf8");
    request.on("data", (chunk) => {
      body += chunk;
    });
    request.on("end", () => resolve(body));
    request.on("error", reject);
  });
}

function sendJSON(response, status, payload) {
  response.writeHead(status, { "Content-Type": "application/json" });
  response.end(JSON.stringify(payload));
}

function requiredEnv(name) {
  const value = process.env[name]?.trim();
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}
