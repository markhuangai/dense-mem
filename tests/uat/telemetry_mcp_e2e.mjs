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

const evidence = `Dense-Mem uses PostgreSQL. Telemetry E2E ${runID}.`;
const remember = await mcpTool("remember", submissionArguments(evidence));
const submissionID = String(remember.submission_id ?? "");
if (!submissionID) {
  throw new Error("remember did not return a submission_id");
}

const submissionStatus = await waitForFirstDisposition(submissionID);
await mcpTool("recall_memory", {
  query: `Dense-Mem PostgreSQL Telemetry E2E ${runID}`,
  limit: 5,
});

const signals = await waitForTelemetrySignals();
console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  submission_status: submissionStatus,
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

function submissionArguments(content) {
  const fullSpan = { evidence_index: 0, start: 0, end: [...content].length };
  const spanFor = (surface) => {
    const start = content.indexOf(surface);
    if (start < 0) {
      throw new Error(`surface ${surface} is not in evidence`);
    }
    return { evidence_index: 0, start, end: start + [...surface].length };
  };
  return {
    evidence: [{
      content,
      source_type: "document",
      source: `telemetry:${runID}`,
      source_group: `telemetry:${runID}`,
      idempotency_key: runID,
    }],
    proposal: {
      entities: [
        { ref: "subject", name: "Dense-Mem", entity_kind: "project", evidence: [spanFor("Dense-Mem")] },
        { ref: "object", name: "PostgreSQL", entity_kind: "product", evidence: [spanFor("PostgreSQL")] },
      ],
      relationships: [{
        proposal_id: "relationship_1",
        subject_ref: "subject",
        object_ref: "object",
        predicate: { surface: "uses", ...spanFor("uses") },
        evidence: [fullSpan],
      }],
    },
  };
}

async function waitForFirstDisposition(submissionID) {
  let lastSubmission = {};
  for (let attempt = 0; attempt < 150; attempt += 1) {
    const submission = await mcpTool("get_submission_status", { submission_id: submissionID });
    lastSubmission = submission;
    const processingState = String(submission.processing_state ?? "");
    if (processingState === "completed" && ["current", "not_required"].includes(String(submission.search_state ?? ""))) {
      return processingState;
    }
    if (["rejected", "failed", "quarantined"].includes(processingState)) {
      throw new Error(`submission reached ${processingState}: ${JSON.stringify(submission)}`);
    }
    await delay(2_000);
  }
  throw new Error(`timed out waiting for first submission disposition: ${JSON.stringify(lastSubmission)}`);
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
