import { navigate, openNavigation, confirmAction, expectPageContained, expectCompactNumberField } from "../tests-support/portal";
import { randomUUID } from "node:crypto";
import { expect, test } from "@playwright/test";
import type { CommunityStatus } from "../src/community-api-types";

const controlURL = process.env.DENSE_MEM_CONTROL_URL!;
const token = process.env.DENSE_MEM_CONTROL_TOKEN!;

test("operators save per-team concurrency for the next window and retain rejected drafts", async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${token}` };
  async function control(path: string, body?: unknown, method = body ? "PATCH" : "GET") {
    const response = await request.fetch(`${controlURL}/control/api${path}`, { method, headers, ...(body ? { data: body } : {}) });
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data;
  }
  const original = await control("/config/ontology-maintenance");
  const before = await control("/ontology/status");
  const teams = await control("/teams?limit=10&offset=0");
  const team = teams[0];
  expect(team).toBeTruthy();
  try {
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Ontology maintenance");
    await page.getByLabel("Team for override").selectOption(team.id);
    await page.getByLabel("Concurrency override").fill("9");
    await expect(page.getByRole("button", { name: "Stage team override" })).toBeDisabled();
    await page.getByLabel("Concurrency override").fill("3");
    await page.getByRole("button", { name: "Stage team override" }).click();
    await page.getByLabel("Default concurrency per team").fill("9");
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("invalid ontology maintenance input");
    await expect(page.getByLabel("Default concurrency per team")).toHaveValue("9");
    await expect(page.getByText(/Staged: 3/)).toBeVisible();
    await page.getByLabel("Default concurrency per team").fill("2");
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByText("Changes saved", { exact: true })).toBeVisible();
    const saved = await control("/config/ontology-maintenance");
    expect(saved.effective.team_max_concurrency).toBe(2);
    expect(saved.effective.team_concurrency_overrides).toContainEqual({ team_id: team.id, max_concurrency: 3 });
    await page.getByRole("button", { name: "Refresh ontology coverage" }).click();
    await expect(page.getByText(/Staged: 3.*Saved: 3.*Current effective:/)).toBeVisible();
    expect((await control("/ontology/status")).window.policy).toEqual(before.window.policy);
    await expectPageContained(page);
    await page.getByRole("button", { name: `Use default for ${team.name}`, exact: true }).click();
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect.poll(async () => (await control("/config/ontology-maintenance")).effective.team_concurrency_overrides.some((item: { team_id: string }) => item.team_id === team.id)).toBe(false);
  } finally {
    await control("/config/ontology-maintenance", { items: original.items.map(({ key, value }: { key: string; value: string }) => ({ key, value })) });
  }
});

test("team overview distinguishes live topic projections, disabled history and unavailable status", async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${token}` };
  const configResponse = await request.get(`${controlURL}/control/api/config/ontology-maintenance`, { headers });
  expect(configResponse.ok()).toBeTruthy();
  const original = (await configResponse.json()).data;
  const teamsResponse = await request.get(`${controlURL}/control/api/teams`, { headers });
  expect(teamsResponse.ok()).toBeTruthy();
  const team = (await teamsResponse.json()).data[0];
  const statusPath = `/control/api/teams/${team.id}/community/status`;
  try {
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    for (const enabled of [true, false]) {
      const configured = await request.patch(`${controlURL}/control/api/config/ontology-maintenance`, { headers, data: { items: [{ key: "ONTOLOGY_MAINTENANCE_ENABLED", value: String(enabled) }] } });
      expect(configured.ok()).toBeTruthy();
      const response = page.waitForResponse((response) => new URL(response.url()).pathname === statusPath);
      await page.goto(`${controlURL}/?section=teams&team_id=${team.id}&team_tab=overview`);
      const status = (await (await response).json()).data as CommunityStatus;
      const row = page.locator(".metric-row").filter({ has: page.getByText(enabled ? "Topic projections" : "Community history", { exact: true }) });
      await expect(row).not.toContainText("not run");
      await expect(row.locator("strong")).toHaveText(String((enabled ? status.current_community_count : status.latest_run?.community_count) ?? "n/a"));
      if (enabled) {
        expect(status.latest_run).toBeUndefined();
        expect(status.projection_coverage).toBeTruthy();
        const coverage = status.projection_coverage!;
        if (coverage.coverage_complete && coverage.current_topics === 0) await expect(row).toContainText("No current topics");
        else if (!coverage.coverage_complete && coverage.total_topics === 0) await expect(row).toContainText("Pending discovery");
        else {
          for (const [count, label] of [[coverage.current_topics, "current"], [coverage.pending_topics, "pending"], [coverage.stale_topics, "stale"], [coverage.failed_topics, "failed"]]) await expect(row).toContainText(`${count} ${label}`);
        }
        if (coverage.failed_topics > 0) await expect(row).toHaveClass(/warning/);
      } else {
        await expect(row).toContainText(status.latest_run ? `Historical run: ${status.latest_run.status}` : "No historical runs");
      }
    }
    await page.route(`**${statusPath}`, (route) => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ message: "status unavailable" }) }));
    await page.reload();
    const row = page.locator(".metric-row").filter({ has: page.getByText("Community history", { exact: true }) });
    await expect(row).toContainText("Status unavailable");
    await expect(row.locator("strong")).toHaveText("n/a");
    await expect(row).toHaveClass(/warning/);
  } finally {
    await page.unroute(`**${statusPath}`);
    const restored = await request.patch(`${controlURL}/control/api/config/ontology-maintenance`, { headers, data: { items: original.items.map(({ key, value }: { key: string; value: string }) => ({ key, value })) } });
    expect(restored.ok()).toBeTruthy();
  }
});

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
  await expect(page.getByRole("group", { name: "Model capacity", exact: true })).toBeVisible();
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

test("operators configure context capacity and retired budget controls reject writes", async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${token}` };
  async function control(path: string, body?: unknown, method = body ? "PATCH" : "GET") {
    const response = await request.fetch(`${controlURL}/control/api${path}`, { method, headers, ...(body ? { data: body } : {}) });
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data;
  }
  const original = await control("/config/ontology-maintenance");
  const before = (await control("/ontology/status")).window;
  try {
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Ontology maintenance");
    await page.getByLabel("Model context window (tokens)").fill("262144");
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByText("Changes saved", { exact: true })).toBeVisible();
    expect((await control("/config/ontology-maintenance")).effective.context_window_tokens).toBe(262144);
    expect((await control("/ontology/status")).window.policy).toEqual(before.policy);
    await expectCompactNumberField(page, "Model context window (tokens)");
    await expect(page.getByRole("button", { name: "Add allowance now" })).toHaveCount(0);
    await expect(page.getByRole("progressbar", { name: "Input tokens", exact: true })).toHaveCount(0);
    const retired = await request.post(`${controlURL}/control/api/ontology/allowances`, { headers, data: { operation_key: randomUUID() } });
    expect(retired.status()).toBe(404);
    for (const [key, value] of [["ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS", "0"], ["ONTOLOGY_MAINTENANCE_INPUT_TOKENS", "1"], ["ONTOLOGY_MAINTENANCE_OUTPUT_TOKENS", "1"]]) {
      const rejected = await request.patch(`${controlURL}/control/api/config/ontology-maintenance`, { headers, data: { items: [{ key, value }] } });
      expect(rejected.status()).toBe(422);
    }
  } finally {
    await control("/config/ontology-maintenance", { items: original.items.map(({ key, value }: { key: string; value: string }) => ({ key, value })) });
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
