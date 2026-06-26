import { expect, Page, test } from "@playwright/test";

const team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Default",
  description: "",
  metadata: null,
  config: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

type TestKey = {
  id: string;
  team_id: string;
  name: string;
  key_suffix: string;
  scopes: string[];
  rate_limit: number;
  last_used_at: string | null;
  expires_at: string | null;
  created_at: string;
};

const key: TestKey = {
  id: "22222222-2222-4222-8222-222222222222",
  team_id: team.id,
  name: "default profile",
  key_suffix: "abc123",
  scopes: ["read", "write"],
  rate_limit: 120,
  last_used_at: "2026-05-02T13:00:00Z",
  expires_at: null,
  created_at: "2026-04-30T12:00:00Z",
};

type TestBan = {
  ip: string;
  reason: string;
  source: "auto" | "manual";
  failure_count: number;
  banned_at: string;
  expires_at: string | null;
  last_failed_at: string | null;
  metadata: Record<string, unknown> | null;
  revoked_at: string | null;
};

const ban: TestBan = {
  ip: "203.0.113.10",
  reason: "auth failures: AUTH_INVALID",
  source: "auto",
  failure_count: 11,
  banned_at: "2026-05-02T12:00:00Z",
  expires_at: null,
  last_failed_at: "2026-05-02T12:00:00Z",
  metadata: {},
  revoked_at: null,
};

const metrics = {
  window: {
    from: "2026-05-02T12:00:00Z",
    to: "2026-05-02T13:00:00Z",
    bucket_seconds: 60,
    retention_days: 30,
  },
  system: {
    requests: 42,
    errors: 2,
    avg_latency_ms: 18.5,
    max_latency_ms: 90,
  },
  dependencies: [
    { name: "postgres", status: "ok", latency_ms: 3 },
    { name: "neo4j", status: "ok", latency_ms: 8 },
  ],
  teams: [
    { team_id: team.id, team_name: "Default", requests: 42, errors: 2, avg_latency_ms: 18.5, max_latency_ms: 90 },
  ],
  keys: [
    { team_id: team.id, team_name: "Default", key_id: key.id, key_name: "default profile", key_suffix: "abc123", requests: 40, errors: 1, avg_latency_ms: 17, max_latency_ms: 80 },
  ],
  routes: [
    { route: "/api/v1/fragments/:id", method: "GET", status_class: "2xx", requests: 39, errors: 0, avg_latency_ms: 16, max_latency_ms: 70 },
  ],
};

const telemetryCards = [
  { id: "http_requests", label: "HTTP requests", unit: "requests", value: 42 },
  { id: "http_errors", label: "HTTP errors", unit: "requests", value: 2 },
  { id: "verifier_tokens", label: "Verifier tokens", unit: "tokens", value: 1200 },
  { id: "embedding_tokens", label: "Embedding tokens", unit: "tokens", value: 3200 },
  { id: "recalls", label: "Recall requests", unit: "requests", value: 9 },
  { id: "avg_recall_results", label: "Avg recall results", unit: "results", value: 3.2 },
  { id: "llm_recall_used_rate", label: "LLM recall used", unit: "percent", value: 80 },
  { id: "llm_recall_answer_supported_rate", label: "LLM answer supported", unit: "percent", value: 70 },
  { id: "llm_recall_quality_score", label: "LLM recall quality", unit: "percent", value: 75 },
  { id: "llm_recall_missing_context_rate", label: "LLM missing context", unit: "percent", value: 10 },
  { id: "llm_recall_irrelevant_rate", label: "LLM irrelevant recall", unit: "percent", value: 5 },
  { id: "promotions", label: "Promotions", unit: "promotions", value: 4 },
  { id: "promotion_rate", label: "Promotion rate", unit: "percent", value: 66.6 },
  { id: "avg_http_latency", label: "Avg HTTP latency", unit: "ms", value: 18.5 },
  { id: "avg_embedding_latency", label: "Avg embedding latency", unit: "ms", value: 44.2 },
  { id: "avg_verifier_latency", label: "Avg verifier latency", unit: "ms", value: 112.8 },
  { id: "avg_claim_verify_latency", label: "Avg claim-to-verify", unit: "ms", value: 210.4 },
  { id: "avg_claim_promotion_latency", label: "Avg claim-to-promote", unit: "ms", value: 420.9 },
  { id: "avg_verify_promotion_latency", label: "Avg verify-to-promote", unit: "ms", value: 190.1 },
  { id: "pending_claims", label: "Pending claims", unit: "claims", value: 5 },
  { id: "validated_claims", label: "Validated claims", unit: "claims", value: 12 },
  { id: "disputed_claims", label: "Disputed claims", unit: "claims", value: 1 },
  { id: "revalidation_backlog", label: "Revalidation backlog", unit: "facts", value: 7 },
];

