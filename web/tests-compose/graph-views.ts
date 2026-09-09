import { expect, test, type APIRequestContext } from "@playwright/test";

const userUrl = requiredEnv("DENSE_MEM_USER_URL").replace(/\/$/, "");
const controlUrl = requiredEnv("DENSE_MEM_CONTROL_URL").replace(/\/$/, "");
const controlToken = requiredEnv("DENSE_MEM_CONTROL_TOKEN");
const seedTeamID = requiredEnv("DENSE_MEM_E2E_TEAM_ID");
const seedApiKey = requiredEnv("DENSE_MEM_E2E_API_KEY");
const dreamStatement = requiredEnv("DENSE_MEM_E2E_DREAM_STATEMENT");
let mcpRequestID = 0;

type GraphNode = {
  id?: string;
  key?: string;
  type?: string;
  title?: string;
};

type GraphSnapshot = {
  scope?: string;
  depth?: number;
  limit?: number;
  anchor?: { id?: string };
  nodes?: GraphNode[];
  edges?: Array<{ id?: string; source?: string; target?: string }>;
};

type GraphEntity = {
  id: string;
  key: string;
  type: "entity";
};

export function registerGraphViewTests() {
  test("graph views preserve scoped reads and adverse boundaries", async ({ request }, testInfo) => {
    test.setTimeout(120_000);
    const headers = { Authorization: `Bearer ${seedApiKey}` };
    const overviewResponse = await request.get(`${userUrl}/ui/api/graph`, {
      headers,
      params: { scope: "overview" },
    });
    expect(overviewResponse.status()).toBe(200);
    const overview = (await overviewResponse.json() as { data?: GraphSnapshot }).data;
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
    if (!anchor?.id) throw new Error("seeded overview has no entity anchor");

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
    const local = (await localResponse.json() as { data?: GraphSnapshot }).data;
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

    await assertGraphIsolation(request, testInfo, anchor.id);
  });
}

registerGraphViewTests();

function requiredEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required for graph e2e`);
  return value;
}

async function assertGraphIsolation(
  request: APIRequestContext,
  testInfo: { project: { name: string }; workerIndex: number },
  seedAnchorID: string,
) {
  const fixtureID = `graph-isolation-${testInfo.project.name}-${testInfo.workerIndex}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const privateCredential = await createCredential(request, seedTeamID, `${fixtureID}-private`, "credential_private");
  const foreignTeamID = await createTeam(request, `${fixtureID}-foreign`);
  const foreignCredential = await createCredential(request, foreignTeamID, `${fixtureID}-foreign`, "credential_private");
  const privateEntityName = `${fixtureID}-private-entity`;
  const foreignEntityName = `${fixtureID}-foreign-entity`;

  await rememberGraphEntity(request, privateCredential.apiKey, `${fixtureID}:private`, privateEntityName);
  await rememberGraphEntity(request, foreignCredential.apiKey, `${fixtureID}:foreign`, foreignEntityName);

  const privateNode = await waitForGraphEntity(request, privateCredential.apiKey, privateEntityName);
  const foreignNode = await waitForGraphEntity(request, foreignCredential.apiKey, foreignEntityName);

  const privateOverview = await graphSnapshot(request, privateCredential.apiKey, { scope: "overview" });
  expect(privateOverview.nodes?.map((node) => node.id)).toContain(privateNode.id);
  const privateDetail = await request.get(`${userUrl}/ui/api/node-detail`, {
    headers: bearer(privateCredential.apiKey),
    params: { type: privateNode.type, id: privateNode.id },
  });
  expect(privateDetail.status()).toBe(200);
  const privateDetailBody = await privateDetail.json() as { data?: GraphNode };
  expect(privateDetailBody.data?.id).toBe(privateNode.id);

  const seedOverview = await graphSnapshot(request, seedApiKey, { scope: "overview" });
  expectGraphExcludesNode(seedOverview, privateNode);
  expectGraphExcludesNode(seedOverview, foreignNode);
  await expectNodeDetailNotFound(request, seedApiKey, privateNode);
  await expectNodeDetailNotFound(request, seedApiKey, foreignNode);

  const foreignOverview = await graphSnapshot(request, foreignCredential.apiKey, { scope: "overview" });
  expectGraphExcludesNode(foreignOverview, { id: seedAnchorID, key: `entity:${seedAnchorID}`, type: "entity" });
  await expectNodeDetailNotFound(request, foreignCredential.apiKey, { id: seedAnchorID, key: `entity:${seedAnchorID}`, type: "entity" });

  await expectInaccessibleLocalGraph(request, seedApiKey, privateNode);
  await expectInaccessibleLocalGraph(request, seedApiKey, foreignNode);
  await expectInaccessibleLocalGraph(request, foreignCredential.apiKey, { id: seedAnchorID, key: `entity:${seedAnchorID}`, type: "entity" });
}

