import { navigate, openNavigation, confirmAction, expectPageContained, expectCompactNumberField } from "../tests-support/portal";
import { randomUUID } from "node:crypto";
import { expect, test } from "@playwright/test";

const controlURL = process.env.DENSE_MEM_CONTROL_URL!;
const token = process.env.DENSE_MEM_CONTROL_TOKEN!;

test("operators inspect ontology coverage and pause or resume durable admission", async ({ page }) => {
  await page.goto(controlURL);
  await page.getByLabel("Control token").fill(token);
  await page.getByRole("button", { name: "Unlock" }).click();
  await navigate(page, "Ontology maintenance");
  await expect(page.getByRole("heading", { name: "Ontology coverage" })).toBeVisible();
  await expect(page.getByLabel("Enable ontology maintenance")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Pending policy", exact: true })).toBeVisible();
  await expect(page.getByRole("group", { name: "Schedule", exact: true })).toBeVisible();
  await expect(page.getByRole("group", { name: "Processing", exact: true })).toBeVisible();
  await expect(page.getByRole("group", { name: "Allowance per window", exact: true })).toBeVisible();
  await expectCompactNumberField(page, "Cadence (hours)");
  await expectCompactNumberField(page, "Concurrent assessments");
  await expectPageContained(page);
  await page.getByRole("button", { name: "Pause maintenance", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Maintenance paused");
  await page.getByRole("button", { name: "Resume maintenance", exact: true }).click();
  await expect(page.getByRole("status")).toContainText("Maintenance resumed");
  await page.getByLabel("Maximum batches").fill("101");
  await expect(page.getByRole("button", { name: "Run bounded maintenance" })).toBeDisabled();
});

test("operators add allowance to the active window without resetting its policy", async ({ page, request }) => {
  async function control(path: string, body?: unknown, method = body ? "POST" : "GET") {
    const response = await request.fetch(`${controlURL}/control/api${path}`, { method, headers: { Authorization: `Bearer ${token}` }, ...(body ? { data: body } : {}) });
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data;
  }
  const originalConfig = await control("/config/ontology-maintenance");
  const originalStatus = await control("/ontology/status");
  const originalEnabled = originalConfig.items.find((item: { key: string }) => item.key === "ONTOLOGY_MAINTENANCE_ENABLED").value;
  try {
    await control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
    await control("/ontology/resume", { operation_key: randomUUID() });
    const status = await control("/ontology/status");
    const before = status.window;
    expect(before).toBeTruthy();
    expect(Date.parse(before.ends_at)).toBeGreaterThan(Date.parse(status.observed_at));
    await control("/ontology/pause", { operation_key: randomUUID() });
    const rejected = await request.post(`${controlURL}/control/api/ontology/allowances`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { operation_key: randomUUID(), window_id: before.id, input_tokens: 1234, output_tokens: 5678 },
    });
    expect(rejected.status()).toBe(409);
    const paused = (await control("/ontology/status")).window;
    expect(paused.id).toBe(before.id);
    expect(paused.additional_input_tokens).toBe(before.additional_input_tokens);
    expect(paused.additional_output_tokens).toBe(before.additional_output_tokens);
    await control("/ontology/resume", { operation_key: randomUUID() });
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Ontology maintenance");
    await page.getByLabel("Additional input tokens").fill("1234");
    await page.getByLabel("Additional output tokens").fill("5678");
    await page.getByRole("button", { name: "Add allowance now", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("Allowance added to this window");
    const after = (await control("/ontology/status")).window;
    expect(after.id).toBe(before.id);
    expect(after.ends_at).toBe(before.ends_at);
    expect(after.policy).toEqual(before.policy);
    expect(after.additional_input_tokens).toBe(before.additional_input_tokens + 1234);
    expect(after.additional_output_tokens).toBe(before.additional_output_tokens + 5678);
    expect(after.charged_input_tokens).toBeGreaterThanOrEqual(before.charged_input_tokens);
    await expect(page.getByRole("progressbar", { name: "Input tokens", exact: true })).toHaveAttribute("aria-valuetext", new RegExp(` of ${(after.policy.input_tokens + after.additional_input_tokens).toLocaleString()}$`));
    await expect(page.getByRole("progressbar", { name: "Output tokens", exact: true })).toHaveAttribute("aria-valuetext", new RegExp(` of ${(after.policy.output_tokens + after.additional_output_tokens).toLocaleString()}$`));
    await expectCompactNumberField(page, "Additional input tokens");
    await expect(page.getByLabel("Additional input tokens")).toHaveValue("0");
    await expect(page.getByRole("button", { name: "Add allowance now", exact: true })).toBeDisabled();
  } finally {
    const restored = await Promise.allSettled([
      control("/config/ontology-maintenance", { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: originalEnabled }] }, "PATCH"),
      control(originalStatus.paused ? "/ontology/pause" : "/ontology/resume", { operation_key: randomUUID() }),
    ]);
    for (const result of restored) expect(result.status).toBe("fulfilled");
  }
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
    const credential = await control(`/teams/${process.env.DENSE_MEM_E2E_TEAM_ID}/credentials`, { name: `Ontology browser retry ${randomUUID()}`, scopes: ["read", "write"], rate_limit: 300 });
    await fixtureMode("fail-three-then-success");
    const submitted = await request.post(`${process.env.DENSE_MEM_USER_URL}/mcp`, {
      headers: { Authorization: `Bearer ${credential.api_key}`, Accept: "application/json" },
      data: { jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "remember", arguments: {
        target_space: "team_shared", idempotency_key: randomUUID(), evidence: [{ content: `Atlas uses PostgreSQL for browser retry ${randomUUID()}.`, source_type: "document", source: "ontology-browser-followups", source_group: randomUUID() }],
        relationships: [{ ref: "storage", subject: { name: "Atlas", entity_kind: "project" }, predicate: { proposed_key: "uses" }, object: { entity: { name: "PostgreSQL", entity_kind: "product" } }, polarity: "+", evidence_indices: [0] }],
      } } },
    });
    expect(submitted.status()).toBe(200);
    const rpc = await submitted.json();
    const remembered = JSON.parse(rpc.result.content[0].text);
    expect(rpc.result?.isError, JSON.stringify({ processing_state: remembered.processing_state,
      errors: (remembered.errors || []).map(({ code, reason_code, retryable, next_action }: { code: string; reason_code?: string; retryable: boolean; next_action: string }) => ({ code, reason_code, retryable, next_action })) })).not.toBe(true);
    expect(remembered.processing_state).toBe("completed");
    await control("/ontology/resume", { operation_key: randomUUID() });
    const failedCommand = await control("/ontology/runs", { operation_key: randomUUID(), max_batches: 1 });
    await expect.poll(async () => {
      const history = await control("/ontology/runs?limit=200");
      return history.runs.find((run: { id: string }) => run.id === failedCommand.id)?.status;
    }, { timeout: 120000 }).toBe("incomplete");
    const history = await control("/ontology/runs?limit=200");
    const failed = history.runs.find((run: { id: string }) => run.id === failedCommand.id);
    expect(failed.retryable, JSON.stringify({ status: failed.status, failure_code: failed.failure_code, completed_batches: failed.completed_batches })).toBe(true);
    expect(failed.failure_code).toBeTruthy();
    await fixtureMode("normal");
    await control("/ontology/pause", { operation_key: randomUUID() });
    for (let index = 0; index < 52; index += 1) await control("/ontology/pause", { operation_key: randomUUID() });
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Ontology maintenance");
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
