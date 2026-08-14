#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");
const scenario = process.env.DENSE_MEM_E2E_FOUNDATION_SCENARIO ?? "identity_cutover";
let rpcID = 0;

if (scenario === "identity_cleanup_preflight") {
  await verifyCleanupPreflight();
} else if (scenario === "migration_convergence") {
  await verifyMigrationConvergence();
} else if (scenario === "identity_cutover") {
  await verifyIdentityCutover();
} else {
  throw new Error(`unknown v2.5 foundation scenario ${scenario}`);
}

console.log(JSON.stringify({ status: "ok", scenario, commit_sha: process.env.DENSE_MEM_E2E_COMMIT_SHA ?? "unknown" }, null, 2));

async function verifyCleanupPreflight() {
  const first = await controlJSON("/identity-cleanup/preflight");
  const second = await controlJSON("/identity-cleanup/preflight");
  if (first.data?.ready !== false || second.data?.ready !== false) {
    throw new Error("cleanup preflight unexpectedly permitted destructive cleanup");
  }
  const firstCodes = (first.data?.blockers ?? []).map((item) => item.code).sort();
  const secondCodes = (second.data?.blockers ?? []).map((item) => item.code).sort();
  if (JSON.stringify(firstCodes) !== JSON.stringify(secondCodes) || firstCodes.length === 0) {
    throw new Error("cleanup preflight was not stable or returned no bounded blockers");
  }
  for (const blocker of first.data.blockers) {
    if (!/^[a-z0-9_]+$/.test(String(blocker.code)) || typeof blocker.message !== "string" || blocker.message.length > 256) {
      throw new Error("cleanup preflight returned an unbounded blocker");
    }
  }
}

async function verifyMigrationConvergence() {
  const state = postgresRow(`
    SELECT concat(
      (SELECT max(version_id) FROM goose_db_version), '|',
      COALESCE(to_regclass('public.identity_compatibility_state')::text, ''), '|',
      COALESCE(to_regclass('public.actor_identities')::text, ''), '|',
      COALESCE(to_regclass('public.credentials')::text, ''), '|',
      COALESCE(to_regclass('public.ownership_aliases')::text, '')
    );
  `);
  const stateLine = state.join("|");
  if (!/^\d+\|identity_compatibility_state\|actor_identities\|credentials\|ownership_aliases$/.test(stateLine)) {
    throw new Error(`migration baseline/convergence state is incomplete: ${stateLine}`);
  }
  const manifest = spawnSync("bash", ["-lc", "scripts/postgres-schema-catalog.sh check"], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
    timeout: 1_800_000,
  });
  if (manifest.status !== 0) {
    throw new Error(`schema catalog check failed: ${manifest.stderr || "redacted"}`);
  }
}

async function verifyIdentityCutover() {
  const before = postgresRow(`
    SELECT concat(
      p.id::text, '|',
      COALESCE(c.id::text, ''), '|',
      COALESCE(a.legacy_owner_id::text, ''), '|',
      COALESCE(m.team_admin::text, 'false')
    )
    FROM team_profiles p
    LEFT JOIN credentials c ON c.legacy_profile_id = p.id
    LEFT JOIN ownership_aliases a ON a.team_id = p.team_id AND a.legacy_owner_id = p.id
    LEFT JOIN team_memberships m ON m.legacy_profile_id = p.id
    WHERE p.id = ${sqlLiteral(process.env.DENSE_MEM_E2E_PROFILE_ID ?? "00000000-0000-0000-0000-000000000000")}::uuid
    LIMIT 1;
  `, 4);
  if (before.length !== 4 || before.some((value) => !value)) {
    throw new Error("legacy profile does not have stable credential, alias, and membership records");
  }

  const list = await mcpJSON(apiKey, "tools/list", {});
  if (!Array.isArray(list.result?.tools) || !list.result.tools.some((tool) => tool.name === "remember")) {
    throw new Error("canonical credential could not authenticate against MCP");
  }
  const status = await mcpJSON(apiKey, "tools/call", {
    name: "get_submission_status",
    arguments: { submission_id: "00000000-0000-0000-0000-000000000000" },
  });
  if (!status.error || typeof status.error.message !== "string" || status.error.message.includes(teamID)) {
    throw new Error("missing submission status did not preserve bounded owner isolation");
  }
}

async function mcpJSON(key, method, params, headers = {}) {
  const response = await fetch(`${userURL}/mcp`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, Accept: "application/json", "Content-Type": "application/json", ...headers },
    body: JSON.stringify({ jsonrpc: "2.0", id: ++rpcID, method, params }),
  });
  const text = await response.text();
  if (!response.ok && response.status !== 400) {
    throw new Error(`MCP HTTP ${response.status} response body redacted`);
  }
  return text ? JSON.parse(text) : {};
}

async function controlJSON(path) {
  const response = await fetch(`${controlURL}/control/api${path}`, { headers: { Authorization: `Bearer ${controlToken}` } });
  const text = await response.text();
  if (!response.ok) throw new Error(`control HTTP ${response.status} response body redacted`);
  return text ? JSON.parse(text) : {};
}

function postgresRow(sql, expectedFields = 0) {
  const result = spawnSync("docker", ["compose", "-p", composeProject, "-f", composeFile, "exec", "-T", "postgres", "sh", "-ec", 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|" -c "$1"', "v2.5-foundations", sql], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)), encoding: "utf8",
  });
  if (result.status !== 0) throw new Error("postgres foundation query failed");
  const fields = result.stdout.trim().split("|");
  if (expectedFields && fields.length !== expectedFields) throw new Error("postgres foundation query returned unexpected fields");
  return fields;
}

function sqlLiteral(value) { return `'${String(value).replaceAll("'", "''")}'`; }
function requiredEnv(name) { const value = process.env[name]; if (!value) throw new Error(`${name} is required`); return value; }
