import { expect, type APIRequestContext, type Locator, type Page, test } from "@playwright/test";

const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const userUrl = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seedTeamId = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const seedTeamName = requiredEnv("DENSE_MEM_E2E_TEAM_NAME");
const seedApiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const dreamStatement = requiredEnv("DENSE_MEM_E2E_DREAM_STATEMENT");
const prometheusUrl = requiredEnv("DENSE_MEM_PROMETHEUS_URL").replace(/\/$/, "");
const graphAnchorEntityId = process.env.DENSE_MEM_E2E_GRAPH_ANCHOR_ENTITY_ID ?? "";
const graphOriginalObjectEntityId = process.env.DENSE_MEM_E2E_GRAPH_ORIGINAL_OBJECT_ENTITY_ID ?? "";
const graphCorrectedObjectEntityId = process.env.DENSE_MEM_E2E_GRAPH_CORRECTED_OBJECT_ENTITY_ID ?? "";
const graphOriginalRelationshipId = process.env.DENSE_MEM_E2E_GRAPH_ORIGINAL_RELATIONSHIP_ID ?? "";
const graphSuccessorRelationshipId = process.env.DENSE_MEM_E2E_GRAPH_SUCCESSOR_RELATIONSHIP_ID ?? "";

type CreatedProfile = {
  api_key: string;
  key: {
    id: string;
    team_id: string;
    name: string;
    key_suffix: string;
    scopes: string[];
    rate_limit: number;
  };
};

type PrometheusQueryResponse = {
  status?: string;
  data?: {
    result?: Array<{
      value?: [number, string];
    }>;
  };
};

type TelemetryResponse = {
  data?: {
    available?: boolean;
    scope?: {
      type?: string;
      team_id?: string;
      profile_id?: string;
    };
    cards?: unknown;
    windowed_cards?: unknown;
    current_cards?: unknown;
    series?: unknown;
    activity_series?: unknown;
    state_series?: unknown;
  };
};

type UserSessionResponse = {
  data?: {
    auth_method?: string;
    can_manage_team?: boolean;
    key?: {
      id?: string;
      name?: string;
      scopes?: string[];
      role?: string;
    };
    team?: {
      id?: string;
    };
  };
};

type TelemetryCard = {
  id?: string;
  label?: string;
  value?: number;
  unit?: string;
};

test("control panel logs in against compose and creates a team", async ({ page }, testInfo) => {
  await openControlPanel(page);
  await expect(page.getByRole("button", { name: new RegExp(escapeRegExp(seedTeamName)) })).toBeVisible();

  const teamName = uniqueName("Compose Team", testInfo);
  await page.getByRole("button", { name: "New Team" }).click();
  await page.getByLabel("Name").first().fill(teamName);
  await page.getByLabel("Description").first().fill("created by compose e2e");
  await page.getByRole("button", { name: /^Create$/ }).click();

  await expect(page.getByRole("heading", { name: teamName })).toBeVisible();
});

test("control panel rejects an invalid control token", async ({ page }) => {
  await page.goto(`${controlUrl}/`);

  await page.getByLabel("Control token").fill("wrong-token");
  await page.getByRole("button", { name: "Unlock" }).click();

  await expect(page.getByRole("alert")).toContainText(/invalid/i);
});

test("control panel shows operational metrics against compose", async ({ page }) => {
  await openControlPanel(page);

  await page.getByRole("button", { name: /^Metrics$/ }).click();

  await expect(page.getByRole("heading", { name: "Usage Rollup" })).toBeVisible();
  await expect(page.getByLabel("Request metrics")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Dependencies" })).toBeVisible();
  await expect(page.getByText("postgres", { exact: true })).toBeVisible();
  await expect(page.getByText("redis", { exact: true })).toBeVisible();

  await page.getByLabel("Window").selectOption("360");
  await page.getByLabel("Team", { exact: true }).selectOption(seedTeamId);
  await expect(page.getByRole("heading", { name: "Usage Rollup" })).toBeVisible();
  await expectNoShellOverlap(page);
});

test("control overview contains every Top Signals diagnostic", async ({ page }) => {
  await openControlPanel(page);
  await page.getByRole("button", { name: new RegExp(escapeRegExp(seedTeamName)) }).click();

  const topSignals = page.getByLabel("Top signals");
  await expect(topSignals).toBeVisible();
  const rows = topSignals.locator(".metric-row");
  await expect(rows).toHaveCount(4);
  for (let index = 0; index < await rows.count(); index += 1) {
    const row = rows.nth(index);
    await expectMetricDetailContained(row, row.locator(".metric-detail"));
  }
});

test("control panel keeps security settings display-only against compose", async ({ page }) => {
  await openControlPanel(page);

  await page.getByRole("button", { name: /IP Bans/ }).click();

  await expect(page.getByRole("heading", { name: "IP Bans" })).toBeVisible();
  await expect(page.getByLabel("IP ban rules")).toContainText("Protection");
  await expect(page.getByLabel("IP ban rules")).toContainText("Threshold");
  await expect(page.getByRole("button", { name: /Save settings/ })).toHaveCount(0);
  await expectNoShellOverlap(page);
});

test("control panel loads team Dreams without re-evaluation", async ({ page }) => {
  const refreshRequests: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === `/control/api/teams/${seedTeamId}/dreams/refresh`) {
      refreshRequests.push(request.url());
    }
  });

  await openControlPanel(page);
  await page.getByRole("button", { name: new RegExp(escapeRegExp(seedTeamName)) }).click();
  await page.getByRole("button", { name: /team dreams/i }).click();

  await expect(page.getByRole("heading", { name: "Dream Outputs" })).toBeVisible();
  await expect(page.getByText(dreamStatement, { exact: true })).toBeVisible();
  await expect(page.getByText(/authenticated actor context is required/i)).toHaveCount(0);
  expect(refreshRequests).toEqual([]);
});

