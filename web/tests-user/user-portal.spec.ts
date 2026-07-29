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

const evidences = [
  {
    evidence_id: "evidence-1",
    relationship_ids: ["relationship-1"],
    rank: 1,
    context: "Alice uses Dense-Mem for project-x.",
    source: "notes",
    source_type: "manual",
    created_at: "2026-05-02T12:00:00Z",
  },
  {
    evidence_id: "evidence-2",
    relationship_ids: ["relationship-2"],
    rank: 2,
    context: "Alice works on project-x with Dense-Mem.",
    source: "notes",
    source_type: "manual",
    created_at: "2026-05-03T12:00:00Z",
  },
];

const relationships = [
  {
    relationship_id: "relationship-1",
    tier: "verified",
    subject: { entity_id: "entity-alice", name: "Alice", kind: "person" },
    predicate: "uses",
    object: { entity_id: "entity-dense-mem", name: "Dense-Mem", kind: "software" },
    polarity: "+",
    valid_from: "2026-05-02T12:00:00Z",
    evidence_ids: ["evidence-1"],
    search_state: "current",
  },
  {
    relationship_id: "relationship-2",
    tier: "verified",
    subject: { entity_id: "entity-alice", name: "Alice", kind: "person" },
    predicate: "works_on",
    object: { value_id: "value-project-x", value: "project-x", type: "project" },
    polarity: "+",
    valid_from: "2026-05-03T12:00:00Z",
    evidence_ids: ["evidence-2"],
    search_state: "current",
  },
];

const graphSnapshot = {
  scope: "overview",
  depth: 1,
  limit: 80,
  truncated: false,
  nodes: [
    {
      key: "entity:entity-alice",
      id: "entity-alice",
      type: "entity",
      title: "Alice",
    },
    {
      key: "entity:entity-dense-mem",
      id: "entity-dense-mem",
      type: "entity",
      title: "Dense-Mem",
    },
    {
      key: "value:value-project-x",
      id: "value-project-x",
      type: "value",
      title: "project-x",
    },
  ],
  edges: [
    { id: "relationship-1", source: "entity:entity-alice", target: "entity:entity-dense-mem", relationship: "USES", directed: true },
    { id: "relationship-2", source: "entity:entity-alice", target: "value:value-project-x", relationship: "WORKS_ON", directed: true },
  ],
};

const graphNodeDetails = {
  "entity:entity-alice": {
    key: "entity:entity-alice",
    id: "entity-alice",
    type: "entity",
    title: "Alice",
    body: "Person",
    status: "active",
    score: 0.94,
    recorded_at: "2026-05-02T12:00:00Z",
  },
  "entity:entity-dense-mem": {
    key: "entity:entity-dense-mem",
    id: "entity-dense-mem",
    type: "entity",
    title: "Dense-Mem",
    body: "Software",
    status: "active",
    score: 0.88,
    recorded_at: "2026-05-02T12:00:00Z",
  },
  "value:value-project-x": {
    key: "value:value-project-x",
    id: "value-project-x",
    type: "value",
    title: "project-x",
    body: "project-x",
    status: "active",
    score: 0.75,
    recorded_at: "2026-05-02T12:00:00Z",
  },
};

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
  { id: "avg_http_latency", label: "Avg HTTP latency", unit: "ms", value: 16.4 },
  { id: "avg_embedding_latency", label: "Avg embedding latency", unit: "ms", value: 39.1 },
  { id: "avg_verifier_latency", label: "Avg verifier latency", unit: "ms", value: 101.5 },
  { id: "avg_conflict_review_duration", label: "Avg conflict review", unit: "ms", value: 200 },
];

const telemetrySeries = [
  { id: "http_rps", label: "HTTP requests", unit: "rps" },
  { id: "http_errors_rps", label: "HTTP errors", unit: "rps" },
  { id: "embedding_tokens", label: "Embedding tokens", unit: "tokens/s" },
  { id: "verifier_tokens", label: "Verifier tokens", unit: "tokens/s" },
  { id: "recalls", label: "Recall requests", unit: "requests/s" },
  { id: "recall_results", label: "Recall results", unit: "results" },
  { id: "llm_recall_used_rate", label: "LLM recall used", unit: "percent" },
  { id: "llm_recall_answer_supported_rate", label: "LLM answer supported", unit: "percent" },
  { id: "llm_recall_quality_score", label: "LLM recall quality", unit: "percent" },
  { id: "llm_recall_missing_context_rate", label: "LLM missing context", unit: "percent" },
  { id: "llm_recall_irrelevant_rate", label: "LLM irrelevant recall", unit: "percent" },
  { id: "conflict_review_duration", label: "Conflict review", unit: "ms" },
].map((series, index) => ({
  ...series,
  points: [
    { timestamp: "2026-05-02T12:00:00Z", value: index / 20 },
    { timestamp: "2026-05-02T13:00:00Z", value: index / 20 + 0.2 },
  ],
}));

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
  windowed_cards: telemetryCards,
  current_cards: [],
  series: telemetrySeries,
  activity_series: telemetrySeries,
  state_series: [],
};

