#!/usr/bin/env node

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const outputDir = resolve(root, "dashboards");
const job = 'job=~"$job"';
const up = `min(up{${job}}) == 1`;
const ledgerUp = `min(densemem_operational_ledger_collection_success{${job}}) == 1`;
const stat = "stat";
const timeseries = "timeseries";
const labels = Object.freeze({
  statuses: ["pending_evidence", "active", "needs_review", "rejected", "retracted", "superseded", "quarantined", "disputed"],
  embeddingErrorCodes: [
    "provider_rate_limited",
    "provider_timeout",
    "provider_network_error",
    "provider_server_error",
    "provider_quota_exhausted",
    "provider_authentication_failed",
    "provider_permission_denied",
    "provider_contract_rejected",
    "provider_response_invalid",
    "embedding_input_rejected",
    "embedding_contract_mismatch",
    "unknown_embedding_failure",
    "stale",
    "lease_lost",
  ],
});

function selector(extra = "") {
  return extra ? `${job},${extra}` : job;
}

function sparseIncrease(metric, extra = "", window = "$window", grouping = "") {
  const selectorText = selector(extra);
  const ranged = `increase(${metric}{${selectorText}}[${window}])`;
  const first = `min_over_time(${metric}{${selectorText}}[${window}])`;
  const previous = `last_over_time(${metric}{${selectorText}}[${window}] offset ${window})`;
  const fallback = `((${first} unless ignoring(__name__) ${previous}) or (0 * ${ranged}))`;
  const aggregate = grouping ? `sum by (${grouping})` : "sum";
  return `${aggregate}(${ranged} + ${fallback})`;
}

function plainIncrease(metric, extra = "", window = "$window") {
  return `sum(increase(${metric}{${selector(extra)}}[${window}]))`;
}

function counter(metric, extra = "", zero = false, window = "$window") {
  const value = sparseIncrease(metric, extra, window);
  return `${zero ? `(${value} or vector(0))` : value} and on() (${up})`;
}

function plainCounter(metric, extra = "", zero = false, window = "$window") {
  const value = plainIncrease(metric, extra, window);
  return `${zero ? `(${value} or vector(0))` : value} and on() (${up})`;
}

function rateValue(metric, extra = "", grouping = "") {
  return `(${sparseIncrease(metric, extra, "$__rate_interval", grouping)} / ($__rate_interval))`;
}

function rate(metric, extra = "", zero = false, grouping = "") {
  const value = rateValue(metric, extra, grouping);
  return `${zero ? `(${value} or vector(0))` : value} and on() (${up})`;
}

function histogramAverage(metric, multiplier = 1, window = "$window", grouping = "") {
  const sum = sparseIncrease(`${metric}_sum`, "", window, grouping);
  const count = sparseIncrease(`${metric}_count`, "", window, grouping);
  const activeCount = grouping ? `(${count} > 0)` : count;
  return `${multiplier} * (${sum} / ${activeCount}) and on() (${up})`;
}

function quantile(metric, value, multiplier = 1, window = "$window") {
  const buckets = sparseIncrease(`${metric}_bucket`, "", window, "le");
  return `${multiplier} * histogram_quantile(${value}, ${buckets}) and on() (${up})`;
}

function recallRate(extra) {
  const all = counter("densemem_recall_feedback_total");
  const matched = `(${counter("densemem_recall_feedback_total", extra)} or vector(0))`;
  return `100 * (${matched}) / (${all})`;
}

function cost(component = "") {
  const componentLabels = component ? `component="${component}"` : "";
  const costValue = sparseIncrease("densemem_ai_operation_cost_usd_total", componentLabels);
  const unpriced = `((${sparseIncrease("densemem_ai_operation_unpriced_total", componentLabels)}) or vector(0))`;
  const verifier = `((${sparseIncrease("densemem_verifier_requests_total")}) or vector(0))`;
  const embedding = `((${sparseIncrease("densemem_embedding_requests_total")}) or vector(0))`;
  const activity = component === "verifier" ? verifier : component === "embedding" ? embedding : `(${verifier} + ${embedding})`;
  return `((${costValue} unless (${unpriced} > 0)) or (vector(0) unless (((${activity}) + (${unpriced})) > 0))) and on() (${up})`;
}