test("prometheus telemetry is scraped and rendered in control panel and user portal", async ({ page, request }) => {
  const sessionResponse = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(seedApiKey) });
  expect(sessionResponse.status()).toBe(200);
  const sessionBody = await sessionResponse.json() as UserSessionResponse;
  expect(sessionBody.data?.key?.scopes).toEqual(expect.arrayContaining(["write"]));
  const expectedScope = sessionBody.data?.can_manage_team ? "team" : "self";
  const expectedUsageTitle = sessionBody.data?.can_manage_team ? "Team usage" : "My key usage";

  await expect.poll(
    () => prometheusResultCount(request, `densemem_http_requests_total{route="/ui/api/session"}`),
    {
      intervals: [1_000, 5_000, 10_000],
      timeout: 120_000,
    },
  ).toBeGreaterThan(0);

  const telemetryResponse = await request.get(`${userUrl}/ui/api/telemetry?window=15m`, { headers: bearer(seedApiKey) });
  expect(telemetryResponse.status()).toBe(200);
  const telemetryBody = await telemetryResponse.json() as TelemetryResponse;
  expect(telemetryBody.data?.available).toBe(true);
  expect(telemetryBody.data?.scope?.type).toBe(expectedScope);
  expect(telemetryBody.data?.scope?.team_id).toBe(sessionBody.data?.team?.id);
  if (expectedScope === "self") {
    expect(telemetryBody.data?.scope?.profile_id).toBe(sessionBody.data?.key?.id);
  }
  expect(Array.isArray(telemetryBody.data?.cards)).toBe(true);
  expect(Array.isArray(telemetryBody.data?.windowed_cards)).toBe(true);
  expect(Array.isArray(telemetryBody.data?.current_cards)).toBe(true);
  assertTelemetrySeries(telemetryBody);
  const windowedCardLabels = telemetryLabels(telemetryBody.data?.windowed_cards);
  const currentCardLabels = telemetryLabels(telemetryBody.data?.current_cards);
  const activitySeriesLabels = telemetryLabels(telemetryBody.data?.activity_series);
  const stateSeriesLabels = telemetryLabels(telemetryBody.data?.state_series);
  const readyWindowedCardLabels = telemetryReadyLabels(telemetryBody.data?.windowed_cards);
  const readyActivitySeriesLabels = telemetryReadyLabels(telemetryBody.data?.activity_series);
  expect(windowedCardLabels.length).toBeGreaterThan(0);
  expect(currentCardLabels).toContain("Relationships: active");
  expect(activitySeriesLabels.length).toBeGreaterThan(0);
  expect(stateSeriesLabels).toEqual([]);

  await openControlPanel(page);
  await page.getByRole("button", { name: /^Metrics$/ }).click();
  await expect(page.getByRole("heading", { name: "Telemetry" })).toBeVisible();
  await expect(page.getByLabel("Telemetry totals")).toContainText("HTTP requests");
  await expect(page.getByLabel("Telemetry charts")).toContainText("HTTP requests");
  await expect(page.getByLabel("Telemetry current state")).toContainText("Relationships: active");
  await expect(page.getByLabel("Telemetry state history")).toHaveCount(0);

  await openUserPortal(page, seedApiKey);
  await page.getByRole("button", { name: "Usage" }).click();
  for (const label of readyWindowedCardLabels) {
    await expect(page.getByLabel(`${expectedUsageTitle} totals`)).toContainText(label);
  }
  for (const label of readyActivitySeriesLabels) {
    await expect(page.getByLabel(`${expectedUsageTitle} charts`)).toContainText(label);
  }
  await expect(page.getByLabel(`${expectedUsageTitle} current state`)).toContainText("Relationships: active");
  await expect(page.getByLabel(`${expectedUsageTitle} state history`)).toHaveCount(0);
  await expectNoShellOverlap(page);
});

