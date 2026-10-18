import { expect, test } from "@playwright/test";
import { openNavigation } from "../tests-support/portal";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const mcpPublicBaseURL = requiredEnv("DENSE_MEM_E2E_MCP_PUBLIC_BASE_URL").replace(/\/$/, "");
const firstTeamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const secondTeamID = requiredEnv("DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID");
const sessionToken = requiredEnv("DENSE_MEM_E2E_SSO_SESSION_TOKEN");
const csrfToken = requiredEnv("DENSE_MEM_E2E_SSO_CSRF_TOKEN");

test("SSO workspace shows and copies the canonical team-scoped MCP URL after a team switch", async ({ context, page }) => {
  await context.addCookies([
    { name: "dense_mem_sso_session", value: sessionToken, url: userURL, httpOnly: true, sameSite: "Lax" },
    { name: "dense_mem_sso_csrf", value: csrfToken, url: userURL, sameSite: "Lax" },
  ]);
  const resetResponse = await context.request.post(`${userURL}/ui/api/sso/team`, {
    headers: { "X-Dense-Mem-CSRF": csrfToken },
    data: { team_id: firstTeamID },
  });
  expect(resetResponse.status()).toBe(200);

  await page.goto(`${userURL}/ui`);

  const workspace = page.getByLabel("Current workspace");
  await expect(workspace).toBeVisible();
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  const connection = page.getByRole("dialog");
  const copyValue = connection.locator('input[aria-hidden="true"]');
  const firstMCPURL = `${mcpPublicBaseURL}/teams/${firstTeamID}/mcp`;
  await expect(connection.getByText(firstTeamID, { exact: true })).toBeVisible();
  await expect(connection.getByText(firstMCPURL, { exact: true })).toBeVisible();
  await expect(copyValue).toHaveValue(firstMCPURL);
  await expect(connection.getByText("Using this browser origin because MCP_PUBLIC_BASE_URL is not configured.")).toHaveCount(0);

  await connection.getByRole("button", { name: "Copy MCP URL" }).click();
  await expect(connection.getByRole("button", { name: "MCP URL copied" })).toBeVisible();
  await page.keyboard.press("Escape");
  await openNavigation(page);
  await page.getByRole("button", { name: "Choose team", exact: true }).click();
  await page.getByRole("textbox", { name: "Find a team" }).fill(secondTeamID);

  let finishSwitch!: () => void;
  const pendingSwitch = new Promise<void>((resolve) => { finishSwitch = resolve; });
  let switchRequests = 0;
  await page.route("**/ui/api/sso/team", async (route) => {
    switchRequests += 1;
    const response = await route.fetch();
    await pendingSwitch;
    await route.fulfill({ response });
  });
  const selectedTeam = page.locator(".workspace-options").getByRole("button");
  await Promise.all([
    page.waitForResponse((response) => response.url().endsWith("/ui/api/sso/team") && response.request().method() === "POST" && response.status() === 200),
    (async () => {
      try {
        await selectedTeam.dblclick();
        await expect(selectedTeam).toBeDisabled();
      } finally {
        finishSwitch();
      }
    })(),
  ]);
  expect(switchRequests).toBe(1);
  await expect(page.locator(".workspace-picker")).toBeHidden();
  if (await page.locator(".mobile-navigation").isVisible()) await page.keyboard.press("Escape");
  await expect(page.locator(".mobile-navigation")).toBeHidden();
  await page.getByRole("button", { name: "Connect", exact: true }).click();

  const secondMCPURL = `${mcpPublicBaseURL}/teams/${secondTeamID}/mcp`;
  await expect(connection.getByText(secondTeamID, { exact: true })).toBeVisible();
  await expect(connection.getByText(secondMCPURL, { exact: true })).toBeVisible();
  await expect(copyValue).toHaveValue(secondMCPURL);
  await expect(connection.getByText(firstTeamID, { exact: true })).toHaveCount(0);
  await expect(connection.getByText(firstMCPURL, { exact: true })).toHaveCount(0);

  await connection.getByRole("button", { name: "Copy MCP URL" }).click();
  await expect(connection.getByRole("button", { name: "MCP URL copied" })).toBeVisible();
});

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