const telemetrySeries = [
  { id: "http_rps", label: "HTTP requests", unit: "rps" },
  { id: "http_errors_rps", label: "HTTP errors", unit: "rps" },
  { id: "embedding_tokens", label: "Embedding tokens", unit: "tokens/s" },
  { id: "verifier_tokens", label: "Verifier tokens", unit: "tokens/s" },
  { id: "recalls", label: "Recall requests", unit: "requests/s" },
  { id: "promotions", label: "Promotions", unit: "promotions/s" },
  { id: "recall_results", label: "Recall results", unit: "results" },
  { id: "llm_recall_used_rate", label: "LLM recall used", unit: "percent" },
  { id: "llm_recall_answer_supported_rate", label: "LLM answer supported", unit: "percent" },
  { id: "llm_recall_quality_score", label: "LLM recall quality", unit: "percent" },
  { id: "llm_recall_missing_context_rate", label: "LLM missing context", unit: "percent" },
  { id: "llm_recall_irrelevant_rate", label: "LLM irrelevant recall", unit: "percent" },
  { id: "claim_verify_latency", label: "Claim-to-verify", unit: "ms" },
  { id: "claim_promotion_latency", label: "Claim-to-promote", unit: "ms" },
  { id: "verify_promotion_latency", label: "Verify-to-promote", unit: "ms" },
  { id: "pending_claims", label: "Pending claims", unit: "claims" },
  { id: "validated_claims", label: "Validated claims", unit: "claims" },
  { id: "disputed_claims", label: "Disputed claims", unit: "claims" },
  { id: "revalidation_backlog", label: "Revalidation backlog", unit: "facts" },
].map((series, index) => ({
  ...series,
  points: [
    { timestamp: "2026-05-02T12:00:00Z", value: index / 10 },
    { timestamp: "2026-05-02T13:00:00Z", value: index / 10 + 0.4 },
  ],
}));

const currentTelemetryIds = new Set(["pending_claims", "validated_claims", "disputed_claims", "revalidation_backlog"]);

const telemetry = {
  available: true,
  window: {
    key: "1h",
    from: "2026-05-02T12:00:00Z",
    to: "2026-05-02T13:00:00Z",
    step_seconds: 60,
    retention_days: 30,
  },
  scope: { type: "system" },
  cards: telemetryCards,
  windowed_cards: telemetryCards.filter((card) => !currentTelemetryIds.has(card.id)),
  current_cards: telemetryCards.filter((card) => currentTelemetryIds.has(card.id)),
  series: telemetrySeries,
  activity_series: telemetrySeries.filter((series) => !currentTelemetryIds.has(series.id)),
  state_series: telemetrySeries.filter((series) => currentTelemetryIds.has(series.id)),
};

const operationLogs = [
  {
    id: "33333333-3333-4333-8333-333333333333",
    timestamp: "2026-06-15T10:30:00Z",
    severity: "INFO",
    severity_rank: 20,
    message: "control_http_request",
    source: "/home/mark/dense-mem/internal/observability/logger.go:186",
    team_id: null,
    profile_id: null,
    correlation_id: "",
    error: "",
    attrs: {
      method: "GET",
      uri: "/control/api/logs",
      route: "/control/api/logs",
      status: 200,
      request_id: "req-control-1",
    },
  },
  {
    id: "44444444-4444-4444-8444-444444444444",
    timestamp: "2026-06-15T10:29:40Z",
    severity: "DEBUG",
    severity_rank: 10,
    message: "sso login oidc claims read",
    source: "/home/mark/dense-mem/internal/service/sso_service.go:462",
    team_id: team.id,
    profile_id: key.id,
    correlation_id: "corr-sso-1",
    error: "",
    attrs: {
      provider_found: true,
      provider_kind: "generic_oidc",
      provider_enabled: true,
      group_count: 1,
      groups_from_userinfo: true,
      id_token_claim_count: 12,
      userinfo_claim_count: 11,
    },
  },
];