function counterBy(metric, grouping, extra = "", window = "$__rate_interval") {
  return `(${sparseIncrease(metric, extra, window, grouping)} > 0) and on() (${up})`;
}

function counterWithParentZero(metric, extra, parentMetric, parentExtra = "", sparse = true) {
  const child = sparse ? sparseIncrease(metric, extra) : plainIncrease(metric, extra);
  const parent = sparse ? sparseIncrease(parentMetric, parentExtra) : plainIncrease(parentMetric, parentExtra);
  return `(${child} or (vector(0) and on() (${parent}))) and on() (${up})`;
}

function rateWithParentZero(metric, extra, parentMetric, parentExtra = "", parentSparse = true) {
  const parent = parentSparse ? sparseIncrease(parentMetric, parentExtra, "$__rate_interval") : plainIncrease(parentMetric, parentExtra, "$__rate_interval");
  return `(${rateValue(metric, extra)} and on() (${up})) or ((vector(0) and on() (${parent})) and on() (${up}))`;
}

function ledgerGauge(metric, grouping, filter = "") {
  const extra = filter ? `,${filter}` : "";
  return `max by (${grouping}) (${metric}{${job}${extra}}) and on() (${up}) and on() (${ledgerUp})`;
}

function panel(id, title, unit, expr, type = stat, parity = "", description = "") {
  return { id, title, unit, expr, type, parity, description };
}

const statusPanels = labels.statuses.flatMap((status, index) => [
  panel(100 + index, `Current Relationships: ${status}`, "short", ledgerGauge("densemem_operational_relationships_current", "status", `status="${status}"`), stat, `card/relationships_${status}`),
  panel(120 + index, `Relationship transitions: ${status}`, "short", ledgerGauge("densemem_operational_relationship_transitions", "window,status", `window="$window",status="${status}"`), stat, `card/relationship_transitions_${status}`),
]);

