import { expect, Page, test } from "@playwright/test";

const team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Research Team",
  description: "",
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

type TestKey = {
  id: string;
  team_id: string;
  name: string;
  key_suffix: string;
  scopes: string[];
  role: "manager" | "member";
  rate_limit: number;
  last_used_at: string | null;
  expires_at: string | null;
  created_at: string;
};

const readKey: TestKey = {
  id: "22222222-2222-4222-8222-222222222222",
  team_id: team.id,
  name: "Mine",
  key_suffix: "abc123",
  scopes: ["read"],
  role: "member",
  rate_limit: 120,
  last_used_at: null,
  expires_at: null,
  created_at: "2026-05-01T12:00:00Z",
};

const writeKey: TestKey = {
  ...readKey,
  scopes: ["read", "write"],
};

const managerKey: TestKey = {
  ...writeKey,
  id: "33333333-3333-4333-8333-333333333333",
  name: "Manager",
  key_suffix: "mgr123",
  role: "manager",
};

const memberProfile: TestKey = {
  ...writeKey,
  id: "44444444-4444-4444-8444-444444444444",
  name: "Member profile",
  key_suffix: "p6ZAc",
  role: "member",
};

const facts = [
  {
    fact_id: "fact-1",
    subject: "Alice",
    predicate: "works_on",
    object: "project-x",
    status: "active",
    truth_score: 0.94,
    recorded_at: "2026-05-02T12:00:00Z",
  },
];

const claims = [
  {
    claim_id: "claim-1",
    subject: "Alice",
    predicate: "uses",
    object: "Dense-Mem",
    modality: "assertion",
    polarity: "+",
    status: "validated",
    entailment_verdict: "entailed",
    extract_conf: 0.91,
    resolution_conf: 0.88,
    recorded_at: "2026-05-02T12:00:00Z",
  },
];

const fragments = [
  {
    id: "frag-1",
    fragment_id: "frag-1",
    content: "Alice is working on project-x with Dense-Mem.",
    source_type: "manual",
    source: "notes",
    labels: ["project"],
    status: "active",
    created_at: "2026-05-02T12:00:00Z",
    updated_at: "2026-05-02T12:00:00Z",
  },
];

const communities = [
  {
    community_id: "community-1",
    level: 0,
    summary: "Project work around Dense-Mem.",
    member_count: 3,
    top_entities: ["Alice", "project-x", "Dense-Mem"],
    top_predicates: ["works_on", "uses"],
    last_summarized_at: "2026-05-02T12:00:00Z",
  },
];

const telemetryCards = [
  { id: "http_requests", label: "HTTP requests", unit: "requests", value: 9 },
  { id: "http_errors", label: "HTTP errors", unit: "requests", value: 1 },
  { id: "verifier_tokens", label: "Verifier tokens", unit: "tokens", value: 120 },
  { id: "embedding_tokens", label: "Embedding tokens", unit: "tokens", value: 320 },
  { id: "recalls", label: "Recall requests", unit: "requests", value: 3 },
  { id: "avg_recall_results", label: "Avg recall results", unit: "results", value: 2.5 },
  { id: "llm_recall_used_rate", label: "LLM recall used", unit: "percent", value: 80 },
  { id: "llm_recall_answer_supported_rate", label: "LLM answer supported", unit: "percent", value: 70 },
  { id: "llm_recall_quality_score", label: "LLM recall quality", unit: "percent", value: 75 },
  { id: "llm_recall_missing_context_rate", label: "LLM missing context", unit: "percent", value: 10 },
  { id: "llm_recall_irrelevant_rate", label: "LLM irrelevant recall", unit: "percent", value: 5 },
  { id: "promotions", label: "Promotions", unit: "promotions", value: 1 },
  { id: "promotion_rate", label: "Promotion rate", unit: "percent", value: 50 },
  { id: "avg_http_latency", label: "Avg HTTP latency", unit: "ms", value: 16.4 },
  { id: "avg_embedding_latency", label: "Avg embedding latency", unit: "ms", value: 39.1 },
  { id: "avg_verifier_latency", label: "Avg verifier latency", unit: "ms", value: 101.5 },
  { id: "avg_claim_verify_latency", label: "Avg claim-to-verify", unit: "ms", value: 200 },
  { id: "avg_claim_promotion_latency", label: "Avg claim-to-promote", unit: "ms", value: 300 },
  { id: "avg_verify_promotion_latency", label: "Avg verify-to-promote", unit: "ms", value: 90 },
  { id: "pending_claims", label: "Pending claims", unit: "claims", value: 4 },
  { id: "validated_claims", label: "Validated claims", unit: "claims", value: 8 },
  { id: "disputed_claims", label: "Disputed claims", unit: "claims", value: 0 },
  { id: "revalidation_backlog", label: "Revalidation backlog", unit: "facts", value: 2 },
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
    { timestamp: "2026-05-02T12:00:00Z", value: index / 20 },
    { timestamp: "2026-05-02T13:00:00Z", value: index / 20 + 0.2 },
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
  scope: { type: "self", team_id: team.id, profile_id: readKey.id },
  cards: telemetryCards,
  windowed_cards: telemetryCards.filter((card) => !currentTelemetryIds.has(card.id)),
  current_cards: telemetryCards.filter((card) => currentTelemetryIds.has(card.id)),
  series: telemetrySeries,
  activity_series: telemetrySeries.filter((series) => !currentTelemetryIds.has(series.id)),
  state_series: telemetrySeries.filter((series) => currentTelemetryIds.has(series.id)),
};

