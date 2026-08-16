#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const prometheusURL = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");

let rpcID = 0;
const runID = `semantic-holds-e2e-${Date.now()}`;

const sameTeamOtherProfile = await createCredential(teamID, `Semantic Holds Other ${runID}`);
const otherTeam = await createTeam(`Semantic Holds Other Team ${runID}`);
const otherTeamProfile = await createCredential(otherTeam.id, `Semantic Holds Cross Team ${runID}`);

const verifierBeforeTarget = await prometheusValue("densemem_verifier_requests_total", teamID);
const target = await mcpSuccess(apiKey, "remember", rememberInput(
  "proposal",
  `${runID}:target`,
  "Project Nova uses Store.",
));
const targetID = requiredString(target.submission_id, "target submission_id");
const targetStatus = await waitForState(apiKey, targetID, "awaiting_review");
assertSemanticHoldStatus(targetStatus, targetID);
const targetBeforeReplacement = holdSummary(targetID);
if (targetBeforeReplacement.holdState !== "active") {
  throw new Error("target did not become an active semantic hold");
}
if (targetBeforeReplacement.holdCount !== 1 || targetBeforeReplacement.semanticWrites !== 0) {
  throw new Error("target hold did not preserve zero semantic writes and one hold fact");
}

await waitForVerifierRequest(teamID, verifierBeforeTarget);
const verifierBeforeUnauthorized = await prometheusValue("densemem_verifier_requests_total", teamID);
const sameTeamUnauthorized = await mcpRaw(
  sameTeamOtherProfile.apiKey,
  "remember",
  rememberInput("statement", `${runID}:cross-profile`, "Project Nova uses Store.", targetID),
  `${runID}:cross-profile`,
);
assertMCPError(sameTeamUnauthorized, "cross-profile replacement");
const crossTeamUnauthorized = await mcpRaw(
  otherTeamProfile.apiKey,
  "remember",
  rememberInput("statement", `${runID}:cross-team`, "Project Nova uses Store.", targetID),
  `${runID}:cross-team`,
);
assertMCPError(crossTeamUnauthorized, "cross-team replacement");
if (await prometheusValue("densemem_verifier_requests_total", teamID) !== verifierBeforeUnauthorized) {
  throw new Error("unauthorized replacements reached the live verifier");
}
if (postgresCountByCorrelation(`${runID}:cross-profile`) !== 0 || postgresCountByCorrelation(`${runID}:cross-team`) !== 0) {
  throw new Error("unauthorized replacement created a staged ingest");
}

const heldSuccessor = await mcpSuccess(apiKey, "remember", rememberInput(
  "proposal",
  `${runID}:held-successor`,
  "Project Nova uses Store.",
  targetID,
));
const heldSuccessorID = requiredString(heldSuccessor.submission_id, "held successor submission_id");
const heldSuccessorStatus = await waitForState(apiKey, heldSuccessorID, "awaiting_review");
assertSemanticHoldStatus(heldSuccessorStatus, heldSuccessorID);
const afterHeldSuccessor = holdSummary(targetID, heldSuccessorID);
if (afterHeldSuccessor.holdState !== targetBeforeReplacement.holdState || afterHeldSuccessor.supersededBy) {
  throw new Error("held successor changed the target hold");
}
if (afterHeldSuccessor.successorStatus !== "awaiting_review" || afterHeldSuccessor.successorHoldState !== "active") {
  throw new Error("held successor did not create its own hold and release the target slot");
}
if (afterHeldSuccessor.releaseEvents !== 1) {
  throw new Error("held successor did not append one release outcome");
}

const successfulSuccessor = await mcpSuccess(apiKey, "remember", rememberInput(
  "statement",
  `${runID}:successful-successor`,
  "Project Nova uses Store.",
  targetID,
));
const successfulSuccessorID = requiredString(successfulSuccessor.submission_id, "successful successor submission_id");
await waitForState(apiKey, successfulSuccessorID, "completed");
const afterPromotion = holdSummary(targetID, heldSuccessorID, successfulSuccessorID);
if (afterPromotion.holdState !== "superseded" || afterPromotion.supersededBy !== afterPromotion.successorRunID) {
  throw new Error("successful successor did not atomically supersede the target");
}
if (afterPromotion.successorStatus !== "awaiting_review" || afterPromotion.promotedStatus !== "completed") {
  throw new Error("successor status projection is inconsistent after promotion");
}
if (afterPromotion.promotionEvents !== 1 || afterPromotion.supersededEvents !== 1) {
  throw new Error("replacement promotion audit events were not idempotent");
}

