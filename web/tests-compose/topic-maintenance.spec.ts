import { navigate, openNavigation, confirmAction, expectPageContained, expectCompactNumberField } from "../tests-support/portal";
import { randomUUID } from "node:crypto";
import { expect, test, type APIRequestContext } from "@playwright/test";

const controlURL = process.env.DENSE_MEM_CONTROL_URL!;
const token = process.env.DENSE_MEM_CONTROL_TOKEN!;

const retryTest = test.extend<{ topicRequest: APIRequestContext }>({
  topicRequest: [async ({ playwright }, use) => {
    const request = await playwright.request.newContext();
    const headers = { Authorization: `Bearer ${token}` };
    try {
      const config = await request.get(`${controlURL}/control/api/config/topic-maintenance`, { headers });
      expect(config.ok()).toBeTruthy();
      const original = (await config.json()).data;
      const status = await request.get(`${controlURL}/control/api/topic-maintenance/status`, { headers });
      expect(status.ok()).toBeTruthy();
      const initial = (await status.json()).data;
      try {
        await use(request);
      } finally {
        const failures: unknown[] = [];
        for (const restore of [
          () => request.post(`${process.env.DENSE_MEM_E2E_PROVIDER_URL}/ontology-fixture`, { timeout: 5000, data: { mode: "normal" } }),
          () => request.post(`${controlURL}/control/api/topic-maintenance/${initial.paused ? "pause" : "resume"}`, { timeout: 5000, headers, data: { operation_key: randomUUID() } }),
          () => request.patch(`${controlURL}/control/api/config/topic-maintenance`, { timeout: 5000, headers, data: { items: original.items.map(({ key, value }: { key: string; value: string }) => ({ key, value })) } }),
        ]) {
          try { expect((await restore()).ok()).toBeTruthy(); }
          catch (error) { failures.push(error); }
        }
        if (failures.length) throw new AggregateError(failures, "Topic browser settings restoration failed");
      }
    } finally {
      await request.dispose();
    }
  }, { timeout: 30000 }],
});

