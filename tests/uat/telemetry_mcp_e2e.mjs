#!/usr/bin/env node

import { spawnSync } from "node:child_process";
const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const prometheusURL = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");

let rpcID = 0;
const runID = `telemetry-e2e-${Date.now()}`;

const pricing = await controlJSON("/config/telemetry-pricing", { method: "GET" });
const effective = pricing.data?.effective ?? {};
if (!nonEmptyString(effective.verifier_model) || !nonEmptyString(effective.embedding_model)) {
  throw new Error("telemetry pricing must report the existing verifier and embedding models");
}
const pricingKeys = new Set((pricing.data?.items ?? []).map((item) => item.key));
for (const key of [
  "TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS",
  "TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS",
  "TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS",
]) {
  if (!pricingKeys.has(key)) {
    throw new Error(`telemetry pricing response missing ${key}`);
  }
}

await controlJSON("/config/telemetry-pricing", {
  method: "PATCH",
  body: JSON.stringify({
    items: [
      { key: "TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS", value: "1" },
      { key: "TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS", value: "1" },
      { key: "TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS", value: "1" },
    ],
  }),
});

const telemetryContent = `Telemetry E2E ${runID}: Dense-Mem uses exact evidence before semantic processing.`;
const subjectStart = telemetryContent.indexOf("Dense-Mem");
const predicateStart = telemetryContent.indexOf("uses", subjectStart);
const objectStart = telemetryContent.indexOf("exact evidence", predicateStart);
const remember = await mcpTool("remember", {
  evidence: [{
    content: telemetryContent,
    source_type: "document",
    source: `telemetry:${runID}`,
    source_group: `telemetry:${runID}`,
    idempotency_key: runID,
  }],
  relationships: [{
    ref: `${runID}:relationship`,
    subject: {
      name: "Dense-Mem",
      entity_kind: "project",
      span: { evidence_index: 0, start: subjectStart, end: subjectStart + "Dense-Mem".length },
    },
    predicate: {
      proposed_key: "uses",
      surface: "uses",
      span: { evidence_index: 0, start: predicateStart, end: predicateStart + "uses".length },
    },
    object: {
      entity: {
        name: "exact evidence",
        entity_kind: "concept",
        span: { evidence_index: 0, start: objectStart, end: objectStart + "exact evidence".length },
      },
    },
    polarity: "+",
    modality: "statement",
    supports: [{ evidence_index: 0, start: 0, end: Array.from(telemetryContent).length }],
  }],
});
const submissionID = String(remember.submission_id ?? "");
if (!submissionID) {
  throw new Error("remember did not return a submission_id");
}

const placementStatus = await waitForFirstDisposition(submissionID);
await mcpTool("recall_memory", {
  query: `Telemetry E2E ${runID} exact evidence`,
  limit: 5,
});

const signals = await waitForTelemetrySignals();
const profiles = await controlJSON(`/teams/${teamID}/profiles`, { method: "GET" });
const profileID = String(profiles.data?.[0]?.id ?? "");
if (!profileID) {
  throw new Error("telemetry e2e could not resolve the seeded profile id");
}

const telemetryMatrix = await validateTelemetryMatrix(profileID);
const disabledFeatures = await validateDisabledFeatureReasons();
const isolation = await validateTelemetryIsolation(profileID);
const unsupportedScope = await validateUnsupportedScope();
const profileScopeValidation = await validateProfileScopeRequiresTeam(profileID);
const userLifecycle = await validateUserLifecycleCards();
const partialFailure = await validatePartialPrometheusFailure();

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  placement_status: placementStatus,
  remember_acknowledgements: signals.rememberAcknowledgements,
  first_dispositions: signals.firstDispositions,
  recalls: signals.recalls,
  ai_cost_usd: signals.aiCostUSD,
  telemetry_matrix: telemetryMatrix,
  disabled_features: disabledFeatures,
  isolation,
  unsupported_scope: unsupportedScope,
  profile_scope_validation: profileScopeValidation,
  user_lifecycle: userLifecycle,
  partial_prometheus_failure: partialFailure,
}, null, 2));

