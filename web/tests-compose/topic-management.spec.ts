import { randomUUID } from "node:crypto";
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import type { SaveTopicEdits, TopicDefinition, TopicEdit, TopicPublication, TopicRecord, TopicRecordPage } from "../src/user/topic-types";
import { confirmAction, expectPageContained, navigate } from "../tests-support/portal";

const userURL = process.env.DENSE_MEM_USER_URL!;
const controlURL = process.env.DENSE_MEM_CONTROL_URL!;
const teamID = process.env.DENSE_MEM_E2E_TEAM_ID!;
const topicsURL = `${userURL}/ui/api/topics`;

function authorization(key: string) {
  return { Authorization: `Bearer ${key}` };
}

async function createTeam(request: APIRequestContext): Promise<string> {
  const response = await request.post(`${controlURL}/control/api/teams`, {
    headers: authorization(process.env.DENSE_MEM_CONTROL_TOKEN!),
    data: { name: `topic-browser-${randomUUID()}` },
  });
  expect(response.status()).toBe(201);
  return (await response.json()).data.id;
}

async function createCredential(request: APIRequestContext, team: string, role: "manager" | "member") {
  const response = await request.post(`${controlURL}/control/api/teams/${team}/credentials`, {
    headers: authorization(process.env.DENSE_MEM_CONTROL_TOKEN!),
    data: { name: `topic-browser-${randomUUID()}`, role, scopes: ["read", "write"], rate_limit: 300 },
  });
  expect(response.status()).toBe(201);
  return (await response.json()).data.api_key as string;
}

async function signIn(page: Page, key: string) {
  await page.goto(`${userURL}/ui/`);
  await page.getByLabel("API key").fill(key);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(page.getByLabel("Current workspace")).toBeVisible();
  await expect(page.getByRole("button", { name: "Refresh session" })).toBeEnabled();
}

function tab(page: Page, name: string) {
  return page.getByRole("tab", { name, exact: true });
}

function definitionEdit(definition: TopicDefinition, current?: TopicRecord): TopicEdit {
  return {
    action: "define", kind: "topic", definition,
    expected_version: current?.edit_version ?? current?.version ?? 0,
    ...(current ? { record_id: current.id } : {}),
  };
}

async function saveEdits(request: APIRequestContext, key: string, reason: string, edits: TopicEdit[]): Promise<TopicPublication> {
  const data: SaveTopicEdits = { operation_key: randomUUID(), reason, edits };
  const response = await request.post(`${topicsURL}/edits`, { headers: authorization(key), data });
  expect(response.status()).toBe(200);
  return (await response.json()).data;
}

async function readRecord(request: APIRequestContext, key: string, id: string): Promise<TopicRecord> {
  const response = await request.get(`${topicsURL}/records/${id}`, { headers: authorization(key) });
  expect(response.status()).toBe(200);
  return (await response.json()).data;
}

async function readTopics(request: APIRequestContext, key: string): Promise<TopicRecord[]> {
  const response = await request.get(`${topicsURL}/records?kind=topic&limit=200`, { headers: authorization(key) });
  expect(response.status()).toBe(200);
  const result: TopicRecordPage = (await response.json()).data;
  return result.records;
}

async function clickSave(page: Page) {
  const [response] = await Promise.all([
    page.waitForResponse((response) => response.request().method() === "POST" && new URL(response.url()).pathname === "/ui/api/topics/edits"),
    page.getByRole("button", { name: "Save", exact: true }).click(),
  ]);
  return response;
}

async function addEdit(page: Page, reason: string) {
  await page.getByRole("button", { name: "Add topic change", exact: true }).click();
  await page.getByLabel("Audit reason").fill(reason);
}

