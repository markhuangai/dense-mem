import { expect, test } from "@playwright/test";

const controlURL = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const teamName = requiredEnv("DENSE_MEM_E2E_TEAM_NAME");
const fixtureAttemptID = requiredEnv("DENSE_MEM_E2E_DIAGNOSTIC_ATTEMPT_ID");
const validationAttemptID = requiredEnv("DENSE_MEM_E2E_DIAGNOSTIC_VALIDATION_ATTEMPT_ID");
const expiredInvocationID = requiredEnv("DENSE_MEM_E2E_DIAGNOSTIC_EXPIRED_INVOCATION_ID");

test("control panel shows the Remember Attempts diagnostic transcript", async ({ page, request }) => {
  const listResponse = await request.get(`${controlURL}/control/api/remember-attempts?team_id=${encodeURIComponent(teamID)}&outcome=failed&limit=100`, { headers: { Authorization: `Bearer ${controlToken}` } });
  expect(listResponse.status()).toBe(200);
  const list = await listResponse.json() as { data?: Array<{ attempt_id: string; error_code?: string }> };
  const failed = list.data?.find((item) => item.attempt_id === fixtureAttemptID);
  expect(failed).toBeDefined();

  await page.goto(`${controlURL}/?team_id=${teamID}&remember_view=attempts`);
  await page.getByLabel("Control token").fill(controlToken);
  await page.getByRole("button", { name: "Unlock" }).click();
  await expect(page.getByRole("heading", { name: "Remember Attempts" })).toBeVisible();
  await expect(page.getByRole("button", { name: new RegExp(escapeRegExp(teamName)) })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Calls \(\d+\)$/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Attempt Detail" })).toHaveCount(0);
  await page.getByRole("button", { name: /^Calls \(/ }).click();
  await expect(page.getByRole("heading", { name: "Remember Calls" })).toBeVisible();
  const canonicalAttemptRow = page.locator(".remember-attempts-table tbody tr").filter({ has: page.getByRole("button", { name: /Attempt / }) }).first();
  await canonicalAttemptRow.getByRole("button", { name: /Attempt / }).click();
  await expect(page.getByRole("heading", { name: "Remember Attempts" })).toBeVisible();
  await page.getByRole("button", { name: /^Calls \(/ }).click();
  await expect(page.getByRole("heading", { name: "Remember Calls" })).toBeVisible();
  await page.getByRole("button", { name: `Inspect Remember call ${expiredInvocationID}` }).click();
  await expect(page.getByRole("heading", { name: "Call Detail" })).toBeVisible();
  await expect(page.getByText("This capture expired after seven days and its body is no longer available.")).toBeVisible();
  await page.getByRole("button", { name: "View related logs" }).click();
  await expect(page.getByRole("heading", { name: "Operation Logs" })).toBeVisible();
  await expect(page.getByLabel("Correlation ID")).toHaveValue("expired-invocation-correlation");
  await page.getByRole("button", { name: "Teams", exact: true }).click();
  await page.getByRole("button", { name: /team remember attempts/i }).click();
  await expect(page.getByRole("heading", { name: "Remember Attempts" })).toBeVisible();
  await page.getByLabel("Remember attempt outcome").selectOption("failed");
  await expect(page.locator(".remember-attempts-table")).toContainText("Provider Unavailable");
  await page.getByRole("button", { name: `Inspect Remember attempt ${failed?.attempt_id}` }).click();
  await expect(page.getByRole("heading", { name: "Attempt Detail" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Event spine" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Assessor validation" })).toBeVisible();
  await expect(page.getByText("Validation details unavailable for this attempt.")).toBeVisible();
  await expect(page.locator(".remember-event-metadata").filter({ hasText: "<script>bad()</script>" })).toHaveCount(1);
  await expect(page.locator(".remember-event-metadata script")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Original request" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "AI provider exchanges" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Response returned to caller" })).toBeVisible();
  await expect(page.locator(".remember-diagnostic-body pre").first()).toContainText('"name":"remember"');
  await expect(page.locator(".remember-diagnostic-body pre").filter({ hasText: '"isError":true' })).toHaveCount(1);
  await page.locator(".remember-diagnostic-body").first().getByRole("button", { name: "Copy" }).click();
  await expect(page.locator(".remember-diagnostic-body").first().getByRole("button", { name: "Copied" })).toBeVisible();

  await page.getByRole("button", { name: `Inspect Remember attempt ${validationAttemptID}` }).click();
  await expect(page.getByRole("heading", { name: "Assessor validation" })).toBeVisible();
  await expect(page.locator(".remember-validation")).toContainText("Failure class");
  await expect(page.locator(".remember-validation table")).toContainText("Fields");

  await page.goto(`${controlURL}/?team_id=${teamID}&remember_view=calls&invocation_id=${expiredInvocationID}`);
  await expect(page.getByRole("heading", { name: "Call Detail" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Remember call details" })).toContainText(expiredInvocationID);

  const teamHeaders = { Authorization: `Bearer ${controlToken}` };
  const createdTeamIDs: string[] = [];
  try {
    const initialTeamsResponse = await request.get(`${controlURL}/control/api/teams?limit=20`, { headers: teamHeaders });
    expect(initialTeamsResponse.status()).toBe(200);
    const initialTeams = await initialTeamsResponse.json() as { data: Array<{ id: string }> };
    if (initialTeams.data.some((team) => team.id === teamID)) {
      for (let index = 0; index < 20; index += 1) {
        const response = await request.post(`${controlURL}/control/api/teams`, {
          headers: teamHeaders,
          data: { name: `Remember pagination ${Date.now()} ${index}`, description: "Linked team pagination test" },
        });
        expect(response.status()).toBe(201);
        const created = await response.json() as { data: { id: string } };
        createdTeamIDs.push(created.data.id);
      }
    }
    const firstPageResponse = await request.get(`${controlURL}/control/api/teams?limit=20`, { headers: teamHeaders });
    expect(firstPageResponse.status()).toBe(200);
    const firstPage = await firstPageResponse.json() as { data: Array<{ id: string }> };
    expect(firstPage.data.some((team) => team.id === teamID)).toBe(false);

    await page.goto(`${controlURL}/?team_id=${teamID}&remember_view=calls&invocation_id=${expiredInvocationID}`);
    await expect(page.getByRole("heading", { name: "Call Detail" })).toBeVisible();
    await expect(page.getByRole("region", { name: "Remember call details" })).toContainText(expiredInvocationID);
    await page.getByRole("button", { name: /team overview/i }).click();
    const laterPage = page.waitForResponse((response) => response.url().includes("/control/api/teams?limit=100&offset=") && response.status() === 200);
    await page.getByRole("button", { name: "Refresh teams" }).click();
    await laterPage;
    await expect(page.getByRole("heading", { name: teamName })).toBeVisible();
    await page.goto(`${controlURL}/?team_id=22222222-2222-4222-8222-222222222222&remember_view=attempts`);
    await expect(page.getByRole("alert")).toContainText("Linked team is unavailable or you do not have access.");
    await expect(page.getByRole("heading", { name: "Remember Attempts" })).toHaveCount(0);
  } finally {
    for (const id of createdTeamIDs) {
      const response = await request.delete(`${controlURL}/control/api/teams/${id}`, { headers: teamHeaders });
      expect(response.status()).toBe(200);
    }
  }
});

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
