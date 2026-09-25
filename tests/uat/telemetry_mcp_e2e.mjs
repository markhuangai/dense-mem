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
const remember = await mcpTool("remember", {
  idempotency_key: `${runID}:batch`,
  evidence: [{
    content: telemetryContent,
    source_type: "document",
    source: `telemetry:${runID}`,
    source_group: `telemetry:${runID}`,
  }],
  relationships: [{
    ref: `${runID}:relationship`,
    subject: {
      name: "Dense-Mem",
      entity_kind: "project",
    },
    predicate: {
      proposed_key: "uses",
    },
    object: {
      entity: {
        name: "exact evidence",
        entity_kind: "concept",
      },
    },
    polarity: "+",
    evidence_indices: [0],
  }],
});
const submissionID = String(remember.submission_id ?? "");
if (!submissionID) {
  throw new Error("remember did not return a submission_id");
}

const rememberStatus = String(remember.processing_state ?? "");
if (!["completed", "failed"].includes(rememberStatus)) {
  throw new Error(`remember did not return a terminal result: ${JSON.stringify(remember)}`);
}
await mcpTool("recall_memory", {
  query: `Telemetry E2E ${runID} exact evidence`,
  limit: 5,
});

const signals = await waitForTelemetrySignals();
const credentials = await controlJSON(`/teams/${teamID}/credentials`, { method: "GET" });
const profileID = String(credentials.data?.[0]?.id ?? "");
if (!profileID) {
  throw new Error("telemetry e2e could not resolve the seeded credential id");
}

const telemetryMatrix = await validateTelemetryMatrix(profileID);
const disabledFeatures = await validateDisabledFeatureReasons();
const isolation = await validateTelemetryIsolation(profileID);
const unsupportedScope = await validateUnsupportedScope();
const profileScopeValidation = await validateProfileScopeRequiresTeam(profileID);
const retiredUserTelemetry = await validateRetiredUserTelemetry();
const grafanaParity = await validateGrafanaDashboardParity();
const partialFailure = await validatePartialPrometheusFailure(grafanaParity);

