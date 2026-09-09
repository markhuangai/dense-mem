import { expect, test } from "@playwright/test";

const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const teamName = requiredEnv("DENSE_MEM_E2E_TEAM_NAME");
const fixtureAttemptID = requiredEnv("DENSE_MEM_E2E_DIAGNOSTIC_ATTEMPT_ID");

test("control panel shows the Remember Attempts diagnostic transcript", async ({ page, request }) => {
  const listResponse = await request.get(`${controlURL}/control/api/remember-attempts?team_id=${encodeURIComponent(teamID)}&outcome=failed&limit=100`, { headers: { Authorization: `Bearer ${controlToken}` } });
  expect(listResponse.status()).toBe(200);
  const list = await listResponse.json() as { data?: Array<{ attempt_id: string; error_code?: string }> };
  const failed = list.data?.find((item) => item.attempt_id === fixtureAttemptID);
  expect(failed).toBeDefined();

  await page.goto(`${controlURL}/`);
  await page.getByLabel("Control token").fill(controlToken);
  await page.getByRole("button", { name: "Unlock" }).click();
  await expect(page.getByRole("heading", { name: "Teams" })).toBeVisible();
  await page.getByRole("button", { name: new RegExp(escapeRegExp(teamName)) }).click();
  await page.getByRole("button", { name: /team remember attempts/i }).click();
  await expect(page.getByRole("heading", { name: "Remember Attempts" })).toBeVisible();
  await page.getByLabel("Remember attempt outcome").selectOption("failed");
  await expect(page.locator(".remember-attempts-table")).toContainText("Provider Unavailable");
  await page.getByRole("button", { name: `Inspect Remember attempt ${failed?.attempt_id}` }).click();
  await expect(page.getByRole("heading", { name: "Attempt Detail" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Event spine" })).toBeVisible();
  await expect(page.locator(".remember-event-metadata").filter({ hasText: "<script>bad()</script>" })).toHaveCount(1);
  await expect(page.locator(".remember-event-metadata script")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Original request" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "AI provider exchanges" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Response returned to caller" })).toBeVisible();
  await expect(page.locator(".remember-diagnostic-body pre").first()).toContainText('"name":"remember"');
  await expect(page.locator(".remember-diagnostic-body pre").filter({ hasText: '"isError":true' })).toHaveCount(1);
  await page.locator(".remember-diagnostic-body").first().getByRole("button", { name: "Copy" }).click();
  await expect(page.locator(".remember-diagnostic-body").first().getByRole("button", { name: "Copied" })).toBeVisible();
});

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
