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
  await expect(page.getByText(/Nightly enabled|Nightly disabled/)).toBeVisible();
});

test("user recall renders a community result and the exact nested contract", async ({ page, request }) => {
  const response = await request.get(`${userUrl}/ui/api/recall`, {
    params: { query: "Dense-Mem Runtime PostgreSQL", limit: "5" },
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
  await expect(page.getByRole("heading", { name: "Dense-Mem Knowledge" })).toBeVisible();
  await page.getByLabel("Keyword").fill("Dense-Mem Runtime PostgreSQL");
  await page.getByRole("button", { name: "Search", exact: true }).click();

  await expect(page.getByRole("listbox", { name: "Recall result list" })).toBeVisible();
  await expect(page.getByRole("listbox", { name: "Recall result list" }).getByText("Community", { exact: true })).toBeVisible();
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
