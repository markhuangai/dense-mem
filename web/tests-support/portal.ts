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

export async function expectCompactNumberField(page: Page, name: string) {
  const field = page.getByLabel(name, { exact: true });
  await expect(field).toBeVisible();
  const geometry = await field.evaluate((element) => {
    const input = element.getBoundingClientRect();
    const row = element.closest(".setting-row")!.getBoundingClientRect();
    return { width: input.width, left: input.left, right: input.right, rowLeft: row.left, rowRight: row.right };
  });
  expect(geometry.width).toBeGreaterThanOrEqual(80);
  expect(geometry.width).toBeLessThanOrEqual(136);
  expect(geometry.left).toBeGreaterThanOrEqual(geometry.rowLeft);
  expect(geometry.right).toBeLessThanOrEqual(geometry.rowRight + 1);
}

export async function expectConflictCardsContained(page: Page) {
  const cards = page.locator(".evidence-conflict-card");
  expect(await cards.count()).toBeGreaterThanOrEqual(2);
  const boxes = await cards.evaluateAll((elements) => elements.map((element) => {
    const card = element.getBoundingClientRect();
    return { left: card.left, right: card.right, top: card.top, bottom: card.bottom,
      containsContent: Array.from(element.children).every((child) => {
        const content = child.getBoundingClientRect();
        return content.top >= card.top && content.bottom <= card.bottom + 1 && content.left >= card.left && content.right <= card.right + 1;
      }),
    };
  }));
  for (const [index, box] of boxes.entries()) {
    expect(box.containsContent).toBe(true);
    for (const other of boxes.slice(index + 1)) {
      expect(Math.min(box.right, other.right) <= Math.max(box.left, other.left) || Math.min(box.bottom, other.bottom) <= Math.max(box.top, other.top)).toBe(true);
    }
  }
  const pagination = await page.getByLabel("Evidence conflict page navigation").boundingBox();
  expect(pagination!.y).toBeGreaterThanOrEqual(Math.max(...boxes.map((box) => box.bottom)));
  await cards.first().focus();
  await expect(cards.first()).toBeFocused();
}