test("API key login, recall, and read-only navigation", async ({ page }) => {
  const calls = await mockUserApi(page, { key: readKey, canRotate: false });
  await openUserPortal(page, "dm_read");

  await expect(page.getByText("Research Team")).toBeVisible();
  await expect(page.getByLabel("Current workspace")).not.toContainText("Mine");
  await expect(page.getByText("Other profile")).toBeHidden();

  await page.getByLabel("Keyword").fill("project");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page.getByRole("heading", { name: "Alice uses Dense-Mem for project-x." }).first()).toBeVisible();
  await expect(page.getByText("project-x").first()).toBeVisible();
  await expect(page.getByText("Dense-Mem").first()).toBeVisible();
  await expect(page.getByLabel("Knowledge filters")).toBeVisible();
  await expect(page.getByLabel("Inspector")).toContainText("Evidence");
  await expect(page.getByRole("listbox", { name: "Recall result list" }).getByRole("option")).toHaveCount(3);
  await page.getByRole("option").filter({ hasText: "uses: Dense-Mem" }).click();
  await expect(page.getByLabel("Inspector")).toContainText("Relationship");
  await expect(page.getByLabel("Inspector")).toContainText("Tier verified");
  await page.getByRole("checkbox", { name: /Relationship/ }).click();
  await expect(page.getByRole("listbox", { name: "Recall result list" })).not.toContainText("uses: Dense-Mem");
  await expect(page.getByLabel("Inspector")).toContainText("Evidence");
  await page.getByRole("checkbox", { name: /Evidence/ }).click();
  await expect(page.getByLabel("Inspector")).toContainText("Select a result");

  await expect(page.getByRole("button", { name: "Usage" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Evidence" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Relationships" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Communities" })).toHaveCount(0);
  expect(calls.disallowedProfileCalls).toEqual([]);
  expect(calls.telemetryRequests).toEqual([]);
});

test("graph tab renders a nonblank memory graph", async ({ page }) => {
  const calls = await mockUserApi(page, { key: readKey, canRotate: false });
  await openUserPortal(page, "dm_read");

  await page.getByRole("button", { name: "Graph" }).click();
  await expect(page.getByLabel("Knowledge graph")).toBeVisible();
  await expect(page.getByLabel("Graph inspector")).toContainText("Select a node");
  const canvas = page.locator(".graph-canvas canvas").first();
  await expect(canvas).toBeVisible();
  await expect.poll(async () => canvas.evaluate((element) => {
    const canvasElement = element as HTMLCanvasElement;
    const context = canvasElement.getContext("2d");
    if (!context || canvasElement.width === 0 || canvasElement.height === 0) {
      return 0;
    }
    const data = context.getImageData(0, 0, canvasElement.width, canvasElement.height).data;
    let painted = 0;
    for (let index = 3; index < data.length; index += 4) {
      if (data[index] > 0) {
        painted += 1;
      }
    }
    return painted;
  })).toBeGreaterThan(0);
  expect(calls.graphRequests.some((url) => url.includes("/ui/api/graph?scope=overview"))).toBe(true);
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
	await expect(page.getByLabel(`${title} current state`)).toHaveCount(0);
  const usageCharts = page.getByLabel(`${title} charts`);
  for (const series of snapshot.activity_series) {
    await expect(usageCharts).toContainText(series.label);
  }
	await expect(page.getByLabel(`${title} state history`)).toHaveCount(0);
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
    graphRequests: [] as string[],
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

  await page.route("**/ui/api/team/profiles**", async (route) => {
    if (state.canManageTeam) {
      const request = route.request();
      const url = new URL(request.url());
      if (url.pathname === "/ui/api/team/profiles" && request.method() === "GET") {
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

  await page.route("**/ui/api/team", async (route) => {
    if (!state.canManageTeam) {
      calls.disallowedProfileCalls.push(route.request().url());
      await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ message: "team management must not be called" }) });
      return;
    }

    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/ui/api/team" && request.method() === "PATCH") {
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

  await page.route("**/ui/api/node-detail**", async (route) => {
    const url = new URL(route.request().url());
    const key = `${url.searchParams.get("type")}:${url.searchParams.get("id")}`;
    const node = graphNodeDetails[key as keyof typeof graphNodeDetails] ?? graphNodeDetails["entity:entity-alice"];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: node }),
    });
  });

  await page.route("**/ui/api/graph**", async (route) => {
    calls.graphRequests.push(route.request().url());
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: graphSnapshot }),
    });
  });

  await page.route("**/ui/api/recall**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          recall_id: "rec-test",
          results: evidences,
          conflicts: [],
          related_relationships: [relationships[0]],
          related_communities: [{ relationships, evidence_ids: evidences.map((evidence) => evidence.evidence_id) }],
          related_hypotheses: [],
          search_states: { evidence: "current", relationships: "current" },
          degradations: [],
        },
      }),
    });
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
