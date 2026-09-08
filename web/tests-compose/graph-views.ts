import { expect, test } from "@playwright/test";

const userUrl = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const seedApiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const dreamStatement = requiredEnv("DENSE_MEM_E2E_DREAM_STATEMENT");

export function registerGraphViewTests() {
  test("graph views preserve scoped reads and adverse boundaries", async ({ request }) => {
    const headers = { Authorization: `Bearer ${seedApiKey}` };
    const overviewResponse = await request.get(`${userUrl}/ui/api/graph`, {
      headers,
      params: { scope: "overview" },
    });
    expect(overviewResponse.status()).toBe(200);
    const overview = (await overviewResponse.json() as {
      data?: {
        scope?: string;
        depth?: number;
        limit?: number;
        nodes?: Array<{ id?: string; key?: string; type?: string }>;
        edges?: Array<{ id?: string; source?: string; target?: string }>;
      };
    }).data;
    expect(overview?.scope).toBe("overview");
    expect(overview?.depth).toBe(2);
    expect(overview?.limit).toBe(80);
    expect(Array.isArray(overview?.nodes)).toBe(true);
    expect(Array.isArray(overview?.edges)).toBe(true);
    for (const node of overview?.nodes ?? []) {
      expect(["entity", "value"]).toContain(node.type);
      expect(node.id).toBeTruthy();
      expect(node.key).toBeTruthy();
    }
    for (const edge of overview?.edges ?? []) {
      expect(edge.id).toBeTruthy();
      expect(edge.source).toBeTruthy();
      expect(edge.target).toBeTruthy();
    }

    const repeatResponse = await request.get(`${userUrl}/ui/api/graph`, {
      headers,
      params: { scope: "overview" },
    });
    expect(repeatResponse.status()).toBe(200);
    const repeat = (await repeatResponse.json() as { data?: unknown }).data;
    expect(JSON.stringify(repeat)).toBe(JSON.stringify(overview));

    const dreamsResponse = await request.get(`${userUrl}/ui/api/dreams`, {
      headers,
      params: { limit: "20" },
    });
    expect(dreamsResponse.status()).toBe(200);
    const dreams = (await dreamsResponse.json() as {
      data?: { items?: Array<{ dream_id?: string; hypothesis?: string }> };
    }).data?.items ?? [];
    const seededDream = dreams.find((dream) => dream.hypothesis === dreamStatement);
    expect(seededDream?.dream_id).toBeTruthy();
    const graphNodeIDs = new Set((overview?.nodes ?? []).map((node) => node.id));
    for (const dream of dreams) {
      expect(dream.dream_id).toBeTruthy();
      expect(graphNodeIDs.has(dream.dream_id!)).toBe(false);
    }
    expect(graphNodeIDs.has(seededDream!.dream_id!)).toBe(false);

    const anchor = (overview?.nodes ?? []).find((node) => node.type === "entity");
    expect(anchor?.id).toBeTruthy();
    if (!anchor?.id) return;

    const localResponse = await request.get(`${userUrl}/ui/api/graph`, {
      headers,
      params: {
        scope: "local",
        anchor_type: "entity",
        anchor_id: anchor.id,
        depth: "5",
        limit: "181",
      },
    });
    expect(localResponse.status()).toBe(200);
    const local = (await localResponse.json() as { data?: { scope?: string; depth?: number; limit?: number; anchor?: { id?: string } } }).data;
    expect(local?.scope).toBe("local");
    expect(local?.depth).toBe(5);
    expect(local?.limit).toBe(181);
    expect(local?.anchor?.id).toBe(anchor.id);

    const detailResponse = await request.get(`${userUrl}/ui/api/node-detail`, {
      headers,
      params: { type: "entity", id: anchor.id },
    });
    expect(detailResponse.status()).toBe(200);
    const detail = (await detailResponse.json() as { data?: { id?: string; type?: string } }).data;
    expect(detail?.id).toBe(anchor.id);
    expect(detail?.type).toBe("entity");

    const invalidTypeResponse = await request.get(`${userUrl}/ui/api/node-detail`, {
      headers,
      params: { type: "hypothesis", id: anchor.id },
    });
    expect(invalidTypeResponse.status()).toBe(422);

    const missingResponse = await request.get(`${userUrl}/ui/api/node-detail`, {
      headers,
      params: { type: "entity", id: "00000000-0000-0000-0000-000000000000" },
    });
    expect(missingResponse.status()).toBe(404);
  });
}

registerGraphViewTests();

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required for graph e2e`);
  return value;
}