test("operators save per-team concurrency for the next window and retain rejected drafts", async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${token}` };
  async function control(path: string, body?: unknown, method = body ? "PATCH" : "GET") {
    const response = await request.fetch(`${controlURL}/control/api${path}`, { method, headers, ...(body ? { data: body } : {}) });
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data;
  }
  const original = await control("/config/topic-maintenance");
  const before = await control("/topic-maintenance/status");
  const teams = await control("/teams?limit=10&offset=0");
  const team = teams[0];
  expect(team).toBeTruthy();
  try {
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Topic maintenance");
    await page.getByLabel("Team for override").selectOption(team.id);
    await page.getByLabel("Concurrency override").fill("9");
    await expect(page.getByRole("button", { name: "Stage team override" })).toBeDisabled();
    await page.getByLabel("Concurrency override").fill("3");
    await page.getByRole("button", { name: "Stage team override" }).click();
    await page.getByLabel("Default concurrency per team").fill("9");
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("invalid topic maintenance input");
    await expect(page.getByLabel("Default concurrency per team")).toHaveValue("9");
    await expect(page.getByText(/Staged: 3/)).toBeVisible();
    await page.getByLabel("Default concurrency per team").fill("2");
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByText("Changes saved", { exact: true })).toBeVisible();
    const saved = await control("/config/topic-maintenance");
    expect(saved.effective.team_max_concurrency).toBe(2);
    expect(saved.effective.team_concurrency_overrides).toContainEqual({ team_id: team.id, max_concurrency: 3 });
    await page.getByRole("button", { name: "Refresh topic coverage" }).click();
    await expect(page.getByText(/Staged: 3.*Saved: 3.*Current effective:/)).toBeVisible();
    expect((await control("/topic-maintenance/status")).window.policy).toEqual(before.window.policy);
    await expectPageContained(page);
    await page.getByRole("button", { name: `Use default for ${team.name}`, exact: true }).click();
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect.poll(async () => (await control("/config/topic-maintenance")).effective.team_concurrency_overrides.some((item: { team_id: string }) => item.team_id === team.id)).toBe(false);
  } finally {
    await control("/config/topic-maintenance", { items: original.items.map(({ key, value }: { key: string; value: string }) => ({ key, value })) });
  }
});



test("operators inspect topic coverage and pause or resume durable admission", async ({ page }) => {
  await page.goto(controlURL);
  await page.getByLabel("Control token").fill(token);
  await page.getByRole("button", { name: "Unlock" }).click();
  await navigate(page, "Topic maintenance");
  await expect(page.getByRole("heading", { name: "Topic assignment coverage" })).toBeVisible();
  await expect(page.getByLabel("Enable topic maintenance")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Pending policy", exact: true })).toBeVisible();
  await expect(page.getByRole("group", { name: "Schedule", exact: true })).toBeVisible();
  await expect(page.getByRole("group", { name: "Processing", exact: true })).toBeVisible();
  await expect(page.getByRole("group", { name: "Model capacity", exact: true })).toBeVisible();
  await expectCompactNumberField(page, "Cadence (hours)");
  await expectCompactNumberField(page, "Window start time");
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
  const original = await control("/config/topic-maintenance");
  const before = (await control("/topic-maintenance/status")).window;
  try {
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Topic maintenance");
    await page.getByLabel("Model context window (tokens)").fill("262144");
    await page.getByRole("button", { name: "Save config", exact: true }).click();
    await expect(page.getByText("Changes saved", { exact: true })).toBeVisible();
    expect((await control("/config/topic-maintenance")).effective.context_window_tokens).toBe(262144);
    expect((await control("/topic-maintenance/status")).window.policy).toEqual(before.policy);
    await expectCompactNumberField(page, "Model context window (tokens)");
    await expect(page.getByRole("button", { name: "Add allowance now" })).toHaveCount(0);
    await expect(page.getByRole("progressbar", { name: "Input tokens", exact: true })).toHaveCount(0);
    const retired = await request.post(`${controlURL}/control/api/topic-maintenance/allowances`, { headers, data: { operation_key: randomUUID() } });
    expect(retired.status()).toBe(404);
    for (const [key, value] of [["TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS", "0"], ["TOPIC_MAINTENANCE_INPUT_TOKENS", "1"], ["TOPIC_MAINTENANCE_OUTPUT_TOKENS", "1"]]) {
      const rejected = await request.patch(`${controlURL}/control/api/config/topic-maintenance`, { headers, data: { items: [{ key, value }] } });
      expect(rejected.status()).toBe(422);
    }
  } finally {
    await control("/config/topic-maintenance", { items: original.items.map(({ key, value }: { key: string; value: string }) => ({ key, value })) });
  }
});


retryTest("loaded older retry actions follow successful work and server eligibility", async ({ page, topicRequest: request }) => {
  test.setTimeout(300000);
  async function control(path: string, body?: unknown, method = body ? "POST" : "GET") {
    const response = await request.fetch(`${controlURL}/control/api${path}`, { method, headers: { Authorization: `Bearer ${token}` }, ...(body ? { data: body } : {}) });
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data;
  }
  async function fixtureMode(mode: string, evidence = "") {
    const response = await request.post(`${process.env.DENSE_MEM_E2E_PROVIDER_URL}/ontology-fixture`, { data: { mode, evidence } });
    expect(response.ok()).toBeTruthy();
  }
  const team = await control("/teams", { name: `Topic browser retry ${randomUUID()}` });
  const failed = await test.step("create retryable topic failure", async () => {
    await control("/topic-maintenance/pause", { operation_key: randomUUID() });
    await expect.poll(async () => (await control("/topic-maintenance/status")).active_work, { timeout: 30000 }).toBe(0);
    await control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }] }, "PATCH");
    const credential = await control(`/teams/${team.id}/credentials`, { name: "Topic browser retry", scopes: ["read", "write"], rate_limit: 300 });
    const evidence = `A constellation observation for browser retry ${randomUUID()}.`;
    await fixtureMode("ambiguous");
    const submitted = await request.post(`${process.env.DENSE_MEM_USER_URL}/mcp`, {
      headers: { Authorization: `Bearer ${credential.api_key}`, Accept: "application/json" },
      data: { jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "remember", arguments: {
        target_space: "team_shared", idempotency_key: randomUUID(), evidence: [{ content: evidence, source_type: "document", source: "topic-browser-followups", source_group: randomUUID() }],
      } } },
    });
    expect(submitted.status()).toBe(200);
    const rpc = await submitted.json();
    const remembered = JSON.parse(rpc.result.content[0].text);
    expect(rpc.result?.isError, JSON.stringify({ processing_state: remembered.processing_state,
      errors: (remembered.errors || []).map(({ code, reason_code, retryable, next_action }: { code: string; reason_code?: string; retryable: boolean; next_action: string }) => ({ code, reason_code, retryable, next_action })) })).not.toBe(true);
    expect(remembered.processing_state).toBe("completed");
    const evidenceID = remembered.evidence[0].evidence_id;
    await fixtureMode("normal");
    await control("/topic-maintenance/resume", { operation_key: randomUUID() });
    await expect.poll(async () => (await control("/topic-maintenance/status")).coverage_complete, { timeout: 120000 }).toBe(true);
    await fixtureMode("fail-three-then-success", evidence);
    const failedCommand = await control(`/teams/${team.id}/topic-maintenance/reassess`, { operation_key: randomUUID(), evidence_ids: [evidenceID] });
    await expect.poll(async () => {
      const history = await control("/topic-maintenance/runs?limit=200");
      return history.runs.find((run: { id: string }) => run.id === failedCommand.id)?.status;
    }, { timeout: 120000 }).toBe("incomplete");
    const history = await control("/topic-maintenance/runs?limit=200");
    const failed = history.runs.find((run: { id: string }) => run.id === failedCommand.id);
    expect(failed.retryable, JSON.stringify({ status: failed.status, failure_code: failed.failure_code, completed_batches: failed.completed_batches })).toBe(true);
    expect(failed.failure_code).toBeTruthy();
    await fixtureMode("normal");
    return failed;
  });
  await test.step("inspect older failure diagnostics and retry eligibility", async () => {
    await control("/topic-maintenance/pause", { operation_key: randomUUID() });
    for (let index = 0; index < 52; index += 1) await control("/topic-maintenance/pause", { operation_key: randomUUID() });
    await page.goto(controlURL);
    await page.getByLabel("Control token").fill(token);
    await page.getByRole("button", { name: "Unlock" }).click();
    await navigate(page, "Topic maintenance");
    const row = page.locator("tr").filter({ has: page.locator(`td[title="${failed.id}"]`) });
    await expect(page.getByRole("button", { name: "Load older runs" })).toBeVisible();
    await expect(row).toHaveCount(0);
    for (let index = 0; index < 4 && await row.count() === 0; index += 1) {
      await page.getByRole("button", { name: "Load older runs" }).click();
      await expect(page.getByRole("button", { name: "Refresh topic coverage" })).toBeEnabled();
    }
    await expect(row).toHaveCount(1);
    await expect(row.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    await expect.poll(async () => (await control(`/logs?event=topic_failure_capture&reference_type=topic_run&reference_id=${failed.id}&team_id=${team.id}`)).length,
      { timeout: 20000, message: "failed topic work must retain a complete diagnostic manifest" }).toBeGreaterThan(0);
    const payloadOffsets: number[] = [];
    let expiredCapture = false;
    const diagnosticRoute = "**/control/api/logs?**";
    await page.route(diagnosticRoute, async (route) => {
      const url = new URL(route.request().url());
      if (url.searchParams.get("event") !== "topic_failure_payload") return route.continue();
      expect(url.searchParams.get("reference_type")).toBe("topic_run");
      expect(url.searchParams.get("reference_id")).toBe(failed.id);
      expect(url.searchParams.get("team_id")).toBe(team.id);
      expect(url.searchParams.get("attempt_id")).toBeTruthy();
      payloadOffsets.push(Number(url.searchParams.get("offset") || 0));
      url.searchParams.set("limit", "2");
      if (expiredCapture) url.searchParams.set("attempt_id", randomUUID());
      await route.fulfill({ response: await route.fetch({ url: url.toString() }) });
    });
    const diagnosticTrigger = row.getByRole("button", { name: "View failure diagnostics" });
    const diagnosticsDialog = page.getByRole("dialog", { name: /Failure diagnostics/ });
    try {
      await diagnosticTrigger.click();
      await expect(diagnosticsDialog.getByRole("button", { name: "Close failure diagnostics" })).toBeFocused();
      const diagnosticsWidth = Math.min(760, (await page.evaluate(() => window.innerWidth)) - 24);
      await expect(diagnosticsDialog).toHaveCSS("width", `${diagnosticsWidth}px`);
      const diagnostics = page.getByRole("region", { name: "Topic maintenance failure diagnostics" });
      const inspect = diagnostics.getByRole("button", { name: /View failed assessment/ }).first();
      await inspect.click();
      await expect(diagnostics.locator(".remember-diagnostic-exchange")).toHaveCount(3);
      const wireRequest = JSON.parse((await diagnostics.locator(".remember-diagnostic-body pre").first().textContent())!);
      const wireResponse = JSON.parse((await diagnostics.locator(".remember-diagnostic-body pre").nth(1).textContent())!);
      const schema = wireRequest.response_format.json_schema.name;
      const invalid = JSON.parse(wireResponse.choices[0].message.content);
      expect(["topic_assignment_v1", "evidence_equivalence_v1"]).toContain(schema);
      if (schema === "topic_assignment_v1") {
        expect(invalid.request_id).toBe("wrong-request");
      } else {
        expect(invalid.equivalence).toEqual([]);
      }
      expect(payloadOffsets.length).toBeGreaterThan(1);
      expect(payloadOffsets).toEqual(payloadOffsets.map((_, index) => index * 2));
      await expectPageContained(page);
      const denied = await request.get(`${controlURL}/control/api/logs?event=topic_failure_capture`, { headers: { Authorization: `Bearer ${randomUUID()}` } });
      expect(denied.status()).toBe(401);
      expiredCapture = true;
      await inspect.click();
      await expect(diagnostics.getByRole("alert")).toContainText("incomplete or expired");
      await expect(diagnostics.locator(".remember-diagnostic-body")).toHaveCount(0);
    } finally {
      await page.unroute(diagnosticRoute);
    }
    await page.keyboard.press("Escape");
    await expect(diagnosticsDialog).toBeHidden();
    await expect(diagnosticTrigger).toBeFocused();
    await control("/topic-maintenance/resume", { operation_key: randomUUID() });
    await page.getByRole("button", { name: "Refresh topic coverage" }).click();
    await expect(row.getByRole("button", { name: "Retry failed work" })).toBeEnabled();
    const visibleRuns = page.locator("td[title]");
    const visibleIds = await visibleRuns.evaluateAll((cells) => cells.map((cell) => cell.getAttribute("title")));
    const historyRoute = "**/control/api/topic-maintenance/runs?**";
    await page.route(historyRoute, async (route) => {
      if (route.request().method() === "GET" && new URL(route.request().url()).searchParams.has("cursor")) {
        await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ message: "older history refresh unavailable" }) });
      } else {
        await route.continue();
      }
    });
    await page.getByRole("button", { name: "Refresh topic coverage" }).click();
    const refreshFailure = page.getByRole("alert").filter({ hasText: "older history refresh unavailable" });
    await expect(refreshFailure).toBeVisible();
    expect(await visibleRuns.evaluateAll((cells) => cells.map((cell) => cell.getAttribute("title")))).toEqual(visibleIds);
    await expect(row.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    for (const retryButton of await page.getByRole("button", { name: "Retry failed work" }).all()) await expect(retryButton).toBeDisabled();
    await page.unroute(historyRoute);
    await page.getByRole("button", { name: "Refresh topic coverage" }).click();
    await expect(refreshFailure).toHaveCount(0);
    await expect(row.getByRole("button", { name: "Retry failed work" })).toBeEnabled();
    const acceptance = page.waitForResponse((response) => response.request().method() === "POST" && response.url().endsWith(`/topic-maintenance/runs/${failed.id}/retry`));
    await row.getByRole("button", { name: "Retry failed work" }).click();
    const accepted = await acceptance;
    expect(accepted.status()).toBe(200);
    const retry = (await accepted.json()).data;
    await expect.poll(async () => {
      const page = await control("/topic-maintenance/runs?limit=200");
      return page.runs.find((run: { id: string }) => run.id === retry.id)?.status;
    }, { timeout: 120000 }).toBe("completed");
    await expect(row.getByRole("button", { name: "Retry failed work" })).toHaveCount(0, { timeout: 20000 });
    await expect(row).toHaveCount(1);
    const updated = await control("/topic-maintenance/runs?limit=200");
    const older = updated.runs.find((run: { id: string }) => run.id === failed.id);
    expect(older.retryable).toBe(false);
    expect(older.failure_code).toBe(failed.failure_code);
    const completed = updated.runs.find((run: { id: string }) => run.id === retry.id);
    expect(completed.failure_code || "").toBe("");
    const rejected = await request.post(`${controlURL}/control/api/topic-maintenance/runs/${failed.id}/retry`, { headers: { Authorization: `Bearer ${token}` }, data: { operation_key: randomUUID(), max_batches: 1 } });
    expect(rejected.status()).toBe(422);
  });
});

retryTest("operators reassess selected uncategorized Evidence", async ({ page, topicRequest: request }) => {
  test.setTimeout(180000);
  const headers = { Authorization: `Bearer ${token}` };
  async function control(path: string, body?: unknown, method = body ? "POST" : "GET") {
    const response = await request.fetch(`${controlURL}/control/api${path}`, { method, headers, ...(body ? { data: body } : {}) });
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data;
  }
  await control("/topic-maintenance/pause", { operation_key: randomUUID() });
  await control("/config/topic-maintenance", { items: [{ key: "TOPIC_MAINTENANCE_ENABLED", value: "true" }, { key: "TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "300000" }] }, "PATCH");
  const team = await control("/teams", { name: `Reassessment ${randomUUID()}` });
  const credential = await control(`/teams/${team.id}/credentials`, { name: "Reassessment browser", scopes: ["read", "write"], rate_limit: 300 });
  const mode = (value: string) => request.post(`${process.env.DENSE_MEM_E2E_PROVIDER_URL}/ontology-fixture`, { data: { mode: value } });
  expect((await mode("ambiguous")).ok()).toBeTruthy();
  const response = await request.post(`${process.env.DENSE_MEM_USER_URL}/mcp`, {
    headers: { Authorization: `Bearer ${credential.api_key}`, Accept: "application/json" },
    data: { jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: { name: "remember", arguments: {
      target_space: "team_shared", idempotency_key: randomUUID(), evidence: [{ content: `A constellation observation ${randomUUID()}.`, source_type: "manual" }],
    } } },
  });
  expect(response.status()).toBe(200);
  const rpc = await response.json();
  const remembered = JSON.parse(rpc.result.content[0].text);
  expect(remembered.processing_state).toBe("completed");
  const evidenceID = remembered.evidence[0].evidence_id;
  expect((await mode("normal")).ok()).toBeTruthy();
  await control("/topic-maintenance/resume", { operation_key: randomUUID() });
  await page.goto(controlURL);
  await page.getByLabel("Control token").fill(token);
  await page.getByRole("button", { name: "Unlock" }).click();
  await navigate(page, "Topic maintenance");
  await page.getByLabel("Team ID", { exact: true }).fill(team.id);
  const evidence = page.getByLabel("Evidence IDs");
  const submit = page.getByRole("button", { name: "Reassess selected Evidence" });
  await evidence.fill(`${evidenceID}, ${evidenceID}`);
  await expect(submit).toBeDisabled();
  await evidence.fill(`${evidenceID}, ${randomUUID()}`);
  const rejected = page.waitForResponse((item) => item.request().method() === "POST" && item.url().endsWith(`/teams/${team.id}/topic-maintenance/reassess`));
  await submit.click();
  expect((await rejected).status()).toBe(422);
  await evidence.fill(evidenceID);
  const accepted = page.waitForResponse((item) => item.request().method() === "POST" && item.url().endsWith(`/teams/${team.id}/topic-maintenance/reassess`));
  await submit.click();
  const acceptedResponse = await accepted;
  expect(acceptedResponse.status()).toBe(200);
  const command = acceptedResponse.request().postDataJSON();
  const run = (await acceptedResponse.json()).data;
  expect(command.evidence_ids).toEqual([evidenceID]);
  const replay = await control(`/teams/${team.id}/topic-maintenance/reassess`, command);
  expect(replay.id).toBe(run.id);
  await expect(page.getByRole("status")).toContainText(`Reassessment ${run.id.slice(0, 8)} accepted`);
  await expect.poll(async () => {
    const history = await control("/topic-maintenance/runs?limit=100");
    return history.runs.find((item: { id: string }) => item.id === run.id)?.status;
  }, { timeout: 120000 }).toBe("completed");
  await expectPageContained(page);
});
