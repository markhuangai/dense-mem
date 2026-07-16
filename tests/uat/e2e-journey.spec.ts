/**
 * UAT-13 — End-to-end v2 memory journey.
 *
 * Exercises the production client surface:
 *   remember → get_memory_placement → recall_memory → GET /recall
 *
 * Also covers OpenAPI discoverability, stable error envelopes, and team
 * isolation through two provisioned API keys.
 */

import { test, expect, APIRequestContext } from '@playwright/test';
import {
  API_KEY,
  API_KEY_B,
  BASE_URL,
  headers,
  headersForApiKey,
} from './helpers';

const requireApiKeyB = process.env.REQUIRE_API_KEY_B === '1';

async function executeTool(
  request: APIRequestContext,
  name: string,
  apiKey: string,
  data: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const res = await request.post(`${BASE_URL}/api/v1/tools/${name}`, {
    headers: headersForApiKey(apiKey),
    data,
    timeout: 60_000,
  });
  expect(res.status(), `${name} status`).toBe(200);
  return (await res.json()) as Record<string, unknown>;
}

async function waitForPlacement(
  request: APIRequestContext,
  ingestId: string,
): Promise<Record<string, unknown>> {
  const deadline = Date.now() + 180_000;
  let lastPlacement: Record<string, unknown> | null = null;

  while (Date.now() < deadline) {
    const body = await executeTool(request, 'get_memory_placement', API_KEY, {
      ingest_id: ingestId,
    });
    const placement = body.placement as Record<string, unknown> | undefined;
    expect(placement, 'placement body').toBeTruthy();
    lastPlacement = placement ?? null;

    const status = placement?.status;
    if (status === 'completed') {
      return placement;
    }
    if (status === 'failed') {
      throw new Error(`placement failed: ${String(placement?.error ?? '')}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 2_000));
  }

  throw new Error(
    `placement did not complete before timeout: ${JSON.stringify(lastPlacement)}`,
  );
}

function resultContexts(body: Record<string, unknown>): string[] {
  const results = body.results as Array<{ context?: string }> | undefined;
  return (results ?? []).map((result) => result.context ?? '');
}

function wrappedResultContexts(body: Record<string, unknown>): string[] {
  const data = body.data as Record<string, unknown> | undefined;
  return resultContexts(data ?? {});
}

test('UAT-13: production v2 remember and recall journey', async ({ request }) => {
  test.setTimeout(240_000);

  const token = `uat_speed_of_light_${Date.now()}`;
  const memoryText = `${token}: The speed of light in a vacuum is exactly 299792458 meters per second.`;

  const remember = await executeTool(request, 'remember', API_KEY, {
    evidence: [
      {
        content: memoryText,
        source_type: 'conversation',
        source: 'uat-e2e-journey',
        authority: 'primary',
        labels: ['uat', 'physics'],
        metadata: { test: 'e2e-journey', token },
      },
    ],
  });

  expect(remember.status).toBe('queued');
  expect(typeof remember.ingest_id).toBe('string');
  expect(remember.status_tool).toBe('get_memory_placement');

  const placement = await waitForPlacement(request, String(remember.ingest_id));
  expect(Array.isArray(placement.items)).toBe(true);
  expect(JSON.stringify(placement.items)).toContain('fragment_id');

  const recallTool = await executeTool(request, 'recall_memory', API_KEY, {
    query: `${token} speed of light value`,
    limit: 10,
  });
  expect(Array.isArray(recallTool.results)).toBe(true);
  expect(recallTool.discovery_guidance).toContain('recall_memory');
  expect(Array.isArray(recallTool.related_hypotheses)).toBe(true);
  expect(resultContexts(recallTool).join('\n')).toContain('299792458');

  const recallRes = await request.get(`${BASE_URL}/api/v1/recall`, {
    headers: headers(),
    params: { query: `${token} vacuum speed`, limit: '10' },
    timeout: 60_000,
  });
  expect(recallRes.status()).toBe(200);
  const recallBody = (await recallRes.json()) as Record<string, unknown>;
  expect(wrappedResultContexts(recallBody).join('\n')).toContain('299792458');
});

test('OpenAPI documents the production v2 memory surface', async ({ request }) => {
  const res = await request.get(`${BASE_URL}/api/v1/openapi.json`, {
    headers: headers(),
  });
  expect(res.status()).toBe(200);
  const spec = await res.json();
  expect(spec).toHaveProperty('paths');
  const paths = spec.paths as Record<string, unknown>;

  expect(Object.keys(paths)).toEqual(
    expect.arrayContaining([
      '/api/v1/tools/remember',
      '/api/v1/tools/recall_memory',
      '/api/v1/tools/{name}',
      '/api/v1/recall',
    ]),
  );
});

test('error envelope includes stable code field', async ({ request }) => {
  const res = await request.get(`${BASE_URL}/api/v1/tools/not_a_real_tool`, {
    headers: headers(),
  });
  expect(res.status()).toBeGreaterThanOrEqual(400);
  const body = await res.json();
  expect(typeof body.code).toBe('string');
  expect(body.code.length).toBeGreaterThan(0);
});

test('Cross-team isolation: profile B cannot recall profile A memory', async ({
  request,
}) => {
  test.setTimeout(240_000);
  if (!API_KEY_B) {
    if (requireApiKeyB) {
      throw new Error('API_KEY_B is required when REQUIRE_API_KEY_B=1');
    }
    test.skip(true, 'API_KEY_B is required for cross-team isolation');
    return;
  }

  const token = `uat_isolation_${Date.now()}`;
  const memoryText = `${token}: Profile A owns the isolated memory value alpha-only.`;

  const remember = await executeTool(request, 'remember', API_KEY, {
    evidence: [
      {
        content: memoryText,
        source_type: 'conversation',
        source: 'uat-isolation',
        authority: 'primary',
        labels: ['uat', 'isolation'],
      },
    ],
  });
  await waitForPlacement(request, String(remember.ingest_id));

  const profileARecall = await executeTool(request, 'recall_memory', API_KEY, {
    query: `${token} isolated memory`,
    limit: 10,
  });
  expect(resultContexts(profileARecall).join('\n')).toContain(token);

  const profileBRecall = await executeTool(request, 'recall_memory', API_KEY_B, {
    query: `${token} isolated memory`,
    limit: 10,
  });
  expect(resultContexts(profileBRecall).join('\n')).not.toContain(token);
});
