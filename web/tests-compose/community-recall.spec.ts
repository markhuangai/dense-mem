import { navigate, openNavigation, confirmAction, expectPageContained } from "../tests-support/portal";
import { expect, test, type Page } from "@playwright/test";

const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const userUrl = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const teamId = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const teamName = requiredEnv("DENSE_MEM_E2E_TEAM_NAME");
const apiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");

test("control workspace exposes the current community snapshot", async ({ page }) => {
  await openControlPanel(page);
  await page.getByRole("button", { name: new RegExp(escapeRegExp(teamName)) }).click();

  await expect(page.getByText("Communities", { exact: true })).toBeVisible();
  await expect(page.getByText("Community snapshot", { exact: true })).toBeVisible();
  await expect(page.getByText(/Ontology enabled|Ontology disabled/)).toBeVisible();
});

test("user recall renders a community result and the exact nested contract", async ({ page, request }) => {
  const response = await request.get(`${userUrl}/ui/api/recall`, {
    params: { query: "Atlas Redis", limit: "5" },
    headers: { Authorization: `Bearer ${apiKey}` },
  });
  expect(response.status()).toBe(200);
  const body = await response.json() as { data?: { related_communities?: unknown[] } };
  const communities = body.data?.related_communities;
  expect(Array.isArray(communities)).toBe(true);
  expect(communities?.length).toBeGreaterThan(0);
  const community = communities?.[0] as Record<string, unknown>;
  expect(Object.keys(community).sort()).toEqual([
    "community_id",
    "logical_community_id",
    "entity_count",
    "rank",
    "relationship_count",
    "relationships",
    "relationships_truncated",
    "summary",
    "top_entities",
    "top_predicates",
  ].sort());
  expect(Array.isArray(community.relationships)).toBe(true);
  expect(community).not.toHaveProperty("evidence_ids");
  expect(community).not.toHaveProperty("related_relationships");

  await page.goto(`${userUrl}/ui/`);
  await page.getByLabel("API key").fill(apiKey);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByLabel("Current workspace")).toBeVisible();
  await page.getByLabel("Keyword").fill("Atlas Redis");
  const recallResponsePromise = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === "GET" && url.pathname === "/ui/api/recall" && url.searchParams.get("query") === "Atlas Redis";
  });
  await page.getByRole("button", { name: "Search", exact: true }).click();
  const searchResponse = await recallResponsePromise;
  expect(searchResponse.status()).toBe(200);
  const searchBody = await searchResponse.json() as { data: { related_communities: unknown[] } };

  await expect(page.getByRole("listbox", { name: "Recall result list" })).toBeVisible();
  const communityResults = page.getByRole("listbox", { name: "Recall result list" }).getByText("Community", { exact: true });
  await expect(communityResults).toHaveCount(searchBody.data.related_communities.length);
  await expect(communityResults.first()).toBeVisible();
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByRole("checkbox", { name: /Community/ }).uncheck();
  await page.keyboard.press("Escape");
  await expect(communityResults).toHaveCount(0);
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByRole("checkbox", { name: /Community/ }).check();
  await page.keyboard.press("Escape");
  await expect(communityResults).toHaveCount(searchBody.data.related_communities.length);
  await page.getByRole("option").first().focus();
  await page.keyboard.press("Enter");
  await page.getByRole("tab", { name: "Evidence", exact: true }).focus();
  await page.keyboard.press("ArrowRight");
  await expect(page.getByRole("tab", { name: "Lineage", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("button", { name: "Close details panel", exact: true }).click();
  await expectPageContained(page);
});

async function openControlPanel(page: Page) {
  await page.goto(`${controlUrl}/`);
  await page.getByLabel("Control token").fill(controlToken);
  await page.getByRole("button", { name: "Unlock" }).click();
  await expect(page.getByRole("heading", { name: "Teams" })).toBeVisible();
}

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

void teamId;
