import AxeBuilder from "@axe-core/playwright";
import { navigate, openNavigation, confirmAction, expectPageContained, expectCompactNumberField } from "../tests-support/portal";
import { expect, type Page, test } from "@playwright/test";
import type { OntologyDraft } from "../src/user/ontology-types";

const team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Research Team",
  description: "",
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

type TestKey = {
  id: string;
  team_id: string;
  name: string;
  key_suffix: string;
  memory_access: { private: { read: boolean; write: boolean }; team_shared: { read: boolean; write: boolean } };
  scopes: string[];
  role: "manager" | "member";
  rate_limit: number;
  last_used_at: string | null;
  expires_at: string | null;
  created_at: string;
};

type SSOTestCredential = TestKey;

const readKey: TestKey = {
  id: "22222222-2222-4222-8222-222222222222",
  team_id: team.id,
  name: "Mine",
  key_suffix: "abc123",
  scopes: ["read"],
  memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: false } },
  role: "member",
  rate_limit: 120,
  last_used_at: null,
  expires_at: null,
  created_at: "2026-05-01T12:00:00Z",
};

const writeKey: TestKey = {
  ...readKey,
  scopes: ["read", "write"],
  memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: true } },
};

const managerKey: TestKey = {
  ...writeKey,
  id: "33333333-3333-4333-8333-333333333333",
  name: "Manager",
  key_suffix: "mgr123",
  role: "manager",
};

const memberCredential: TestKey = {
  ...writeKey,
  id: "44444444-4444-4444-8444-444444444444",
  name: "Member credential",
  key_suffix: "p6ZAc",
  role: "member",
};

const evidences = [
  {
    evidence_id: "evidence-1",
    relationship_ids: ["relationship-1"],
    rank: 1,
    context: "Alice uses Dense-Mem for project-x.",
    source: "notes",
    source_type: "manual",
    created_at: "2026-05-02T12:00:00Z",
  },
  {
    evidence_id: "evidence-2",
    relationship_ids: ["relationship-2"],
    rank: 2,
    context: "Alice works on project-x with Dense-Mem.",
    source: "notes",
    source_type: "manual",
    created_at: "2026-05-03T12:00:00Z",
  },
];

const relationships = [
  {
    relationship_id: "relationship-1",
    tier: "verified",
    subject: { entity_id: "entity-alice", name: "Alice", kind: "person" },
    predicate: "uses",
    object: { entity_id: "entity-dense-mem", name: "Dense-Mem", kind: "software" },
    polarity: "+",
    valid_from: "2026-05-02T12:00:00Z",
    evidence_ids: ["evidence-1"],
    search_state: "current",
  },
  {
    relationship_id: "relationship-2",
    tier: "verified",
    subject: { entity_id: "entity-alice", name: "Alice", kind: "person" },
    predicate: "works_on",
    object: { value_id: "value-project-x", value: "project-x", type: "project" },
    polarity: "+",
    valid_from: "2026-05-03T12:00:00Z",
    evidence_ids: ["evidence-2"],
    search_state: "current",
  },
];

const graphSnapshot = {
  scope: "overview",
  depth: 2,
  limit: 80,
  truncated: false,
  nodes: [
    {
      key: "entity:entity-alice",
      id: "entity-alice",
      type: "entity",
      title: "Alice",
    },
    {
      key: "entity:entity-dense-mem",
      id: "entity-dense-mem",
      type: "entity",
      title: "Dense-Mem",
    },
    {
      key: "value:value-project-x",
      id: "value-project-x",
      type: "value",
      title: "project-x",
    },
  ],
  edges: [
    { id: "relationship-1", source: "entity:entity-alice", target: "entity:entity-dense-mem", relationship: "USES", directed: true },
    { id: "relationship-2", source: "entity:entity-alice", target: "value:value-project-x", relationship: "WORKS_ON", directed: true },
  ],
};

const graphNodeDetails = {
  "entity:entity-alice": {
    key: "entity:entity-alice",
    id: "entity-alice",
    type: "entity",
    title: "Alice",
    body: "Person",
    status: "active",
    score: 0.94,
    recorded_at: "2026-05-02T12:00:00Z",
  },
  "entity:entity-dense-mem": {
    key: "entity:entity-dense-mem",
    id: "entity-dense-mem",
    type: "entity",
    title: "Dense-Mem",
    body: "Software",
    status: "active",
    score: 0.88,
    recorded_at: "2026-05-02T12:00:00Z",
  },
  "value:value-project-x": {
    key: "value:value-project-x",
    id: "value-project-x",
    type: "value",
    title: "project-x",
    body: "project-x",
    status: "active",
    score: 0.75,
    recorded_at: "2026-05-02T12:00:00Z",
  },
};

