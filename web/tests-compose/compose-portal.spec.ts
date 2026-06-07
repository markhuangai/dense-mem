import { expect, type APIRequestContext, type Page, test } from "@playwright/test";

const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const userUrl = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seedTeamId = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const seedTeamName = requiredEnv("DENSE_MEM_E2E_TEAM_NAME");
const seedApiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const prometheusUrl = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");

type CreatedProfile = {
  api_key: string;
  key: {
    id: string;
    team_id: string;
    name: string;
    key_suffix: string;
    scopes: string[];
    rate_limit: number;
  };
};

type PrometheusQueryResponse = {
  status?: string;
  data?: {
    result?: unknown[];
  };
};

type TelemetryResponse = {
  data?: {
    available?: boolean;
    cards?: unknown;
    series?: unknown;
  };
};

test("control panel logs in against compose and creates a team", async ({ page }, testInfo) => {
  await openControlPanel(page);
  await expect(page.getByRole("button", { name: new RegExp(escapeRegExp(seedTeamName)) })).toBeVisible();

  const teamName = uniqueName("Compose Team", testInfo);
  await page.getByLabel("Name").first().fill(teamName);
  await page.getByLabel("Description").first().fill("created by compose e2e");
  await page.getByRole("button", { name: /^Create$/ }).click();

  await expect(page.getByRole("heading", { name: teamName })).toBeVisible();
});

test("control panel rejects an invalid control token", async ({ page }) => {
  await page.goto(`${controlUrl}/`);

  await page.getByLabel("Control token").fill("wrong-token");
  await page.getByRole("button", { name: "Unlock" }).click();

  await expect(page.getByRole("alert")).toContainText(/invalid/i);
});

test("control panel shows operational metrics against compose", async ({ page }) => {
  await openControlPanel(page);

  await page.getByRole("button", { name: /^Metrics$/ }).click();

  await expect(page.getByRole("heading", { name: "Usage Rollup" })).toBeVisible();
  await expect(page.getByLabel("Request metrics")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Dependencies" })).toBeVisible();
  await expect(page.getByText("postgres")).toBeVisible();
  await expect(page.getByText("neo4j")).toBeVisible();

  await page.getByLabel("Window").selectOption("360");
  await page.getByLabel("Team", { exact: true }).selectOption(seedTeamId);
  await expect(page.getByRole("heading", { name: "Usage Rollup" })).toBeVisible();
  await expectNoShellOverlap(page);
});

test("control panel keeps security settings display-only against compose", async ({ page }) => {
  await openControlPanel(page);

  await page.getByRole("button", { name: /IP Bans/ }).click();

  await expect(page.getByRole("heading", { name: "IP Bans" })).toBeVisible();
  await expect(page.getByLabel("IP ban rules")).toContainText("Protection");
  await expect(page.getByLabel("IP ban rules")).toContainText("Threshold");
  await expect(page.getByRole("button", { name: /Save settings/ })).toHaveCount(0);
  await expectNoShellOverlap(page);
});

test("prometheus telemetry is scraped and rendered in control panel and user portal", async ({ page, request }) => {
  const sessionResponse = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(seedApiKey) });
  expect(sessionResponse.status()).toBe(200);

  await expect.poll(
    () => prometheusResultCount(request, `densemem_http_requests_total{route="/ui/api/session"}`),
    {
      intervals: [1_000, 5_000, 10_000],
      timeout: 120_000,
    },
  ).toBeGreaterThan(0);

  const telemetryResponse = await request.get(`${userUrl}/ui/api/telemetry?window=15m`, { headers: bearer(seedApiKey) });
  expect(telemetryResponse.status()).toBe(200);
  const telemetryBody = await telemetryResponse.json() as TelemetryResponse;
  expect(telemetryBody.data?.available).toBe(true);
  expect(Array.isArray(telemetryBody.data?.cards)).toBe(true);
  assertTelemetrySeries(telemetryBody);
  const cardLabels = telemetryLabels(telemetryBody.data?.cards);
  const seriesLabels = telemetryLabels(telemetryBody.data?.series);
  expect(cardLabels.length).toBeGreaterThan(0);
  expect(seriesLabels.length).toBeGreaterThan(0);

  await openControlPanel(page);
  await page.getByRole("button", { name: /^Metrics$/ }).click();
  await expect(page.getByRole("heading", { name: "Telemetry" })).toBeVisible();
  await expect(page.getByLabel("Telemetry totals")).toContainText("HTTP requests");
  await expect(page.getByLabel("Telemetry charts")).toContainText("HTTP requests");

  await openUserPortal(page, seedApiKey);
  await page.getByRole("button", { name: "Usage" }).click();
  for (const label of cardLabels) {
    await expect(page.getByLabel("Usage totals")).toContainText(label);
  }
  for (const label of seriesLabels) {
    await expect(page.getByLabel("Usage charts")).toContainText(label);
  }
  await expectNoShellOverlap(page);
});