async function validateTelemetryMatrix(profileID) {
  const matrix = [];
  for (const window of ["15m", "1h"]) {
    for (const [scope, params] of [
      ["system", "scope=system"],
      ["team", `scope=team&team_id=${encodeURIComponent(teamID)}`],
      ["profile", `scope=profile&team_id=${encodeURIComponent(teamID)}&profile_id=${encodeURIComponent(profileID)}`],
    ]) {
      const snapshot = await controlJSON(`/telemetry?window=${window}&${params}`, { method: "GET" });
      assertTelemetrySnapshot(snapshot.data, scope, window);
      matrix.push({ window, scope, status: snapshot.data.status, cards: snapshot.data.cards.length, series: snapshot.data.series.length });
    }
  }
  return matrix;
}

function assertTelemetrySnapshot(snapshot, expectedScope, expectedWindow) {
  assert(snapshot && typeof snapshot === "object", `${expectedScope}/${expectedWindow} telemetry snapshot missing`);
  assert(snapshot.window?.key === expectedWindow, `${expectedScope}/${expectedWindow} returned the wrong window`);
  assert(snapshot.scope?.type === expectedScope, `${expectedScope}/${expectedWindow} returned the wrong scope`);
  assert(nonEmptyString(snapshot.generated_at), `${expectedScope}/${expectedWindow} omitted generated_at`);
  assert(["ready", "degraded", "unavailable"].includes(snapshot.status), `${expectedScope}/${expectedWindow} returned an invalid status`);
  const cards = [...(snapshot.windowed_cards ?? []), ...(snapshot.current_cards ?? [])];
  const series = [...(snapshot.activity_series ?? []), ...(snapshot.state_series ?? [])];
  assert(cards.length > 0, `${expectedScope}/${expectedWindow} returned no telemetry cards`);
  assert(series.length > 0, `${expectedScope}/${expectedWindow} returned no telemetry series`);
  const retired = new Set(["verify_verdicts", "dream_promote_candidates"]);
  for (const item of [...cards, ...series]) {
    assert(!retired.has(item.id), `${expectedScope}/${expectedWindow} returned retired item ${item.id}`);
    assert(["ready", "inactive", "unavailable", "unsupported"].includes(item.status), `${item.id} returned an invalid item status`);
    if (item.status === "ready") {
      if ("available" in item) {
        assert(item.available === true, `${item.id} marked ready without available=true`);
      }
      if ("value" in item) {
        assert(Number.isFinite(Number(item.value)), `${item.id} returned a non-finite value`);
      }
      if ("points" in item) {
        assert(Array.isArray(item.points) && item.points.length > 0, `${item.id} marked ready without samples`);
      }
    } else {
      assert(nonEmptyString(item.reason_code), `${item.id} omitted a bounded reason code`);
      assert(nonEmptyString(item.reason), `${item.id} omitted a bounded reason`);
    }
  }
  assert(!JSON.stringify(snapshot).match(/password|stack trace|select .* from/i), `${expectedScope}/${expectedWindow} exposed an internal error`);
}

async function validateDisabledFeatureReasons() {
  const recallBefore = await controlJSON("/config/recall-feedback", { method: "GET" });
  const dreamingBefore = await controlJSON("/config/dreaming", { method: "GET" });
  try {
    await controlJSON("/config/recall-feedback", {
      method: "PATCH",
      body: JSON.stringify({ items: [{ key: "RECALL_FEEDBACK_ENABLED", value: "false" }] }),
    });
    await controlJSON("/config/dreaming", {
      method: "PATCH",
      body: JSON.stringify({ items: [{ key: "DREAMING_ENABLED", value: "false" }] }),
    });
    const snapshot = await controlJSON("/telemetry?window=15m&scope=system", { method: "GET" });
    for (const id of ["llm_recall_used_rate", "llm_recall_quality_score", "dream_feedbacks"]) {
      const item = (snapshot.data.windowed_cards ?? []).find((card) => card.id === id);
      assert(item?.status === "inactive", `disabled feature item ${id} was not inactive`);
      assert(item.reason_code === "feature_disabled", `disabled feature item ${id} omitted feature_disabled`);
    }
    return true;
  } finally {
    try {
      await restoreConfig("/config/recall-feedback", recallBefore);
    } finally {
      await restoreConfig("/config/dreaming", dreamingBefore);
    }
  }
}

