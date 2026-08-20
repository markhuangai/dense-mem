import { expect, test } from "@playwright/test";

const userURL = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const firstTeamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const secondTeamID = requiredEnv("DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID");
const sessionToken = requiredEnv("DENSE_MEM_E2E_SSO_SESSION_TOKEN");
const csrfToken = requiredEnv("DENSE_MEM_E2E_SSO_CSRF_TOKEN");
const origin = new URL(userURL).origin;

test("SSO workspace shows and copies the canonical team-scoped MCP URL after a team switch", async ({ context, page }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
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
  const firstMCPURL = `${origin}/teams/${firstTeamID}/mcp`;
  await expect(workspace).toBeVisible();
  await expect(workspace.getByText(firstTeamID, { exact: true })).toBeVisible();
  await expect(workspace.getByText(firstMCPURL, { exact: true })).toBeVisible();
  await expect(workspace.getByText("Using this browser origin because MCP_PUBLIC_BASE_URL is not configured.")).toHaveCount(0);

  await workspace.getByRole("button", { name: "Copy MCP URL" }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(firstMCPURL);

  await Promise.all([
    page.waitForResponse((response) => response.url().endsWith("/ui/api/sso/team") && response.request().method() === "POST" && response.status() === 200),
    workspace.getByLabel("Active team").selectOption(secondTeamID),
  ]);

  const secondMCPURL = `${origin}/teams/${secondTeamID}/mcp`;
  await expect(workspace.getByText(secondTeamID, { exact: true })).toBeVisible();
  await expect(workspace.getByText(secondMCPURL, { exact: true })).toBeVisible();
  await expect(workspace.getByText(firstTeamID, { exact: true })).toHaveCount(0);
  await expect(workspace.getByText(firstMCPURL, { exact: true })).toHaveCount(0);

  await workspace.getByRole("button", { name: "Copy MCP URL" }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(secondMCPURL);
});

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