const verifierBeforeSupersededRetry = await prometheusValue("densemem_verifier_requests_total", teamID);
const supersededRetry = await mcpRaw(
  apiKey,
  "remember",
  rememberInput("statement", `${runID}:superseded-retry`, "Project Nova uses Store.", targetID),
  `${runID}:superseded-retry`,
);
assertMCPError(supersededRetry, "superseded replacement");
if (await prometheusValue("densemem_verifier_requests_total", teamID) !== verifierBeforeSupersededRetry) {
  throw new Error("superseded replacement reached the live verifier");
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  target_submission_id: targetID,
  held_successor_submission_id: heldSuccessorID,
  successful_successor_submission_id: successfulSuccessorID,
  target_hold_state: afterPromotion.holdState,
  successful_successor_state: afterPromotion.promotedStatus,
  negative_cases: ["cross-profile", "cross-team", "superseded-target"],
}, null, 2));

function rememberInput(modality, idempotencyKey, content, replacementID = "") {
  const subject = "Project Nova";
  const predicate = "uses";
  const object = "Store";
  const input = {
    evidence: [{
      content,
      source_type: "document",
      source: `${runID}:${idempotencyKey}`,
      source_group: runID,
      idempotency_key: `${idempotencyKey}:evidence`,
    }],
    relationships: [{
      ref: `${idempotencyKey}:relationship`,
      subject: {
        name: subject,
        entity_kind: "project",
      },
      predicate: {
        proposed_key: predicate,
      },
      object: {
        entity: {
          name: object,
          entity_kind: "product",
        },
      },
      polarity: "+",
      modality,
      evidence_indices: [0],
    }],
    idempotency_key: idempotencyKey,
  };
  if (replacementID) {
    input.replaces_submission_id = replacementID;
  }
  return input;
}

async function waitForState(key, submissionID, expected) {
  const terminalStates = new Set(["completed", "awaiting_review", "rejected", "failed", "quarantined"]);
  for (let attempt = 0; attempt < 240; attempt += 1) {
    const status = await mcpSuccess(key, "get_submission_status", { submission_id: submissionID });
    const state = stringValue(status.processing_state);
    if (state === expected) {
      return status;
    }
    if (terminalStates.has(state)) {
      throw new Error(`submission ${submissionID} reached ${state} while waiting for ${expected}`);
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for ${submissionID} to reach ${expected}`);
}

function assertSemanticHoldStatus(status, submissionID) {
  const hold = status.semantic_hold;
  if (hold?.state !== "active" || !Array.isArray(hold.issues) || hold.issues.length === 0) {
    throw new Error("awaiting_review status did not expose bounded semantic hold issues");
  }
  if (hold.replacement?.tool !== "remember" || hold.replacement?.replaces_submission_id !== submissionID) {
    throw new Error("semantic hold did not expose complete replacement guidance");
  }
  if (typeof hold.replacement?.instruction !== "string" || !hold.replacement.instruction.includes("complete corrected replacement batch")) {
    throw new Error("semantic hold replacement instruction is missing or unbounded");
  }
}

function holdSummary(targetID, heldSuccessorID = "", successfulSuccessorID = "") {
  const row = postgresRow(`
    WITH target AS (
      SELECT placement_run_id, semantic_hold_state, superseded_by_placement_run_id::text AS superseded_by
      FROM placement_runs
      WHERE team_id = ${sqlLiteral(teamID)}::uuid AND ingest_id = ${sqlLiteral(targetID)}::uuid
    ), held_successor AS (
      SELECT run.status, run.semantic_hold_state, run.placement_run_id
      FROM placement_runs AS run
      WHERE run.team_id = ${sqlLiteral(teamID)}::uuid AND run.ingest_id = ${sqlLiteral(heldSuccessorID || "00000000-0000-0000-0000-000000000000")}::uuid
    ), successful_successor AS (
      SELECT run.status, run.placement_run_id
      FROM placement_runs AS run
      WHERE run.team_id = ${sqlLiteral(teamID)}::uuid AND run.ingest_id = ${sqlLiteral(successfulSuccessorID || "00000000-0000-0000-0000-000000000000")}::uuid
    )
    SELECT
      COALESCE((SELECT semantic_hold_state FROM target), ''),
      COALESCE((SELECT superseded_by FROM target), ''),
      (SELECT count(*) FROM submission_holds AS hold WHERE hold.team_id = ${sqlLiteral(teamID)}::uuid AND hold.placement_run_id = (SELECT placement_run_id FROM target)),
      (SELECT count(*) FROM entity_resolution_events WHERE team_id = ${sqlLiteral(teamID)}::uuid AND assessment_id IN (SELECT assessment_id FROM placement_assessments WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM target))),
      COALESCE((SELECT status FROM held_successor), ''),
      COALESCE((SELECT semantic_hold_state FROM held_successor), ''),
      (SELECT count(*) FROM placement_outcomes WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM held_successor) AND outcome_kind = 'submission_replacement_released'),
      COALESCE((SELECT status FROM successful_successor), ''),
      COALESCE((SELECT placement_run_id::text FROM successful_successor), ''),
      (SELECT count(*) FROM placement_outcomes WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM successful_successor) AND outcome_kind = 'submission_replacement_promoted'),
      (SELECT count(*) FROM placement_outcomes WHERE team_id = ${sqlLiteral(teamID)}::uuid AND placement_run_id = (SELECT placement_run_id FROM target) AND outcome_kind = 'submission_hold_superseded');
  `, 11);
  return {
    holdState: stringValue(row[0]),
    supersededBy: stringValue(row[1]),
    holdCount: positiveCount(row[2]),
    semanticWrites: positiveCount(row[3]),
    successorStatus: stringValue(row[4]),
    successorHoldState: stringValue(row[5]),
    releaseEvents: positiveCount(row[6]),
    promotedStatus: stringValue(row[7]),
    successorRunID: stringValue(row[8]),
    promotionEvents: positiveCount(row[9]),
    supersededEvents: positiveCount(row[10]),
  };
}

function postgresCountByCorrelation(correlationID) {
  return positiveCount(postgresRow(`
    SELECT count(*) FROM knowledge_ingests
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND metadata #>> '{actor,correlation_id}' = ${sqlLiteral(correlationID)};
  `, 1)[0]);
}

async function mcpSuccess(key, name, args, correlationID = "") {
  const response = await mcpRaw(key, name, args, correlationID);
  if (response.error) {
    throw new Error(`MCP ${name} returned a bounded error`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} did not return JSON content`);
  }
  return JSON.parse(text);
}

