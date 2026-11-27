import { randomUUID } from "node:crypto";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import type { TelemetrySnapshot } from "../src/telemetry/types";
import { expectPageContained, expectPortalAccessible, navigate } from "../tests-support/portal";

const controlURL = process.env.DENSE_MEM_CONTROL_URL!.replace(/\/$/, "");
const userURL = process.env.DENSE_MEM_USER_URL!.replace(/\/$/, "");
const controlToken = process.env.DENSE_MEM_CONTROL_TOKEN!;
const seedTeamID = process.env.DENSE_MEM_E2E_TEAM_ID!;
const seedTeam = process.env.DENSE_MEM_E2E_TEAM_NAME!;
const apiKey = process.env.DENSE_MEM_E2E_API_KEY!;

for (const theme of ["Light", "Dark"] as const) {
  test(`Obsidian portals are accessible and keyboard operable in ${theme}`, async ({ page, request }, testInfo) => {
    test.setTimeout(180_000);
    const mobile = testInfo.project.name === "mobile";
    await page.setViewportSize(mobile ? { width: 390, height: 844 } : { width: 1440, height: 1000 });

    await page.goto(`${controlURL}/`);
    await expect(page.getByRole("heading", { name: "Sign in to Control" })).toBeVisible();
    await pickTheme(page, theme);
    await checkAccess(page, "Control sign-in");
    await page.getByLabel("Control token").fill(controlToken);
    await page.getByRole("button", { name: "Unlock", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Teams", exact: true })).toBeVisible();
    await expect(page.locator(".topbar")).toHaveCSS("border-bottom-width", "1px");
    const teamCard = page.locator(".team-directory-card").filter({ hasText: seedTeam });
    await expect(teamCard).toBeVisible();
    await checkAccess(page, "All teams");

    if (!mobile) {
      await expect(page.locator(".primary-rail")).toHaveCSS("border-right-width", "1px");
      await expect(page.locator(".primary-rail")).toHaveCSS("width", "232px");
      await page.getByRole("button", { name: "Collapse navigation" }).click();
      const expand = page.getByRole("button", { name: "Expand navigation" });
      await expect(expand).toHaveAttribute("aria-expanded", "false");
      await expect(page.locator(".primary-rail")).toHaveCSS("width", "58px");
      const workspace = page.getByRole("button", { name: "Choose team", exact: true });
      await expect(workspace).toHaveCSS("justify-content", "center");
      await expect(workspace).toHaveCSS("padding-left", "0px");
      const logsLink = page.getByRole("link", { name: "Logs", exact: true });
      await expect(logsLink).not.toHaveAttribute("title");
      await logsLink.focus();
      await expect(page.getByRole("tooltip", { name: "Logs", exact: true })).toHaveCount(1);
      await expand.click();
    } else {
      await expect(page.getByRole("button", { name: "Command", exact: true })).toHaveCSS("padding-left", "0px");
      await expect(page.getByRole("button", { name: "Sign out", exact: true })).toHaveCSS("padding-left", "0px");
    }

    await waitForTeamSamples(request);
    await teamCard.click();
    const telemetry = page.getByRole("region", { name: "Team telemetry", exact: true });
    const plot = telemetry.getByRole("group", { name: "HTTP requests samples" });
    await expect(plot).toBeVisible();
    await expect(page.getByLabel("Top signals", { exact: true }).locator(".metric-row")).toHaveCount(4);
    await plot.focus();
    await plot.press("Home");
    await expect(telemetry.locator("[aria-live=polite]")).toContainText("rps");
    await plot.press("End");
    const rangeResponse = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === "/control/api/telemetry" && url.searchParams.get("window") === "15m"
        && url.searchParams.get("scope") === "team" && url.searchParams.get("team_id") === seedTeamID;
    });
    await telemetry.getByRole("button", { name: "15m", exact: true }).click();
    expect((await rangeResponse).status()).toBe(200);
    await expect(telemetry.getByRole("button", { name: "15m", exact: true })).toHaveAttribute("aria-pressed", "true");
    await expect(plot).toBeVisible();
    await checkAccess(page, "Team Overview with telemetry");

    const heights = [await headerHeight(page)];
    for (const tab of ["Credentials", "Team settings"]) {
      await navigate(page, tab);
      await expect(page.locator(".mobile-navigation")).toBeHidden();
      heights.push(await headerHeight(page));
    }
    expect(Math.max(...heights), "team header height").toBeLessThanOrEqual(125);
    expect(Math.max(...heights) - Math.min(...heights), "team header height variation").toBeLessThanOrEqual(8);

    const teamName = page.getByLabel("Name", { exact: true });
    await teamName.fill(`${seedTeam} unsaved`);
    const command = page.getByRole("button", { name: "Command", exact: true });
    await page.keyboard.press("Control+k");
    const palette = page.getByRole("dialog", { name: "Command palette" });
    const input = palette.getByRole("combobox");
    await expect(input).toBeFocused();
    await checkAccess(page, "Command palette");
    await page.keyboard.press("Escape");
    await expect(command).toBeFocused();
    await page.keyboard.press("Meta+k");
    await input.fill("Logs");
    await input.press("Enter");
    const confirmation = page.getByRole("alertdialog", { name: "Discard unsaved changes?" });
    await expect(confirmation.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
    await expect(confirmation.getByRole("button", { name: "Discard changes", exact: true })).toHaveCSS("background-color", "rgb(180, 35, 46)");
    await checkAccess(page, "Destructive confirmation");
    await page.keyboard.press("Control+k");
    await expect(palette).toBeHidden();
    await confirmation.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(teamName).toHaveValue(`${seedTeam} unsaved`);
    await teamName.fill(seedTeam);
    await expect(confirmation).toBeHidden();
    await page.keyboard.press("Control+k");
    await input.fill("Logs");
    await input.press("Enter");
    await expect(page.getByRole("heading", { name: "Operation Logs", exact: true })).toBeVisible();
    await expect(page.locator("#portal-content")).toBeFocused();
    await expect(page.locator(".filter-bar > label").first()).toHaveCSS("display", "grid");
    await checkAccess(page, "Operation Logs");

    await navigate(page, "Settings");
    await expect(page.locator(".mobile-navigation")).toBeHidden();
    const settingsTabs = page.getByRole("tablist", { name: "Settings sections", exact: true });
    await expect(settingsTabs).toBeVisible();
    const tabLayout = await settingsTabs.evaluate((element) => ({ inCard: Boolean(element.closest(".surface")), height: element.getBoundingClientRect().height }));
    expect(tabLayout.inCard).toBe(false);
    expect(tabLayout.height).toBeLessThanOrEqual(mobile ? 82 : 42);
    const timezone = page.getByLabel("Timezone", { exact: true });
    const currentTimezone = await timezone.inputValue();
    await timezone.selectOption(currentTimezone === "UTC" ? "Local" : "UTC");
    const reset = page.getByRole("button", { name: "Clear Timezone override", exact: true });
    const timezoneBox = await timezone.boundingBox();
    const resetBox = await reset.boundingBox();
    expect(Math.abs(timezoneBox!.y - resetBox!.y)).toBeLessThanOrEqual(1);
    await checkAccess(page, "Settings");

    const searchTerm = await rememberEvidence(request);
    await page.goto(`${userURL}/ui/`);
    await pickTheme(page, theme);
    await page.getByLabel("API key", { exact: true }).fill(apiKey);
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await page.getByLabel("Keyword", { exact: true }).fill(searchTerm);
    await page.getByRole("button", { name: "Search", exact: true }).click();
    const result = page.getByRole("option").filter({ hasText: searchTerm });
    await expect(result).toBeVisible();
    await expect(page.locator(".knowledge-results-panel")).toHaveCSS("border-radius", "6px");
    await checkAccess(page, "Knowledge Search");
    await result.press("Enter");
    await expect(page.getByRole("heading", { name: "Evidence details", exact: true })).toBeVisible();
    await checkAccess(page, "Knowledge Evidence Inspector");
    await page.getByRole("button", { name: "Close details panel", exact: true }).click();
    await expect(page.locator(".contextual-sheet")).toBeHidden();
    await page.getByRole("button", { name: "Connect", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Connect to Dense-Mem", exact: true })).toBeVisible();
    await checkAccess(page, "Knowledge Connect");
  });
}

async function pickTheme(page: Page, theme: "Light" | "Dark") {
  const trigger = page.getByRole("button", { name: "Choose theme", exact: true });
  await trigger.click();
  await page.getByRole("menuitem", { name: theme, exact: true }).click();
  await expect(page.locator("main")).toHaveAttribute("data-theme", theme.toLowerCase());
  await expect(page.locator('[data-slot="dropdown-menu-content"]')).toBeHidden();
  await expect(trigger).toBeFocused();
}

async function checkAccess(page: Page, view: string) {
  await expectPortalAccessible(page, view);
  await expectPageContained(page);
}

async function headerHeight(page: Page) {
  const header = page.locator(".team-workspace-header");
  await expect(header).toBeVisible();
  return header.evaluate((element) => element.getBoundingClientRect().height);
}

async function waitForTeamSamples(request: APIRequestContext) {
  const headers = { Authorization: `Bearer ${controlToken}` };
  const session = await request.get(`${userURL}/ui/api/session`, { headers: { Authorization: `Bearer ${apiKey}` } });
  expect(session.status()).toBe(200);
  await expect.poll(async () => {
    const response = await request.get(`${controlURL}/control/api/telemetry?scope=team&team_id=${seedTeamID}&window=15m`, { headers });
    if (!response.ok()) return 0;
    const { data } = await response.json() as { data: TelemetrySnapshot };
    const series = data.series.find(({ id }) => id === "http_rps");
    return (series?.points ?? []).filter(({ value }) => Number.isFinite(value)).length;
  }, { timeout: 90_000, intervals: [1_000, 3_000, 5_000], message: "team Prometheus samples" }).toBeGreaterThan(0);
}

async function rememberEvidence(request: APIRequestContext) {
  const term = `obsidian-${randomUUID()}`;
  const response = await request.post(`${userURL}/mcp`, {
    headers: { Authorization: `Bearer ${apiKey}`, Accept: "application/json", "MCP-Protocol-Version": "2025-11-25" },
    data: {
      jsonrpc: "2.0", id: term, method: "tools/call",
      params: { name: "remember", arguments: {
        target_space: "team_shared", idempotency_key: term,
        evidence: [{ source_type: "manual", content: `The portal verification marker is ${term}.` }],
      } },
    },
  });
  expect(response.status()).toBe(200);
  const body = await response.json();
  expect(body.error).toBeUndefined();
  expect(JSON.parse(body.result.content[0].text).processing_state).toBe("completed");
  return term;
}
