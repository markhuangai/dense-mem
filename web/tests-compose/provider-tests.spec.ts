import { expect, test } from "@playwright/test";
import { navigate, expectPageContained } from "../tests-support/portal";
import type { ProviderTestTarget } from "../src/api";

const controlURL = process.env.DENSE_MEM_CONTROL_URL!;
const token = process.env.DENSE_MEM_CONTROL_TOKEN!;
const fixtureURL = "http://synchronous-write-provider:8787";

for (const width of [1440, 768, 390]) {
  test(`server-side provider diagnostics at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const response = await page.request.get(`${controlURL}/control/api/diagnostics/providers`, { headers: { Authorization: `Bearer ${token}` } });
    expect(response.ok()).toBe(true);
    const targets: ProviderTestTarget[] = (await response.json()).data;
    const generative = targets.find((target) => target.operation === "assess" && target.available)!;
    const decision = targets.find((target) => target.operation === "decide" && target.available)!;
    expect(generative).toBeTruthy();
    expect(decision).toBeTruthy();
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock", exact: true }).click();
    await navigate(page, "Settings");
    await page.getByRole("tab", { name: "AI providers", exact: true }).click();
    const model = page.getByRole("article", { name: `${generative.model} (${generative.operation})`, exact: true });
    await model.getByRole("button", { name: /Send test request/ }).click();
    await expect(model.getByText("Sample succeeded", { exact: true })).toBeVisible();
    await expect(model.getByText(/HTTP response received/)).toBeVisible();
    await expectPageContained(page);
    try {
      const mode = await page.request.post(`${fixtureURL}/decision-fixture`, { data: { mode: "401" } });
      expect(mode.ok()).toBe(true);
      const row = page.getByRole("article", { name: `${decision.model} (${decision.operation})`, exact: true });
      await row.getByRole("button", { name: /Send test request/ }).click();
      await expect(row.getByText("Sample failed", { exact: true })).toBeVisible();
      await expect(row.getByText("401", { exact: true })).toBeVisible();
      await expect(row.getByText(/HTTP response received/)).toBeVisible();
      await expectPageContained(page);
    } finally {
      const reset = await page.request.post(`${fixtureURL}/decision-fixture`, { data: { mode: "normal" } });
      expect(reset.ok()).toBe(true);
    }
  });
}