async function mcpRaw(key, name, args, correlationID = "") {
  const headers = {
    Authorization: `Bearer ${key}`,
    Accept: "application/json",
    "Content-Type": "application/json",
  };
  if (correlationID) {
    headers["X-Correlation-ID"] = correlationID;
  }
  return httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers,
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/call",
      params: { name, arguments: args },
    }),
  });
}

function assertMCPError(response, label) {
  if (!response.error || response.result !== undefined) {
    throw new Error(`${label} unexpectedly succeeded`);
  }
}

async function createTeam(name) {
  const response = await controlJSON("/control/api/teams", {
    method: "POST",
    body: JSON.stringify({ name, description: "semantic holds e2e" }),
  });
  const id = stringValue(response.data?.id);
  if (!id) {
    throw new Error("control API did not return a team ID");
  }
  return { id };
}

async function createCredential(targetTeamID, name) {
  const response = await controlJSON(`/control/api/teams/${targetTeamID}/credentials`, {
    method: "POST",
    body: JSON.stringify({ name, role: "member", scopes: ["read", "write"], rate_limit: 300 }),
  });
  const newCredential = stringValue(response.data?.api_key);
  if (!newCredential) {
    throw new Error("control API did not return a credential key");
  }
  return { apiKey: newCredential };
}

async function controlJSON(path, options) {
  return httpJSON(`${controlURL}${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  });
}

async function prometheusValue(metric, targetTeamID) {
  const url = new URL("/api/v1/query", `${prometheusURL}/`);
  url.searchParams.set("query", `sum(${metric}{team_id="${targetTeamID}"})`);
  const response = await httpJSON(url.toString(), { method: "GET" });
  const value = response.data?.result?.[0]?.value?.[1];
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) {
    throw new Error(`Prometheus returned a non-numeric ${metric}`);
  }
  return parsed;
}

async function waitForVerifierRequest(targetTeamID, previousValue) {
  for (let attempt = 0; attempt < 24; attempt += 1) {
    if (await prometheusValue("densemem_verifier_requests_total", targetTeamID) > previousValue) {
      return;
    }
    await delay(2_000);
  }
  throw new Error("target verifier request was not reflected in Prometheus");
}

function postgresRow(sql, expectedFields = 0) {
  const result = spawnSync("docker", [
    "compose", "-p", composeProject, "-f", composeFile,
    "exec", "-T", "postgres", "sh", "-ec",
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -F "|" -c "$1"',
    "semantic-holds-e2e", sql,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error("postgres semantic-hold query failed");
  }
  const output = result.stdout.trim();
  if (!output) {
    throw new Error("postgres semantic-hold query returned no rows");
  }
  const fields = output.split("|");
  if (expectedFields > 0 && fields.length !== expectedFields) {
    throw new Error("postgres semantic-hold query returned an unexpected column count");
  }
  return fields;
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function requiredString(value, field) {
  const result = stringValue(value);
  if (!result) {
    throw new Error(`${field} is missing`);
  }
  return result;
}

function positiveCount(value) {
  const parsed = Number(value ?? 0);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new Error("postgres query contained an invalid count");
  }
  return parsed;
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: response body redacted`);
  }
  return text ? JSON.parse(text) : {};
}

function stringValue(value) {
  return typeof value === "string" ? value.trim() : "";
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