test("API key login, recall, and read-only knowledge tabs", async ({ page }) => {
  const calls = await mockUserApi(page, { key: readKey, canRotate: false });
  await openUserPortal(page, "dm_read");

  await expect(page.getByText("Research Team")).toBeVisible();
  await expect(page.getByLabel("Current workspace")).not.toContainText("Mine");
  await expect(page.getByText("Other profile")).toBeHidden();

  await page.getByLabel("Keyword").fill("project");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page.getByRole("heading", { name: "Alice" }).first()).toBeVisible();
  await expect(page.getByText("project-x").first()).toBeVisible();
  await expect(page.getByText("Dense-Mem").first()).toBeVisible();
  await expect(page.getByLabel("Knowledge filters")).toBeVisible();
  await expect(page.getByLabel("Inspector")).toContainText("Fact");
  await expect(page.getByRole("listbox", { name: "Recall result list" }).getByRole("option")).toHaveCount(3);
  await page.getByRole("option").filter({ hasText: "uses: Dense-Mem" }).click();
  await expect(page.getByLabel("Inspector")).toContainText("Claim");
  await expect(page.getByLabel("Inspector")).toContainText("Tier 1.5");
  await page.getByRole("checkbox", { name: /Claim/ }).click();
  await expect(page.getByRole("listbox", { name: "Recall result list" })).not.toContainText("uses: Dense-Mem");
  await expect(page.getByLabel("Inspector")).toContainText("Fact");
  await page.getByRole("checkbox", { name: /Fact/ }).click();
  await expect(page.getByLabel("Inspector")).toContainText("Fragment");
  await expect(page.getByLabel("Inspector")).toContainText("Alice is working on project-x with Dense-Mem.");

  await expect(page.getByRole("button", { name: "Usage" })).toHaveCount(0);

  await page.getByRole("button", { name: "Facts" }).click();
  await expect(page.getByText("works_on: project-x")).toBeVisible();

  await page.getByRole("button", { name: "Claims" }).click();
  await expect(page.getByText("uses: Dense-Mem")).toBeVisible();

  await page.getByRole("button", { name: "Fragments" }).click();
  await expect(page.getByText("Alice is working on project-x with Dense-Mem.")).toBeVisible();

  await page.getByRole("button", { name: "Communities" }).click();
  await expect(page.getByText("Project work around Dense-Mem.")).toBeVisible();
  expect(calls.disallowedProfileCalls).toEqual([]);
  expect(calls.telemetryRequests).toEqual([]);
});

test("read-only key cannot regenerate itself", async ({ page }) => {
  await mockUserApi(page, { key: readKey, canRotate: false });
  await openUserPortal(page, "dm_read");

  await page.getByRole("button", { name: /My key/i }).click();
  await expect(page.getByRole("button", { name: /Regenerate key/i })).toBeDisabled();
});

test("write key regenerates only through the self-rotate endpoint", async ({ page }) => {
  const calls = await mockUserApi(page, { key: writeKey, canRotate: true });
  await openUserPortal(page, "dm_write");

  await page.getByRole("button", { name: /My key/i }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Regenerate key/i }).click();

  const details = page.getByLabel("Knowledge details");
  await expect(details.getByLabel("Generated API key")).toHaveValue("dm_new_plaintext");
  await expect(details.getByText("******new123")).toBeVisible();
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem("denseMem.userApiKey"))).toBe("dm_new_plaintext");
  expect(calls.rotateBodies).toEqual(["{}"]);
  expect(calls.disallowedProfileCalls).toEqual([]);
});