const recallFeedbackEvents = [
  {
    recall_id: "rec_1234567890",
    created_at: "2026-06-23T10:00:00Z",
    updated_at: "2026-06-23T10:01:00Z",
    feedback_at: "2026-06-23T10:01:00Z",
    team_id: team.id,
    profile_id: key.id,
    key_id: key.id,
    auth_method: "api_key",
    tool_name: "recall_memory",
    query: "Why was recall bad?",
    tool_args: {
      input: { query: "Why was recall bad?", limit: 5 },
      effective: { query: "Why was recall bad?", limit: 5, include_evidence: false, use_communities: false },
    },
    result_refs: [
      { type: "fragment", id: "fragment-1", rank: 1, tier: "2", final_score: 0.74, status_at_recall: "active" },
    ],
    result_count: 1,
    snapshot_state: "captured",
    used: true,
    answer_supported: false,
    quality: "low",
    missing_context: true,
    irrelevant: false,
    feedback_comment: "Returned stale UI notes instead of the requested button pattern. SearchPanel disabled-state and listbox accessibility pattern.",
    irrelevant_result_refs: [{ type: "fragment", id: "fragment-1", rank: 1 }],
    resolved_results: [
      {
        type: "fragment",
        id: "fragment-1",
        rank: 1,
        resolution_status: "found",
        current_status: "retracted",
        current: { content: "The old fragment has been retracted.", status: "retracted" },
        ref: { type: "fragment", id: "fragment-1", rank: 1, tier: "2", status_at_recall: "active" },
      },
    ],
  },
  {
    recall_id: "rec_pending",
    created_at: "2026-06-23T10:02:00Z",
    updated_at: "2026-06-23T10:02:00Z",
    feedback_at: null,
    team_id: team.id,
    profile_id: key.id,
    key_id: key.id,
    auth_method: "api_key",
    tool_name: "recall_memory",
    query: "Pending recall waiting",
    tool_args: {},
    result_refs: [],
    result_count: 0,
    snapshot_state: "captured",
    quality: "",
  },
];

const dreamRationale = "Generated by pairing same-profile knowledge that is not already the same predicate/type, then keeping it as a hypothesis until user feedback confirms or rejects it.";

const dreamRun = {
  run_id: "run-1",
  team_id: team.id,
  run_date: "2026-06-14",
  started_at: "2026-06-14T14:30:00Z",
  completed_at: "2026-06-14T14:31:00Z",
  reflect_ran: true,
  reevaluate_ran: true,
  dream_ran: true,
  stale_facts: 0,
  candidate_claims: 0,
  disputed_claims: 0,
  clarifications: 0,
  reevaluated_dreams: 0,
  created_dreams: 1,
  status: "completed",
};

const dream = {
  dream_id: "dream-1",
  team_id: team.id,
  hypothesis: "A may affect B.",
  what_if: "What if A and B are related?",
  possible_outcome: "Future decisions should check A against B.",
  rationale: dreamRationale,
  likelihood: 0.45,
  confidence: 0.55,
  status: "proposed",
  cycle: "2026-06-14",
  cycle_run_id: dreamRun.run_id,
  generator_model: "heuristic",
  source_refs: [],
  created_at: "2026-06-14T14:30:00Z",
  updated_at: "2026-06-14T14:31:00Z",
};

type TestProfile = typeof team;

test("team creation flow", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [] });
  await openPortal(page);

  await page.getByRole("button", { name: "New Team" }).click();
  await page.getByLabel("Name").first().fill("Work Team");
  await page.getByLabel("Description").first().fill("for work");
  await page.getByRole("button", { name: /^Create$/ }).click();

  await expect(page.getByRole("heading", { name: "Work Team" })).toBeVisible();
});

test("API key creation shows plaintext once", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [] });
  await openPortal(page);

  await page.getByRole("button", { name: /Team Profiles/ }).click();
  await page.getByLabel("Role").selectOption("member");
  await page.getByLabel("Permission").selectOption("read");
  await page.getByRole("button", { name: "Create profile" }).click();

  await expect(page.getByLabel("Generated API key")).toHaveValue("dm_plain_once");
  await page.getByRole("button", { name: "Dismiss API key" }).click();
  await expect(page.getByLabel("Generated API key")).toBeHidden();
});

test("team and profile names update and profile key regenerates", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /Team Settings/ }).click();
  await page.locator("#team-name").fill("Renamed Team");
  await page.getByRole("button", { name: /^Save$/ }).click();
  await expect(page.getByRole("heading", { name: "Renamed Team" })).toBeVisible();

  await page.getByRole("button", { name: /Team Profiles/ }).click();
  await page.getByLabel("Profile name default profile").fill("Research profile");
  await page.getByRole("button", { name: /Save profile default profile/ }).click();
  await expect(page.getByLabel("Profile name Research profile")).toHaveValue("Research profile");

  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Regenerate key for profile Research profile/ }).click();
  await expect(page.getByLabel("Generated API key")).toHaveValue("dm_rotated_once");
});

