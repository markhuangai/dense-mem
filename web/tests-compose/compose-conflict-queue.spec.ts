import { expect, test, type Page } from "@playwright/test";

const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seedTeamId = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const seedTeamName = requiredEnv("DENSE_MEM_E2E_TEAM_NAME");

test("control panel shows the read-only conflict queue workspace", async ({ page }) => {
  await openControlPanel(page);
  await page.getByRole("button", { name: new RegExp(escapeRegExp(seedTeamName)) }).click();
  await page.getByRole("button", { name: /team conflicts/i }).click();

  await expect(page.getByRole("heading", { name: "Conflict queue" })).toBeVisible();
  await expect(page.getByLabel("Conflict queue summary")).toContainText(/Open|Overdue/);
  await expect(page.getByRole("table", { name: "Active relationship conflicts" })).toBeVisible();
  await expect(page.getByRole("button", { name: /resolve|retry|vote|delete|force winner|lease release/i })).toHaveCount(0);

  const requests: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === `/control/api/teams/${seedTeamId}/conflicts/queue`) {
      requests.push(request.url());
    }
  });
  await page.getByLabel("Show").selectOption("overdue");
  await expect.poll(() => requests.at(-1) ?? "").toContain("status=overdue");
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
    throw new Error(`${name} is required for conflict queue compose e2e`);
  }
  return value;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