test("write member key shows own usage telemetry", async ({ page }) => {
  const calls = await mockUserApi(page, { key: writeKey, canRotate: true });
  await openUserPortal(page, "dm_write");

  await page.getByRole("button", { name: "Usage" }).click();
  await expectUsageDashboard(page, "My key usage", telemetry);
  expect(calls.telemetryRequests.length).toBeGreaterThan(0);
  expect(calls.telemetryRequests.every((url) => url.includes("/ui/api/telemetry?window=1h"))).toBe(true);
  expect(calls.telemetryRequests.every((url) => !url.includes("scope="))).toBe(true);
  expect(calls.disallowedProfileCalls).toEqual([]);
});

test("manager key shows team usage telemetry", async ({ page }) => {
  const calls = await mockUserApi(page, {
    key: managerKey,
    canManageTeam: true,
    canRotate: true,
    profiles: [managerKey, memberProfile],
  });
  await openUserPortal(page, "dm_manager");

  await page.getByRole("button", { name: "Usage" }).click();
  await expectUsageDashboard(page, "Team usage", telemetry);
  expect(calls.telemetryRequests.length).toBeGreaterThan(0);
  expect(calls.telemetryRequests.every((url) => url.includes("/ui/api/telemetry?window=1h"))).toBe(true);
  expect(calls.telemetryRequests.every((url) => !url.includes("scope="))).toBe(true);
});

test("invalid API key shows login error", async ({ page }) => {
  await page.route("**/ui/api/session", async (route) => {
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ message: "invalid api key" }),
    });
  });
  await page.goto("/ui/");

  await page.getByLabel("API key").fill("wrong");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByRole("alert")).toContainText("invalid api key");
});

