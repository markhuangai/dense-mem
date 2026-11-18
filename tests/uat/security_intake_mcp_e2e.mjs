#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { securityIntakeCases } from "./security_intake_cases.mjs";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const prometheusURL = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");
const composeProject = requiredEnv("DENSE_MEM_E2E_COMPOSE_PROJECT");
const composeFile = requiredEnv("DENSE_MEM_E2E_COMPOSE_FILE");

let rpcID = 0;
const runID = `security-intake-e2e-${Date.now()}`;

const { structuralRejections, rejectedCases, acceptedCases } = securityIntakeCases(runID);

assertScanPolicyColumnRemoved();

const verifierBeforeRejects = await prometheusValue("densemem_verifier_requests_total", teamID);
if (verifierBeforeRejects !== 0) {
  throw new Error("fresh security intake stack already recorded verifier requests");
}

const initialRejections = rejectedCases.filter((testCase) => structuralRejections.has(testCase.name));
const reviewRejections = rejectedCases.filter((testCase) => !structuralRejections.has(testCase.name));
for (const testCase of initialRejections) {
  await verifyRejection(testCase, "remember.initial_scan");
  const audit = await waitForAuditRecord(apiKey, testCase.correlationID);
  assertSafeAuditRecord(audit, testCase, teamID);
}
await assertVerifierStable(verifierBeforeRejects, teamID);
for (const testCase of reviewRejections) await verifyRejection(testCase, "remember.intake_review");

async function verifyRejection(testCase, component) {
  const response = await mcpRaw(apiKey, "remember", evidenceRememberInput(
    testCase.content,
    `${runID}:${testCase.name}`,
    `security-intake:${runID}:${testCase.name}`,
    testCase.clientComment,
  ), testCase.correlationID);
  assertRejectedResponse(response, testCase, component);
  assertNoCorrelatedIngest(testCase.correlationID);
}

const verifierAfterRejects = await waitForVerifierRequests(verifierBeforeRejects + reviewRejections.length, verifierBeforeRejects + reviewRejections.length * 3, teamID);
await assertVerifierStable(verifierAfterRejects, teamID);
let verifierAfterAccepted = verifierAfterRejects;
const acceptedResults = [];
for (const testCase of acceptedCases) {
  const correlationID = `${runID}:${testCase.name}`;
  const input = evidenceRememberInput(
    testCase.payload,
    `${runID}:${testCase.name}`,
    `security-intake:${runID}:${testCase.name}`,
    testCase.clientComment,
  );
  const accepted = await mcpSuccess(apiKey, "remember", input, correlationID);
  const submissionID = stringValue(accepted.submission_id);
  if (!submissionID) {
    throw new Error(`${testCase.name} remember did not return a submission_id`);
  }
  if (accepted.processing_state === "failed") {
    if (!testCase.allowProviderQuarantine || accepted.errors?.[0]?.code !== "submission_policy_rejected" || accepted.search_state !== "not_required") {
      throw new Error(`${testCase.name} provider rejection returned an unexpected terminal result: ${JSON.stringify(accepted)}`);
    }
    assertNoCorrelatedIngest(correlationID);
  } else {
    if (accepted.processing_state !== "completed") {
      throw new Error(`${testCase.name} remember returned an unexpected processing state: ${JSON.stringify(accepted)}`);
    }
    assertAcceptedIngest(submissionID, input.evidence[0].content, accepted.evidence.length);
  }
  if (testCase.name === "acronym_paths" || testCase.name === "acronym_hyphenation") {
    const replay = await mcpSuccess(apiKey, "remember", input, `${correlationID}:replay`);
    if (replay.submission_id !== submissionID || replay.processing_state !== accepted.processing_state ||
        JSON.stringify(replay.evidence) !== JSON.stringify(accepted.evidence)) {
      throw new Error(`${testCase.name} exact-key replay changed its terminal result`);
    }
  }
  verifierAfterAccepted = await waitForExactlyTwoVerifierRequests(verifierAfterAccepted, teamID);
  assertTerminalEvidenceDisposition(accepted, testCase.name);
  acceptedResults.push({
    name: testCase.name,
    submission_id: submissionID,
    processing_state: accepted.processing_state,
  });
}
verifierAfterAccepted = await assertVerifierStable(verifierAfterAccepted, teamID);

function assertTerminalEvidenceDisposition(placement, label) {
  if (Object.hasOwn(placement, "relationship_results") || !placement.evidence?.length) throw new Error(`${label} must return the submitted Evidence dispositions`);
  for (const result of placement.evidence) {
    if (result.evidence_index !== 0 || (placement.processing_state === "completed" && result.disposition !== "stored")) throw new Error(`${label} did not store exact Evidence`);
    if (placement.processing_state === "failed" && (result.disposition !== "not_stored" || result.reason !== "submission_policy_rejected")) throw new Error(`${label} rejection must commit no Evidence`);
  }
}

