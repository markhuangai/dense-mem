/**
 * UAT-08 — Phase 4: Concurrent promotion safety.
 *
 * Verifies that concurrent POST /api/v1/claims/:id/promote calls for the same
 * claim create no duplicate facts under contention.
 *
 * Uses Promise.all to fire parallel promote requests and asserts:
 * - at least one 201 response
 * - all 201 responses share the same fact_id
 * - no 5xx errors occur
 */

import { test, expect } from '@playwright/test';
import {
  headers,
  API_KEY_B,
  headersForApiKey,
  createAndVerifyClaim,
  BASE_URL,
} from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase4-concurrency';

// UAT-08a: Concurrent promotes on the same claim produce exactly one fact
test('UAT-08a: concurrent promotes produce exactly one fact', async ({ request }) => {
  test.setTimeout(90_000);

  const claim = await createAndVerifyClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'xenon_concurrency',
    object: 'inert',
  });
  expect(claim.status).toBe('validated');

  // Fire 5 concurrent promote requests for the same claim
  const concurrency = 5;
  const results = await Promise.allSettled(
    Array.from({ length: concurrency }).map(() =>
      request.post(`${BASE_URL}/api/v1/claims/${claim.id}/promote`, {
        headers: headers(profileId),
        data: { policy: 'single_supporter' },
      }),
    ),
  );

  const responses = results.filter((r) => r.status === 'fulfilled').map(
    (r) => (r as PromiseFulfilledResult<Awaited<ReturnType<typeof request.post>>>).value,
  );

  // All requests should complete without 5xx
  for (const res of responses) {
    expect(res.status()).toBeLessThan(500);
  }

  // At least one should be 201 (the winner)
  const created = responses.filter((r) => r.status() === 201);
  expect(created.length).toBeGreaterThanOrEqual(1);

  // All 201 responses must share the same fact_id
  const factIds = new Set<string>();
  for (const res of created) {
    const body = await res.json();
    factIds.add(body.fact_id);
  }
  expect(factIds.size).toBe(1);
});

// UAT-08b: Repeated sequential promotes do not create another fact
test('UAT-08b: sequential repeat promote is rejected after first promotion', async ({
  request,
}) => {
  const claim = await createAndVerifyClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'neon_repeat_promote',
    object: 'gaseous',
  });

  const promote = () =>
    request.post(`${BASE_URL}/api/v1/claims/${claim.id}/promote`, {
      headers: headers(profileId),
      data: { policy: 'single_supporter' },
    });

  const res1 = await promote();
  const res2 = await promote();

  expect(res1.status()).toBe(201);
  expect(res2.status()).toBe(409);

  const body1 = await res1.json();
  const body2 = await res2.json();
  expect(body1.fact_id).toBeDefined();
  expect(body2.code).toBe('needs_claim_validated');
});

// UAT-08c: Cross-profile isolation under concurrency
test('UAT-08c: concurrent promotes are profile-isolated', async ({ request }) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  // Profile A validates and promotes
  const claimA = await createAndVerifyClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'krypton_isolated',
    object: 'rare',
  });

  // Profile B concurrently tries to promote profile A's claim — must fail
  const results = await Promise.allSettled(
    Array.from({ length: 3 }).map(() =>
      request.post(`${BASE_URL}/api/v1/claims/${claimA.id}/promote`, {
        headers: headersForApiKey(API_KEY_B),
        data: { policy: 'single_supporter' },
      }),
    ),
  );

  for (const r of results) {
    if (r.status === 'fulfilled') {
      expect(r.value.status()).toBeGreaterThanOrEqual(400);
      expect(r.value.status()).toBeLessThan(500);
    }
  }
});
