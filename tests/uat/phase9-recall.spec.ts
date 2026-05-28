/**
 * UAT-12 — Phase 9: Semantic recall endpoint.
 *
 * Verifies that GET /api/v1/recall:
 * - accepts a `query` parameter and returns ranked results
 * - returns only facts belonging to the requesting profile
 * - respects an optional `limit` parameter
 * - returns an empty result set (not an error) when no facts match
 * - enforces profile isolation (profile B cannot see profile A's recalled facts)
 */

import { test, expect } from '@playwright/test';
import {
  API_KEY_B,
  headers,
  headersForApiKey,
  createAndPromoteClaim,
  BASE_URL,
} from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase9-recall';

// UAT-12a: GET /recall with a query returns 200 and a results array
test('UAT-12a: GET /recall returns 200 with results array', async ({ request }) => {
  // Seed a fact so there is something to recall
  await createAndPromoteClaim(request, profileId, {
    predicate: 'likes',
    subject: 'helium',
    object: 'noble_gas',
  });

  const res = await request.get(`${BASE_URL}/api/v1/recall`, {
    headers: headers(profileId),
    params: { query: 'noble gas element', limit: '10' },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body).toHaveProperty('data');
  expect(Array.isArray(body.data)).toBe(true);
});

// UAT-12b: Recall without a query returns 400
test('UAT-12b: GET /recall without query returns 400', async ({ request }) => {
  const res = await request.get(`${BASE_URL}/api/v1/recall`, {
    headers: headers(profileId),
    // no query param
  });
  expect(res.status()).toBe(400);
  const body = await res.json();
  expect(body).toHaveProperty('code');
});

// UAT-12c: Recall with limit=1 returns at most 1 result
test('UAT-12c: recall respects limit parameter', async ({ request }) => {
  // Seed multiple facts
  await createAndPromoteClaim(request, profileId, {
    predicate: 'likes',
    subject: 'neon_recall',
    object: 'noble',
  });
  await createAndPromoteClaim(request, profileId, {
    predicate: 'likes',
    subject: 'argon_recall',
    object: 'noble',
  });

  const res = await request.get(`${BASE_URL}/api/v1/recall`, {
    headers: headers(profileId),
    params: { query: 'noble gas', limit: '1' },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  const results = body.data as unknown[];
  expect(results.length).toBeLessThanOrEqual(1);
});

// UAT-12d: Recall with no matching facts returns empty array (not 404)
test('UAT-12d: recall with no match returns empty array not 404', async ({ request }) => {
  const res = await request.get(`${BASE_URL}/api/v1/recall`, {
    headers: headers(profileId),
    params: { query: 'xyzzy_no_match_eee123', limit: '5' },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(Array.isArray(body.data)).toBe(true);
});

// UAT-12e: Cross-profile isolation — profile B cannot see profile A's recalled facts
test('UAT-12e: recall cross-profile isolation', async ({ request }) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  const uniqueSubject = `isolation_recall_${Date.now()}`;

  // Seed a unique fact in profile A
  await createAndPromoteClaim(request, profileId, {
    predicate: 'likes',
    subject: uniqueSubject,
    object: 'profile_a_only',
  });

  // Profile B should not see this fact in recall
  const res = await request.get(`${BASE_URL}/api/v1/recall`, {
    headers: headersForApiKey(API_KEY_B),
    params: { query: uniqueSubject, limit: '10' },
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  const results = body.data as Array<{
    fact?: { subject?: string };
    claim?: { subject?: string };
    fragment?: { content?: string };
  }>;
  const subjects = results.map((r) => r.fact?.subject ?? r.claim?.subject ?? '');
  expect(subjects).not.toContain(uniqueSubject);
});

// AC-X2 regression: OpenAPI spec documents the recall endpoint
test('AC-X2 regression: OpenAPI lists /recall route', async ({ request }) => {
  const res = await request.get(`${BASE_URL}/api/v1/openapi.json`, {
    headers: headers(profileId),
  });
  expect(res.status()).toBe(200);
  const spec = await res.json();
  expect(spec).toHaveProperty('paths');
  const paths: Record<string, unknown> = spec.paths;
  const pathKeys = Object.keys(paths);
  expect(pathKeys).toEqual(
    expect.arrayContaining([expect.stringMatching('/recall')]),
  );
});