const dashboards = [
  {
    uid: "dense-mem-service",
    title: "Dense-Mem Overview and Lifecycle",
    tags: ["dense-mem", "operations"],
    description: "System-wide service health and canonical Relationship lifecycle. Lifecycle values are durable ledger gauges and are deduplicated across server replicas.",
    panels: [
      panel(1, "Prometheus scrape health", "short", 'min(up{job=~"$job"})', stat),
      panel(2, "Canonical ledger collection", "short", 'min(densemem_operational_ledger_collection_success{job=~"$job"}) * min(up{job=~"$job"})', stat),
      panel(3, "Conflict queue collection", "short", 'min(densemem_conflict_queue_collection_success{job=~"$job"}) * min(up{job=~"$job"})', stat, "card/conflict_queue_collection_success"),
      panel(4, "HTTP requests", "short", plainCounter("densemem_http_requests_total", "", true), stat, "card/http_requests"),
      panel(5, "HTTP errors", "short", counterWithParentZero("densemem_http_requests_total", 'status_class=~"4xx|5xx"', "densemem_http_requests_total", "", false), stat, "card/http_errors"),
      panel(6, "Average HTTP request latency", "ms", histogramAverage("densemem_http_request_duration_seconds", 1000), stat, "card/avg_http_latency"),
      panel(7, "HTTP request rate", "reqps", rate("densemem_http_requests_total", "", true), timeseries, "series/http_rps"),
      panel(8, "HTTP error rate", "reqps", rateWithParentZero("densemem_http_requests_total", 'status_class=~"4xx|5xx"', "densemem_http_requests_total", "", false), timeseries, "series/http_errors_rps"),
      panel(9, "Relationship corrections", "short", ledgerGauge("densemem_operational_relationship_corrections", "window", 'window="$window"'), stat, "card/relationship_corrections"),
      ...statusPanels,
    ],
  },
  {
    uid: "dense-mem-ai-recall",
    title: "Dense-Mem AI and Recall",
    tags: ["dense-mem", "operations", "ai"],
    description: "System-wide provider, assessor, recall, and feedback measures. Missing usage, unpriced calls, and absent feedback remain no-data states instead of appearing as zero.",
    panels: [
      panel(1, "Embedding requests", "short", counter("densemem_embedding_requests_total"), stat, "card/embedding_requests"),
      panel(2, "Embedding errors", "short", counterWithParentZero("densemem_embedding_errors_total", `code=~"${labels.embeddingErrorCodes.join("|")}"`, "densemem_embedding_requests_total"), stat, "card/embedding_errors"),
      panel(3, "Embedding tokens", "short", counter("densemem_embedding_tokens_total", 'kind="total"'), stat, "card/embedding_tokens"),
      panel(4, "Average embedding latency", "ms", histogramAverage("densemem_embedding_duration_seconds", 1000), stat, "card/avg_embedding_latency"),
      panel(5, "Embedding request rate", "reqps", rate("densemem_embedding_requests_total"), timeseries, "series/embedding_requests"),
      panel(6, "Embedding error rate", "reqps", rateWithParentZero("densemem_embedding_errors_total", `code=~"${labels.embeddingErrorCodes.join("|")}"`, "densemem_embedding_requests_total"), timeseries, "series/embedding_errors"),
      panel(49, "Embedding token rate", "short", rate("densemem_embedding_tokens_total", 'kind="total"'), timeseries, "series/embedding_tokens"),
      panel(7, "Verifier requests", "short", counter("densemem_verifier_requests_total"), stat, "card/verifier_requests"),
      panel(8, "Verifier tokens", "short", counter("densemem_verifier_tokens_total", 'kind="total"'), stat, "card/verifier_tokens"),
      panel(9, "Average verifier latency", "ms", histogramAverage("densemem_verifier_duration_seconds", 1000), stat, "card/avg_verifier_latency"),
      panel(10, "Verifier request rate", "reqps", rate("densemem_verifier_requests_total"), timeseries, "series/verifier_requests"),
      panel(11, "Verifier token rate", "short", rate("densemem_verifier_tokens_total", 'kind="total"'), timeseries, "series/verifier_tokens"),
      panel(12, "Recall requests", "short", counter("densemem_recall_requests_total"), stat, "card/recalls"),
      panel(13, "Average recall results", "short", histogramAverage("densemem_recall_results"), stat, "card/avg_recall_results"),
      panel(14, "P95 recall latency", "ms", quantile("densemem_recall_duration_seconds", "0.95", 1000), stat, "card/p95_recall_latency"),
      panel(15, "Recall request rate", "reqps", rate("densemem_recall_requests_total"), timeseries, "series/recalls"),
      panel(16, "Recall results per request", "short", histogramAverage("densemem_recall_results", 1, "$__rate_interval"), timeseries, "series/recall_results"),
      panel(17, "Recall p95 latency", "ms", quantile("densemem_recall_duration_seconds", "0.95", 1000, "$__rate_interval"), timeseries, "series/recall_p95_latency"),
      panel(18, "LLM recall used", "percent", recallRate('used="true"'), stat, "card/llm_recall_used_rate"),
      panel(19, "LLM answers supported", "percent", recallRate('answer_supported="true"'), stat, "card/llm_recall_answer_supported_rate"),
      panel(20, "LLM recall quality", "percent", histogramAverage("densemem_recall_feedback_quality_score", 100), stat, "card/llm_recall_quality_score"),
      panel(21, "LLM missing context", "percent", recallRate('missing_context="true"'), stat, "card/llm_recall_missing_context_rate"),
      panel(22, "LLM irrelevant recall", "percent", recallRate('irrelevant="true"'), stat, "card/llm_recall_irrelevant_rate"),
      panel(23, "LLM recall used rate", "percent", `100 * (${rate("densemem_recall_feedback_total", 'used="true"', true)}) / (${rate("densemem_recall_feedback_total")})`, timeseries, "series/llm_recall_used_rate"),
      panel(24, "LLM answer supported rate", "percent", `100 * (${rate("densemem_recall_feedback_total", 'answer_supported="true"', true)}) / (${rate("densemem_recall_feedback_total")})`, timeseries, "series/llm_recall_answer_supported_rate"),
      panel(25, "LLM recall quality", "percent", histogramAverage("densemem_recall_feedback_quality_score", 100, "$__rate_interval"), timeseries, "series/llm_recall_quality_score"),
      panel(26, "LLM missing context rate", "percent", `100 * (${rate("densemem_recall_feedback_total", 'missing_context="true"', true)}) / (${rate("densemem_recall_feedback_total")})`, timeseries, "series/llm_recall_missing_context_rate"),
      panel(27, "LLM irrelevant recall rate", "percent", `100 * (${rate("densemem_recall_feedback_total", 'irrelevant="true"', true)}) / (${rate("densemem_recall_feedback_total")})`, timeseries, "series/llm_recall_irrelevant_rate"),
      panel(28, "Dream feedback", "short", counter("densemem_dream_feedback_total"), stat, "card/dream_feedbacks"),
      panel(29, "Dream feedback rate", "short", rate("densemem_dream_feedback_total"), timeseries, "series/dream_feedbacks"),
      panel(30, "Assessor requests", "short", counter("densemem_assessor_requests_total"), stat, "card/assessor_requests"),
      panel(31, "Assessor request failures", "short", counterWithParentZero("densemem_assessor_requests_total", 'outcome=~"provider_error|malformed_exhausted"', "densemem_assessor_requests_total"), stat, "card/assessor_request_failures"),
      panel(32, "Assessor validation failures", "short", counterWithParentZero("densemem_assessor_validation_failures_total", "", "densemem_assessor_requests_total"), stat, "card/assessor_validation_failures"),
      panel(33, "Assessor tokens", "short", counter("densemem_assessor_tokens_total"), stat, "card/assessor_tokens"),
      panel(34, "Average assessor latency", "ms", histogramAverage("densemem_assessor_duration_seconds", 1000), stat, "card/avg_assessor_duration"),
      panel(35, "Assessor terminal failures", "short", counterWithParentZero("densemem_assessor_terminal_failures_total", "", "densemem_assessor_requests_total"), stat, "card/assessor_terminal_failures"),
      panel(36, "Assessor request rate", "reqps", rate("densemem_assessor_requests_total"), timeseries, "series/assessor_requests"),
      panel(37, "Assessor request failure rate", "reqps", rateWithParentZero("densemem_assessor_requests_total", 'outcome=~"provider_error|malformed_exhausted"', "densemem_assessor_requests_total"), timeseries, "series/assessor_request_failures"),
      panel(38, "Assessor validation failure rate", "reqps", rateWithParentZero("densemem_assessor_validation_failures_total", "", "densemem_assessor_requests_total"), timeseries, "series/assessor_validation_failures"),
      panel(39, "Assessor token rate", "short", rate("densemem_assessor_tokens_total"), timeseries, "series/assessor_tokens"),
      panel(40, "Assessor latency", "ms", histogramAverage("densemem_assessor_duration_seconds", 1000, "$__rate_interval"), timeseries, "series/assessor_duration"),
      panel(41, "Assessor terminal failures", "reqps", rateWithParentZero("densemem_assessor_terminal_failures_total", "", "densemem_assessor_requests_total"), timeseries, "series/assessor_terminal_failures"),
      panel(42, "AI cost", "currencyUSD", cost(), stat, "card/ai_cost_usd", "No data means missing usage or pricing; configure the retained Telemetry pricing settings."),
      panel(43, "Verifier cost", "currencyUSD", cost("verifier"), stat, "card/verifier_cost_usd", "No data means missing usage or pricing; configure the retained Telemetry pricing settings."),
      panel(44, "Embedding cost", "currencyUSD", cost("embedding"), stat, "card/embedding_cost_usd", "No data means missing usage or pricing; configure the retained Telemetry pricing settings."),
      panel(45, "Average conflict review duration", "ms", histogramAverage("densemem_conflict_review_duration_seconds", 1000), stat, "card/avg_conflict_review_duration"),
      panel(46, "Conflict review latency", "ms", histogramAverage("densemem_conflict_review_duration_seconds", 1000, "$__rate_interval"), timeseries, "series/conflict_review_duration"),
      panel(47, "Provider usage without pricing or usage", "short", counterBy("densemem_operation_provider_usage_unpriced_total", "operation,component,reason"), timeseries, "", "Counts missing provider usage and pricing. Label values are closed and contain no identity or content."),
      panel(48, "Provider token volume", "short", counterBy("densemem_operation_provider_tokens_total", "operation,component,kind,source"), timeseries, "", "Bounded operation, component, token kind, and source labels; no provider or model identity."),
    ],
  },
  {
    uid: "dense-mem-workflows",
    title: "Dense-Mem Remember and Dream",
    tags: ["dense-mem", "operations", "workflows"],
    description: "System-wide Remember and Dream activity plus canonical Hypothesis and lifecycle data. Durable collector values are gauges for rolling windows, never counters.",
    panels: [
      panel(1, "Remember calls", "short", counter("densemem_remember_acknowledgements_total"), stat, "card/remember_requests"),
      panel(2, "Average Remember duration", "ms", histogramAverage("densemem_remember_acknowledgement_duration_seconds", 1000), stat, "card/avg_remember_duration"),
      panel(3, "P95 Remember duration", "ms", quantile("densemem_remember_acknowledgement_duration_seconds", "0.95", 1000), stat, "card/p95_remember_duration"),
      panel(4, "Remember calls per second", "reqps", rate("densemem_remember_acknowledgements_total"), timeseries, "series/remember_requests"),
      panel(5, "Remember average duration", "ms", histogramAverage("densemem_remember_acknowledgement_duration_seconds", 1000, "$__rate_interval"), timeseries, "series/avg_remember_duration"),
      panel(6, "Remember p95 duration", "ms", quantile("densemem_remember_acknowledgement_duration_seconds", "0.95", 1000, "$__rate_interval"), timeseries, "series/p95_remember_duration"),
      panel(7, "MCP transport attempts", "short", counterBy("densemem_mcp_transport_requests_total", "method,status_class"), timeseries),
      panel(8, "MCP transport duration", "s", histogramAverage("densemem_mcp_transport_duration_seconds", 1, "$__rate_interval", "method,status_class"), timeseries),
      panel(9, "MCP logical tool outcomes", "short", counterBy("densemem_mcp_tool_results_total", "outcome"), timeseries),
      panel(10, "Logical operation attempts", "short", counterBy("densemem_logical_operation_attempts_total", "operation,classification,outcome"), timeseries),
      panel(11, "Logical operation duration", "s", histogramAverage("densemem_logical_operation_duration_seconds", 1, "$__rate_interval", "operation,outcome"), timeseries),
      panel(12, "Logical operation recoveries", "short", counterBy("densemem_logical_operation_recoveries_total", "operation,outcome"), timeseries),
      panel(13, "Remember phase duration", "s", histogramAverage("densemem_remember_phase_duration_seconds", 1, "$__rate_interval", "phase,outcome"), timeseries),
      panel(14, "Dream cycle attempts", "short", counterBy("densemem_dream_cycle_attempts_total", "lane,status"), timeseries),
      panel(15, "Dream cycle duration", "s", histogramAverage("densemem_dream_cycle_duration_seconds", 1, "$__rate_interval", "lane,status"), timeseries),
      panel(16, "Dream provider attempts", "short", counterBy("densemem_dream_provider_attempts_total", "stage,outcome"), timeseries),
      panel(17, "Dream provider duration", "s", histogramAverage("densemem_dream_provider_duration_seconds", 1, "$__rate_interval", "stage,outcome"), timeseries),
      panel(18, "Dream feedback actions", "short", counterBy("densemem_dream_feedback_actions_total", "decision,outcome"), timeseries),
      panel(19, "Recall Hypothesis expansions", "short", counterBy("densemem_recall_hypothesis_expansions_total", "outcome"), timeseries),
      panel(20, "Recall Hypotheses returned", "short", counter("densemem_recall_hypotheses_returned_total", "", false, "$__rate_interval"), timeseries),
      panel(21, "Dream runs", "short", ledgerGauge("densemem_operational_dream_runs", "window,lane,status", 'window="$window"'), timeseries),
      panel(22, "Dream input targets", "short", ledgerGauge("densemem_operational_dream_input_targets", "window,lane,status", 'window="$window"'), timeseries),
      panel(23, "Dream evidence targets", "short", ledgerGauge("densemem_operational_dream_evidence_targets", "window,lane,status", 'window="$window"'), timeseries),
      panel(24, "Dream targets evaluated", "short", ledgerGauge("densemem_operational_dream_evaluated_targets", "window,lane,status", 'window="$window"'), timeseries),
      panel(25, "Dream provider proposals", "short", ledgerGauge("densemem_operational_dream_provider_proposals", "window,lane,status", 'window="$window"'), timeseries),
      panel(26, "Dream Hypotheses created", "short", ledgerGauge("densemem_operational_dream_created_hypotheses", "window,lane,status", 'window="$window"'), timeseries),
      panel(27, "Dream Hypotheses rejected", "short", ledgerGauge("densemem_operational_dream_rejected_hypotheses", "window,lane,status", 'window="$window"'), timeseries),
      panel(28, "Current Hypotheses", "short", ledgerGauge("densemem_operational_hypotheses", "lane,status"), timeseries),
      panel(29, "Hypothesis backlog", "short", ledgerGauge("densemem_operational_hypothesis_backlog", "lane"), stat),
      panel(30, "Oldest Hypothesis backlog age", "s", ledgerGauge("densemem_operational_hypothesis_oldest_backlog_age_seconds", "lane"), stat),
      panel(31, "Dream feedback events", "short", ledgerGauge("densemem_operational_dream_feedback_events", "window,decision", 'window="$window"'), timeseries),
      panel(32, "Remember attempts", "short", ledgerGauge("densemem_operational_remember_attempts", "window,outcome", 'window="$window"'), timeseries),
      panel(33, "Dream-confirmed Relationships", "short", ledgerGauge("densemem_operational_dream_confirmed_relationships", "window,status", 'window="$window"'), timeseries),
      panel(34, "Relationship lifecycle transitions", "short", ledgerGauge("densemem_operational_relationship_transitions", "window,status", 'window="$window"'), timeseries),
      panel(35, "Relationship corrections", "short", ledgerGauge("densemem_operational_relationship_corrections", "window" , 'window="$window"'), timeseries),
    ],
  },
];