test("manager saves, replays and clears a Topic parent", async ({ page, request }, testInfo) => {
  test.setTimeout(90_000);
  await page.setViewportSize(testInfo.project.name === "mobile" ? { width: 390, height: 844 } : { width: 1440, height: 1000 });
  const team = await createTeam(request);
  const manager = await createCredential(request, team, "manager");
  const definition = (key: string): TopicDefinition => ({ key, label: key, description: "Organizational topic" });
  await saveEdits(request, manager, "Define parent Topics", ["parent", "alternate"].map((key) => definitionEdit(definition(key))));
  const records = await readTopics(request, manager);
  const parent = records.find((record) => record.definition?.key === "parent")!;
  const alternate = records.find((record) => record.definition?.key === "alternate")!;
  const child = (await saveEdits(request, manager, "Define child Topic", [
    definitionEdit({ ...definition("child"), parent_id: parent.id }),
  ])).records[0];

  await signIn(page, manager);
  await navigate(page, "Topics");
  const editChild = page.getByRole("button", { name: "Edit child", exact: true });
  await expect(page.locator(".mobile-navigation")).toBeHidden();
  for (const theme of ["Light", "Dark"]) {
    await page.getByRole("button", { name: "Choose theme", exact: true }).click();
    await page.getByRole("menuitem", { name: theme, exact: true }).click();
    await expect(editChild).toBeVisible();
    await checkAccess(page, `${theme} populated Topic catalog`);
    await editChild.click();
    await expect(page.getByLabel("Definition parent")).toHaveValue(parent.id);
    await checkAccess(page, `${theme} populated Topic editor`);
    if (theme === "Light") await tab(page, "Catalog").click();
  }
  await expect(page.getByLabel("Definition parent")).toHaveValue(parent.id);
  await page.getByLabel("Definition parent").fill(alternate.id);
  await addEdit(page, "Change Topic parent");
  const response = await clickSave(page);
  expect(response.status()).toBe(200);
  const publication: TopicPublication = (await response.json()).data;
  const payload: SaveTopicEdits = response.request().postDataJSON();
  await expect(page.getByRole("status").filter({ hasText: "Topic changes saved." })).toBeVisible();

  const replay = await request.post(`${topicsURL}/edits`, { headers: authorization(manager), data: payload });
  expect(replay.status()).toBe(200);
  expect((await replay.json()).data).toMatchObject({ id: publication.id, revision: publication.revision, existing: true });
  expect((await readRecord(request, manager, child.id)).definition?.parent_id).toBe(alternate.id);

  await tab(page, "Catalog").click();
  await editChild.click();
  await expect(page.getByLabel("Definition parent")).toHaveValue(alternate.id);
  await page.getByLabel("Definition parent").fill("");
  await addEdit(page, "Make child standalone");
  expect((await clickSave(page)).status()).toBe(200);
  expect((await readRecord(request, manager, child.id)).definition?.parent_id).toBeUndefined();
  await expectPageContained(page);
});

async function checkAccess(page: Page, view: string) {
  const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21aa"]).analyze();
  expect(violations.map(({ id, nodes }) => ({ id, elements: nodes.map(({ target }) => target) })), view).toEqual([]);
  await expectPageContained(page);
}

test("Control Overview keeps required metrics when telemetry is disabled", async ({ page }) => {
  await page.goto(`${controlURL}/`);
  await page.getByLabel("Control token", { exact: true }).fill(process.env.DENSE_MEM_CONTROL_TOKEN!);
  await page.getByRole("button", { name: "Unlock", exact: true }).click();
  const telemetry = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/control/api/telemetry" && url.searchParams.get("team_id") === teamID;
  });
  await page.locator(".team-directory-card").filter({ hasText: process.env.DENSE_MEM_E2E_TEAM_NAME! }).click();
  expect((await telemetry).status()).toBe(503);
  await expect(page.getByText("Telemetry unavailable", { exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Top signals", exact: true }).locator(".metric-row")).toHaveCount(4);
  await expect(page.getByRole("region", { name: "Team activity", exact: true })).not.toContainText("metrics unavailable");
});

test("manager saves with cookie authentication and reviews a real version conflict", async ({ page, request }) => {
  const team = await createTeam(request);
  const manager = await createCredential(request, team, "manager");
  const member = await createCredential(request, team, "member");
  expect(await readTopics(request, manager)).toEqual([]);
  const label = `Browser vocabulary ${randomUUID()}`;
  const reason = "Review browser vocabulary";
  await signIn(page, manager);
  await navigate(page, "Topics");
  await page.getByRole("button", { name: "New topic", exact: true }).click();
  await page.getByLabel("Definition key").fill(`browser-${randomUUID()}`);
  await page.getByLabel("Definition label").fill(label);
  await page.getByLabel("Definition description").fill("Original browser description");
  await addEdit(page, "Create Topic through the cookie session");
  const creation = await clickSave(page);
  expect(creation.status()).toBe(200);
  expect(await creation.request().headerValue("authorization")).toBeNull();
  expect(await creation.request().headerValue("x-dense-mem-csrf")).not.toBeNull();
  const created: TopicPublication = (await creation.json()).data;
  const id = created.records[0].id;
  const noCSRF = await page.evaluate(async () => (await fetch("/ui/api/topics/edits", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: "{}",
  })).status);
  expect(noCSRF).toBe(403);

  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByLabel("API key")).toBeVisible();
  await signIn(page, manager);
  await navigate(page, "Topics");
  expect(await page.evaluate(() => sessionStorage.getItem("denseMem.userApiKey"))).toBeNull();
  await page.getByRole("button", { name: `Edit ${label}`, exact: true }).click();
  await expect(page.getByLabel("Definition description")).toHaveValue("Original browser description");

  await page.getByLabel("Definition description").fill("Conflicting browser edit");
  await addEdit(page, reason);
  const current = await readRecord(request, manager, id);
  await saveEdits(request, manager, "Intervening revision", [
    definitionEdit({ ...current.definition!, description: "Intervening description" }, current),
  ]);
  const rejected = await clickSave(page);
  expect(rejected.status()).toBe(409);
  await expect(page.getByRole("alert").filter({ hasText: /refresh the record/i })).toBeVisible();
  await expect(page.getByLabel("Audit reason")).toHaveValue(reason);
  await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
  expect((await readRecord(request, manager, id)).definition?.description).toBe("Intervening description");

  await page.getByRole("button", { name: "Refresh records", exact: true }).click();
  await expect(page.getByRole("status").filter({ hasText: /Topic records refreshed/ })).toBeVisible();
  await expect(page.getByLabel("Definition description")).toHaveValue("Intervening description");
  await expect(page.getByLabel("Audit reason")).toHaveValue(reason);
  await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
  await page.getByLabel("Definition description").fill("Reviewed browser edit");
  await addEdit(page, reason);
  expect((await clickSave(page)).status()).toBe(200);
  expect((await readRecord(request, manager, id)).definition?.description).toBe("Reviewed browser edit");
  await expectPageContained(page);

  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByLabel("API key")).toBeVisible();
  await signIn(page, member);
  await expect(page.getByRole("link", { name: "Topics", exact: true })).toHaveCount(0);
  const denied = await request.get(`${topicsURL}/status`, { headers: authorization(member) });
  expect(denied.status()).toBe(403);
});

