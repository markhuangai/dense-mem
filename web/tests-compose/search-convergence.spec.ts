import { expect, test, type Page } from "@playwright/test";

const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");

test("control panel renders document-authoritative search convergence", async ({ page }) => {
  await openControlPanel(page);
  await page.getByRole("button", { name: "Search", exact: true }).click();

  const panel = page.getByRole("region", { name: "Search convergence" });
  await expect(panel).toBeVisible();
  await expect(panel.getByRole("heading", { name: "Search convergence" })).toBeVisible();
  const summary = panel.getByLabel("Search convergence summary");
  await expect(summary).toContainText("Status");
  await expect(summary).toContainText("Expected");
  await expect(summary).toContainText("Current");
  await expect(summary).toContainText("Drifted");
  await expect(panel.getByLabel("Legacy embedding job diagnostics")).toBeVisible();
  await expectNoShellOverlap(page);
});

test("control panel surfaces a bounded convergence error", async ({ page }) => {
  let requestCount = 0;
  await page.route("**/control/api/search/convergence", async (route) => {
    requestCount += 1;
    await route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ message: "temporary convergence outage" }),
    });
  });

  await openControlPanel(page);
  await page.getByRole("button", { name: "Search", exact: true }).click();

  const panel = page.getByRole("region", { name: "Search convergence" });
  await expect(panel).toBeVisible();
  await expect(panel.getByRole("alert")).toHaveText("temporary convergence outage");
  expect(requestCount).toBeGreaterThan(0);
  await expectNoShellOverlap(page);
});

async function openControlPanel(page: Page) {
  await page.goto(`${controlUrl}/`);
  await page.getByLabel("Control token").fill(controlToken);
  await page.getByRole("button", { name: "Unlock" }).click();
  await expect(page.getByRole("heading", { name: "Teams" })).toBeVisible();
}

async function expectNoShellOverlap(page: Page) {
  const boxes = await page.locator(".topbar, .primary-rail, .resource-rail, .detail-pane").evaluateAll((elements) => (
    elements.map((element) => {
      const rect = element.getBoundingClientRect();
      return { left: rect.left, top: rect.top, right: rect.right, bottom: rect.bottom, width: rect.width, height: rect.height };
    })
  ));
  expect(boxes.length).toBeGreaterThan(0);
  for (let i = 0; i < boxes.length; i += 1) {
    expect(boxes[i].width).toBeGreaterThan(0);
    expect(boxes[i].height).toBeGreaterThan(0);
    for (let j = i + 1; j < boxes.length; j += 1) {
      const horizontalOverlap = Math.min(boxes[i].right, boxes[j].right) - Math.max(boxes[i].left, boxes[j].left);
      const verticalOverlap = Math.min(boxes[i].bottom, boxes[j].bottom) - Math.max(boxes[i].top, boxes[j].top);
      expect(horizontalOverlap > 0 && verticalOverlap > 0, `${i} and ${j} shell boxes overlap`).toBe(false);
    }
  }
}

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required for search convergence compose e2e`);
  }
  return value;
}