test("responsive user portal layout", async ({ page }) => {
  await mockUserApi(page, { key: readKey, canRotate: false });
  await openUserPortal(page, "dm_read");

  await expect(page.getByRole("heading", { name: "Knowledge" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Recall" })).toBeVisible();
  const shellMinHeight = await page.locator(".app-shell").evaluate((element) => Number.parseFloat(getComputedStyle(element).minHeight));
  expect(shellMinHeight).toBeGreaterThanOrEqual((page.viewportSize()?.height ?? 0) - 1);
  await expect(page.locator(".top-nav-bar")).toBeVisible();
  await expect(page.locator(".primary-rail")).toHaveCount(0);
  await expect(page.locator(".resource-rail")).toHaveCount(0);
  await expect(page.locator(".knowledge-results-panel")).toHaveCSS("border-radius", "0px");
  await expectNoShellOverlap(page);

  if ((page.viewportSize()?.width ?? 1000) < 700) {
    await expect(page.locator(".workspace")).toHaveCSS("grid-template-columns", /[0-9.]+px/);
  }
});

test("manager team page separates team editing from profile management", async ({ page }) => {
  await mockUserApi(page, {
    key: managerKey,
    canManageTeam: true,
    canRotate: true,
    profiles: [managerKey, memberProfile],
  });
  await openUserPortal(page, "dm_manager");

  await page.getByRole("button", { name: /^Team$/ }).click();
  const surface = page.locator(".team-management-surface");
  await expect(surface.getByRole("heading", { name: "Team" })).toBeVisible();
  await expect(surface.getByRole("heading", { name: "Profiles" })).toBeVisible();

  const spacing = await surface.evaluate((element) => {
    const sections = Array.from(element.querySelectorAll(".surface-section"));
    if (sections.length < 2) {
      throw new Error("team management sections are missing");
    }
    const first = sections[0].getBoundingClientRect();
    const second = sections[1].getBoundingClientRect();
    const secondStyle = getComputedStyle(sections[1]);
    return {
      borderTopWidth: Number.parseFloat(secondStyle.borderTopWidth),
      marginGap: second.top - first.bottom,
      paddingTop: Number.parseFloat(secondStyle.paddingTop),
    };
  });
  expect(spacing.marginGap).toBeGreaterThanOrEqual(18);
  expect(spacing.paddingTop).toBeGreaterThanOrEqual(18);
  expect(spacing.borderTopWidth).toBeGreaterThanOrEqual(1);
  await expectNoShellOverlap(page);
});

async function openUserPortal(page: Page, apiKey: string) {
  await page.goto("/ui/");
  await page.getByLabel("API key").fill(apiKey);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByText("Research Team")).toBeVisible();
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
  }));
  expect(overflow.documentWidth).toBeLessThanOrEqual(overflow.viewportWidth + 1);
  for (const tableWrap of overflow.tableWraps) {
    expect(tableWrap.scrollWidth, `${tableWrap.className} overflowed horizontally`).toBeLessThanOrEqual(tableWrap.clientWidth + 1);
  }

  const boxes = await page.locator(".topbar, .primary-rail, .top-nav-bar, .resource-rail, .detail-pane").evaluateAll((elements) => (
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

async function expectUsageDashboard(page: Page, title: string, snapshot: typeof telemetry) {
  const usageTotals = page.getByLabel(`${title} totals`);
  for (const card of snapshot.windowed_cards) {
    await expect(usageTotals).toContainText(card.label);
  }
  const usageCurrentState = page.getByLabel(`${title} current state`);
  for (const card of snapshot.current_cards) {
    await expect(usageCurrentState).toContainText(card.label);
  }
  const usageCharts = page.getByLabel(`${title} charts`);
  for (const series of snapshot.activity_series) {
    await expect(usageCharts).toContainText(series.label);
  }
  const usageStateHistory = page.getByLabel(`${title} state history`);
  for (const series of snapshot.state_series) {
    await expect(usageStateHistory).toContainText(series.label);
  }
}

function rectanglesOverlap(
  first: { left: number; top: number; right: number; bottom: number },
  second: { left: number; top: number; right: number; bottom: number },
) {
  return first.left < second.right && first.right > second.left && first.top < second.bottom && first.bottom > second.top;
}

async function mockUserApi(
  page: Page,
  state: { key: TestKey; canRotate: boolean; canManageTeam?: boolean; profiles?: TestKey[] },
) {
  const calls = {
    rotateBodies: [] as string[],
    disallowedProfileCalls: [] as string[],
    telemetryRequests: [] as string[],
  };
  let currentTeam = { ...team };
  let currentKey = { ...state.key };
  let currentCanRotate = state.canRotate;
  let currentProfiles = (state.profiles ?? []).map((profile) => ({ ...profile }));

  await page.route("**/ui/api/sso/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });

  await page.route("**/api/v1/teams/**/profiles**", async (route) => {
    if (state.canManageTeam) {
      const request = route.request();
      const url = new URL(request.url());
      if (url.pathname === `/api/v1/teams/${currentTeam.id}/profiles` && request.method() === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            data: currentProfiles,
            pagination: { limit: 50, offset: 0, total: currentProfiles.length },
          }),
        });
        return;
      }
    }
    calls.disallowedProfileCalls.push(route.request().url());
    await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ message: "profile list must not be called" }) });
  });

  await page.route("**/api/v1/teams/*", async (route) => {
    if (!state.canManageTeam) {
      calls.disallowedProfileCalls.push(route.request().url());
      await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ message: "team management must not be called" }) });
      return;
    }

    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === `/api/v1/teams/${currentTeam.id}` && request.method() === "PATCH") {
      const body = JSON.parse(request.postData() ?? "{}") as Partial<typeof team>;
      currentTeam = {
        ...currentTeam,
        name: body.name ?? currentTeam.name,
        description: body.description ?? currentTeam.description,
      };
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: currentTeam }) });
      return;
    }

    await route.fallback();
  });

  await page.route("**/ui/api/session", async (route) => {
    const authorization = route.request().headers().authorization ?? "";
    if (!authorization.startsWith("Bearer ")) {
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ message: "authentication required" }),
      });
      return;
    }

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          team: currentTeam,
          key: currentKey,
          can_rotate: currentCanRotate,
          can_manage_team: state.canManageTeam ?? false,
        },
      }),
    });
  });

  await page.route("**/ui/api/key/rotate", async (route) => {
    calls.rotateBodies.push(route.request().postData() ?? "");
    currentKey = { ...currentKey, key_suffix: "new123", last_used_at: null };
    currentCanRotate = currentKey.scopes.includes("write");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { api_key: "dm_new_plaintext", key: currentKey } }),
    });
  });

  await page.route("**/ui/api/telemetry**", async (route) => {
    calls.telemetryRequests.push(route.request().url());
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: telemetryForKey(currentKey) }),
    });
  });

  await page.route("**/api/v1/recall**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          { tier: "1", score: 0.94, fact: facts[0], semantic_rank: 0, keyword_rank: 0, final_score: 0 },
          { tier: "1.5", score: 0.45, claim: claims[0], semantic_rank: 0, keyword_rank: 0, final_score: 0 },
          { tier: "2", score: 0.02, fragment: fragments[0], semantic_rank: 1, keyword_rank: 2, final_score: 0.02 },
        ],
      }),
    });
  });

  await page.route("**/api/v1/facts**", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: facts, has_more: false }) });
  });

  await page.route("**/api/v1/claims**", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: claims, has_more: false }) });
  });

  await page.route("**/api/v1/fragments**", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: fragments, has_more: false }) });
  });

  await page.route("**/api/v1/communities**", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: communities, total: communities.length }) });
  });

  return calls;
}

function telemetryForKey(key: TestKey) {
  return {
    ...telemetry,
    scope: key.role === "manager"
      ? { type: "team", team_id: team.id }
      : { type: "self", team_id: team.id, profile_id: key.id },
  };
}