for (const width of [1440, 900, 390]) for (const theme of ["light", "dark"] as const) {
  test(`redesign knowledge ${width}px ${theme}: search, graph, ontology and connection`, async ({ page }, testInfo) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    await page.setViewportSize({ width, height: 1000 });
    await page.emulateMedia({ colorScheme: theme, reducedMotion: "reduce" });
    await mockUserApi(page, { key: managerKey, canManageTeam: true, credentials: [memberCredential] });
    await page.route("**/ui/api/ontology/**", async (route) => {
      const url = new URL(route.request().url());
      const data = url.pathname.endsWith("/status") ? { observed_at: "2026-10-18T12:00:00Z", revision: 1, coverage_complete: true, discovery_complete: true, pending_markers: 0, counts: { eligible: 0, organized: 0, pending: 0, ambiguous: 0, failed: 0 } } : { records: [], sources: [], drafts: [], publications: [] };
      await route.fulfill({ json: { data } });
    });
    await page.goto("/ui/");
    await page.screenshot({ path: testInfo.outputPath("knowledge-auth.png"), fullPage: true });
    await page.getByLabel("API key").fill("dm_manager");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await expect(page.getByLabel("Current workspace")).toContainText("Research Team");
    await page.getByLabel("Keyword").fill("project");
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.getByRole("listbox", { name: "Recall result list" }).getByRole("option")).toHaveCount(3);
    await page.getByRole("button", { name: "Filters", exact: true }).click();
    await page.getByRole("checkbox", { name: /Relationship/ }).uncheck();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("listbox")).not.toContainText("uses: Dense-Mem");
    await expectPageContained(page);
    expect((await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze()).violations).toEqual([]);
    await page.screenshot({ path: testInfo.outputPath("knowledge-search.png"), fullPage: true });
    await page.getByRole("option").first().focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("tab", { name: "Lineage", exact: true })).toBeVisible();
    await page.getByRole("tab", { name: "Evidence", exact: true }).focus();
    await page.keyboard.press("ArrowRight");
    await expect(page.getByRole("tab", { name: "Lineage", exact: true })).toHaveAttribute("aria-selected", "true");
    if (await page.getByRole("dialog").isVisible()) await page.keyboard.press("Escape");
    await navigate(page, "Graph");
    await expect(page.locator(".graph-canvas canvas").first()).toBeVisible();
    await page.getByLabel("Inspect graph node").selectOption("entity:entity-alice");
    await expect(page.getByRole("heading", { name: "Alice", exact: true })).toBeVisible();
    if (await page.getByRole("dialog").isVisible()) await page.keyboard.press("Escape");
    await expectPageContained(page);
    await page.screenshot({ path: testInfo.outputPath("knowledge-graph.png"), fullPage: true });
    await navigate(page, "My credentials");
    await expectPageContained(page);
    await page.screenshot({ path: testInfo.outputPath("knowledge-credentials.png"), fullPage: true });
    await navigate(page, "Team");
    await expectCompactNumberField(page, "Rate limit");
    const nameWidth = (await page.getByLabel("Credential name", { exact: true }).boundingBox())!.width;
    const rateWidth = (await page.getByLabel("Rate limit", { exact: true }).boundingBox())!.width;
    expect(nameWidth).toBeGreaterThan(rateWidth);
    await expectPageContained(page);
    await page.screenshot({ path: testInfo.outputPath("knowledge-team.png"), fullPage: true });
    await navigate(page, "Ontology");
    const coverage = page.getByRole("region", { name: "Team ontology coverage" });
    await expect(coverage).toContainText("no eligible shared records discovered");
    await expect(coverage.getByRole("progressbar")).toHaveCount(0);
    await page.getByRole("tab", { name: "Drafts", exact: true }).click();
    await expect(page.getByLabel("Audit reason")).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("knowledge-ontology.png"), fullPage: true });
    await page.getByRole("button", { name: "Connect", exact: true }).click();
    await expect(page.getByRole("dialog")).toContainText(team.id);
    await expect(page.getByRole("dialog")).toContainText("Private: Read/write");
    expect((await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze()).violations).toEqual([]);
    await page.keyboard.press("Escape");
    await expect(page.getByRole("button", { name: "Connect", exact: true })).toBeFocused();
    expect(pageErrors).toEqual([]);
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`ontology long draft and history labels stay usable in ${theme} mode`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme });
    await mockUserApi(page, { key: managerKey, canManageTeam: true });
    const reason = "Browser correction ".padEnd(512, "x");
    const publication = { id: "publication-long", revision: 1, origin: "manager", reason, records: [], record_views: [] };
    const otherPublication = { ...publication, id: "publication-other", revision: 2, reason: "Another reviewed publication" };
    let draft: OntologyDraft | null = null;
    await page.route("**/ui/api/ontology/**", async (route) => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      if (request.method() === "POST" && path.endsWith("/drafts")) {
        draft = { ...request.postDataJSON(), id: "draft-long", version: 1, state: "open", base_revision: 1, updated_at: "2026-10-18T00:00:00Z" };
        await route.fulfill({ json: { data: draft } });
        return;
      }
      if (request.method() === "POST" && path.endsWith("/discard")) {
        draft = { ...draft!, version: 2, state: "discarded" };
        await route.fulfill({ json: { data: draft } });
        return;
      }
      const data = path.endsWith("/status") ? { observed_at: "2026-10-18T12:00:00Z", revision: 1, coverage_complete: true, discovery_complete: true, pending_markers: 0, counts: { eligible: 0, organized: 0, pending: 0, ambiguous: 0, failed: 0 } }
        : path.endsWith("/publications/publication-long") ? publication
          : path.endsWith("/publications/publication-other") ? otherPublication
          : { records: [], sources: [], drafts: draft ? [draft] : [], publications: [publication, otherPublication] };
      await route.fulfill({ json: { data } });
    });
    await openUserPortal(page, "dm_manager");
    await navigate(page, "Ontology");
    await page.getByRole("tab", { name: "Drafts", exact: true }).click();
    await page.getByLabel("Definition key").fill("browser-long");
    await page.getByLabel("Definition label").fill("Vocabulary ".padEnd(256, "y"));
    await page.getByLabel("Definition description").fill("Review a saved correction on a narrow screen.");
    await page.getByLabel("Audit reason").fill(reason);
    await page.getByRole("button", { name: "Add correction to draft" }).click();
    await page.getByRole("button", { name: "Save draft", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("Draft saved");
    await expectPageContained(page);
    await expect(page.getByRole("button", { name: `${reason} · open · revision 1`, exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Discard draft", exact: true }).click();
    await page.getByRole("alertdialog").getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.getByRole("alertdialog")).toBeHidden();
    await expect(page.getByRole("button", { name: "Discard draft", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "Discard draft", exact: true }).click();
    await confirmAction(page, "Discard draft");
    await expect(page.getByRole("status")).toContainText("Draft discarded");
    await expectPageContained(page);
    await page.getByRole("tab", { name: "History", exact: true }).click();
    await expectPageContained(page);
    await page.getByRole("button", { name: `Revision 1 · manager · ${reason}`, exact: true }).click();
    await expect(page.getByRole("heading", { name: "Publication revision 1", exact: true })).toBeVisible();
    await page.getByLabel("Rollback reason", { exact: true }).fill("Keep this unsaved reason");
    const other = page.getByRole("button", { name: "Revision 2 · manager · Another reviewed publication", exact: true });
    await other.click();
    await confirmAction(page, "Cancel");
    await expect(page.getByRole("heading", { name: "Publication revision 1", exact: true })).toBeVisible();
    await expect(page.getByLabel("Rollback reason", { exact: true })).toHaveValue("Keep this unsaved reason");
    await other.click();
    await confirmAction(page, "Discard reason");
    await expect(page.getByRole("heading", { name: "Publication revision 2", exact: true })).toBeVisible();
    await expect(page.getByLabel("Rollback reason", { exact: true })).toHaveValue("");
    await expectPageContained(page);
    expect((await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze()).violations).toEqual([]);
  });
}

test("API key login, recall, and read-only navigation", async ({ page }) => {
  const calls = await mockUserApi(page, { key: readKey });
  await openUserPortal(page, "dm_read");

  await expect(page.getByLabel("Current workspace")).toBeVisible();
  await expect(page.getByLabel("Current workspace")).not.toContainText("Mine");
  await expect(page.getByText("Other credential")).toBeHidden();

  await page.getByLabel("Keyword").fill("project");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page.getByRole("heading", { name: "Alice uses Dense-Mem for project-x." }).first()).toBeVisible();
  await expect(page.getByText("project-x").first()).toBeVisible();
  await expect(page.getByRole("listbox", { name: "Recall result list" })).toContainText("Dense-Mem");
  await expect(page.getByRole("listbox", { name: "Recall result list" }).getByRole("option")).toHaveCount(3);
  await page.getByRole("option").first().click();
  await expect(page.getByLabel("Inspector", { exact: true })).toContainText("Evidence");
  await page.getByRole("button", { name: "Close details panel" }).click();
  await page.getByRole("option").filter({ hasText: "uses: Dense-Mem" }).click();
  await expect(page.getByLabel("Inspector", { exact: true })).toContainText("Relationship");
  await expect(page.getByLabel("Inspector", { exact: true })).toContainText("Tier verified");
  await page.getByRole("button", { name: "Close details panel" }).click();
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await expect(page.getByLabel("Knowledge filters")).toBeVisible();
  await page.getByRole("checkbox", { name: /Relationship/ }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("listbox", { name: "Recall result list" })).not.toContainText("uses: Dense-Mem");
  await page.getByRole("button", { name: "Open details", exact: true }).click();
  await expect(page.getByLabel("Inspector", { exact: true })).toContainText("Evidence");
  await page.getByRole("button", { name: "Close details panel" }).click();
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByRole("checkbox", { name: /Evidence/ }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("option")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Open details", exact: true })).toBeDisabled();

  await expect(page.getByRole("link", { name: "Usage" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Evidence" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Relationships" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Communities" })).toHaveCount(0);
  expect(calls.disallowedCredentialCalls).toEqual([]);
  expect(calls.telemetryRequests).toEqual([]);
});

test("graph tab renders a nonblank memory graph", async ({ page }) => {
  const calls = await mockUserApi(page, { key: readKey });
  await openUserPortal(page, "dm_read");

  await navigate(page, "Graph");
  await expect(page.getByLabel("Knowledge graph")).toBeVisible();
  await page.getByRole("button", { name: "Open details", exact: true }).click();
  await expect(page.getByLabel("Graph inspector", { exact: true })).toContainText("Select a node");
  await page.getByRole("button", { name: "Close details panel" }).click();
  const canvas = page.locator(".graph-canvas canvas").first();
  await expect(canvas).toBeVisible();
  await expect.poll(async () => canvas.evaluate((element) => {
    const canvasElement = element as HTMLCanvasElement;
    const context = canvasElement.getContext("2d");
    if (!context || canvasElement.width === 0 || canvasElement.height === 0) {
      return 0;
    }
    const data = context.getImageData(0, 0, canvasElement.width, canvasElement.height).data;
    let painted = 0;
    for (let index = 3; index < data.length; index += 4) {
      if (data[index] > 0) {
        painted += 1;
      }
    }
    return painted;
  })).toBeGreaterThan(0);
  expect(calls.graphRequests.some((url) => url.includes("/ui/api/graph?scope=overview"))).toBe(true);

  await page.getByRole("button", { name: "Graph controls", exact: true }).click();
  const controls = page.getByLabel("Graph controls", { exact: true });
  await controls.getByRole("button", { name: "Local" }).click();
  await controls.getByLabel("Anchor ID").fill("entity-alice");
  await controls.getByLabel("Depth").focus();
  await controls.getByLabel("Depth").press("End");
  await expect(controls.getByLabel("Depth")).toHaveValue("5");
  await controls.getByLabel("Relationship limit").fill("181");
  await controls.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect.poll(() => calls.graphRequests.some((requestUrl) => {
    const url = new URL(requestUrl);
    return url.searchParams.get("depth") === "5" && url.searchParams.get("limit") === "181";
  })).toBe(true);
});

test("graph refresh replaces a corrected endpoint while the graph stays open", async ({ page }) => {
  await page.addInitScript(() => {
    const trackedWindow = window as Window & { __denseMemGraphLabels?: string[] };
    trackedWindow.__denseMemGraphLabels = [];
    const clearRect = CanvasRenderingContext2D.prototype.clearRect;
    CanvasRenderingContext2D.prototype.clearRect = function (x, y, width, height) {
      trackedWindow.__denseMemGraphLabels = [];
      return clearRect.call(this, x, y, width, height);
    };
    const fillText = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function (text, x, y, maxWidth) {
      const labels = trackedWindow.__denseMemGraphLabels ?? [];
      labels.push(String(text));
      if (labels.length > 10_000) {
        labels.splice(0, labels.length - 5_000);
      }
      trackedWindow.__denseMemGraphLabels = labels;
      if (maxWidth === undefined) {
        return fillText.call(this, text, x, y);
      }
      return fillText.call(this, text, x, y, maxWidth);
    };
  });
  const original = {
    ...graphSnapshot,
    nodes: [
      { key: "entity:entity-alice", id: "entity-alice", type: "entity", title: "Alice" },
      { key: "entity:entity-wrong-project", id: "entity-wrong-project", type: "entity", title: "Wrong Project" },
    ],
    edges: [
      { id: "relationship-original", source: "entity:entity-alice", target: "entity:entity-wrong-project", relationship: "USES", directed: true },
    ],
  };
  const corrected = {
    ...graphSnapshot,
    nodes: [
      { key: "entity:entity-alice", id: "entity-alice", type: "entity", title: "Alice" },
      { key: "entity:entity-corrected-project", id: "entity-corrected-project", type: "entity", title: "Corrected Project" },
    ],
    edges: [
      { id: "relationship-successor", source: "entity:entity-alice", target: "entity:entity-corrected-project", relationship: "USES", directed: true },
    ],
  };
  const harness = await mockUserApi(page, {
    key: readKey,
    graphSnapshots: [original, corrected],
  });
  await openUserPortal(page, "dm_read");

  await navigate(page, "Graph");
  await expect.poll(() => graphCanvasLabels(page)).toContain("Wrong Project");
  const requestsBeforeRefresh = harness.graphRequests.length;

  harness.correctRelationship();
  await page.getByRole("button", { name: "Graph controls", exact: true }).click();
  await page.getByLabel("Graph controls", { exact: true }).getByRole("button", { name: "Refresh", exact: true }).click();
  await expect.poll(() => harness.graphRequests.length).toBeGreaterThan(requestsBeforeRefresh);
  await expect.poll(() => graphCanvasLabels(page)).toContain("Corrected Project");
  await page.waitForTimeout(300);
  const labels = await graphCanvasLabels(page);
  expect(labels).toContain("Corrected Project");
  expect(labels).not.toContain("Wrong Project");
});

test("read-only key cannot regenerate itself", async ({ page }) => {
  await mockUserApi(page, { key: readKey });
  await openUserPortal(page, "dm_read");

  await navigate(page, "My credentials");
  await expect(page.getByRole("button", { name: /Regenerate key/i })).toBeDisabled();
});

test("write key regenerates only through the self-rotate endpoint", async ({ page }) => {
  const calls = await mockUserApi(page, { key: writeKey });
  await openUserPortal(page, "dm_write");

  await navigate(page, "My credentials");
  await page.getByRole("button", { name: /Regenerate key/i }).click();
  await confirmAction(page, "Regenerate key");

  const details = page.getByLabel("Knowledge details");
  await expect(details.getByLabel("Generated API key")).toHaveValue("dm_new_plaintext");
  await expect(details.getByText("******new123")).toBeVisible();
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem("denseMem.userApiKey"))).toBeNull();
  expect(calls.rotateBodies).toEqual(["{}"]);
  expect(calls.disallowedCredentialCalls).toEqual([]);
});

test("SSO manager sees the same team management destinations and explicit memory access", async ({ page }) => {
  await mockUserApi(page, { key: managerKey, canManageTeam: true, credentials: [memberCredential] });
  await mockSSOUserApi(page, [], "manager");
  await page.goto("/ui/");
  await expect(page.getByLabel("Current workspace")).toContainText(team.name);
  await navigate(page, "Team");
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue(team.name);
  await openNavigation(page);
  await expect(page.getByRole("link", { name: "Ontology", exact: true })).toBeVisible();
  if (await page.getByRole("dialog").isVisible()) await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("Private: Read/write");
  await expect(page.getByRole("dialog")).toContainText("Team read/write");
});

test("SSO member can create and operate on a selected second credential", async ({ page }) => {
  const firstCredential: SSOTestCredential = {
    ...writeKey,
    id: "55555555-5555-4555-8555-555555555555",
    name: "Profile key",
    key_suffix: "profile1",
  };
  const calls = await mockSSOUserApi(page, [firstCredential]);
  await page.goto("/ui/");

  await expect(page.getByLabel("Current workspace")).toContainText(team.name);
  await navigate(page, "My credentials");
  await expect(page.getByRole("heading", { name: "My credentials" })).toBeVisible();

  await page.getByLabel("Credential name", { selector: "#sso-personal-credential-name" }).fill("Second key");
  await expect(page.getByLabel("Memory binding")).toHaveCount(0);
  await page.getByRole("button", { name: "Create API key" }).click();

  await expect(page.getByLabel("Generated API key")).toHaveValue("dm_sso_second_plaintext");
  await expect(page.getByRole("button", { name: /Second key/ })).toBeVisible();
  expect(calls.createBodies).toEqual([expect.objectContaining({
    name: "Second key",
    scopes: ["read", "write"],
  })]);

  const secondCredentialID = "66666666-6666-4666-8666-666666666666";
  await page.getByRole("button", { name: /Second key/ }).click();
  await page.getByRole("button", { name: /Regenerate key/i }).click();
  await confirmAction(page, "Regenerate key");
  await expect(page.getByLabel("Generated API key")).toHaveValue("dm_sso_second_rotated");
  expect(calls.rotatePaths).toEqual([`/ui/api/sso/credentials/${secondCredentialID}/rotate`]);

  await page.getByRole("button", { name: /Revoke key/i }).click();
  await confirmAction(page, "Revoke key");
  await expect(page.getByRole("button", { name: /Profile key/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Second key/ })).toBeHidden();
  expect(calls.revokePaths).toEqual([`/ui/api/sso/credentials/${secondCredentialID}`]);
});

test("write member key has no Usage dashboard", async ({ page }) => {
  const calls = await mockUserApi(page, { key: writeKey });
  await openUserPortal(page, "dm_write");

  await expect(page.getByRole("link", { name: "Usage" })).toHaveCount(0);
  expect(calls.telemetryRequests).toEqual([]);
  expect(calls.disallowedCredentialCalls).toEqual([]);
});

test("manager key retains Team navigation without a Usage dashboard", async ({ page }) => {
  const calls = await mockUserApi(page, {
    key: managerKey,
    canManageTeam: true,
    credentials: [managerKey, memberCredential],
  });
  await openUserPortal(page, "dm_manager");

  await openNavigation(page);
  await expect(page.getByRole("link", { name: "Team" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Usage" })).toHaveCount(0);
  expect(calls.telemetryRequests).toEqual([]);
});

test("invalid API key shows login error", async ({ page }) => {
  await page.route("**/ui/api/session", async (route) => {
    await route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ message: "invalid api key" }),
    });
  });
  await page.goto("/ui/");

  await page.getByLabel("API key").fill("wrong");
  await page.getByRole("button", { name: "Sign in" }).click();

  await expect(page.getByRole("alert")).toContainText("invalid api key");
});

test("responsive user portal layout", async ({ page }) => {
  await mockUserApi(page, { key: readKey });
  await openUserPortal(page, "dm_read");

  await expect(page.getByLabel("Current workspace")).toBeVisible();
  await openNavigation(page);
  await expect(page.getByRole("link", { name: "Search memory" })).toBeVisible();
  if (await page.locator(".mobile-navigation").isVisible()) await page.keyboard.press("Escape");
  const shellMinHeight = await page.locator(".app-shell").evaluate((element) => Number.parseFloat(getComputedStyle(element).minHeight));
  expect(shellMinHeight).toBeGreaterThanOrEqual((page.viewportSize()?.height ?? 0) - 1);
  if ((page.viewportSize()?.width ?? 0) >= 768) {
    await expect(page.locator(".primary-rail")).toBeVisible();
  } else {
    await expect(page.getByRole("button", { name: "Open navigation" })).toBeVisible();
  }
  await expect(page.locator(".resource-rail")).toHaveCount(0);
  await expect(page.locator(".knowledge-results-panel")).toHaveCSS("border-radius", "10px");
  await expectNoShellOverlap(page);

  await expectPageContained(page);
});

test("manager team page separates team editing from credential management", async ({ page }) => {
  await mockUserApi(page, {
    key: managerKey,
    canManageTeam: true,
    credentials: [managerKey, memberCredential],
  });
  await openUserPortal(page, "dm_manager");

  await navigate(page, "Team");
  const surface = page.locator(".team-management-surface");
  await expect(surface.getByRole("heading", { name: "Team" })).toBeVisible();
  await expect(surface.getByRole("heading", { name: "Credentials" })).toBeVisible();

  const spacing = await surface.evaluate((element) => {
    const sections = Array.from(element.querySelectorAll(".surface-section"));
    if (sections.length < 2) {
      throw new Error("team management sections are missing");
    }
    const first = sections[0].getBoundingClientRect();
    const second = sections[1].getBoundingClientRect();
    const secondStyle = getComputedStyle(sections[1]);
    return {
      borderTopWidth: Number.parseFloat(secondStyle.borderTopWidth),
      marginGap: second.top - first.bottom,
      paddingTop: Number.parseFloat(secondStyle.paddingTop),
    };
  });
  expect(spacing.marginGap).toBeGreaterThanOrEqual(18);
  expect(spacing.paddingTop).toBeGreaterThanOrEqual(18);
  expect(spacing.borderTopWidth).toBeGreaterThanOrEqual(1);
  const rename = page.getByLabel(`Credential name ${memberCredential.name}`, { exact: true });
  await rename.fill("Keep unfinished credential name");
  const refresh = page.waitForResponse((response) => response.request().method() === "GET" && new URL(response.url()).pathname === "/ui/api/team/credentials");
  await page.getByRole("button", { name: "Refresh credentials", exact: true }).click();
  await refresh;
  await expect(rename).toHaveValue("Keep unfinished credential name");
  await page.evaluate(() => window.scrollTo(0, 0));
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
  await expectNoShellOverlap(page);
});

async function openUserPortal(page: Page, apiKey: string) {
  await page.goto("/ui/");
  await page.getByLabel("API key").fill(apiKey);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByLabel("Current workspace")).toBeVisible();
}

async function expectNoShellOverlap(page: Page) {
  const overflow = await page.evaluate(() => ({
    viewportWidth: window.innerWidth,
    documentWidth: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
    tableWraps: Array.from(document.querySelectorAll(".table-wrap")).map((element) => ({
      className: element.className,
      clientWidth: element.clientWidth,
      scrollWidth: element.scrollWidth,
    })),
  }));
  expect(overflow.documentWidth).toBeLessThanOrEqual(overflow.viewportWidth + 1);
  await expectPageContained(page);

  const boxes = await page.locator(".topbar, .primary-rail, .top-nav-bar, .resource-rail, .detail-pane").evaluateAll((elements) => (
    elements.filter((element) => element.getBoundingClientRect().width > 0).map((element) => {
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

async function mockUserApi(
  page: Page,
  state: {
    key: TestKey;
    canManageTeam?: boolean;
    credentials?: TestKey[];
    graphSnapshots?: Array<typeof graphSnapshot>;
  },
) {
  const calls = {
    rotateBodies: [] as string[],
    disallowedCredentialCalls: [] as string[],
    telemetryRequests: [] as string[],
    graphRequests: [] as string[],
  };
  let currentTeam = { ...team };
  let currentKey = { ...state.key };
  let currentCredentials = (state.credentials ?? []).map((credential) => ({ ...credential }));
  let portalSessionCreated = false;
  let graphSnapshotIndex = 0;

  await page.route("**/ui/api/sso/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });

  await page.route("**/ui/api/team/credentials**", async (route) => {
    if (state.canManageTeam) {
      const request = route.request();
      const url = new URL(request.url());
      if (url.pathname === "/ui/api/team/credentials" && request.method() === "GET") {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            data: currentCredentials,
            pagination: { limit: 50, offset: 0, total: currentCredentials.length },
          }),
        });
        return;
      }
    }
    calls.disallowedCredentialCalls.push(route.request().url());
    await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ message: "credential list must not be called" }) });
  });

  await page.route("**/ui/api/team", async (route) => {
    if (!state.canManageTeam) {
      calls.disallowedCredentialCalls.push(route.request().url());
      await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ message: "team management must not be called" }) });
      return;
    }

    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/ui/api/team" && request.method() === "PATCH") {
      const body = JSON.parse(request.postData() ?? "{}") as Partial<typeof team>;
      currentTeam = {
        ...currentTeam,
        name: body.name ?? currentTeam.name,
        description: body.description ?? currentTeam.description,
      };
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: currentTeam }) });
      return;
    }

    await route.fallback();
  });

  await page.route("**/ui/api/session", async (route) => {
    if (route.request().method() === "POST") {
      portalSessionCreated = true;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: { status: "signed_in" } }),
      });
      return;
    }
    const authorization = route.request().headers().authorization ?? "";
    if (!authorization.startsWith("Bearer ")) {
      if (portalSessionCreated) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            data: {
              team: currentTeam,
              membership: { team_id: currentTeam.id, name: currentKey.name, grants: currentKey.scopes, role: currentKey.role },
              credential: currentKey,
              memory_access: currentKey.memory_access,
              teams: [],
              personal_credentials: [],
              mcp_public_base_url: "",
            },
          }),
        });
        return;
      }
      await route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ message: "authentication required" }),
      });
      return;
    }

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          team: currentTeam,
          membership: { team_id: currentTeam.id, name: currentKey.name, grants: currentKey.scopes, role: currentKey.role },
          credential: currentKey,
              memory_access: currentKey.memory_access,
          teams: [],
          personal_credentials: [],
          mcp_public_base_url: "",
        },
      }),
    });
  });

  await page.route("**/ui/api/credential/rotate", async (route) => {
    calls.rotateBodies.push(route.request().postData() ?? "");
    currentKey = { ...currentKey, key_suffix: "new123", last_used_at: null };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { api_key: "dm_new_plaintext", credential: currentKey } }),
    });
  });

  await page.route("**/ui/api/telemetry**", async (route) => {
    calls.telemetryRequests.push(route.request().url());
    await route.fulfill({ status: 404 });
  });

  await page.route("**/ui/api/node-detail**", async (route) => {
    const url = new URL(route.request().url());
    const key = `${url.searchParams.get("type")}:${url.searchParams.get("id")}`;
    const node = graphNodeDetails[key as keyof typeof graphNodeDetails] ?? graphNodeDetails["entity:entity-alice"];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: node }),
    });
  });

  await page.route("**/ui/api/graph**", async (route) => {
    calls.graphRequests.push(route.request().url());
    const snapshots = state.graphSnapshots ?? [graphSnapshot];
    const snapshot = snapshots[Math.min(graphSnapshotIndex, snapshots.length - 1)];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { "Cache-Control": "no-store" },
      body: JSON.stringify({ data: snapshot }),
    });
  });

  await page.route("**/ui/api/recall**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          recall_id: "rec-test",
          results: evidences,
          conflicts: [],
          related_relationships: [relationships[0]],
          related_communities: [{ relationships, evidence_ids: evidences.map((evidence) => evidence.evidence_id) }],
          search_states: { evidence: "current", relationships: "current" },
          degradations: [],
        },
      }),
    });
  });

  return {
    ...calls,
    correctRelationship() {
      graphSnapshotIndex = Math.max(0, (state.graphSnapshots?.length ?? 1) - 1);
    },
  };
}