function gridPos(type, layout) {
  const width = type === timeseries ? 12 : 8;
  const height = type === timeseries ? 8 : 5;
  if (layout.x + width > 24) {
    layout.y += layout.rowHeight;
    layout.x = 0;
    layout.rowHeight = 0;
  }
  const position = { h: height, w: width, x: layout.x, y: layout.y };
  layout.x += width;
  layout.rowHeight = Math.max(layout.rowHeight, height);
  return position;
}

function buildPanel(value, layout) {
  const panel = {
    datasource: { type: "prometheus", uid: "$datasource" },
    description: value.parity ? `Parity: ${value.parity}. ${value.description}`.trim() : value.description,
    fieldConfig: { defaults: { unit: value.unit, color: { mode: "palette-classic" }, custom: { drawStyle: "line", lineWidth: 2, fillOpacity: 8, spanNulls: false } }, overrides: [] },
    gridPos: gridPos(value.type, layout),
    id: value.id,
    options: value.type === stat
      ? { colorMode: "value", graphMode: "area", justifyMode: "auto", orientation: "auto", reduceOptions: { calcs: ["lastNotNull"], fields: "", values: false }, textMode: "auto" }
      : { legend: { calcs: ["lastNotNull", "max"], displayMode: "table", placement: "bottom" }, tooltip: { mode: "multi", sort: "desc" } },
    targets: [{ datasource: { type: "prometheus", uid: "$datasource" }, expr: value.expr, instant: value.type === stat, range: value.type === timeseries, refId: "A" }],
    title: value.title,
    type: value.type,
  };
  if (value.parity.startsWith("card/")) panel.targets[0].instant = true;
  return panel;
}

