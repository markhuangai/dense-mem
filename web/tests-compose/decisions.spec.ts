import { expect, test } from "@playwright/test";
import { confirmAction, navigate, expectPageContained } from "../tests-support/portal";

const controlURL = process.env.DENSE_MEM_CONTROL_URL!;
const token = process.env.DENSE_MEM_CONTROL_TOKEN!;

for (const width of [1440, 768, 390]) {
  test(`decision loading, saved and error states at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Settings");
    let release: () => void = () => {};
    const held = new Promise<void>((resolve) => { release = resolve; });
    await page.route("**/control/api/config/decisions", async (route) => {
      await held;
      await route.continue();
    });
    await page.getByRole("tab", { name: "Decisions", exact: true }).click();
    await expect(page.getByText("Loading config", { exact: true })).toBeVisible();
    await expectPageContained(page);
    await page.screenshot({ path: testInfo.outputPath(`decisions-${width}-loading.png`), fullPage: true });
    release();
    await expect(page.getByText(/Provider configured: openai/)).toBeVisible();
    await page.unroute("**/control/api/config/decisions");
    const security = page.getByRole("switch", { name: /Security review/ });
    const before = await security.getAttribute("aria-checked");
    await security.click();
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("Changes saved");
    await expectPageContained(page);
    await page.screenshot({ path: testInfo.outputPath(`decisions-${width}-saved.png`), fullPage: true });
    await page.getByRole("button", { name: "Refresh decision config" }).click();
    await expect(security).toHaveAttribute("aria-checked", before === "true" ? "false" : "true");
    await security.click();
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("Changes saved");
    await page.route("**/control/api/config/decisions", async (route) => {
      if (route.request().method() === "PATCH") await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ message: "Decision settings unavailable" }) });
      else await route.continue();
    });
    await page.getByRole("switch", { name: /Recall reranking/ }).click();
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("Decision settings unavailable");
    await expectPageContained(page);
    await page.screenshot({ path: testInfo.outputPath(`decisions-${width}-error.png`), fullPage: true });
    await page.getByRole("button", { name: "Refresh decision config" }).click();
    await confirmAction(page, "Discard changes");
    await expect(page.getByRole("switch", { name: /Recall reranking/ })).toHaveAttribute("aria-checked", "true");
  });
}