test("MCP supersedes and retracts caller-owned evidence against compose", async ({ request }) => {
  const runID = `compose-evidence-lifecycle-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const toolsResponse = await mcpCall(request, "tools/list", {});
  expect(mcpToolNames(toolsResponse)).toEqual(expect.arrayContaining(["remember", "retract_evidence"]));
  const originalContent = `The deployment protects a prompt for ${runID}.`;
  const replacementContent = `The deployment protects a replacement prompt for ${runID}.`;

  const original = mcpToolPayload(await mcpCall(request, "tools/call", {
    name: "remember",
    arguments: {
      evidence: [{
        content: originalContent,
        source_type: "manual",
        idempotency_key: `${runID}:original`,
      }],
      relationships: [securityRelationship(originalContent, `${runID}:original-relationship`)],
    },
  }));
  expect(original.processing_state).toBe("queued");
  const originalSubmissionID = requiredString(original, "submission_id");
  const originalItem = await waitForSubmissionEvidence(request, originalSubmissionID);
  const originalEvidenceID = requiredString(originalItem, "evidence_id");

  const replacement = mcpToolPayload(await mcpCall(request, "tools/call", {
    name: "remember",
    arguments: {
      evidence: [{
        content: replacementContent,
        source_type: "manual",
        supersedes_evidence_ids: [originalEvidenceID],
        idempotency_key: `${runID}:replacement`,
      }],
      relationships: [securityRelationship(replacementContent, `${runID}:replacement-relationship`)],
    },
  }));
  expect(replacement.processing_state).toBe("queued");
  const replacementItem = await waitForSubmissionEvidence(request, requiredString(replacement, "submission_id"));
  const replacementEvidenceID = requiredString(replacementItem, "evidence_id");
  expect(replacementItem.superseded_evidence_ids).toEqual([originalEvidenceID]);

  const retractionArgs = {
    evidence_ids: [replacementEvidenceID],
    reason: "compose e2e retraction",
    idempotency_key: `${runID}:retract`,
  };
  const retraction = mcpToolPayload(await mcpCall(request, "tools/call", {
    name: "retract_evidence",
    arguments: retractionArgs,
  }));
  expect(retraction.processing_state).toBe("completed");
  expect(retraction.retracted_evidence_ids).toEqual([replacementEvidenceID]);

  const replay = mcpToolPayload(await mcpCall(request, "tools/call", {
    name: "retract_evidence",
    arguments: retractionArgs,
  }));
  expect(replay.decision_id).toBe(retraction.decision_id);
});

test("MCP recall feedback is submitted and surfaced through compose telemetry", async ({ page, request }) => {
  const recallFeedbackWasEnabled = await recallFeedbackEnabled(request);
  await setRecallFeedback(request, true);

  try {
    await mcpCall(request, "initialize", {
      protocolVersion: "2024-11-05",
      capabilities: {},
      clientInfo: { name: "compose-recall-feedback", version: "1.0.0" },
    });

    const toolsResponse = await mcpCall(request, "tools/list", {});
    expect(mcpToolNames(toolsResponse)).toEqual(expect.arrayContaining(["recall_memory", "submit_recall_session_feedback"]));

    const recallPayload = mcpToolPayload(await mcpCall(request, "tools/call", {
      name: "recall_memory",
      arguments: {
        query: "compose e2e recall feedback",
        limit: 5,
      },
    }));

    expect(Array.isArray(recallPayload.results)).toBe(true);
    expect(typeof recallPayload.recall_id).toBe("string");
    expect(String(recallPayload.recall_id)).toMatch(/^rec_/);

    const submitPayload = mcpToolPayload(await mcpCall(request, "tools/call", {
      name: "submit_recall_session_feedback",
      arguments: {
        recalls: [{
          recall_event_id: recallPayload.recall_id,
          used: true,
          answer_supported: true,
          quality: "high",
          missing_context: false,
          irrelevant: false,
        }],
      },
    }));
    expect(submitPayload.recorded).toBe(true);
    expect(submitPayload.recorded_count).toBe(1);

    await expect.poll(
      () => prometheusQueryValue(request, `sum(densemem_recall_feedback_total{used="true",answer_supported="true",quality="high",missing_context="false",irrelevant="false"})`),
      {
        intervals: [1_000, 5_000, 10_000],
        timeout: 120_000,
      },
    ).toBeGreaterThan(0);

    await expect.poll(
      () => prometheusQueryValue(request, "sum(densemem_recall_feedback_quality_score_count)"),
      {
        intervals: [1_000, 5_000, 10_000],
        timeout: 120_000,
      },
    ).toBeGreaterThan(0);

    const telemetryResponse = await request.get(`${userUrl}/ui/api/telemetry?window=15m`, { headers: bearer(seedApiKey) });
    expect(telemetryResponse.status()).toBe(200);
    const telemetryBody = await telemetryResponse.json() as TelemetryResponse;
    expect(telemetryLabels(telemetryBody.data?.cards)).toEqual(expect.arrayContaining([
      "LLM recall used",
      "LLM answer supported",
      "LLM recall quality",
      "LLM missing context",
      "LLM irrelevant recall",
    ]));

    await expect.poll(
      () => telemetryCardValue(request, "llm_recall_used_rate").catch(() => -1),
      {
        intervals: [1_000, 5_000, 10_000],
        timeout: 120_000,
      },
    ).toBe(100);
    await expect.poll(
      () => telemetryCardValue(request, "llm_recall_answer_supported_rate").catch(() => -1),
      {
        intervals: [1_000, 5_000, 10_000],
        timeout: 120_000,
      },
    ).toBe(100);
    await expect.poll(
      () => telemetryCardValue(request, "llm_recall_quality_score").catch(() => -1),
      {
        intervals: [1_000, 5_000, 10_000],
        timeout: 120_000,
      },
    ).toBe(100);

    const finalTelemetry = await userTelemetry(request);
    expect(cardValue(finalTelemetry, "llm_recall_missing_context_rate")).toBe(0);
    expect(cardValue(finalTelemetry, "llm_recall_irrelevant_rate")).toBe(0);

    await openUserPortal(page, seedApiKey);
    await page.getByRole("button", { name: "Usage" }).click();
    const usageTitle = await userUsageTitle(request, seedApiKey);
    const usageTotals = page.getByLabel(`${usageTitle} totals`);
    await expect(usageTotals).toContainText("LLM recall used");
    await expect(usageTotals).toContainText("LLM answer supported");
    await expect(usageTotals).toContainText("LLM recall quality");
    await expect(usageTotals).toContainText("100%");
  } finally {
    await setRecallFeedback(request, recallFeedbackWasEnabled);
  }
});

test("user portal logs in with a real API key and shows only that profile", async ({ page, request }, testInfo) => {
  const otherProfile = await createTeamProfile(request, uniqueName("Other profile", testInfo), ["read"]);
  const sessionResponse = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(seedApiKey) });
  expect(sessionResponse.status()).toBe(200);
  const sessionBody = await sessionResponse.json() as UserSessionResponse;
  const seedProfileName = sessionBody.data?.key?.name;
  if (!seedProfileName) {
    throw new Error("seed API key session did not return a profile name");
  }

  await openUserPortal(page, seedApiKey);

  await expect(page.getByText(seedTeamName)).toBeVisible();
  await expect(page.getByLabel("Current workspace")).not.toContainText(seedProfileName);
  await page.getByRole("button", { name: /My key/i }).click();
  await expect(page.getByText(seedProfileName, { exact: true })).toBeVisible();
  await expect(page.getByText(otherProfile.key.name)).toBeHidden();

  await page.getByRole("button", { name: "Recall" }).click();
  await expect(page.getByLabel("Knowledge explorer")).toBeVisible();
  await expect(page.getByText("No recall results")).toBeVisible();

  await page.getByRole("button", { name: "Graph" }).click();
  await expect(page.getByLabel("Knowledge graph")).toBeVisible();
  await expect(page.getByLabel("Graph totals")).toContainText(/Nodes\s*[1-9]/);

  await page.getByRole("button", { name: "Dreams" }).click();
  await expect(page.getByRole("heading", { name: "Dream Outputs" })).toBeVisible();
  await expect(page.getByText(dreamStatement, { exact: true })).toBeVisible();
});

test("user portal renders the corrected live graph with depth five and an uncapped limit", async ({ page }) => {
  test.skip(
    !graphAnchorEntityId || !graphOriginalObjectEntityId || !graphCorrectedObjectEntityId || !graphOriginalRelationshipId || !graphSuccessorRelationshipId,
    "submission-status graph fixture is required",
  );
  await openUserPortal(page, seedApiKey);
  await page.getByRole("button", { name: "Graph" }).click();

  const controls = page.getByLabel("Graph controls");
  await expect(controls.getByLabel("Relationship limit")).toHaveValue("80");
  await expect(controls.getByLabel("Depth")).toHaveValue("2");
  await expect(controls.getByLabel("Depth")).toHaveAttribute("max", "5");
  await controls.getByRole("button", { name: "Local" }).click();
  await controls.getByLabel("Anchor ID").fill(graphAnchorEntityId);
  await controls.getByLabel("Depth").focus();
  await controls.getByLabel("Depth").press("End");
  await expect(controls.getByLabel("Depth")).toHaveValue("5");
  await controls.getByLabel("Relationship limit").fill("181");

  const graphResponsePromise = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/ui/api/graph" && url.searchParams.get("depth") === "5" && url.searchParams.get("limit") === "181";
  });
  await controls.getByRole("button", { name: "Refresh" }).click();
  const graphResponse = await graphResponsePromise;
  expect(graphResponse.status()).toBe(200);
  expect(graphResponse.headers()["cache-control"]).toBe("no-store");
  const graph = (await graphResponse.json() as {
    data: {
      depth: number;
      limit: number;
      nodes: Array<{ id: string }>;
      edges: Array<{ id: string }>;
    };
  }).data;
  expect(graph.depth).toBe(5);
  expect(graph.limit).toBe(181);
  expect(graph.nodes.map((node) => node.id)).toContain(graphCorrectedObjectEntityId);
  expect(graph.nodes.map((node) => node.id)).not.toContain(graphOriginalObjectEntityId);
  expect(graph.edges.map((edge) => edge.id)).toContain(graphSuccessorRelationshipId);
  expect(graph.edges.map((edge) => edge.id)).not.toContain(graphOriginalRelationshipId);
  await expect(page.getByLabel("Graph totals")).toContainText(new RegExp(`Nodes\\s*${graph.nodes.length}`));
  await expect(page.getByLabel("Graph totals")).toContainText(new RegExp(`Edges\\s*${graph.edges.length}`));
  await expect(page.locator(".graph-canvas canvas").first()).toBeVisible();
});

test("read-only user key cannot regenerate itself", async ({ page, request }, testInfo) => {
  const readOnly = await createTeamProfile(request, uniqueName("Read only", testInfo), ["read"]);

  await openUserPortal(page, readOnly.api_key);
  await expect(page.getByRole("button", { name: "Usage" })).toHaveCount(0);
  await page.getByRole("button", { name: /My key/i }).click();

  await expect(page.getByRole("button", { name: /Regenerate key/i })).toBeDisabled();

  const telemetryResponse = await request.get(`${userUrl}/ui/api/telemetry?window=15m`, { headers: bearer(readOnly.api_key) });
  expect(telemetryResponse.status()).toBe(403);
});

test("write user key regenerates itself and invalidates the old key", async ({ page, request }, testInfo) => {
  const writable = await createTeamProfile(request, uniqueName("Writable", testInfo), ["read", "write"]);

  await openUserPortal(page, writable.api_key);
  await page.getByRole("button", { name: /My key/i }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Regenerate key/i }).click();

  const rotatedKey = page.getByLabel("Generated API key");
  await expect(rotatedKey).toHaveValue(/^dm_/);
  const newApiKey = await rotatedKey.inputValue();
  expect(newApiKey).not.toBe(writable.api_key);
  await expect
    .poll(() => page.evaluate(() => sessionStorage.getItem("denseMem.userApiKey")))
    .toBeNull();

  const oldSession = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(writable.api_key) });
  expect(oldSession.status()).toBe(401);

  const newSession = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(newApiKey) });
  expect(newSession.status()).toBe(200);
});

test("remembered API-key login uses a seven-day server session", async ({ page, request, browser }, testInfo) => {
  await setSSOCookieSecure(request, false);
  const writable = await createTeamProfile(request, uniqueName("Cookie session", testInfo), ["read", "write"]);

  await page.goto(`${userUrl}/ui/`);
  await page.getByLabel("API key").fill(writable.api_key);
  await page.getByRole("checkbox", { name: /7 days/i }).check();
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Knowledge", exact: true })).toBeVisible();

  const cookies = await page.context().cookies(`${userUrl}/ui/`);
  const sessionCookie = cookies.find((cookie) => cookie.name === "dense_mem_ui_session");
  const csrfCookie = cookies.find((cookie) => cookie.name === "dense_mem_ui_csrf");
  expect(sessionCookie).toBeDefined();
  expect(sessionCookie?.path).toBe("/ui");
  expect(sessionCookie?.httpOnly).toBe(true);
  expect(sessionCookie?.secure).toBe(false);
  expect(sessionCookie?.sameSite).toBe("Lax");
  expect((sessionCookie?.expires ?? 0) - Date.now() / 1000).toBeGreaterThan(6 * 24 * 60 * 60);
  expect(csrfCookie?.path).toBe("/ui");
  expect(csrfCookie?.httpOnly).toBe(false);
  expect(await page.evaluate(() => sessionStorage.getItem("denseMem.userApiKey"))).toBeNull();

  const storageState = await page.context().storageState();
  const freshContext = await browser.newContext({ storageState });
  const freshPage = await freshContext.newPage();
  try {
    await freshPage.goto(`${userUrl}/ui/`);
    await expect(freshPage.getByRole("heading", { name: "Knowledge", exact: true })).toBeVisible();
    expect(await freshPage.evaluate(() => sessionStorage.getItem("denseMem.userApiKey"))).toBeNull();
  } finally {
    await freshContext.close();
  }

  const missingCsrfStatus = await page.evaluate(async () => {
    const response = await fetch("/ui/api/key/rotate", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    return response.status;
  });
  expect(missingCsrfStatus).toBe(403);

  await page.getByRole("button", { name: /My key/i }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: /Regenerate key/i }).click();
  const rotatedKey = page.getByLabel("Generated API key");
  const newApiKey = await rotatedKey.inputValue();
  expect(newApiKey).not.toBe(writable.api_key);
  expect(await page.evaluate(() => sessionStorage.getItem("denseMem.userApiKey"))).toBeNull();

  const oldBearerSession = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(writable.api_key) });
  expect(oldBearerSession.status()).toBe(401);
  const cookieSession = await page.evaluate(async () => {
    const response = await fetch("/ui/api/session", { credentials: "include" });
    return { status: response.status, body: await response.json() as UserSessionResponse };
  });
  expect(cookieSession.status).toBe(200);
  expect(cookieSession.body.data?.auth_method).toBe("api_key_session");

  await page.getByRole("button", { name: /sign out/i }).click();
  await expect(page.getByLabel("API key", { exact: true })).toBeVisible();
  const remainingCookies = await page.context().cookies(`${userUrl}/ui/`);
  expect(remainingCookies.some((cookie) => cookie.name === "dense_mem_ui_session")).toBe(false);
  expect(remainingCookies.some((cookie) => cookie.name === "dense_mem_ui_csrf")).toBe(false);
});

test("user self-rotate endpoint rejects profile edits", async ({ request }, testInfo) => {
  const writable = await createTeamProfile(request, uniqueName("No edit", testInfo), ["read", "write"]);

  const response = await request.post(`${userUrl}/ui/api/key/rotate`, {
    headers: bearer(writable.api_key),
    data: { name: "Renamed from user portal" },
  });

  expect(response.status()).toBe(422);
  const body = await response.json() as { message?: string };
  expect(body.message).toContain("does not accept editable fields");

  const session = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(writable.api_key) });
  expect(session.status()).toBe(200);
});

test("user portal shows an invalid API key error", async ({ page }) => {
  await page.goto(`${userUrl}/ui/`);

  await page.getByLabel("API key").fill("wrong-key");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByRole("alert")).toContainText(/invalid|auth/i);
});

async function openControlPanel(page: Page) {
  await page.goto(`${controlUrl}/`);
  await page.getByLabel("Control token").fill(controlToken);
  await page.getByRole("button", { name: "Unlock" }).click();
  await expect(page.getByRole("heading", { name: "Teams" })).toBeVisible();
}

async function openUserPortal(page: Page, apiKey: string) {
  await page.goto(`${userUrl}/ui/`);
  await page.getByLabel("API key").fill(apiKey);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Dense-Mem Knowledge" })).toBeVisible();
}

async function createTeamProfile(request: APIRequestContext, name: string, scopes: string[]): Promise<CreatedProfile> {
  const response = await request.post(`${controlUrl}/control/api/teams/${seedTeamId}/profiles`, {
    headers: bearer(controlToken),
    data: { name, scopes, rate_limit: 300 },
  });
  if (response.status() !== 201) {
    throw new Error(`create profile failed: ${response.status()} ${await response.text()}`);
  }
  const payload = await response.json() as { data: CreatedProfile };
  return payload.data;
}

async function setSSOCookieSecure(request: APIRequestContext, enabled: boolean) {
  const response = await request.patch(`${controlUrl}/control/api/config/sso`, {
    headers: bearer(controlToken),
    data: { items: [{ key: "SSO_COOKIE_SECURE", value: String(enabled) }] },
  });
  expect(response.status()).toBe(200);
}

async function recallFeedbackEnabled(request: APIRequestContext) {
  const response = await request.get(`${controlUrl}/control/api/config/recall-feedback`, {
    headers: bearer(controlToken),
  });
  if (response.status() !== 200) {
    throw new Error(`get recall feedback config failed: ${response.status()} ${await response.text()}`);
  }
  const payload = await response.json() as { data?: { effective?: { enabled?: boolean } } };
  return payload.data?.effective?.enabled === true;
}

async function setRecallFeedback(request: APIRequestContext, enabled: boolean) {
  const response = await request.patch(`${controlUrl}/control/api/config/recall-feedback`, {
    headers: bearer(controlToken),
    data: { items: [{ key: "RECALL_FEEDBACK_ENABLED", value: enabled ? "true" : "false" }] },
  });
  if (response.status() !== 200) {
    throw new Error(`set recall feedback failed: ${response.status()} ${await response.text()}`);
  }
}

async function prometheusResultCount(request: APIRequestContext, query: string) {
  const response = await request.get(`${prometheusUrl}/api/v1/query`, { params: { query } });
  if (response.status() !== 200) {
    return 0;
  }
  const body = await response.json() as PrometheusQueryResponse;
  if (body.status !== "success" || !Array.isArray(body.data?.result)) {
    return 0;
  }
  return body.data.result.length;
}

async function prometheusQueryValue(request: APIRequestContext, query: string) {
  const response = await request.get(`${prometheusUrl}/api/v1/query`, { params: { query } });
  if (response.status() !== 200) {
    return 0;
  }
  const body = await response.json() as PrometheusQueryResponse;
  const rawValue = body.data?.result?.[0]?.value?.[1];
  if (body.status !== "success" || typeof rawValue !== "string") {
    return 0;
  }
  const value = Number.parseFloat(rawValue);
  return Number.isFinite(value) ? value : 0;
}

let mcpRequestID = 0;

async function mcpCall(request: APIRequestContext, method: string, params: unknown) {
  mcpRequestID += 1;
  const response = await request.post(`${userUrl}/mcp`, {
    headers: bearer(seedApiKey),
    data: {
      jsonrpc: "2.0",
      id: mcpRequestID,
      method,
      params,
    },
  });
  if (response.status() !== 200) {
    throw new Error(`MCP ${method} failed: ${response.status()} ${await response.text()}`);
  }
  return await response.json() as Record<string, unknown>;
}

function mcpToolNames(response: Record<string, unknown>) {
  expect(response.error).toBeUndefined();
  const result = response.result;
  expect(isRecord(result)).toBe(true);
  if (!isRecord(result) || !Array.isArray(result.tools)) {
    return [];
  }
  return result.tools
    .map((tool) => (isRecord(tool) && typeof tool.name === "string" ? tool.name : ""))
    .filter(Boolean);
}

function mcpToolPayload(response: Record<string, unknown>) {
  expect(response.error).toBeUndefined();
  const result = response.result;
  expect(isRecord(result)).toBe(true);
  if (!isRecord(result) || !Array.isArray(result.content)) {
    throw new Error("MCP tool result content missing");
  }
  const first = result.content[0];
  if (!isRecord(first) || typeof first.text !== "string") {
    throw new Error("MCP tool result text missing");
  }
  return JSON.parse(first.text) as Record<string, unknown>;
}

async function waitForSubmissionEvidence(request: APIRequestContext, submissionID: string): Promise<Record<string, unknown>> {
  for (let attempt = 0; attempt < 90; attempt += 1) {
    const status = mcpToolPayload(await mcpCall(request, "tools/call", {
      name: "get_submission_status",
      arguments: { submission_id: submissionID },
    }));
    const first = Array.isArray(status.evidence) ? status.evidence[0] : undefined;
    if (isRecord(first) && typeof first.evidence_id === "string" && first.evidence_id !== "") {
      return first;
    }
    const processingState = typeof status.processing_state === "string" ? status.processing_state : "";
    if (["failed", "rejected", "quarantined"].includes(processingState)) {
      throw new Error(`submission ${submissionID} reached ${processingState}: ${JSON.stringify(status.errors ?? [])}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 1_000));
  }
  throw new Error(`submission status did not return evidence for ${submissionID}`);
}

