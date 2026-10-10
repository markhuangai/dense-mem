import { randomUUID } from "node:crypto";
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


test("loaded older retry actions follow successful work and server eligibility", async ({ page, request }) => {
  test.setTimeout(180000);
  async function control(path: string, body?: unknown, method = body ? "POST" : "GET") {
    const response = await request.fetch(`${controlURL}/control/api${path}`, { method, headers: { Authorization: `Bearer ${token}` }, ...(body ? { data: body } : {}) });
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data;
  }
  async function fixtureMode(mode: string) {
    const response = await request.post(`${process.env.DENSE_MEM_E2E_PROVIDER_URL}/ontology-fixture`, { data: { mode } });
    expect(response.ok()).toBeTruthy();
  }
  const original = await control("/config/ontology-maintenance");
  const initial = await control("/ontology/status");
  try {
    await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
    await control("/ontology/pause", { operation_key: randomUUID() });
    const credential = await control(`/teams/${process.env.DENSE_MEM_E2E_TEAM_ID}/credentials`, { name: `Ontology browser retry ${randomUUID()}`, scopes: ["read", "write"], rate_limit: 300, memory_binding: "shared_only" });
    await fixtureMode("fail-three-then-success");
    const submitted = await request.post(`${process.env.DENSE_MEM_USER_URL}/mcp`, {
      headers: { Authorization: `Bearer ${credential.api_key}`, Accept: "application/json" },
      data: { jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "remember", arguments: {
        idempotency_key: randomUUID(), evidence: [{ content: `Atlas uses PostgreSQL for browser retry ${randomUUID()}.`, source_type: "document", source: "ontology-browser-followups", source_group: randomUUID() }],
        relationships: [{ ref: "storage", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }],
      } } },
    });
    expect(submitted.status()).toBe(200);
    const rpc = await submitted.json();
    expect(rpc.result?.isError).not.toBe(true);
    expect(JSON.parse(rpc.result.content[0].text).processing_state).toBe("completed");
    await control("/ontology/resume", { operation_key: randomUUID() });
    const failedCommand = await control("/ontology/runs", { operation_key: randomUUID(), max_batches: 1 });
    await expect.poll(async () => {
      const history = await control("/ontology/runs?limit=200");
      return history.runs.find((run: { id: string }) => run.id === failedCommand.id)?.status;
    }, { timeout: 120000 }).toBe("incomplete");
    const history = await control("/ontology/runs?limit=200");
    const failed = history.runs.find((run: { id: string }) => run.id === failedCommand.id);
    expect(failed.retryable).toBe(true);
    expect(failed.failure_code).toBeTruthy();
    await fixtureMode("normal");
    await control("/ontology/pause", { operation_key: randomUUID() });
    for (let index = 0; index < 52; index += 1) await control("/ontology/pause", { operation_key: randomUUID() });
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await page.getByRole("button", { name: "Config", exact: true }).click();
    await page.getByRole("tab", { name: "Ontology", exact: true }).click();
    const row = page.locator("tr").filter({ has: page.locator(`td[title="${failed.id}"]`) });
    await expect(page.getByRole("button", { name: "Load older runs" })).toBeVisible();
    await expect(row).toHaveCount(0);
    for (let index = 0; index < 4 && await row.count() === 0; index += 1) {
      await page.getByRole("button", { name: "Load older runs" }).click();
      await expect(page.getByRole("button", { name: "Refresh ontology coverage" })).toBeEnabled();
    }
    await expect(row).toHaveCount(1);
    await expect(row.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    await control("/ontology/resume", { operation_key: randomUUID() });
    await page.getByRole("button", { name: "Refresh ontology coverage" }).click();
    await expect(row.getByRole("button", { name: "Retry failed work" })).toBeEnabled();
    const visibleRuns = page.locator("td[title]");
    const visibleIds = await visibleRuns.evaluateAll((cells) => cells.map((cell) => cell.getAttribute("title")));
    const historyRoute = "**/control/api/ontology/runs?**";
    await page.route(historyRoute, async (route) => {
      if (route.request().method() === "GET" && new URL(route.request().url()).searchParams.has("cursor")) {
        await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ message: "older history refresh unavailable" }) });
      } else {
        await route.continue();
      }
    });
    await page.getByRole("button", { name: "Refresh ontology coverage" }).click();
    const refreshFailure = page.getByRole("alert").filter({ hasText: "older history refresh unavailable" });
    await expect(refreshFailure).toBeVisible();
    expect(await visibleRuns.evaluateAll((cells) => cells.map((cell) => cell.getAttribute("title")))).toEqual(visibleIds);
    await expect(row.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    for (const retryButton of await page.getByRole("button", { name: "Retry failed work" }).all()) await expect(retryButton).toBeDisabled();
    await page.unroute(historyRoute);
    await page.getByRole("button", { name: "Refresh ontology coverage" }).click();
    await expect(refreshFailure).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Retry failed work" })).toBeEnabled();
    const acceptance = page.waitForResponse((response) => response.request().method() === "POST" && response.url().endsWith(`/ontology/runs/${failed.id}/retry`));
    await row.getByRole("button", { name: "Retry failed work" }).click();
    const accepted = await acceptance;
    expect(accepted.status()).toBe(200);
    const retry = (await accepted.json()).data;
    await expect.poll(async () => {
      const page = await control("/ontology/runs?limit=200");
      return page.runs.find((run: { id: string }) => run.id === retry.id)?.status;
    }, { timeout: 120000 }).toBe("completed");
    await expect(row.getByRole("button", { name: "Retry failed work" })).toHaveCount(0, { timeout: 20000 });
    await expect(row).toHaveCount(1);
    const updated = await control("/ontology/runs?limit=200");
    const older = updated.runs.find((run: { id: string }) => run.id === failed.id);
    expect(older.retryable).toBe(false);
    expect(older.failure_code).toBe(failed.failure_code);
    const completed = updated.runs.find((run: { id: string }) => run.id === retry.id);
    expect(completed.failure_code || "").toBe("");
    const rejected = await request.post(`${controlURL}/control/api/ontology/runs/${failed.id}/retry`, { headers: { Authorization: `Bearer ${token}` }, data: { operation_key: randomUUID(), max_batches: 1 } });
    expect(rejected.status()).toBe(422);
  } finally {
    await fixtureMode("normal");
    await control(initial.paused ? "/ontology/pause" : "/ontology/resume", { operation_key: randomUUID() });
    await control("/config/ontology-maintenance", { items: original.items.map(({ key, value }: { key: string; value: string }) => ({ key, value })) }, "PATCH");
  }
});
