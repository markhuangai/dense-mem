import { expect, test } from "@playwright/test";

const controlURL = process.env.DENSE_MEM_CONTROL_URL!;
const token = process.env.DENSE_MEM_CONTROL_TOKEN!;

test("operators inspect ontology coverage and pause or resume durable admission", async ({ page }) => {
  await page.goto(controlURL);
  await page.getByLabel("Control token").fill(token);
  await page.getByRole("button", { name: "Unlock" }).click();
  await page.getByRole("button", { name: "Config", exact: true }).click();
  await page.getByRole("tab", { name: "Ontology", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Ontology coverage" })).toBeVisible();
  await expect(page.getByLabel("Enable ontology maintenance")).toBeVisible();
  await expect(page.getByText(/Pending policy:/)).toBeVisible();
  await page.getByRole("button", { name: "Pause maintenance", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Maintenance paused");
  await page.getByRole("button", { name: "Resume maintenance", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Maintenance resumed");
  await page.getByLabel("Maximum batches").fill("101");
  await expect(page.getByRole("button", { name: "Run bounded maintenance" })).toBeDisabled();
});