const isolatedTeam = await createTeam(`Security Intake Isolated ${runID}`);
const isolatedProfile = await createCredential(isolatedTeam.id, "Security Intake Isolated Profile");
const isolatedAudit = await userJSON("/ui/api/team/audit-log?limit=100", isolatedProfile.apiKey);
if ((isolatedAudit.data ?? []).some((entry) => rejectedCases.some((testCase) => entry.correlation_id === testCase.correlationID))) {
  throw new Error("security rejection audit entry leaked across teams");
}

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  verifier_requests_before_rejects: verifierBeforeRejects,
  verifier_requests_after_rejects: verifierAfterRejects,
  verifier_requests_after_accepted: verifierAfterAccepted,
  rejected_cases: rejectedCases.map((testCase) => ({
    name: testCase.name,
    error_code: testCase.expectedCode,
  })),
  accepted_cases: acceptedResults,
}, null, 2));

async function mcpSuccess(key, name, args, correlationID = "") {
  const response = await mcpRaw(key, name, args, correlationID);
  if (response.error) {
    throw new Error(`MCP ${name} returned ${boundedMCPError(response)}`);
  }
  if (response.result === undefined) {
    throw new Error(`MCP ${name} did not return a result`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} result did not contain text`);
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

function assertRejectedResponse(response, testCase, component) {
  if (response.error || !response.result?.isError) {
    throw new Error(`${testCase.name} returned an unexpected terminal MCP shape`);
  }
  const structured = response.result.structuredContent;
  const text = response.result.content?.[0]?.text;
  let payload = structured;
  if (!payload && typeof text === "string") {
    try { payload = JSON.parse(text); } catch { payload = null; }
  }
  const actual = payload?.errors?.[0]?.code;
  const explanation = payload?.errors?.[0];
  if (!explanation?.message?.includes("security check") || !explanation.message.includes("Input location:") ||
      !explanation.remediation?.includes("factual meaning") || !Array.isArray(explanation.details?.issues) ||
      explanation.details.issues.length === 0) {
    throw new Error("security rejection did not explain the verified category, input location, and legitimate recovery");
  }
  if (JSON.stringify(payload) !== JSON.stringify(JSON.parse(text))) {
    throw new Error("security rejection text and structured content differed");
  }
  for (const item of (payload.evidence ?? [])) {
    if (item.disposition === "not_stored" && (!item.message || !item.remediation)) {
      throw new Error("rejected batch item omitted its explanation or recovery guidance");
    }
  }
  if (actual !== "submission_policy_rejected" || payload?.processing_state !== "failed") {
    throw new Error(`${testCase.name} returned ${actual || "missing code"} instead of terminal policy rejection`);
  }
  if (explanation.details?.component !== component) throw new Error(`${testCase.name} rejected in ${explanation.details?.component} instead of ${component}`);
}

function boundedMCPError(response) {
  const message = response?.error?.message;
  return typeof message === "string" ? message : "missing_error_message";
}

function evidenceRememberInput(payload, idempotencyKey, source, clientComment = "") {
  return {
    target_space: "team_shared", idempotency_key: idempotencyKey,
    evidence: [{ content: `Project uses Store. ${payload}`, source_type: "document", source: clientComment || source, source_group: source }],
  };
}

async function waitForExactlyTwoVerifierRequests(before, targetTeamID) {
  return waitForVerifierRequests(before + 2, before + 2, targetTeamID);
}

async function waitForVerifierRequests(minimum, maximum, targetTeamID) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const value = await prometheusValue("densemem_verifier_requests_total", targetTeamID);
    if (value > maximum) throw new Error("Remember exceeded its verifier request budget");
    if (value >= minimum) return value;
    await delay(5_000);
  }
  throw new Error("safe remember did not produce its partition and assessment verifier requests");
}

async function assertVerifierStable(expected, targetTeamID) {
  let observed = expected;
  for (let attempt = 0; attempt < 3; attempt += 1) {
    await delay(5_000);
    observed = await prometheusValue("densemem_verifier_requests_total", targetTeamID);
    if (observed !== expected) {
      throw new Error("Remember produced an unexpected verifier request after its terminal result");
    }
  }
  return observed;
}

async function waitForAuditRecord(key, correlationID) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const response = await userJSON("/ui/api/team/audit-log?limit=100", key);
    const entry = (response.data ?? []).find((item) => item.correlation_id === correlationID);
    if (entry) {
      return entry;
    }
    await delay(1_000);
  }
  throw new Error("timed out waiting for security rejection audit record");
}

function assertSafeAuditRecord(entry, testCase, expectedTeamID) {
  if (entry.operation !== "SECURITY_REJECTED" || entry.entity_type !== "memory_intake_attempt") {
    throw new Error("security rejection audit record has an unexpected operation");
  }
  const metadata = entry.metadata ?? {};
  if (metadata.reason_code !== testCase.expectedCode || metadata.surface !== "remember") {
    throw new Error("security rejection audit metadata is incomplete");
  }
  if ("policy_version" in metadata || "policy_hash" in metadata) {
    throw new Error("security rejection audit metadata retained scanner policy identity");
  }
  if (!Array.isArray(metadata.signals) || metadata.signals.length === 0) {
    throw new Error("security rejection audit record has no bounded signals");
  }
  if (entry.team_id !== expectedTeamID || entry.correlation_id !== testCase.correlationID) {
    throw new Error("security rejection audit record has the wrong tenant context");
  }
  const serialized = JSON.stringify(entry);
  const forbiddenValues = [testCase.content, ...(testCase.auditForbiddenValues ?? [])];
  if (forbiddenValues.some((value) => serialized.includes(JSON.stringify(value).slice(1, -1)))) {
    throw new Error("security rejection audit record retained rejected evidence");
  }
  for (const signal of metadata.signals) {
    if (!Number.isInteger(signal.evidence_index) || !Number.isInteger(signal.span_start) || !Number.isInteger(signal.span_end) || signal.span_end <= signal.span_start) {
      throw new Error("security rejection audit signal does not contain a bounded span");
    }
    if (typeof signal.kind !== "string" || typeof signal.rule_id !== "string" || typeof signal.severity !== "string") {
      throw new Error("security rejection audit signal does not contain bounded rule metadata");
    }
  }
}

function assertScanPolicyColumnRemoved() {
  const count = Number(postgresQuery(`
    SELECT count(*)
    FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'evidence_security_events'
      AND column_name = 'scan_policy_hash';
  `));
  if (count !== 0) {
    throw new Error("evidence_security_events still exposes scan_policy_hash");
  }
}

function assertAcceptedIngest(ingestID, expectedContent, partitions) {
  const storedContent = postgresQuery(`
    SELECT encode(convert_to(content, 'UTF8'), 'hex')
    FROM remember_submitted_items
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND ingest_id = ${sqlLiteral(ingestID)}::uuid
      AND submitted_index = 0;
  `);
  if (storedContent !== Buffer.from(expectedContent).toString("hex")) {
    throw new Error("accepted remember did not preserve exact evidence content");
  }
  const passState = postgresQuery(`
    SELECT count(*)::text || '|' ||
           COALESCE(bool_and(
             NOT (metadata ? 'policy_version') AND NOT (metadata ? 'policy_hash')
           ), false)::text
    FROM evidence_security_events
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND ingest_id = ${sqlLiteral(ingestID)}::uuid
      AND event_kind = 'deterministic_scan'
      AND decision = 'pass';
  `);
  if (passState !== `${partitions}|true`) {
    throw new Error(`accepted remember deterministic pass state was ${passState || "missing"}`);
  }
}

function assertNoCorrelatedIngest(correlationID) {
  const count = Number(postgresQuery(`
    SELECT count(*)
    FROM knowledge_ingests
    WHERE team_id = ${sqlLiteral(teamID)}::uuid
      AND metadata #>> '{actor,correlation_id}' = ${sqlLiteral(correlationID)};
  `));
  if (count !== 0) {
    throw new Error("rejected evidence created a knowledge ingest");
  }
}

async function createTeam(name) {
  const response = await controlJSON("/control/api/teams", {
    method: "POST",
    body: JSON.stringify({ name, description: "security intake isolation e2e" }),
  });
  const id = stringValue(response.data?.id);
  if (!id) {
    throw new Error("control API did not return an isolated team ID");
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
    throw new Error("control API did not return an isolated credential key");
  }
  return { apiKey: newCredential };
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

async function userJSON(path, key) {
  return httpJSON(`${userURL}${path}`, {
    method: "GET",
    headers: { Authorization: `Bearer ${key}` },
  });
}

function postgresQuery(sql) {
  const result = spawnSync("docker", [
    "compose",
    "-p",
    composeProject,
    "-f",
    composeFile,
    "exec",
    "-T",
    "postgres",
    "sh",
    "-ec",
    'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "$1"',
    "security-intake-e2e",
    sql,
  ], {
    cwd: fileURLToPath(new URL("../..", import.meta.url)),
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`postgres query failed (${result.status})`);
  }
  return result.stdout.trim();
}

function sqlLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: ${redactHTTPBody(text)}`);
  }
  return text ? JSON.parse(text) : {};
}

function redactHTTPBody(_text) {
  return "response body redacted";
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
