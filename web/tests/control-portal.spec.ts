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

type TestProfile = typeof team;

test("team creation flow", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [] });
  await openPortal(page);

  await page.getByLabel("Name").first().fill("Work Team");
  await page.getByLabel("Description").first().fill("for work");
  await page.getByRole("button", { name: /^Create$/ }).click();

  await expect(page.getByRole("heading", { name: "Work Team" })).toBeVisible();
});

test("API key creation shows plaintext once", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [] });
  await openPortal(page);

  await page.getByRole("button", { name: /Profiles & API Keys/ }).click();
  await page.getByRole("button", { name: "Create profile" }).click();

  await expect(page.getByText("dm_plain_once")).toBeVisible();
  await page.getByRole("button", { name: "Dismiss API key" }).click();
  await expect(page.getByText("dm_plain_once")).toBeHidden();
});

test("team and profile names update and profile key regenerates", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.locator("#team-name").fill("Renamed Team");
  await page.getByRole("button", { name: /^Save$/ }).click();
  await expect(page.getByRole("heading", { name: "Renamed Team" })).toBeVisible();

  await page.getByRole("button", { name: /Profiles & API Keys/ }).click();
  await page.getByLabel("Profile name default profile").fill("Research profile");
  await page.getByRole("button", { name: /Save profile default profile/ }).click();
  await expect(page.getByLabel("Profile name Research profile")).toHaveValue("Research profile");

  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Regenerate key for profile Research profile/ }).click();
  await expect(page.getByText("dm_rotated_once")).toBeVisible();
});

test("team profile list and delete flow", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  await page.getByRole("button", { name: /Profiles & API Keys/ }).click();
  await expect(page.getByText("******abc123")).toBeVisible();
  const keyRow = page.getByRole("row", { name: /abc123/ });
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

test("team delete flow", async ({ page }) => {
  await mockApi(page, { teams: [team], keys: [key] });
  await openPortal(page);

  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /^Delete$/ }).click();

  await expect(page.getByLabel("Team details").getByText("No teams")).toBeVisible();
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
  await page.getByRole("button", { name: /Profiles & API Keys/ }).click();
  await expect(page.getByRole("heading", { name: "Profiles" })).toBeVisible();

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

async function mockApi(page: Page, state: { teams: TestProfile[]; keys: TestKey[]; bans?: TestBan[] }) {
  let teams = [...state.teams];
  let keys = [...state.keys];
  let bans = [...(state.bans ?? [])];
  await page.route("**/control/api/**", async (route) => {
    const url = route.request().url();
    const method = route.request().method();

    if (url.endsWith("/session")) {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { authenticated: true } }) });
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
      const body = route.request().postDataJSON() as { label?: string; name: string };
      expect(body.label).toBeUndefined();
      const created = { ...key, name: body.name };
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
}

function pageOf<T>(data: T[]) {
  return { data, pagination: { limit: 20, offset: 0, total: data.length } };
}