function buildDashboard(dashboard) {
  const layout = { x: 0, y: 0, rowHeight: 0 };
  const panels = dashboard.panels.map((value) => buildPanel(value, layout));
  return {
    __inputs: [],
    __requires: [
      { type: "grafana", id: "grafana", name: "Grafana", version: "13.2.2" },
      { type: "datasource", id: "prometheus", name: "Prometheus", version: "1.0.0" },
    ],
    annotations: { list: [{ builtIn: 1, datasource: { type: "grafana", uid: "-- Grafana --" }, enable: true, hide: true, iconColor: "rgba(0, 211, 255, 1)", name: "Annotations & Alerts", type: "dashboard" }] },
    description: dashboard.description,
    editable: false,
    fiscalYearStartMonth: 0,
    graphTooltip: 1,
    id: null,
    links: [],
    liveNow: false,
    panels,
    refresh: "30s",
    schemaVersion: 41,
    tags: dashboard.tags,
    templating: {
      list: [
        { current: {}, datasource: { type: "prometheus", uid: "$datasource" }, includeAll: false, label: "Prometheus datasource", name: "datasource", options: [], query: "prometheus", refresh: 1, type: "datasource" },
        { current: { selected: true, text: "dense-mem", value: "dense-mem" }, datasource: { type: "prometheus", uid: "$datasource" }, definition: "label_values(up, job)", includeAll: false, label: "Job", multi: false, name: "job", query: { query: "label_values(up, job)", refId: "PrometheusVariableQueryEditor-VariableQuery" }, refresh: 1, sort: 1, type: "query" },
        { current: { selected: true, text: "1h", value: "1h" }, label: "Rolling totals", name: "window", options: ["15m", "30m", "1h", "12h", "1d", "7d", "30d"].map((value) => ({ selected: value === "1h", text: value, value })), query: "15m,30m,1h,12h,1d,7d,30d", type: "custom" },
      ],
    },
    time: { from: "now-1h", to: "now" },
    timepicker: { refresh_intervals: ["30s", "1m", "5m", "15m", "30m", "1h"] },
    timezone: "browser",
    title: dashboard.title,
    uid: dashboard.uid,
    version: 1,
    weekStart: "",
  };
}

mkdirSync(outputDir, { recursive: true });
for (const dashboard of dashboards) {
  const path = resolve(outputDir, `${dashboard.uid}.json`);
  const { panels, ...metadata } = buildDashboard(dashboard);
  const panelLines = panels.map((value) => `    ${JSON.stringify(value)}`).join(",\n");
  const output = `${JSON.stringify(metadata, null, 2).slice(0, -2)},\n  "panels": [\n${panelLines}\n  ]\n}`;
  writeFileSync(path, `${output}\n`);
}