function requiredString(value: Record<string, unknown>, field: string) {
  const result = value[field];
  if (typeof result !== "string" || result === "") {
    throw new Error(`MCP response missing ${field}`);
  }
  return result;
}

function securityRelationship(content: string, ref: string) {
  const supportText = content;
  const supportStart = content.indexOf(supportText);
  const subjectStart = content.indexOf("deployment", supportStart);
  const predicateStart = content.indexOf("protects", subjectStart);
  const objectStart = content.indexOf("prompt", predicateStart);
  return {
    ref,
    subject: {
      name: "deployment",
      entity_kind: "concept",
      span: { evidence_index: 0, start: subjectStart, end: subjectStart + "deployment".length },
    },
    predicate: {
      proposed_key: "protects",
      surface: "protects",
      span: { evidence_index: 0, start: predicateStart, end: predicateStart + "protects".length },
    },
    object: {
      entity: {
        name: "prompt",
        entity_kind: "concept",
        span: { evidence_index: 0, start: objectStart, end: objectStart + "prompt".length },
      },
    },
    polarity: "+",
    modality: "statement",
    supports: [{ evidence_index: 0, start: supportStart, end: supportStart + Array.from(supportText).length }],
  };
}

function assertTelemetrySeries(body: TelemetryResponse) {
	const series = body.data?.series;
	expect(Array.isArray(series)).toBe(true);
	expect(Array.isArray(body.data?.activity_series)).toBe(true);
	expect(Array.isArray(body.data?.state_series)).toBe(true);
	if (!Array.isArray(series)) {
		throw new Error("telemetry series must be an array");
	}
  expect(series.length).toBeGreaterThan(0);
  for (const item of series) {
    expect(isRecord(item)).toBe(true);
    if (!isRecord(item)) {
      throw new Error("telemetry series item must be an object");
    }
    expect(Array.isArray(item.points)).toBe(true);
  }
}

