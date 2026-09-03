import { expect, test } from "@playwright/test";

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
  const copyValue = workspace.locator('input[aria-hidden="true"]');
  const firstMCPURL = `${mcpPublicBaseURL}/teams/${firstTeamID}/mcp`;
  await expect(workspace).toBeVisible();
  await expect(workspace.getByText(firstTeamID, { exact: true })).toBeVisible();
  await expect(workspace.getByText(firstMCPURL, { exact: true })).toBeVisible();
  await expect(copyValue).toHaveValue(firstMCPURL);
  await expect(workspace.getByText("Using this browser origin because MCP_PUBLIC_BASE_URL is not configured.")).toHaveCount(0);

  await workspace.getByRole("button", { name: "Copy MCP URL" }).click();
  await expect(workspace.getByRole("button", { name: "MCP URL copied" })).toBeVisible();

  await Promise.all([
    page.waitForResponse((response) => response.url().endsWith("/ui/api/sso/team") && response.request().method() === "POST" && response.status() === 200),
    workspace.getByLabel("Active team").selectOption(secondTeamID),
  ]);

  const secondMCPURL = `${mcpPublicBaseURL}/teams/${secondTeamID}/mcp`;
  await expect(workspace.getByText(secondTeamID, { exact: true })).toBeVisible();
  await expect(workspace.getByText(secondMCPURL, { exact: true })).toBeVisible();
  await expect(copyValue).toHaveValue(secondMCPURL);
  await expect(workspace.getByText(firstTeamID, { exact: true })).toHaveCount(0);
  await expect(workspace.getByText(firstMCPURL, { exact: true })).toHaveCount(0);

  await workspace.getByRole("button", { name: "Copy MCP URL" }).click();
  await expect(workspace.getByRole("button", { name: "MCP URL copied" })).toBeVisible();
});

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
