import { expect, test } from "@playwright/test";

const userUrl = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const firstTeamId = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const secondTeamId = requiredEnv("DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID");
const sessionToken = requiredEnv("DENSE_MEM_E2E_SSO_SESSION_TOKEN");
const csrfToken = requiredEnv("DENSE_MEM_E2E_SSO_CSRF_TOKEN");
const origin = new URL(userUrl).origin;

test("SSO workspace shows and copies the canonical team-scoped MCP URL after a team switch", async ({ context, page }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  await context.addCookies([
    { name: "dense_mem_sso_session", value: sessionToken, url: userUrl, httpOnly: true, sameSite: "Lax" },
    { name: "dense_mem_sso_csrf", value: csrfToken, url: userUrl, sameSite: "Lax" },
  ]);
  const resetResponse = await context.request.post(`${userUrl}/ui/api/sso/team`, {
    headers: { "X-Dense-Mem-CSRF": csrfToken },
    data: { team_id: firstTeamId },
  });
  expect(resetResponse.status()).toBe(200);

  await page.goto(`${userUrl}/ui`);

  const workspace = page.getByLabel("Current workspace");
  const firstMCPURL = `${origin}/teams/${firstTeamId}/mcp`;
  await expect(workspace).toBeVisible();
  await expect(workspace.getByText(firstTeamId, { exact: true })).toBeVisible();
  await expect(workspace.getByText(firstMCPURL, { exact: true })).toBeVisible();
  await expect(workspace.getByText("Using this browser origin because MCP_PUBLIC_BASE_URL is not configured.")).toBeVisible();

  await workspace.getByRole("button", { name: "Copy MCP URL" }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(firstMCPURL);

  await Promise.all([
    page.waitForResponse((response) => response.url().endsWith("/ui/api/sso/team") && response.request().method() === "POST" && response.status() === 200),
    workspace.getByLabel("Active team").selectOption(secondTeamId),
  ]);

  const secondMCPURL = `${origin}/teams/${secondTeamId}/mcp`;
  await expect(workspace.getByText(secondTeamId, { exact: true })).toBeVisible();
  await expect(workspace.getByText(secondMCPURL, { exact: true })).toBeVisible();
  await expect(workspace.getByText(firstTeamId, { exact: true })).toHaveCount(0);
  await expect(workspace.getByText(firstMCPURL, { exact: true })).toHaveCount(0);

  await workspace.getByRole("button", { name: "Copy MCP URL" }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(secondMCPURL);
});

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