test("team profile list and delete flow", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /Team Profiles/ }).click();
  await expect(page.getByText("******abc123")).toBeVisible();
  const keyRow = page.getByRole("row", { name: /abc123/ });
  await expect(keyRow).toContainText("Read/write");
  await expect(keyRow.getByText(/May/)).toBeVisible();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Delete profile default profile/ }).click();

  await expect(page.getByRole("button", { name: /Delete profile default profile/ })).toBeHidden();
  await expect(page.getByText("******abc123")).toBeHidden();
});

test("IP ban list and clear reset flow", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key], bans: [ban] });
  await openPortal(page);

  await page.getByRole("button", { name: /IP Bans/ }).click();
  await expect(page.getByText("203.0.113.10")).toBeVisible();
  await expect(page.getByRole("row", { name: /203.0.113.10/ })).toContainText("11");

  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Clear IP ban and reset strikes for 203.0.113.10/ }).click();

  await expect(page.getByText("203.0.113.10")).toBeHidden();
});

test("metrics tab renders operational totals and filter queries", async ({ page }) => {
  const calls = await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /^Metrics$/ }).click();
  await expect(page.locator(".resource-rail")).toHaveCount(0);

  const telemetryTotals = page.getByLabel("Telemetry totals");
  for (const card of telemetry.windowed_cards) {
    await expect(telemetryTotals).toContainText(card.label);
  }
  const telemetryCurrentState = page.getByLabel("Telemetry current state");
  for (const card of telemetry.current_cards) {
    await expect(telemetryCurrentState).toContainText(card.label);
  }
  const telemetryCharts = page.getByLabel("Telemetry charts");
  for (const series of telemetry.activity_series) {
    await expect(telemetryCharts).toContainText(series.label);
  }
  const telemetryStateHistory = page.getByLabel("Telemetry state history");
  for (const series of telemetry.state_series) {
    await expect(telemetryStateHistory).toContainText(series.label);
  }

  const summary = page.getByLabel("Request metrics");
  await expect(summary).toContainText("42");
  await expect(summary).toContainText("2");
  await expect(summary).toContainText("4.8%");
  await expect(summary).toContainText("19 ms");
  await expect(summary).toContainText("90 ms");
  await expect(page.getByText("postgres")).toBeVisible();
  await expect(page.getByText("neo4j")).toBeVisible();
  await expect(page.getByRole("row", { name: /Default\s+42\s+2\s+19 ms\s+90 ms/ })).toBeVisible();
  await expect(page.getByRole("row", { name: /default profile\s+\*\*\*\*\*\*abc123\s+Default\s+40\s+1\s+17 ms/ })).toBeVisible();
  await expect(page.getByRole("row", { name: /\/api\/v1\/fragments\/:id\s+GET\s+2xx\s+39\s+0/ })).toBeVisible();

  await page.getByLabel("Window").selectOption("360");
  await page.getByLabel("Team", { exact: true }).selectOption(team.id);

  await expect.poll(() => calls.metricsUrls.some((url) => (
    url.includes("window_minutes=360") && url.includes(`team_id=${team.id}`)
  ))).toBe(true);
  await expectNoShellOverlap(page);
});

test("logs tab renders structured details and raw output", async ({ page }) => {
  const calls = await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /^Logs$/ }).click();
  await expect(page.locator(".resource-rail")).toHaveCount(0);

  await expect(page.getByText("GET /control/api/logs status 200")).toBeVisible();
  await expect(page.getByText("event=control_http_request")).toBeVisible();
  await expect(page.getByText("provider_kind=generic_oidc")).toBeVisible();

  await page.getByRole("button", { name: /View raw log GET \/control\/api\/logs status 200/ }).click();
  await expect(page.getByLabel(/Raw log body GET \/control\/api\/logs status 200/)).toContainText('"msg": "control_http_request"');
  await expectNoShellOverlap(page);

  await page.getByLabel("Rows").selectOption("25");
  await expect.poll(() => calls.logUrls.some((url) => url.includes("limit=25"))).toBe(true);
});