console.log(JSON.stringify({
  status: "ok",
  run_id: runID,
  remember_status: rememberStatus,
  remember_calls: signals.rememberCalls,
  remember_duration_samples: signals.rememberDurationSamples,
  recalls: signals.recalls,
  ai_cost_usd: signals.aiCostUSD,
  telemetry_matrix: telemetryMatrix,
  disabled_features: disabledFeatures,
  isolation,
  unsupported_scope: unsupportedScope,
  profile_scope_validation: profileScopeValidation,
  retired_user_telemetry: retiredUserTelemetry,
  grafana_parity: grafanaParity,
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

async function validateRetiredUserTelemetry() {
  const response = await fetch(`${userURL}/ui/api/telemetry?window=15m`, {
    method: "GET",
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  assert(response.status === 404, `retired user telemetry returned HTTP ${response.status}`);
  return { status: response.status };
}

async function validateGrafanaDashboardParity() {
  const password = requiredEnv("DENSE_MEM_E2E_GRAFANA_ADMIN_PASSWORD");
  const url = "http://grafana:3000";
  const authorization = `Basic ${Buffer.from(`admin:${password}`).toString("base64")}`;
  const request = async (path, options = {}) => {
    const response = await fetch(`${url}${path}`, {
      ...options,
      headers: {
        Authorization: authorization,
        "Content-Type": "application/json",
        ...(options.headers ?? {}),
      },
    });
    const body = await response.text();
    if (!response.ok) {
      throw new Error(`Grafana HTTP ${response.status} ${path}: ${body.slice(0, 500)}`);
    }
    return body ? JSON.parse(body) : {};
  };

  const verifyScrapeFailure = async () => {
    try {
      const response = await request("/api/ds/query", {
        method: "POST",
        body: JSON.stringify({
          from: "now-1m",
          to: "now",
          queries: [{
            refId: "A",
            datasource: { type: "prometheus", uid: datasource.uid },
            expr: 'min(up{job=~"dense-mem"})',
            format: "time_series",
            instant: true,
            range: false,
            interval: "30s",
            intervalMs: 30_000,
            maxDataPoints: 2,
          }],
        }),
      });
      return response.results?.A?.error || (response.results?.A?.frames?.length ?? 0) === 0;
    } catch {
      return true;
    }
  };

  const anonymous = await fetch(`${url}/api/search?type=dash-db&limit=1`);
  assert(anonymous.status === 401, `Grafana dashboard API was accessible without credentials: HTTP ${anonymous.status}`);

  const expectedUIDs = ["dense-mem-service", "dense-mem-ai-recall", "dense-mem-workflows"];
  let dashboards = [];
  for (let attempt = 0; attempt < 45; attempt += 1) {
    dashboards = await request("/api/search?type=dash-db&limit=100");
    const ready = new Set(dashboards.map((dashboard) => dashboard.uid));
    if (expectedUIDs.every((uid) => ready.has(uid))) break;
    await delay(2_000);
  }
  const availableUIDs = new Set(dashboards.map((dashboard) => dashboard.uid));
  for (const uid of expectedUIDs) {
    assert(availableUIDs.has(uid), `Grafana did not provision dashboard ${uid}`);
  }

  const datasource = await request("/api/datasources/name/Dense-Mem%20Prometheus");
  assert(nonEmptyString(datasource.uid), "Grafana Prometheus datasource did not receive an installation UID");
  assert(datasource.url === "http://prometheus:9090", "Grafana datasource did not target the real Prometheus service");
  const query = (expression, options = {}) => {
    const intervalMs = options.intervalMs ?? 30_000;
    const interval = options.interval ?? `${Math.max(1, Math.round(intervalMs / 1000))}s`;
    const resolvedExpression = expression
      .replaceAll("$__rate_interval", options.rateInterval ?? interval)
      .replaceAll("$__interval_ms", String(intervalMs))
      .replaceAll("$__interval", interval);
    return request("/api/ds/query", {
      method: "POST",
      body: JSON.stringify({
        from: options.from ?? "now-15m",
        to: options.to ?? "now",
        queries: [{
          refId: "A",
          datasource: { type: "prometheus", uid: datasource.uid },
          expr: resolvedExpression,
          format: "time_series",
          instant: options.instant ?? true,
          range: !(options.instant ?? true),
          interval,
          intervalMs,
          maxDataPoints: options.maxDataPoints ?? 120,
        }],
      }),
    });
  };

  const legacy = await controlJSON("/telemetry?window=15m&scope=system", { method: "GET" });
  const snapshot = legacy.data;
  assert(snapshot?.window?.key === "15m", "legacy telemetry parity snapshot omitted the 15-minute window");
  const panels = [];
  for (const uid of expectedUIDs) {
    const result = await request(`/api/dashboards/uid/${uid}`);
    assert(result.dashboard?.templating?.list?.some((variable) => variable.name === "datasource"), `${uid} omitted the portable datasource variable`);
    assert(result.dashboard?.templating?.list?.some((variable) => variable.name === "job"), `${uid} omitted the bounded scrape-job selector`);
    panels.push(...(result.dashboard.panels ?? []));
  }
  const parityPanels = new Map();
  for (const panel of panels) {
    const match = /^Parity: (card|series)\/([a-z0-9_]+)/.exec(panel.description ?? "");
    if (match) {
      assert(Array.isArray(panel.targets) && panel.targets.length === 1, `Grafana panel ${panel.title} must have one parity query`);
      assert(panel.targets[0].datasource?.uid === "$datasource", `Grafana panel ${panel.title} contains a fixed datasource UID`);
      assert(!/team_id|profile_id/.test(panel.targets[0].expr), `Grafana panel ${panel.title} contains identity labels`);
      const key = `${match[1]}/${match[2]}`;
      assert(!parityPanels.has(key), `Grafana maps legacy ${key} more than once`);
      parityPanels.set(key, panel);
    }
  }
  const cards = [...(snapshot.windowed_cards ?? snapshot.cards ?? []), ...(snapshot.current_cards ?? [])];
  const series = [...(snapshot.activity_series ?? snapshot.series ?? []), ...(snapshot.state_series ?? [])];
  const evaluated = [];
  for (const [presentation, items] of [["card", cards], ["series", series]]) {
    for (const item of items) {
      const panel = parityPanels.get(`${presentation}/${item.id}`);
      assert(panel, `Grafana omitted the legacy ${presentation} measure ${item.id}`);
      assert(Array.isArray(panel.targets) && panel.targets.length === 1, `Grafana panel for ${item.id} must use one Prometheus query`);
      const target = panel.targets[0];
      const expression = target.expr
        .replaceAll("$job", "dense-mem")
        .replaceAll("$window", snapshot.window.key);
      const stepSeconds = Number(snapshot.window.step_seconds);
      const stepMs = stepSeconds * 1000;
      const lastPoint = presentation === "series" && item.status === "ready" ? item.points.at(-1) : null;
      if (presentation === "series" && item.status === "ready") {
        assert(lastPoint, `ready series ${item.id} had no API sample`);
      }
      const from = Date.parse(snapshot.window.from).toString();
      const to = Date.parse(lastPoint?.timestamp ?? snapshot.window.to).toString();
      const queryOptions = {
        from,
        to,
        instant: true,
        interval: `${stepSeconds}s`,
        intervalMs: stepMs,
        maxDataPoints: Math.ceil((Date.parse(snapshot.window.to) - Date.parse(snapshot.window.from)) / stepMs) + 1,
      };
      const response = await query(expression, queryOptions);
      const queryResult = response.results?.A;
      assert(queryResult && !queryResult.error, `Grafana query failed for ${presentation} ${item.id}: ${queryResult?.error ?? "missing result"}`);
      if (presentation === "card") {
        let actual = grafanaFrameNumber(queryResult.frames ?? []);
        if (item.status === "ready") {
          let expected = Number(item.value);
          const ledgerCard = item.id === "relationship_corrections"
            || item.id.startsWith("relationship_transitions_")
            || item.id.startsWith("relationships_");
          if (ledgerCard) {
            // Direct PostgreSQL reads can precede the next Prometheus scrape.
            for (let attempt = 0; attempt < 9 && actual !== expected; attempt += 1) {
              await delay(5_000);
              const latest = (await controlJSON(`/telemetry?window=${snapshot.window.key}&scope=system`, { method: "GET" })).data;
              const latestCard = [...(latest.windowed_cards ?? []), ...(latest.current_cards ?? [])].find((card) => card.id === item.id);
              assert(latestCard?.status === "ready", `ledger card ${item.id} became unavailable during parity verification`);
              expected = Number(latestCard.value);
              const refreshed = await query(expression, { ...queryOptions, to: Date.now().toString() });
              const refreshedResult = refreshed.results?.A;
              assert(refreshedResult && !refreshedResult.error, `Grafana query failed for ledger card ${item.id}: ${refreshedResult?.error ?? "missing result"}`);
              actual = grafanaFrameNumber(refreshedResult.frames ?? []);
            }
          }
          assert(actual !== null, `Grafana returned no numeric value for ready card ${item.id}`);
          assertClose(actual, expected, `Grafana card ${item.id}`);
        } else if (item.status === "unavailable") {
          assert(actual === null, `Grafana showed unavailable card ${item.id} as ${actual}`);
        }
      } else if (item.status === "ready") {
        const actual = grafanaFrameLastSample(queryResult.frames ?? []);
        assert(actual, `Grafana returned no numeric samples for ready series ${item.id}`);
        assertClose(actual.value, Number(lastPoint.value), `Grafana series ${item.id}`);
      } else if (item.status === "unavailable") {
        assert(grafanaFrameNumber(queryResult.frames ?? []) === null, `Grafana showed unavailable series ${item.id} as a value`);
      }
      if (presentation === "series" && (item.status === "ready" || item.status === "unavailable")) {
        let rangeValue = null;
        for (let attempt = 0; attempt < 9; attempt += 1) {
          const range = await query(expression, {
            from,
            to: Date.now().toString(),
            instant: false,
            interval: `${stepSeconds}s`,
            intervalMs: stepMs,
            maxDataPoints: Math.ceil((Date.parse(snapshot.window.to) - Date.parse(snapshot.window.from)) / stepMs) + 1,
          });
          const rangeResult = range.results?.A;
          assert(rangeResult && !rangeResult.error, `Grafana range query failed for series ${item.id}: ${rangeResult?.error ?? "missing result"}`);
          rangeValue = grafanaFrameNumber(rangeResult.frames ?? []);
          if (item.status === "unavailable" || rangeValue !== null) break;
          await delay(10_000);
        }
        if (item.status === "ready") {
          assert(rangeValue !== null, `Grafana range query returned no samples for ready series ${item.id}`);
        } else {
          assert(rangeValue === null, `Grafana range query showed unavailable series ${item.id} as ${rangeValue}`);
        }
      }
      if (item.status === "ready") evaluated.push(`${presentation}/${item.id}`);
    }
  }

  for (const title of ["Logical operation attempts", "Provider token volume", "Provider usage without pricing or usage"]) {
    const groupedPanel = panels.find((panel) => panel.title === title);
    assert(groupedPanel, `Grafana omitted grouped activity panel ${title}`);
    const expression = groupedPanel.targets[0].expr.replaceAll("$job", "dense-mem");
    const result = await query(expression, { rateInterval: snapshot.window.key });
    const queryResult = result.results?.A;
    assert(queryResult && !queryResult.error, `Grafana grouped query failed for ${title}: ${queryResult?.error ?? "missing result"}`);
    const values = (queryResult.frames ?? []).flatMap((frame) =>
      (frame.schema?.fields ?? []).flatMap((field, index) => {
        if (field.type !== "number") return [];
        return (frame.data?.values?.[index] ?? [])
          .filter((value) => value !== null && value !== undefined && value !== "")
          .map(Number);
      })
    );
    if (title !== "Provider usage without pricing or usage") {
      assert(values.length > 0, `Grafana grouped panel ${title} lost active activity`);
    }
    assert(values.every((value) => Number.isFinite(value) && value > 0), `Grafana grouped panel ${title} returned a zero-only series`);
  }

  for (const title of ["MCP transport duration", "Logical operation duration", "Remember phase duration", "Dream cycle duration", "Dream provider duration"]) {
    const panel = panels.find((item) => item.title === title);
    assert(panel, `Grafana omitted grouped duration panel ${title}`);
    const result = await query(panel.targets[0].expr.replaceAll("$job", "dense-mem"), { rateInterval: snapshot.window.key });
    const queryResult = result.results?.A;
    assert(queryResult && !queryResult.error, `Grafana grouped duration query failed for ${title}: ${queryResult?.error ?? "missing result"}`);
    const series = (queryResult.frames ?? []).flatMap((frame) =>
      (frame.schema?.fields ?? []).flatMap((field, index) => field.type === "number" ? [frame.data?.values?.[index] ?? []] : [])
    );
    if (title === "MCP transport duration") assert(series.length > 0, "Grafana lost active MCP transport duration");
    assert(series.every((values) => values.some((value) => value !== null && value !== undefined && value !== "" && Number.isFinite(Number(value)))),
      `Grafana grouped duration panel ${title} returned an inactive series`);
  }

  const zeroErrors = (snapshot.windowed_cards ?? []).find((item) => item.id === "embedding_errors");
  assert(zeroErrors?.status === "ready" && Number(zeroErrors.value) === 0, "the empty embedding-error counter was not a valid zero with parent activity");
  const feedbackPanel = parityPanels.get("card/llm_recall_used_rate");
  const feedback = (snapshot.windowed_cards ?? []).find((item) => item.id === "llm_recall_used_rate");
  assert(feedbackPanel && feedback && feedback.status !== "ready", "the telemetry scenario unexpectedly recorded host feedback");
  const feedbackQuery = await query(feedbackPanel.targets[0].expr
    .replaceAll("$job", "dense-mem")
    .replaceAll("$window", snapshot.window.key));
  assert(grafanaFrameNumber(feedbackQuery.results?.A?.frames ?? []) === null, "absent host feedback appeared as a numeric Grafana value");

  const feedbackConfig = await controlJSON("/config/recall-feedback", { method: "GET" });
  let feedbackPercent;
  try {
    await controlJSON("/config/recall-feedback", {
      method: "PATCH",
      body: JSON.stringify({ items: [{ key: "RECALL_FEEDBACK_ENABLED", value: "true" }] }),
    });
    const team = await controlJSON("/teams", {
      method: "POST",
      body: JSON.stringify({ name: `${runID} Grafana feedback`, description: "Grafana feedback UAT" }),
    });
    const feedbackTeamID = String(team.data?.id ?? "");
    assert(feedbackTeamID, "Grafana feedback fixture did not create a team");
    const credential = await controlJSON(`/teams/${feedbackTeamID}/credentials`, {
      method: "POST",
      body: JSON.stringify({ name: `${runID} Grafana feedback key`, scopes: ["read", "write"], rate_limit: 300 }),
    });
    const feedbackAPIKey = String(credential.data?.api_key ?? "");
    assert(feedbackAPIKey, "Grafana feedback fixture did not create a credential");
    const recalls = [];
    for (const used of [true, false]) {
      const recall = await mcpTool("recall_memory", { query: `Grafana feedback ${runID} ${used}`, limit: 1 }, feedbackAPIKey);
      assert(nonEmptyString(recall.recall_id), "feedback test recall omitted its event ID");
      recalls.push({ recall_event_id: recall.recall_id, used, answer_supported: used, quality: "high" });
    }
    const recorded = await mcpTool("submit_recall_session_feedback", { recalls }, feedbackAPIKey);
    assert(recorded.recorded === true && recorded.recorded_count === 2, "feedback test did not record both recall events");
    const percentPanel = parityPanels.get("series/llm_recall_used_rate");
    const requestRatePanel = parityPanels.get("series/recalls");
    assert(percentPanel && requestRatePanel, "Grafana omitted feedback or request-rate panels");
    const rangeOptions = { interval: "15s", intervalMs: 15_000, rateInterval: "1m" };
    const percentExpression = percentPanel.targets[0].expr.replaceAll("$job", "dense-mem");
    for (let attempt = 0; attempt < 12; attempt += 1) {
      const result = await query(percentExpression, rangeOptions);
      assert(!result.results?.A?.error, `Grafana feedback percentage query failed: ${result.results?.A?.error}`);
      feedbackPercent = grafanaFrameNumber(result.results?.A?.frames ?? []);
      if (feedbackPercent !== null) break;
      await delay(5_000);
    }
    assert(feedbackPercent !== null, "Grafana feedback percentage stayed empty after two recorded events");
    assertClose(feedbackPercent, 50, "Grafana feedback percentage");
    const rateResult = await query(requestRatePanel.targets[0].expr.replaceAll("$job", "dense-mem"), rangeOptions);
    assert(!rateResult.results?.A?.error, `Grafana request-rate query failed: ${rateResult.results?.A?.error}`);
    assert(grafanaFrameNumber(rateResult.results?.A?.frames ?? []) !== null, "Grafana request rate was empty at a 15-second display step");
  } finally {
    await restoreConfig("/config/recall-feedback", feedbackConfig);
  }

  const longLegacy = await controlJSON("/telemetry?window=30d&scope=system", { method: "GET" });
  const longSnapshot = longLegacy.data;
  assert(longSnapshot?.window?.key === "30d", "legacy telemetry omitted the supported 30-day window");
  const longCards = [...(longSnapshot.windowed_cards ?? []), ...(longSnapshot.current_cards ?? [])];
  const longCard = longCards.find((item) => item.id === "http_requests");
  const longCardPanel = parityPanels.get("card/http_requests");
  assert(longCard?.status === "ready" && longCardPanel, "30-day HTTP request measure was unavailable");
  const longStepSeconds = Number(longSnapshot.window.step_seconds);
  const longStepMs = longStepSeconds * 1000;
  const longCardResult = await query(longCardPanel.targets[0].expr
    .replaceAll("$job", "dense-mem")
    .replaceAll("$window", longSnapshot.window.key), {
    from: Date.parse(longSnapshot.window.from).toString(),
    to: Date.parse(longSnapshot.window.to).toString(),
    instant: true,
    interval: `${longStepSeconds}s`,
    intervalMs: longStepMs,
  });
  const longCardValue = grafanaFrameNumber(longCardResult.results?.A?.frames ?? []);
  assert(longCardValue !== null, "Grafana returned no value for the 30-day HTTP request measure");
  assertClose(longCardValue, Number(longCard.value), "Grafana 30-day HTTP requests");
  const longSeries = (longSnapshot.activity_series ?? []).find((item) => item.id === "http_rps");
  const longSeriesPanel = parityPanels.get("series/http_rps");
  assert(longSeries?.status === "ready" && longSeriesPanel, "30-day HTTP rate series was unavailable");
  const longSeriesResult = await query(longSeriesPanel.targets[0].expr
    .replaceAll("$job", "dense-mem")
    .replaceAll("$window", longSnapshot.window.key), {
    from: Date.parse(longSnapshot.window.from).toString(),
    to: Date.parse(longSnapshot.window.to).toString(),
    // A fresh stack has no samples at Grafana's aligned six-hour range steps.
    instant: true,
    interval: `${longStepSeconds}s`,
    intervalMs: longStepMs,
  });
  const longSeriesValue = grafanaFrameLastSample(longSeriesResult.results?.A?.frames ?? []);
  assert(longSeriesValue, "Grafana returned no samples for the 30-day HTTP rate series");
  assertClose(longSeriesValue.value, Number(longSeries.points.at(-1)?.value), "Grafana 30-day HTTP rate");

  const beforeSparseSystem = (await controlJSON("/telemetry?window=1h&scope=system", { method: "GET" })).data;
  const sparseFixture = await createSparseTelemetryTeam();
  let sparseSnapshot;
  for (let attempt = 0; attempt < 45; attempt += 1) {
    const response = await controlJSON(`/telemetry?window=1h&scope=team&team_id=${encodeURIComponent(sparseFixture.teamID)}`, { method: "GET" });
    sparseSnapshot = response.data;
    const sparseCards = [...(sparseSnapshot.windowed_cards ?? []), ...(sparseSnapshot.current_cards ?? [])];
    const requests = sparseCards.find((item) => item.id === "embedding_requests");
    const latency = sparseCards.find((item) => item.id === "avg_embedding_latency");
    const tokens = sparseCards.find((item) => item.id === "embedding_tokens");
    if (requests?.status === "ready" && Number(requests.value) > 0 && latency?.status === "ready" && tokens?.status === "unavailable") break;
    await delay(2_000);
  }
  assert(sparseSnapshot?.window?.key === "1h", "sparse-team telemetry snapshot omitted the one-hour window");
  const sparseCards = [...(sparseSnapshot.windowed_cards ?? []), ...(sparseSnapshot.current_cards ?? [])];
  const sparseEmbeddingRequests = sparseCards.find((item) => item.id === "embedding_requests");
  const sparseEmbeddingLatency = sparseCards.find((item) => item.id === "avg_embedding_latency");
  const sparseEmbeddingTokens = sparseCards.find((item) => item.id === "embedding_tokens");
  if (sparseEmbeddingRequests?.status !== "ready" || Number(sparseEmbeddingRequests.value) <= 0) {
    const raw = await query(`densemem_embedding_requests_total{job=~"dense-mem",team_id="${sparseFixture.teamID}"}`);
    const detail = {
      card_status: sparseEmbeddingRequests?.status,
      card_reason_code: sparseEmbeddingRequests?.reason_code,
      raw_metric_value: grafanaFrameNumber(raw.results?.A?.frames ?? []),
    };
    throw new Error(`new-team embedding counter did not record a sparse first sample: ${JSON.stringify(detail)}`);
  }
  assert(sparseEmbeddingLatency?.status === "ready", "new-team embedding histogram was unavailable");
  if (sparseEmbeddingTokens?.status !== "unavailable") {
    const raw = await query(`densemem_embedding_tokens_total{job=~"dense-mem",team_id="${sparseFixture.teamID}"}`);
    throw new Error(`missing provider usage did not mark the new-team token measure unavailable: ${JSON.stringify({
      card_status: sparseEmbeddingTokens?.status,
      card_reason_code: sparseEmbeddingTokens?.reason_code,
      raw_metric_value: grafanaFrameNumber(raw.results?.A?.frames ?? []),
    })}`);
  }
  const afterSparseSystem = (await controlJSON("/telemetry?window=1h&scope=system", { method: "GET" })).data;
  const systemCards = (value) => [...(value.windowed_cards ?? []), ...(value.current_cards ?? [])];
  const beforeRequests = systemCards(beforeSparseSystem).find((item) => item.id === "embedding_requests");
  const afterRequests = systemCards(afterSparseSystem).find((item) => item.id === "embedding_requests");
  const afterLatency = systemCards(afterSparseSystem).find((item) => item.id === "avg_embedding_latency");
  assert(afterRequests?.status === "ready" && Number(afterRequests.value) > Number(beforeRequests?.value ?? 0), "new-team first samples did not reach the system embedding total");
  assert(afterLatency?.status === "ready", "system embedding latency was unavailable after the sparse samples");
  const sparseCounterPanel = parityPanels.get("card/embedding_requests");
  const sparseLatencyPanel = parityPanels.get("card/avg_embedding_latency");
  assert(sparseCounterPanel && sparseLatencyPanel, "Grafana omitted sparse provider-measure panels");
  const sparseFrom = Date.parse(afterSparseSystem.window.from).toString();
  const sparseTo = Date.parse(afterSparseSystem.window.to).toString();
  const sparseStepSeconds = Number(afterSparseSystem.window.step_seconds);
  const sparseInterval = { interval: `${sparseStepSeconds}s`, intervalMs: sparseStepSeconds * 1000 };
  const systemExpression = (panel) => panel.targets[0].expr
    .replaceAll("$job", "dense-mem")
    .replaceAll("$window", afterSparseSystem.window.key);
  const sparseCounterResult = await query(systemExpression(sparseCounterPanel), {
    from: sparseFrom,
    to: sparseTo,
    instant: true,
    ...sparseInterval,
  });
  const sparseCounterValue = grafanaFrameNumber(sparseCounterResult.results?.A?.frames ?? []);
  assert(sparseCounterValue !== null, "Grafana lost the sparse first embedding counter sample");
  assertClose(sparseCounterValue, Number(afterRequests.value), "Grafana system embedding requests after sparse samples");
  const sparseLatencyResult = await query(systemExpression(sparseLatencyPanel), {
    from: sparseFrom,
    to: sparseTo,
    instant: true,
    ...sparseInterval,
  });
  const sparseLatencyValue = grafanaFrameNumber(sparseLatencyResult.results?.A?.frames ?? []);
  assert(sparseLatencyValue !== null, "Grafana lost the sparse first embedding histogram sample");
  assertClose(sparseLatencyValue, Number(afterLatency.value), "Grafana system embedding latency after sparse samples");
  for (const metric of ["densemem_embedding_requests_total", "densemem_embedding_duration_seconds_count"]) {
    const selector = `${metric}{job=~"dense-mem",team_id="${sparseFixture.teamID}"}`;
    const first = await query(`min_over_time(${selector}[1h])`);
    const previous = await query(`last_over_time(${selector}[1h] offset 1h)`);
    assert(grafanaFrameNumber(first.results?.A?.frames ?? []) !== null, `${metric} did not expose the new team's first sample`);
    assert(grafanaFrameNumber(previous.results?.A?.frames ?? []) === null, `${metric} had an earlier sample for the new team`);
  }
  const unpricedSystem = await controlJSON("/telemetry?window=15m&scope=system", { method: "GET" });
  const unpricedCards = [...(unpricedSystem.data.windowed_cards ?? []), ...(unpricedSystem.data.current_cards ?? [])];
  for (const [id, component] of [["ai_cost_usd", ""], ["embedding_cost_usd", "embedding"]]) {
    const item = unpricedCards.find((card) => card.id === id);
    const panel = parityPanels.get(`card/${id}`);
    assert(item?.status === "unavailable" && panel, `unpriced ${component || "AI"} cost was not unavailable in the live telemetry source`);
    const result = await query(panel.targets[0].expr.replaceAll("$job", "dense-mem").replaceAll("$window", "15m"));
    assert(grafanaFrameNumber(result.results?.A?.frames ?? []) === null, `Grafana displayed unpriced ${component || "AI"} cost as a numeric value`);
  }

  const httpRequestsPanel = parityPanels.get("card/http_requests");
  const ledgerHealthPanel = panels.find((panel) => panel.title === "Canonical ledger collection");
  assert(httpRequestsPanel && ledgerHealthPanel, "Grafana omitted scrape-failure verification panels");
  const substituteVariables = (expression) => expression.replaceAll("$job", "dense-mem").replaceAll("$window", snapshot.window.key);
  const verifyTargetScrapeFailure = async () => {
    const up = await query('min(up{job=~"dense-mem"})');
    if (grafanaFrameNumber(up.results?.A?.frames ?? []) !== 0) return false;
    const staleRequests = await query(substituteVariables(httpRequestsPanel.targets[0].expr));
    const ledgerHealth = await query(substituteVariables(ledgerHealthPanel.targets[0].expr));
    return grafanaFrameNumber(staleRequests.results?.A?.frames ?? []) === null
      && grafanaFrameNumber(ledgerHealth.results?.A?.frames ?? []) === null;
  };
  return {
    dashboards: expectedUIDs,
    datasource: datasource.name,
    evaluated_ready_measures: evaluated.length,
    mapped_measures: parityPanels.size,
    valid_zero_embedding_errors: true,
    absent_feedback_no_data: true,
    feedback_percent: feedbackPercent,
    long_window: "30d",
    sparse_first_sample_counter_and_histogram: true,
    missing_provider_usage_no_data: true,
    verifyScrapeFailure,
    verifyTargetScrapeFailure,
  };
}

function grafanaFrameNumber(frames) {
  return grafanaFrameLastSample(frames)?.value ?? null;
}

function grafanaFrameLastSample(frames) {
  let latest = null;
  for (const frame of frames) {
    const fields = frame.schema?.fields ?? [];
    const values = frame.data?.values ?? [];
    const timeIndex = fields.findIndex((field) => field.type === "time");
    const times = timeIndex >= 0 ? values[timeIndex] ?? [] : [];
    for (let index = 0; index < fields.length; index += 1) {
      if (fields[index].type === "time") continue;
      const samples = values[index] ?? [];
      for (let sample = samples.length - 1; sample >= 0; sample -= 1) {
        const raw = samples[sample];
        if (raw === null || raw === undefined || raw === "") continue;
        const value = Number(raw);
        if (!Number.isFinite(value)) continue;
        const timeValue = timeIndex >= 0 ? times[sample] : sample;
        const timestamp = typeof timeValue === "string" ? Date.parse(timeValue) : Number(timeValue);
        const candidate = { timestamp: Number.isFinite(timestamp) ? timestamp : sample, value };
        if (latest === null || candidate.timestamp > latest.timestamp) latest = candidate;
        break;
      }
    }
  }
  return latest;
}

async function createSparseTelemetryTeam() {
  const team = await controlJSON("/teams", {
    method: "POST",
    body: JSON.stringify({ name: `${runID} sparse telemetry`, description: "sparse telemetry UAT" }),
  });
  const sparseTeamID = String(team.data?.id ?? "");
  assert(sparseTeamID, "sparse telemetry fixture did not create a team");
  const credential = await controlJSON(`/teams/${sparseTeamID}/credentials`, {
    method: "POST",
    body: JSON.stringify({ name: `${runID} sparse telemetry key`, scopes: ["read", "write"], rate_limit: 300 }),
  });
  const sparseProfileID = String(credential.data?.credential?.id ?? "");
  const sparseAPIKey = String(credential.data?.api_key ?? "");
  assert(sparseProfileID && sparseAPIKey, "sparse telemetry fixture did not create a credential");
  await mcpTool("recall_memory", {
    query: `[fixture-fault:embedding-no-usage] Sparse telemetry E2E ${runID} exact evidence`,
    limit: 5,
  }, sparseAPIKey);
  return { teamID: sparseTeamID };
}

function assertClose(actual, expected, label) {
  const tolerance = expected === 0 ? 0.05 : Math.max(0.1, Math.abs(expected) * 0.1);
  assert(Math.abs(actual - expected) <= tolerance, `${label}=${actual} differed from the API value ${expected}`);
}

async function validatePartialPrometheusFailure(grafana) {
  const project = process.env.DENSE_MEM_E2E_COMPOSE_PROJECT;
  const composeFile = process.env.DENSE_MEM_E2E_COMPOSE_FILE;
  if (!project || !composeFile) {
    return { skipped: true, reason: "compose coordinates were not provided" };
  }
  const composeArgs = ["compose", "-p", project, "-f", composeFile];
  await waitForHTTP(`${prometheusURL}/-/ready`, 60_000);
  const stoppedServer = spawnSync("docker", [...composeArgs, "stop", "server"], { encoding: "utf8" });
  assert(stoppedServer.status === 0, `failed to stop Dense-Mem for scrape-failure test: ${stoppedServer.stderr}`);
  try {
    let scrapeFailureVerified = false;
    for (let attempt = 0; attempt < 45; attempt += 1) {
      if (await grafana.verifyTargetScrapeFailure()) {
        scrapeFailureVerified = true;
        break;
      }
      await delay(2_000);
    }
    assert(scrapeFailureVerified, "Grafana retained target metrics after the Dense-Mem scrape failed");
  } finally {
    const startedServer = spawnSync("docker", [...composeArgs, "start", "server"], { encoding: "utf8" });
    assert(startedServer.status === 0, `failed to restart Dense-Mem after scrape-failure test: ${startedServer.stderr}`);
    await waitForHTTP(`${controlURL}/control/api/telemetry?window=15m&scope=system`, 60_000, {
      headers: { Authorization: `Bearer ${controlToken}` },
    });
  }

  const stoppedPrometheus = spawnSync("docker", [...composeArgs, "stop", "prometheus"], { encoding: "utf8" });
  assert(stoppedPrometheus.status === 0, `failed to stop Prometheus for query-failure test: ${stoppedPrometheus.stderr}`);
  try {
    assert(await grafana.verifyScrapeFailure(), "Grafana presented a Prometheus outage as a successful query");
    const degraded = await controlJSON("/telemetry?window=15m&scope=system", { method: "GET" });
    assert(degraded.data.status === "degraded", `Prometheus failure did not degrade the snapshot: ${degraded.data.status}`);
    const failed = [...(degraded.data.cards ?? []), ...(degraded.data.series ?? [])].filter((item) => item.reason_code === "query_failed");
    assert(failed.length > 0, "Prometheus failure did not disclose query_failed items");
    const lifecycle = (degraded.data.current_cards ?? []).find((card) => card.id === "relationships_active");
    assert(lifecycle?.status === "ready", "Prometheus failure hid successful lifecycle data");
    return { degraded: true, failed_items: failed.length, lifecycle_preserved: true };
  } finally {
    const startedPrometheus = spawnSync("docker", [...composeArgs, "start", "prometheus"], { encoding: "utf8" });
    assert(startedPrometheus.status === 0, `failed to restart Prometheus after partial-failure test: ${startedPrometheus.stderr}`);
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

async function mcpTool(name, args, key = apiKey) {
  const response = await httpJSON(`${userURL}/mcp`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${key}`,
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

async function waitForTelemetrySignals() {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    const signals = {
      rememberCalls: await prometheusValue("densemem_remember_acknowledgements_total"),
      rememberDurationSamples: await prometheusValue("densemem_remember_acknowledgement_duration_seconds_count"),
      recalls: await prometheusValue("densemem_recall_requests_total"),
      aiCostUSD: await prometheusValue("densemem_ai_operation_cost_usd_total"),
    };
    if (signals.rememberCalls > 0 && signals.rememberDurationSamples > 0 && signals.recalls > 0 && signals.aiCostUSD > 0) {
      return signals;
    }
    await delay(5_000);
  }
  throw new Error("timed out waiting for synchronous Remember, recall, and AI-cost telemetry");
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

async function waitForHTTP(url, timeoutMs, options = {}) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url, options);
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

await import("./memory_pack_e2e.mjs");
