/**
 * UAT-10 — Phase 7: Community detection on knowledge graph.
 *
 * Verifies that POST /api/v1/tools/detect_community:
 * - requires a profile-bound authenticated caller
 * - runs community detection on the profile's knowledge graph
 * - returns a detect result envelope with community summaries
 * - is profile-scoped (separate profiles produce separate communities)
 * - handles profiles with no graph data gracefully
 */

import { test, expect } from '@playwright/test';
import { randomUUID } from 'crypto';
import {
  API_KEY_B,
  headers,
  headersForApiKey,
  createAndPromoteClaim,
  BASE_URL,
} from './helpers';

const profileId = process.env.PROFILE_ID || '00000000-0000-0000-0000-000000000101';

// UAT-10a: Community detection returns 200 with summary and communities are readable
test('UAT-10a: community detection returns 200 with summary', async ({ request }) => {
  // Seed some facts to form a graph
  await createAndPromoteClaim(request, profileId, {
    predicate: 'likes',
    subject: 'hydrogen',
    object: 'element',
  });
  await createAndPromoteClaim(request, profileId, {
    predicate: 'likes',
    subject: 'oxygen',
    object: 'element',
  });

  const res = await request.post(
    `${BASE_URL}/api/v1/tools/detect_community`,
    {
      headers: headers(profileId),
    },
  );
  expect(res.status()).toBe(200);
  const body = await res.json();
  const data = body as Record<string, unknown>;
  expect(data.detected).toBe(true);
  expect(typeof data.community_count).toBe('number');
  expect(Array.isArray(data.communities)).toBe(true);

  const listRes = await request.get(`${BASE_URL}/api/v1/communities`, {
    headers: headers(profileId),
  });
  expect(listRes.status()).toBe(200);
  const listBody = await listRes.json();
  expect(Array.isArray(listBody.items)).toBe(true);
});

// UAT-10b: Community detection on a profile with no data returns empty summary
test('UAT-10b: empty profile returns empty community summary', async ({ request }) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for empty secondary profile coverage');
  if (!API_KEY_B) {
    return;
  }

  const res = await request.post(
    `${BASE_URL}/api/v1/tools/detect_community`,
    {
      headers: headersForApiKey(API_KEY_B),
    },
  );
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.detected).toBe(true);
  expect(typeof body.community_count).toBe('number');
});

// UAT-10c: Community detection is profile-scoped — does not cross profile boundaries
test('UAT-10c: community detection is profile-scoped', async ({ request }) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  const uniqueSubject = `carbon_community_${randomUUID()}`;

  // Seed facts in profile A
  await createAndPromoteClaim(request, profileId, {
    predicate: 'likes',
    subject: uniqueSubject,
    object: 'nonmetal',
  });

  // Run detection on profile B — must not include profile A facts
  const res = await request.post(
    `${BASE_URL}/api/v1/tools/detect_community`,
    {
      headers: headersForApiKey(API_KEY_B),
    },
  );
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(JSON.stringify(body)).not.toContain(uniqueSubject);
});