test("recall feedback tab renders query, params, result ids, and resolved state", async ({ page }) => {
  const calls = await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /^Feedback$/ }).click();

  await expect(page.getByText("Why was recall bad?")).toBeVisible();
  await expect(page.getByText("Pending recall waiting")).toHaveCount(0);
  await expect(page.getByText("missing context", { exact: true })).toBeVisible();
  await page.getByLabel("Include pending").check();
  await expect(page.getByText("Pending recall waiting")).toBeVisible();
  await expect.poll(() => calls.recallFeedbackUrls.some((url) => url.includes("include_pending=true"))).toBe(true);

  await page.getByRole("button", { name: /View recall feedback rec_1234567890/ }).click();
  const resultRefs = page.getByRole("table", { name: "Recall feedback result refs" });
  await expect(resultRefs.getByRole("row", { name: /fragment-1/ })).toBeVisible();
  const commentDetails = page.getByRole("table", { name: "Recall feedback comment details" });
  await expect(commentDetails.getByText("Returned stale UI notes instead of the requested button pattern. SearchPanel disabled-state and listbox accessibility pattern.")).toBeVisible();
  await expect(commentDetails.getByText("#1 fragment:fragment-1")).toBeVisible();
  await expect(resultRefs.getByText("The old fragment has been retracted.")).toBeVisible();
  await expect(page.getByLabel(/Raw recall feedback rec_1234567890/)).toContainText('"feedback_comment"');
  await expectNoShellOverlap(page);

  await page.getByLabel("Quality").selectOption("low");
  await expect.poll(() => calls.recallFeedbackUrls.some((url) => url.includes("quality=low"))).toBe(true);
});

test("dream outputs keep rationale behind info tooltip", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /Team Dreams/ }).click();
  await expect(page.locator(".resource-rail")).toBeVisible();

  await expect(page.getByLabel("Dreaming status")).toContainText("Global force");
  await expect(page.getByText("A may affect B.")).toBeVisible();
  await expectNoShellOverlap(page);
  const rationale = page.locator(".info-tooltip-content", { hasText: dreamRationale });
  await expect(rationale).toBeHidden();
  await page.getByRole("button", { name: /Why this hypothesis: A may affect B\./ }).hover();
  await expect(rationale).toBeVisible();
});

test("team workspace header stays compact across team tabs", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  const heights: number[] = [];
  const workspaceTabs = page.locator(".team-workspace-tabs");
  for (const tab of ["Overview", "Profiles", "Dreams", "Settings"]) {
    await workspaceTabs.getByRole("button", { name: `Team ${tab}` }).click();
    const box = await page.locator(".team-workspace-header").boundingBox();
    expect(box, `${tab} header was not rendered`).not.toBeNull();
    heights.push(box?.height ?? 0);
  }

  expect(Math.max(...heights), `team header heights: ${heights.join(", ")}`).toBeLessThanOrEqual(125);
  expect(Math.max(...heights) - Math.min(...heights), `team header heights: ${heights.join(", ")}`).toBeLessThanOrEqual(8);
});

test("config tab uses horizontal subnavigation", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /^Config$/ }).click();

  const tabs = page.getByRole("tablist", { name: "Config sections" });
  await expect(tabs).toBeVisible();
  const tabsBox = await tabs.boundingBox();
  expect(tabsBox, "config tabs were not rendered").not.toBeNull();
  const maxTabsHeight = (page.viewportSize()?.width ?? 0) <= 620 ? 82 : 42;
  expect(tabsBox?.height ?? 0).toBeLessThanOrEqual(maxTabsHeight);
  await expect.poll(() => tabs.evaluate((element) => element.closest(".surface") === null)).toBe(true);
  await tabs.getByRole("tab", { name: /^Logs$/ }).click();
  await expect(page.getByRole("heading", { name: "Operation Logs" })).toBeVisible();
  await expectNoShellOverlap(page);
});

test("recall feedback retention config saves from control portal", async ({ page }) => {
  const calls = await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /^Config$/ }).click();
  await page.getByRole("tab", { name: /^Recall$/ }).click();

  await expect(page.getByLabel("Investigation retention days")).toHaveValue("30");
  await page.getByLabel("Investigation retention days").fill("45");
  await page.getByRole("button", { name: /Save config/ }).click();

  await expect.poll(() => calls.recallConfigBodies.some((body) => (
    body.items.some((item) => item.key === "RECALL_FEEDBACK_RETENTION_DAYS" && item.value === "45")
  ))).toBe(true);
});

test("team delete flow", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /Team Settings/ }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /^Delete$/ }).click();

  await expect(page.getByLabel("Control details").getByText("No teams")).toBeVisible();
});