function telemetryLabels(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => (isRecord(item) && typeof item.label === "string" ? item.label : ""))
    .filter(Boolean);
}

function telemetryReadyLabels(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return telemetryLabels(value.filter((item) => isRecord(item) && item.status === "ready"));
}

async function userTelemetry(request: APIRequestContext) {
  const response = await request.get(`${userUrl}/ui/api/telemetry?window=15m`, { headers: bearer(seedApiKey) });
  if (response.status() !== 200) {
    throw new Error(`user telemetry failed: ${response.status()} ${await response.text()}`);
  }
  return await response.json() as TelemetryResponse;
}

async function userUsageTitle(request: APIRequestContext, apiKey: string) {
  const response = await request.get(`${userUrl}/ui/api/session`, { headers: bearer(apiKey) });
  if (response.status() !== 200) {
    throw new Error(`user session failed: ${response.status()} ${await response.text()}`);
  }
  const body = await response.json() as UserSessionResponse;
  return body.data?.can_manage_team ? "Team usage" : "My key usage";
}

async function telemetryCardValue(request: APIRequestContext, id: string) {
  return cardValue(await userTelemetry(request), id);
}

function cardValue(body: TelemetryResponse, id: string) {
  const cards = body.data?.cards;
  if (!Array.isArray(cards)) {
    throw new Error("telemetry cards must be an array");
  }
  const card = cards.find((item): item is TelemetryCard => isRecord(item) && item.id === id);
  if (!card || typeof card.value !== "number") {
    throw new Error(`telemetry card ${id} missing numeric value`);
  }
  return card.value;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

async function expectNoShellOverlap(page: Page) {
  const boxes = await page.locator(".topbar, .primary-rail, .resource-rail, .detail-pane").evaluateAll((elements) => (
    elements.map((element) => {
      const rect = element.getBoundingClientRect();
      return {
        className: element.className,
        left: rect.left,
        top: rect.top,
        right: rect.right,
        bottom: rect.bottom,
        width: rect.width,
        height: rect.height,
      };
    })
  ));
  for (const box of boxes) {
    expect(box.width).toBeGreaterThan(0);
    expect(box.height).toBeGreaterThan(0);
  }
  for (let i = 0; i < boxes.length; i += 1) {
    for (let j = i + 1; j < boxes.length; j += 1) {
      expect(rectanglesOverlap(boxes[i], boxes[j]), `${boxes[i].className} overlaps ${boxes[j].className}`).toBe(false);
    }
  }
}

function rectanglesOverlap(
  first: { left: number; top: number; right: number; bottom: number },
  second: { left: number; top: number; right: number; bottom: number },
) {
  return first.left < second.right && first.right > second.left && first.top < second.bottom && first.bottom > second.top;
}

async function expectMetricDetailContained(row: Locator, detail: Locator) {
  const geometry = await row.evaluate((element) => {
    const detailElement = element.querySelector<HTMLElement>(".metric-detail");
    const labelElement = element.querySelector<HTMLElement>(".metric-label");
    const valueElement = element.querySelector<HTMLElement>("strong");
    if (!detailElement || !labelElement || !valueElement) {
      throw new Error("metric row structure is incomplete");
    }
    const rowRect = element.getBoundingClientRect();
    const detailRect = detailElement.getBoundingClientRect();
    const labelRect = labelElement.getBoundingClientRect();
    const valueRect = valueElement.getBoundingClientRect();
    return {
      row: { left: rowRect.left, right: rowRect.right, bottom: rowRect.bottom },
      detail: { left: detailRect.left, right: detailRect.right, top: detailRect.top, bottom: detailRect.bottom },
      firstLineBottom: Math.max(labelRect.bottom, valueRect.bottom),
      clientWidth: detailElement.clientWidth,
      scrollWidth: detailElement.scrollWidth,
    };
  });

  expect(geometry.detail.left).toBeGreaterThanOrEqual(geometry.row.left);
  expect(geometry.detail.right).toBeLessThanOrEqual(geometry.row.right + 1);
  expect(geometry.detail.bottom).toBeLessThanOrEqual(geometry.row.bottom + 1);
  expect(geometry.detail.top).toBeGreaterThanOrEqual(geometry.firstLineBottom);
  expect(geometry.scrollWidth).toBeLessThanOrEqual(geometry.clientWidth + 1);
  await expect(detail).toHaveCSS("overflow-wrap", "anywhere");
  await expect(detail).toHaveCSS("white-space", "normal");
}

function bearer(token: string) {
  return {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
  };
}

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required for compose e2e`);
  }
  return value;
}

function uniqueName(prefix: string, testInfo: { project: { name: string }; workerIndex: number }) {
  const suffix = Math.random().toString(36).slice(2, 8);
  return `${prefix} ${testInfo.project.name} ${testInfo.workerIndex} ${suffix}`;
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