async function createTeam(request: APIRequestContext, name: string): Promise<string> {
  const response = await request.post(`${controlUrl}/control/api/teams`, {
    headers: bearer(controlToken),
    data: { name, description: "graph view isolation fixture" },
  });
  expect(response.status()).toBe(201);
  const body = await response.json() as { data?: { id?: string } };
  return requiredString(body.data?.id, "created team ID");
}

async function createCredential(
  request: APIRequestContext,
  teamID: string,
  name: string,
  memoryBinding: "credential_private",
): Promise<{ apiKey: string }> {
  const response = await request.post(`${controlUrl}/control/api/teams/${teamID}/credentials`, {
    headers: bearer(controlToken),
    data: { name, scopes: ["read", "write"], rate_limit: 300, memory_binding: memoryBinding },
  });
  expect(response.status()).toBe(201);
  const body = await response.json() as { data?: { api_key?: string } };
  return { apiKey: requiredString(body.data?.api_key, "created credential API key") };
}

async function rememberGraphEntity(
  request: APIRequestContext,
  apiKey: string,
  idempotencyKey: string,
  entityName: string,
) {
  const objectName = `${entityName}-target`;
  const payload = await mcpToolPayload(request, apiKey, "remember", {
    idempotency_key: idempotencyKey,
    evidence: [{
      content: `${entityName} mentions ${objectName}.`,
      source_type: "manual",
      source: "graph-view-isolation-e2e",
      source_group: idempotencyKey,
    }],
    relationships: [{
      ref: `${idempotencyKey}:relationship`,
      subject: { name: entityName, entity_kind: "project" },
      predicate: { proposed_key: "mentions" },
      object: { entity: { name: objectName, entity_kind: "concept" } },
      polarity: "+",
      evidence_indices: [0],
    }],
  });
  expect(payload.processing_state).toBe("completed");
}

async function waitForGraphEntity(
  request: APIRequestContext,
  apiKey: string,
  entityName: string,
): Promise<GraphEntity> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    const snapshot = await graphSnapshot(request, apiKey, { scope: "overview", q: entityName });
    const node = snapshot.nodes?.find((item) => item.type === "entity" && item.title === entityName);
    if (node?.id && node.key) return { id: node.id, key: node.key, type: "entity" };
    if (attempt + 1 < 20) await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error("remembered graph entity did not become visible to its owner");
}

async function graphSnapshot(
  request: APIRequestContext,
  apiKey: string,
  params: Record<string, string>,
): Promise<GraphSnapshot> {
  const response = await request.get(`${userUrl}/ui/api/graph`, {
    headers: bearer(apiKey),
    params,
  });
  expect(response.status()).toBe(200);
  const body = await response.json() as { data?: GraphSnapshot };
  if (!body.data) throw new Error("graph response omitted data");
  return body.data;
}

function expectGraphExcludesNode(snapshot: GraphSnapshot, node: GraphEntity) {
  expect(snapshot.nodes?.some((item) => item.id === node.id)).toBe(false);
  expect(snapshot.edges?.some((edge) => edge.source === node.key || edge.target === node.key)).toBe(false);
}

async function expectNodeDetailNotFound(request: APIRequestContext, apiKey: string, node: GraphEntity) {
  const response = await request.get(`${userUrl}/ui/api/node-detail`, {
    headers: bearer(apiKey),
    params: { type: node.type, id: node.id },
  });
  expect(response.status()).toBe(404);
}

async function expectInaccessibleLocalGraph(request: APIRequestContext, apiKey: string, node: GraphEntity) {
  const snapshot = await graphSnapshot(request, apiKey, {
    scope: "local",
    anchor_type: node.type,
    anchor_id: node.id,
  });
  expect(snapshot.scope).toBe("local");
  expect(snapshot.anchor?.id).toBe(node.id);
  expectGraphExcludesNode(snapshot, node);
}

async function mcpToolPayload(
  request: APIRequestContext,
  apiKey: string,
  name: string,
  args: Record<string, unknown>,
): Promise<{ processing_state?: string }> {
  const response = await request.post(`${userUrl}/mcp`, {
    headers: {
      ...bearer(apiKey),
      Accept: "application/json",
      "MCP-Protocol-Version": "2025-11-25",
    },
    data: {
      jsonrpc: "2.0",
      id: ++mcpRequestID,
      method: "tools/call",
      params: { name, arguments: args },
    },
  });
  expect(response.status()).toBe(200);
  const body = await response.json() as {
    error?: unknown;
    result?: { isError?: boolean; content?: Array<{ text?: string }> };
  };
  if (body.error || body.result?.isError) throw new Error(`MCP ${name} returned an error`);
  const text = body.result?.content?.[0]?.text;
  if (typeof text !== "string") throw new Error(`MCP ${name} omitted JSON content`);
  return JSON.parse(text) as { processing_state?: string };
}

function bearer(token: string) {
  return { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
}

function requiredString(value: string | undefined, field: string): string {
  if (!value) throw new Error(`${field} is missing`);
  return value;
}
