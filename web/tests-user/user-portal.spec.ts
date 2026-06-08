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
  series: telemetrySeries,
};

test("API key login, recall, and read-only knowledge tabs", async ({ page }) => {
  const calls = await mockUserApi(page, { key: readKey, canRotate: false });
  await openUserPortal(page, "dm_read");

  await expect(page.getByText("Research Team")).toBeVisible();
  await expect(page.getByText("Mine")).toBeVisible();
  await expect(page.getByText("Other profile")).toBeHidden();

  await page.getByLabel("Keyword").fill("project");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page.getByRole("heading", { name: "Alice" }).first()).toBeVisible();
  await expect(page.getByText("project-x").first()).toBeVisible();
  await expect(page.getByText("Dense-Mem").first()).toBeVisible();

  await page.getByRole("button", { name: "Usage" }).click();
  const usageTotals = page.getByLabel("Usage totals");
  for (const card of telemetryCards) {
    await expect(usageTotals).toContainText(card.label);
  }
  const usageCharts = page.getByLabel("Usage charts");
  for (const series of telemetrySeries) {
    await expect(usageCharts).toContainText(series.label);
  }

  await page.getByRole("button", { name: "Facts" }).click();
  await expect(page.getByText("works_on: project-x")).toBeVisible();

  await page.getByRole("button", { name: "Claims" }).click();
  await expect(page.getByText("uses: Dense-Mem")).toBeVisible();

  await page.getByRole("button", { name: "Fragments" }).click();
  await expect(page.getByText("Alice is working on project-x with Dense-Mem.")).toBeVisible();

  await page.getByRole("button", { name: "Communities" }).click();
  await expect(page.getByText("Project work around Dense-Mem.")).toBeVisible();
  expect(calls.disallowedProfileCalls).toEqual([]);
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
  await expect(details.getByText("dm_new_plaintext")).toBeVisible();
  await expect(details.getByText("******new123")).toBeVisible();
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem("denseMem.userApiKey"))).toBe("dm_new_plaintext");
  expect(calls.rotateBodies).toEqual(["{}"]);
  expect(calls.disallowedProfileCalls).toEqual([]);
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

  await expect(page.getByRole("heading", { name: "Dense-Mem Knowledge" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Recall" })).toBeVisible();
  const shellMinHeight = await page.locator(".app-shell").evaluate((element) => Number.parseFloat(getComputedStyle(element).minHeight));
  expect(shellMinHeight).toBeGreaterThanOrEqual((page.viewportSize()?.height ?? 0) - 1);
  await expect(page.locator(".control-sidebar")).toHaveCSS("border-radius", "8px");
  await expect(page.locator(".surface").first()).toHaveCSS("border-radius", "8px");
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
  const boxes = await page.locator(".topbar, .control-sidebar, .detail-pane").evaluateAll((elements) => (
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

async function mockUserApi(
  page: Page,
  state: { key: TestKey; canRotate: boolean; canManageTeam?: boolean; profiles?: TestKey[] },
) {
  const calls = {
    rotateBodies: [] as string[],
    disallowedProfileCalls: [] as string[],
  };
  let currentTeam = { ...team };
  let currentKey = { ...state.key };
  let currentCanRotate = state.canRotate;
  let currentProfiles = (state.profiles ?? []).map((profile) => ({ ...profile }));

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
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: telemetry }),
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