test("manager inspects shared sources and keeps both revisions in Topic history", async ({ page, request }) => {
  const manager = await createCredential(request, teamID, "manager");
  const records = await readTopics(request, manager);
  const topic = records.find((record) => !record.retired && record.definition?.label === "Manager storage vocabulary")!;
  expect(topic?.definition).toBeTruthy();
  const original = topic.definition!;
  const description = `Shared database facilities reviewed ${randomUUID()}`;
  const reason = `Browser catalog edit ${randomUUID()}`;

  await signIn(page, manager);
  await navigate(page, "Topics");
  await tab(page, "Sources").click();
  await expect(page.getByRole("heading", { name: "Shared sources", exact: true })).toBeVisible();
  for (const text of ["Atlas stores PostgreSQL data.", "Atlas uses PostgreSQL for data storage."]) {
    await expect(page.getByText(text, { exact: true })).toBeVisible();
  }

  await tab(page, "Catalog").click();
  const editTopic = page.getByRole("button", { name: `Edit ${original.label}`, exact: true });
  await editTopic.click();
  await page.getByLabel("Definition description").fill(description);
  await tab(page, "Catalog").click();
  await editTopic.click();
  await confirmAction(page, "Cancel");
  await tab(page, "Edit").click();
  await expect(page.getByLabel("Definition description")).toHaveValue(description);
  await addEdit(page, reason);
  const response = await clickSave(page);
  expect(response.status()).toBe(200);
  const publication: TopicPublication = (await response.json()).data;
  expect((await readRecord(request, manager, topic.id)).definition?.description).toBe(description);

  await tab(page, "Catalog").click();
  await editTopic.click();
  await page.getByLabel("Definition description").fill(original.description);
  const restoreReason = `Restore original Topic description ${randomUUID()}`;
  await addEdit(page, restoreReason);
  const restored = await clickSave(page);
  expect(restored.status()).toBe(200);
  const restoration: TopicPublication = (await restored.json()).data;
  expect((await readRecord(request, manager, topic.id)).definition).toEqual(original);

  await tab(page, "History").click();
  for (const [saved, auditReason] of [[publication, reason], [restoration, restoreReason]] as const) {
    await page.getByRole("button", { name: `Revision ${saved.revision} · manager · ${auditReason}`, exact: true }).click();
    await expect(page.getByRole("heading", { name: `Publication revision ${saved.revision}`, exact: true })).toBeVisible();
    await expect(page.getByLabel("Publication detail", { exact: true })).toContainText(original.label);
  }
  await expectPageContained(page);
});