test("user portal logs in with a real API key and shows only that profile", async ({ page, request }, testInfo) => {
  const otherProfile = await createTeamProfile(request, uniqueName("Other profile", testInfo), ["read"]);

  await openUserPortal(page, seedApiKey);

  await expect(page.getByText(seedTeamName)).toBeVisible();
  await expect(page.getByText("default profile")).toBeVisible();
  await expect(page.getByText(otherProfile.key.name)).toBeHidden();

  await page.getByRole("button", { name: "Facts" }).click();
  await expect(page.getByRole("heading", { name: "Facts" })).toBeVisible();
  await expect(page.getByText("No facts")).toBeVisible();

  await page.getByRole("button", { name: "Claims" }).click();
  await expect(page.getByRole("heading", { name: "Claims" })).toBeVisible();
  await expect(page.getByText("No claims")).toBeVisible();

  await page.getByRole("button", { name: "Fragments" }).click();
  await expect(page.getByRole("heading", { name: "Fragments" })).toBeVisible();
  await expect(page.getByText("No fragments")).toBeVisible();

  await page.getByRole("button", { name: "Communities" }).click();
  await expect(page.getByRole("heading", { name: "Communities" })).toBeVisible();
  await expect(page.getByText("No communities")).toBeVisible();
});

test("read-only user key cannot regenerate itself", async ({ page, request }, testInfo) => {
  const readOnly = await createTeamProfile(request, uniqueName("Read only", testInfo), ["read"]);

  await openUserPortal(page, readOnly.api_key);
  await page.getByRole("button", { name: /My key/i }).click();

  await expect(page.getByRole("button", { name: /Regenerate key/i })).toBeDisabled();
});

test("write user key regenerates itself and invalidates the old key", async ({ page, request }, testInfo) => {
  const writable = await createTeamProfile(request, uniqueName("Writable", testInfo), ["read", "write"]);

  await openUserPortal(page, writable.api_key);
  await page.getByRole("button", { name: /My key/i }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Regenerate key/i }).click();

  const rotatedKey = page.getByLabel("Knowledge details").locator(".secret-box code").first();
  await expect(rotatedKey).toHaveText(/^dm_/);
  const newApiKey = (await rotatedKey.textContent()) ?? "";
  expect(newApiKey).not.toBe(writable.api_key);
  await expect
    .poll(() => page.evaluate(() => sessionStorage.getItem("denseMem.userApiKey")))
    .toBe(newApiKey);

  const oldSession = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(writable.api_key) });
  expect(oldSession.status()).toBe(401);

  const newSession = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(newApiKey) });
  expect(newSession.status()).toBe(200);
});

test("user self-rotate endpoint rejects profile edits", async ({ request }, testInfo) => {
  const writable = await createTeamProfile(request, uniqueName("No edit", testInfo), ["read", "write"]);

  const response = await request.post(`${userUrl}/ui/api/key/rotate`, {
    headers: bearer(writable.api_key),
    data: { name: "Renamed from user portal" },
  });

  expect(response.status()).toBe(422);
  const body = await response.json() as { message?: string };
  expect(body.message).toContain("does not accept editable fields");

  const session = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(writable.api_key) });
  expect(session.status()).toBe(200);
});

test("user portal shows an invalid API key error", async ({ page }) => {
  await page.goto(`${userUrl}/ui/`);

  await page.getByLabel("API key").fill("wrong-key");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByRole("alert")).toContainText(/invalid|auth/i);
});

async function openControlPanel(page: Page) {
  await page.goto(`${controlUrl}/`);
  await page.getByLabel("Control token").fill(controlToken);
  await page.getByRole("button", { name: "Unlock" }).click();
  await expect(page.getByRole("heading", { name: "Teams" })).toBeVisible();
}

async function openUserPortal(page: Page, apiKey: string) {
  await page.goto(`${userUrl}/ui/`);
  await page.getByLabel("API key").fill(apiKey);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Dense-Mem Knowledge" })).toBeVisible();
}

async function createTeamProfile(request: APIRequestContext, name: string, scopes: string[]): Promise<CreatedProfile> {
  const response = await request.post(`${controlUrl}/control/api/teams/${seedTeamId}/profiles`, {
    headers: bearer(controlToken),
    data: { name, scopes, rate_limit: 300 },
  });
  if (response.status() !== 201) {
    throw new Error(`create profile failed: ${response.status()} ${await response.text()}`);
  }
  const payload = await response.json() as { data: CreatedProfile };
  return payload.data;
}

async function prometheusResultCount(request: APIRequestContext, query: string) {
  const response = await request.get(`${prometheusUrl}/api/v1/query`, { params: { query } });
  if (response.status() !== 200) {
    return 0;
  }
  const body = await response.json() as PrometheusQueryResponse;
  if (body.status !== "success" || !Array.isArray(body.data?.result)) {
    return 0;
  }
  return body.data.result.length;
}

function assertTelemetrySeries(body: TelemetryResponse) {
  const series = body.data?.series;
  expect(Array.isArray(series)).toBe(true);
  if (!Array.isArray(series)) {
    throw new Error("telemetry series must be an array");
  }
  expect(series.length).toBeGreaterThan(0);
  for (const item of series) {
    expect(isRecord(item)).toBe(true);
    if (!isRecord(item)) {
      throw new Error("telemetry series item must be an object");
    }
    expect(Array.isArray(item.points)).toBe(true);
  }
}

function telemetryLabels(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => (isRecord(item) && typeof item.label === "string" ? item.label : ""))
    .filter(Boolean);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
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

function bearer(token: string) {
  return {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
  };
}

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required for compose e2e`);
  }
  return value;
}

function uniqueName(prefix: string, testInfo: { project: { name: string }; workerIndex: number }) {
  const suffix = Math.random().toString(36).slice(2, 8);
  return `${prefix} ${testInfo.project.name} ${testInfo.workerIndex} ${suffix}`;
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