test("auth token failure", async ({ page }) => {
  await page.route("**/control/api/session", async (route) => {
    await route.fulfill({ status: 401, contentType: "application/json", body: JSON.stringify({ message: "invalid token" }) });
  });
  await page.goto("/");

  await page.getByLabel("Control token").fill("wrong");
  await page.getByRole("button", { name: "Unlock" }).click();

  await expect(page.getByRole("alert")).toContainText("invalid token");
});

test("responsive portal layout", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await expect(page.getByRole("heading", { name: "Dense-Mem Control" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Teams" })).toBeVisible();
  const shellMinHeight = await page.locator(".app-shell").evaluate((element) => Number.parseFloat(getComputedStyle(element).minHeight));
  expect(shellMinHeight).toBeGreaterThanOrEqual((page.viewportSize()?.height ?? 0) - 1);
  await expect(page.locator(".topbar")).toHaveCSS("border-bottom-width", "1px");
  await expect(page.locator(".primary-rail")).toHaveCSS("border-right-width", "1px");
  await expect(page.locator(".resource-rail")).toHaveCSS("border-right-width", "1px");
  await expect(page.locator(".surface").first()).toHaveCSS("border-radius", "8px");
  await page.getByRole("button", { name: /Team Profiles/ }).click();
  await expect(page.getByRole("heading", { name: "Profiles" })).toBeVisible();
  await expectNoShellOverlap(page);

  if ((page.viewportSize()?.width ?? 1000) < 700) {
    await expect(page.locator(".workspace")).toHaveCSS("grid-template-columns", /[0-9.]+px/);
  }
});

async function openPortal(page: Page) {
  await page.goto("/");
  await page.getByLabel("Control token").fill("secret");
  await page.getByRole("button", { name: "Unlock" }).click();
  await expect(page.getByRole("heading", { name: "Teams" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Default/ })).toBeVisible();
}

async function expectNoShellOverlap(page: Page) {
  const overflow = await page.evaluate(() => ({
    viewportWidth: window.innerWidth,
    documentWidth: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    tableWraps: Array.from(document.querySelectorAll(".table-wrap")).map((element) => ({
      className: element.className,
      clientWidth: element.clientWidth,
      scrollWidth: element.scrollWidth,
    })),
    navContainers: Array.from(document.querySelectorAll(".workspace, .detail-pane, .primary-rail, .top-nav-tabs, .rail-tabs, .team-workspace-tabs, .config-tabs")).map((element) => ({
      className: element.className,
      clientWidth: element.clientWidth,
      scrollWidth: element.scrollWidth,
    })),
  }));
  expect(overflow.documentWidth).toBeLessThanOrEqual(overflow.viewportWidth + 1);
  for (const tableWrap of overflow.tableWraps) {
    expect(tableWrap.scrollWidth, `${tableWrap.className} overflowed horizontally`).toBeLessThanOrEqual(tableWrap.clientWidth + 1);
  }
  for (const navContainer of overflow.navContainers) {
    expect(navContainer.scrollWidth, `${navContainer.className} overflowed horizontally`).toBeLessThanOrEqual(navContainer.clientWidth + 1);
  }

  const boxes = await page.locator(".topbar, .primary-rail, .resource-rail, .detail-pane").evaluateAll((elements) => (
    elements.map((element) => {
      const rect = element.getBoundingClientRect();
      return {
        className: element.className,
        left: rect.left,
        top: rect.top,
        right: rect.right,
        bottom: rect.bottom,
        width: rect.width,
        height: rect.height,
      };
    })
  ));
  for (const box of boxes) {
    expect(box.width).toBeGreaterThan(0);
    expect(box.height).toBeGreaterThan(0);
  }
  for (let i = 0; i < boxes.length; i += 1) {
    for (let j = i + 1; j < boxes.length; j += 1) {
      expect(rectanglesOverlap(boxes[i], boxes[j]), `${boxes[i].className} overlaps ${boxes[j].className}`).toBe(false);
    }
  }
}

function rectanglesOverlap(
  first: { left: number; top: number; right: number; bottom: number },
  second: { left: number; top: number; right: number; bottom: number },
) {
  return first.left < second.right && first.right > second.left && first.top < second.bottom && first.bottom > second.top;
}

async function mockApi(page: Page, state: { teams: TestProfile[]; keys: TestKey[]; bans?: TestBan[] }) {
  const calls = {
    metricsUrls: [] as string[],
    logUrls: [] as string[],
    recallFeedbackUrls: [] as string[],
    recallConfigBodies: [] as Array<{ items: Array<{ key: string; value: string }> }>,
  };
  let teams = [...state.teams];
  let keys = [...state.keys];
  let bans = [...(state.bans ?? [])];
  let recallFeedbackConfig = {
    update_time: "2026-06-16T12:00:00Z",
    items: [
      { key: "RECALL_FEEDBACK_ENABLED", value: "true", effective_value: "true", updated_at: "2026-06-16T12:00:00Z" },
      { key: "RECALL_FEEDBACK_RETENTION_DAYS", value: "30", effective_value: "30", updated_at: "2026-06-16T12:00:00Z" },
    ],
    effective: { enabled: true, retention_days: 30 },
  };
  await page.route("**/control/api/**", async (route) => {
    const url = route.request().url();
    const method = route.request().method();

    if (url.endsWith("/session")) {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { authenticated: true } }) });
    }
    if (url.includes("/telemetry") && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: telemetry }) });
    }
    if (url.includes("/metrics") && method === "GET") {
      calls.metricsUrls.push(url);
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: metrics }) });
    }
    if (url.includes("/config/general") && method === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            update_time: "2026-06-16T09:00:00Z",
            items: [{ key: "APP_TIMEZONE", value: "Local", effective_value: "Local", updated_at: "2026-06-16T09:00:00Z" }],
            effective: { timezone: "Local" },
          },
        }),
      });
    }
    if (url.includes("/config/sso") && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { update_time: "2026-06-09T12:00:00Z", items: [] } }) });
    }
    if (url.includes("/config/dreaming") && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { update_time: "2026-06-11T03:00:00Z", items: [], effective: {} } }) });
    }
    if (url.includes("/config/community-detection") && method === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            update_time: "2026-06-15T03:30:00Z",
            items: [{ key: "COMMUNITY_DETECTION_ENABLED", value: "false", effective_value: "false", updated_at: "2026-06-15T03:30:00Z" }],
            effective: { enabled: false, start_time_local: "03:30", timezone: "Local", max_concurrency: 1, jitter_seconds: 600 },
          },
        }),
      });
    }
    if (url.includes("/config/operation-logs") && method === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            update_time: "2026-06-14T12:00:00Z",
            items: [{ key: "OPERATION_LOG_RETENTION_DAYS", value: "30", effective_value: "30", updated_at: "2026-06-14T12:00:00Z" }],
            effective: { retention_days: 30 },
          },
        }),
      });
    }
    if (url.includes("/config/recall-feedback") && method === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: recallFeedbackConfig }),
      });
    }
    if (url.includes("/config/recall-feedback") && method === "PATCH") {
      const body = route.request().postDataJSON() as { items: Array<{ key: string; value: string }> };
      calls.recallConfigBodies.push(body);
      recallFeedbackConfig = {
        ...recallFeedbackConfig,
        update_time: "2026-06-16T12:01:00Z",
        items: recallFeedbackConfig.items.map((item) => {
          const update = body.items.find((candidate) => candidate.key === item.key);
          return update ? { ...item, value: update.value, effective_value: update.value, updated_at: "2026-06-16T12:01:00Z" } : item;
        }),
        effective: {
          enabled: body.items.find((item) => item.key === "RECALL_FEEDBACK_ENABLED")?.value === "true",
          retention_days: Number(body.items.find((item) => item.key === "RECALL_FEEDBACK_RETENTION_DAYS")?.value ?? "30"),
        },
      };
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: recallFeedbackConfig }) });
    }
    if (url.includes("/control/api/recall-feedback-events/rec_1234567890") && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: recallFeedbackEvents[0] }) });
    }
    if (url.includes("/control/api/recall-feedback-events") && method === "GET") {
      const parsedUrl = new URL(url);
      const limit = Number(parsedUrl.searchParams.get("limit") ?? "100");
      const offset = Number(parsedUrl.searchParams.get("offset") ?? "0");
      const quality = parsedUrl.searchParams.get("quality") ?? "";
      const includePending = parsedUrl.searchParams.get("include_pending") === "true";
      calls.recallFeedbackUrls.push(url);
      const filtered = recallFeedbackEvents.filter((event) => {
        if (quality) {
          return event.quality === quality;
        }
        return includePending || event.quality;
      });
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: filtered.slice(offset, offset + limit), pagination: { limit, offset, total: filtered.length } }),
      });
    }
    if (url.includes("/control/api/logs") && method === "GET") {
      const parsedUrl = new URL(url);
      const limit = Number(parsedUrl.searchParams.get("limit") ?? "100");
      const offset = Number(parsedUrl.searchParams.get("offset") ?? "0");
      calls.logUrls.push(url);
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: operationLogs.slice(offset, offset + limit), pagination: { limit, offset, total: operationLogs.length } }),
      });
    }
    if (url.includes(`/teams/${team.id}/dreaming/status`) && method === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          data: {
            effective_config: {
              enabled: true,
              force_enabled: true,
              start_time_local: "03:00",
              timezone: "UTC",
              reflect_enabled: true,
              reevaluate_enabled: true,
              dream_enabled: true,
              max_outputs: 5,
              team_enabled: true,
              source: "global_force",
            },
            latest_run: dreamRun,
            pending_count: 0,
          },
        }),
      });
    }
    if (url.includes(`/teams/${team.id}/dreaming/runs`) && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: [dreamRun] }) });
    }
    if (url.includes(`/teams/${team.id}/dreams`) && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { items: [dream], next_cursor: "" } }) });
    }
    if (url.endsWith("/teams") && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(pageOf(teams)) });
    }
    if (url.endsWith("/teams") && method === "POST") {
      const body = route.request().postDataJSON() as { name: string; description: string };
      const created = { ...team, id: "33333333-3333-4333-8333-333333333333", name: body.name, description: body.description };
      teams = [...teams, created];
      return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ data: created }) });
    }
    if (url.includes("/profiles") && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(pageOf(keys)) });
    }
    if (url.includes("/profiles/") && url.endsWith("/rotate") && method === "POST") {
      const body = route.request().postDataJSON() as { name: string };
      const rotated = { ...(keys.find((item) => url.includes(item.id)) ?? key), name: body.name, key_suffix: "rot8ed", last_used_at: null };
      keys = keys.map((item) => (item.id === rotated.id ? rotated : item));
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { api_key: "dm_rotated_once", key: rotated } }) });
    }
    if (url.includes("/profiles/") && method === "PATCH") {
      const body = route.request().postDataJSON() as { name: string };
      const updated = { ...(keys.find((item) => url.endsWith(`/profiles/${item.id}`)) ?? key), name: body.name };
      keys = keys.map((item) => (item.id === updated.id ? updated : item));
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: updated }) });
    }
    if (url.endsWith("/profiles") && method === "POST") {
      const body = route.request().postDataJSON() as { label?: string; name: string; scopes: string[] };
      expect(body.label).toBeUndefined();
      expect(body.scopes).toEqual(["read"]);
      const created = { ...key, name: body.name, scopes: body.scopes };
      keys = [created, ...keys];
      return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ data: { api_key: "dm_plain_once", key: created } }) });
    }
    if (url.includes("/profiles/") && method === "DELETE") {
      keys = keys.filter((item) => !url.endsWith(`/profiles/${item.id}`));
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { status: "deleted" } }) });
    }
    if (url.includes("/teams/") && method === "PATCH") {
      const body = route.request().postDataJSON() as { name: string; description: string };
      const updated = { ...(teams.find((item) => url.endsWith(`/teams/${item.id}`)) ?? team), name: body.name, description: body.description };
      teams = teams.map((item) => (item.id === updated.id ? updated : item));
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: updated }) });
    }
    if (url.includes("/teams/") && method === "DELETE") {
      teams = [];
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { status: "deleted" } }) });
    }
    if (url.endsWith("/security/settings") && method === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: { enabled: true, failure_threshold: 10, failure_window_seconds: 600, ban_duration_seconds: 0, updated_at: "2026-05-01T12:00:00Z" } }),
      });
    }
    if (url.includes("/security/bans") && method === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(pageOf(bans)) });
    }
    if (url.endsWith("/security/bans") && method === "POST") {
      const body = route.request().postDataJSON() as { ip: string; reason: string };
      const created = { ...ban, ip: body.ip, reason: body.reason, source: "manual" as const, failure_count: 0, last_failed_at: null };
      bans = [created, ...bans];
      return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ data: created }) });
    }
    if (url.includes("/security/bans/") && method === "DELETE") {
      bans = bans.filter((item) => !url.endsWith(`/security/bans/${item.ip}`));
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { status: "deleted" } }) });
    }
    return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ message: "not found" }) });
  });
  return calls;
}

function pageOf<T>(data: T[]) {
  return { data, pagination: { limit: 20, offset: 0, total: data.length } };
}
