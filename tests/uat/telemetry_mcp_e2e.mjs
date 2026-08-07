#!/usr/bin/env node
const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const prometheusURL = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");

let rpcID = 0;
const runID = `telemetry-e2e-${Date.now()}`;

const pricing = await controlJSON("/control/api/config/telemetry-pricing", { method: "GET" });
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

await controlJSON("/control/api/config/telemetry-pricing", {
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
console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  placement_status: placementStatus,
  remember_acknowledgements: signals.rememberAcknowledgements,
  first_dispositions: signals.firstDispositions,
  recalls: signals.recalls,
  ai_cost_usd: signals.aiCostUSD,
}, null, 2));

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

async function mcpTool(name, args) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${apiKey}`,
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
