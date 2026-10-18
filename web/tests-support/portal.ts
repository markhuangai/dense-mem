import { expect, type Page } from "@playwright/test";

export async function openNavigation(page: Page) {
  await expect(page.locator(".app-shell")).toBeVisible();
  const trigger = page.getByLabel("Open navigation", { exact: true });
  await expect(page.locator('.mobile-navigation[data-state="closed"]')).toBeHidden();
  if (await trigger.isVisible() && !await page.locator(".mobile-navigation").isVisible()) await trigger.click();
}

export async function navigate(page: Page, name: string | RegExp) {
  if (await page.locator(".contextual-sheet").isVisible()) await page.keyboard.press("Escape");
  await expect(page.locator(".contextual-sheet")).toBeHidden();
  await openNavigation(page);
  await page.getByRole("link", { name, exact: true }).click();
}

export async function confirmAction(page: Page, name: string) {
  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name, exact: true }).click();
}

export async function expectPageContained(page: Page) {
  const geometry = await page.evaluate(() => ({ viewport: Math.min(innerWidth, document.documentElement.clientWidth), document: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) }));
  expect(geometry.document).toBeLessThanOrEqual(geometry.viewport + 1);
  for (const table of await page.locator(".table-wrap").all()) {
    if (!await table.isVisible()) continue;
    expect(await table.evaluate((element) => getComputedStyle(element).overflowX)).toBe("auto");
  }
}