async function restoreConfig(path, snapshot) {
  const items = (snapshot.data?.items ?? []).map((item) => ({ key: item.key, value: item.value }));
  if (items.length > 0) {
    await controlJSON(path, { method: "PATCH", body: JSON.stringify({ items }) });
  }
}

async function validateTelemetryIsolation(profileID) {
  const foreignTeam = await controlJSON("/teams", {
    method: "POST",
    body: JSON.stringify({ name: `Telemetry foreign ${runID}`, description: "telemetry isolation" }),
  });
  const foreignTeamID = String(foreignTeam.data?.id ?? "");
  assert(foreignTeamID, "telemetry isolation fixture did not create a foreign team");
  const foreign = await controlJSON(`/telemetry?window=15m&scope=team&team_id=${foreignTeamID}`, { method: "GET" });
  assertTelemetrySnapshot(foreign.data, "team", "15m");
  const foreignActive = (foreign.data.current_cards ?? []).find((card) => card.id === "relationships_active");
  assert(Number(foreignActive?.value ?? 0) === 0, "foreign team saw another team's current Relationship count");

  const mismatchedProfile = await controlJSON(`/telemetry?window=15m&scope=profile&team_id=${foreignTeamID}&profile_id=${profileID}`, { method: "GET" });
  assertTelemetrySnapshot(mismatchedProfile.data, "profile", "15m");
  const mismatchedActive = (mismatchedProfile.data.current_cards ?? []).find((card) => card.id === "relationships_active");
  assert(Number(mismatchedActive?.value ?? 0) === 0, "profile telemetry crossed a team boundary");
  return { foreign_team_isolated: true, mismatched_profile_isolated: true };
}

async function validateUnsupportedScope() {
  const response = await fetch(`${controlURL}/control/api/telemetry?window=15m&scope=unsupported`, {
    method: "GET",
    headers: { Authorization: `Bearer ${controlToken}` },
  });
  const body = await response.text();
  assert(response.status === 422, `unsupported telemetry scope returned HTTP ${response.status}`);
  assert(!body.match(/password|stack trace|select .* from/i), "unsupported telemetry scope exposed an internal error");
  return { status: response.status, bounded: true };
}

async function validateProfileScopeRequiresTeam(profileID) {
  const response = await fetch(`${controlURL}/control/api/telemetry?window=15m&scope=profile&profile_id=${encodeURIComponent(profileID)}`, {
    method: "GET",
    headers: { Authorization: `Bearer ${controlToken}` },
  });
  const body = await response.text();
  assert(response.status === 422, `profile telemetry without team returned HTTP ${response.status}`);
  assert(body.includes("team_id is required"), "profile telemetry without team omitted the bounded validation reason");
  assert(!body.match(/password|stack trace|select .* from/i), "profile scope validation exposed an internal error");
  return { status: response.status, requires_team: true };
}

