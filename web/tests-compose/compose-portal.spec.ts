import { expect, type APIRequestContext, type Page, test } from "@playwright/test";

const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const userUrl = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seedTeamId = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const seedTeamName = requiredEnv("DENSE_MEM_E2E_TEAM_NAME");
const seedApiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");

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