async function mockSSOUserApi(page: Page, credentials: SSOTestCredential[], role: "member" | "manager" = "member") {
  const calls = {
    createBodies: [] as Array<Record<string, unknown>>,
    rotatePaths: [] as string[],
    revokePaths: [] as string[],
  };
  let currentCredentials = credentials.map((credential) => ({ ...credential }));

  await page.route("**/ui/api/sso/providers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    });
  });

  await page.route("**/ui/api/session", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: {
          team,
          membership: { team_id: team.id, name: "SSO member", grants: ["read", "write"], role },
          credential: null,
          teams: [{ team, membership: { team_id: team.id, name: "SSO member", grants: ["read", "write"], role } }],
          personal_credentials: currentCredentials,
          memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: true } },
          mcp_public_base_url: "",
        },
      }),
    });
  });

  await page.route("**/ui/api/sso/credentials", async (route) => {
    const request = route.request();
    if (request.method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: currentCredentials }),
      });
      return;
    }
    const body = JSON.parse(request.postData() ?? "{}") as Record<string, unknown>;
    calls.createBodies.push(body);
    const created: SSOTestCredential = {
      ...currentCredentials[0],
      id: "66666666-6666-4666-8666-666666666666",
      name: String(body.name ?? "Second key"),
      key_suffix: "second1",
      scopes: Array.isArray(body.scopes) ? body.scopes.map(String) : ["read"],
      rate_limit: Number(body.rate_limit ?? 120),
      expires_at: typeof body.expires_at === "string" ? body.expires_at : null,
      memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: Array.isArray(body.scopes) && body.scopes.includes("write") } },
    };
    currentCredentials = [...currentCredentials, created];
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({ data: { api_key: "dm_sso_second_plaintext", credential: created } }),
    });
  });

  await page.route("**/ui/api/sso/credentials/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const segments = url.pathname.split("/").filter(Boolean);
    const credentialID = segments.at(-1) ?? "";
    if (request.method() === "POST" && segments.at(-1) === "rotate") {
      const selectedID = segments.at(-2) ?? "";
      calls.rotatePaths.push(url.pathname);
      currentCredentials = currentCredentials.map((credential) => credential.id === selectedID
        ? { ...credential, key_suffix: "second2" }
        : credential);
      const rotated = currentCredentials.find((credential) => credential.id === selectedID);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: { api_key: "dm_sso_second_rotated", credential: rotated } }),
      });
      return;
    }
    if (request.method() === "DELETE") {
      calls.revokePaths.push(url.pathname);
      currentCredentials = currentCredentials.filter((credential) => credential.id !== credentialID);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: { status: "revoked" } }),
      });
      return;
    }
    await route.fallback();
  });

  return calls;
}

async function graphCanvasLabels(page: Page): Promise<string[]> {
  return page.evaluate(() => (
    (window as Window & { __denseMemGraphLabels?: string[] }).__denseMemGraphLabels ?? []
  ));
}
