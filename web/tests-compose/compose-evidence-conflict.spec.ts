import { expect, test, type Page } from "@playwright/test";

const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seedTeamId = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const seedTeamName = requiredEnv("DENSE_MEM_E2E_TEAM_NAME");

test("Evidence view resolves a case, handles stale review, and stays keyboard-safe without mobile overflow", async ({ page }) => {
  await openControlPanel(page);
  await page.getByRole("button", { name: new RegExp(escapeRegExp(seedTeamName)) }).click();
  await page.getByRole("button", { name: /team conflicts/i }).click();

  const evidenceTab = page.getByRole("tab", { name: "Evidence" });
  await evidenceTab.focus();
  await expect(evidenceTab).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Conflict queue" })).toBeVisible();
  await expect(page.getByLabel("Evidence conflicts")).toBeVisible();

  const initial = await listOpenConflicts(page);
  expect(initial.length).toBeGreaterThanOrEqual(2);
  const successCase = initial[0];
  const staleCase = initial[1];

  const successCard = caseCard(page, successCase.conflict_id);
  await successCard.click();
  await expect(page.getByRole("heading", { name: "Cited evidence conflict" })).toBeVisible();
  await page.getByLabel("Review reason").fill("browser successful resolution");
  const refreshed = page.waitForResponse((response) => {
    const pathname = new URL(response.url()).pathname;
    return response.request().method() === "GET" && response.status() === 200 && pathname === `/control/api/teams/${seedTeamId}/evidence-conflicts`;
  });
  await page.getByRole("button", { name: "Resolve" }).click();
  await refreshed;
  await expect(caseCard(page, successCase.conflict_id)).toHaveCount(0);

  const staleCard = caseCard(page, staleCase.conflict_id);
  await staleCard.click();
  await expect(page.getByRole("heading", { name: "Cited evidence conflict" })).toBeVisible();
  await page.getByLabel("Review reason").fill("browser stale resolution");

  const external = await page.request.post(`${controlUrl}/control/api/teams/${seedTeamId}/evidence-conflicts/${staleCase.conflict_id}/resolution`, {
    headers: { Authorization: `Bearer ${controlToken}`, "Content-Type": "application/json" },
    data: { expected_version: staleCase.version, decision: "resolve", reason: "external concurrent review" },
  });
  expect(external.status()).toBe(200);

  await page.getByRole("button", { name: "Resolve" }).click();
  await expect(page.getByText("This conflict changed in another review. The latest version is loaded; your reason remains.")).toBeVisible();
  await expect(page.getByLabel("Review reason")).toHaveValue("browser stale resolution");
  await expect(page.getByText(/Review history \(2\)/)).toBeVisible();

  const noHorizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
  expect(noHorizontalOverflow).toBe(true);
});

async function openControlPanel(page: Page) {
  await page.goto(`${controlUrl}/`);
  await page.getByLabel("Control token").fill(controlToken);
  await page.getByRole("button", { name: "Unlock" }).click();
  await expect(page.getByRole("heading", { name: "Teams" })).toBeVisible();
}

async function listOpenConflicts(page: Page): Promise<Array<{ conflict_id: string; version: number }>> {
  const response = await page.request.get(`${controlUrl}/control/api/teams/${seedTeamId}/evidence-conflicts?status=open&limit=25`, {
    headers: { Authorization: `Bearer ${controlToken}`, Accept: "application/json" },
  });
  expect(response.status()).toBe(200);
  const payload = await response.json();
  return (payload.data?.items ?? []).map((item: { conflict_id: string; version: number }) => ({ conflict_id: item.conflict_id, version: item.version }));
}

function caseCard(page: Page, conflictId: string) {
  return page.locator(".evidence-conflict-card").filter({ hasText: conflictId.slice(0, 8) });
}

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required for evidence conflict compose e2e`);
  return value;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