async function validateUserLifecycleCards() {
  const response = await fetch(`${userURL}/ui/api/telemetry?window=15m`, {
    method: "GET",
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  const body = await response.text();
  assert(response.ok, `user telemetry returned HTTP ${response.status}: ${body}`);
  const snapshot = JSON.parse(body).data;
  const currentCards = snapshot?.current_cards ?? [];
  assert(currentCards.some((card) => card.id === "relationships_active"), "user telemetry omitted current Relationship cards");
  return { current_relationship_cards: true };
}

async function validatePartialPrometheusFailure() {
  const project = process.env.DENSE_MEM_E2E_COMPOSE_PROJECT;
  const composeFile = process.env.DENSE_MEM_E2E_COMPOSE_FILE;
  if (!project || !composeFile) {
    return { skipped: true, reason: "compose coordinates were not provided" };
  }
  const composeArgs = ["compose", "-p", project, "-f", composeFile];
  const stopped = spawnSync("docker", [...composeArgs, "stop", "prometheus"], { encoding: "utf8" });
  assert(stopped.status === 0, `failed to stop Prometheus for partial-failure test: ${stopped.stderr}`);
  try {
    const degraded = await controlJSON("/telemetry?window=15m&scope=system", { method: "GET" });
    assert(degraded.data.status === "degraded", `Prometheus failure did not degrade the snapshot: ${degraded.data.status}`);
    const failed = [...(degraded.data.cards ?? []), ...(degraded.data.series ?? [])].filter((item) => item.reason_code === "query_failed");
    assert(failed.length > 0, "Prometheus failure did not disclose query_failed items");
    const lifecycle = (degraded.data.current_cards ?? []).find((card) => card.id === "relationships_active");
    assert(lifecycle?.status === "ready", "Prometheus failure hid successful lifecycle data");
    return { degraded: true, failed_items: failed.length, lifecycle_preserved: true };
  } finally {
    const started = spawnSync("docker", [...composeArgs, "start", "prometheus"], { encoding: "utf8" });
    assert(started.status === 0, `failed to restart Prometheus after partial-failure test: ${started.stderr}`);
    await waitForHTTP(`${prometheusURL}/-/ready`, 60_000);
  }
}

async function controlJSON(path, options) {
  return httpJSON(`${controlURL}/control/api${path}`, {
    ...options,
    headers: {
      Authorization: `Bearer ${controlToken}`,
      "Content-Type": "application/json",
      ...(options.headers ?? {}),
    },
  });
}

async function mcpTool(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${apiKey}`,
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: ++rpcID,
      method: "tools/call",
      params: { name, arguments: args },
    }),
  });
  if (response.error) {
    throw new Error(`MCP ${name} error: ${JSON.stringify(response.error)}`);
  }
  const text = response.result?.content?.[0]?.text;
  if (typeof text !== "string") {
    throw new Error(`MCP ${name} result missing text`);
  }
  return JSON.parse(text);
}

async function waitForFirstDisposition(submissionID) {
  let lastStatus = "";
  for (let attempt = 0; attempt < 150; attempt += 1) {
    const placement = await mcpTool("get_submission_status", { submission_id: submissionID });
    lastStatus = String(placement.processing_state ?? "");
    if (["completed", "rejected", "failed", "quarantined"].includes(lastStatus)) {
      return lastStatus;
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for first disposition (last status: ${lastStatus || "unknown"})`);
}

async function waitForTelemetrySignals() {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const signals = {
      rememberAcknowledgements: await prometheusValue("densemem_remember_acknowledgements_total"),
      firstDispositions: await prometheusValue("densemem_remember_first_disposition_total"),
      recalls: await prometheusValue("densemem_recall_requests_total"),
      aiCostUSD: await prometheusValue("densemem_ai_operation_cost_usd_total"),
    };
    if (signals.rememberAcknowledgements > 0 && signals.firstDispositions > 0 && signals.recalls > 0 && signals.aiCostUSD > 0) {
      return signals;
    }
    await delay(5_000);
  }
  throw new Error("timed out waiting for remember, first-disposition, recall, and AI-cost telemetry");
}

async function prometheusValue(metric) {
  const url = new URL("/api/v1/query", `${prometheusURL}/`);
  url.searchParams.set("query", `sum(${metric}{team_id=\"${teamID}\"})`);
  const response = await httpJSON(url.toString(), { method: "GET" });
  const value = response.data?.result?.[0]?.value?.[1];
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) {
    throw new Error(`Prometheus returned a non-numeric ${metric} value`);
  }
  return parsed;
}

async function httpJSON(url, options) {
  const response = await fetch(url, options);
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status} ${url}: ${redactHTTPBody(text)}`);
  }
  return text ? JSON.parse(text) : {};
}

function redactHTTPBody(text) {
  return text.replace(/"api_key"\s*:\s*"[^"]*"/g, "\"api_key\":\"<redacted>\"");
}

function nonEmptyString(value) {
  return typeof value === "string" && value.trim() !== "";
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

async function waitForHTTP(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) {
        return;
      }
    } catch {
      // Retry until the bounded deadline.
    }
    await delay(1_000);
  }
  throw new Error(`timed out waiting for ${url}`);
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
