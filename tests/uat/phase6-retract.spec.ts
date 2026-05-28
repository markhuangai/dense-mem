/**
 * UAT-09 — Phase 6: Fragment retraction.
 *
 * Verifies that POST /api/v1/fragments/:id/retract:
 * - marks a fragment as retracted
 * - cascades retraction to any claims that relied solely on the retracted fragment
 * - is profile-isolated (profile B cannot retract profile A's fragments)
 * - returns stable error codes for unknown/already-retracted fragments
 */

import { test, expect } from '@playwright/test';
import {
  headers,
  API_KEY_B,
  headersForApiKey,
  seedFragmentForProfile,
  createAndVerifyClaim,
  BASE_URL,
} from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase6-retract';

// UAT-09a: Retract a fragment returns 200 and marks it retracted
test('UAT-09a: POST /fragments/:id/retract returns 200 and retracted state', async ({
  request,
}) => {
  const frag = await seedFragmentForProfile(request, profileId, 'Fragment to be retracted.');

  const res = await request.post(`${BASE_URL}/api/v1/fragments/${frag.id}/retract`, {
    headers: headers(profileId),
  });
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body).toMatchObject({ status: 'retracted' });
});

// UAT-09b: Retracting an already-retracted fragment is idempotent (200)
test('UAT-09b: retracting an already-retracted fragment is idempotent', async ({
  request,
}) => {
  const frag = await seedFragmentForProfile(request, profileId, 'Fragment retracted twice.');

  // First retraction
  const res1 = await request.post(`${BASE_URL}/api/v1/fragments/${frag.id}/retract`, {
    headers: headers(profileId),
  });
  expect(res1.status()).toBe(200);

  // Second retraction — must be idempotent
  const res2 = await request.post(`${BASE_URL}/api/v1/fragments/${frag.id}/retract`, {
    headers: headers(profileId),
  });
  expect(res2.status()).toBe(200);
});

// UAT-09c: Retracting a non-existent fragment returns 404
test('UAT-09c: retracting non-existent fragment returns 404', async ({ request }) => {
  const res = await request.post(
    `${BASE_URL}/api/v1/fragments/00000000-0000-0000-0000-000000000000/retract`,
    {
      headers: headers(profileId),
    },
  );
  expect(res.status()).toBe(404);
  const body = await res.json();
  expect(body).toHaveProperty('code');
  expect(typeof body.code).toBe('string');
});

// UAT-09d: Cross-profile isolation — profile B cannot retract profile A's fragment
test('UAT-09d: cross-profile isolation — profile B cannot retract profile A fragment', async ({
  request,
}) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  const frag = await seedFragmentForProfile(
    request,
    profileId,
    'Profile A fragment — must not be retractable by profile B.',
  );

  const res = await request.post(`${BASE_URL}/api/v1/fragments/${frag.id}/retract`, {
    headers: headersForApiKey(API_KEY_B),
  });
  // Must return 404 or 403, not 200
  expect(res.status()).not.toBe(200);
  expect(res.status()).toBeGreaterThanOrEqual(400);
});

// UAT-09e: Facts backed solely by a retracted fragment require revalidation
test('UAT-09e: fact backed by retracted fragment is marked needs_revalidation', async ({
  request,
}) => {
  test.setTimeout(90_000);

  const frag = await seedFragmentForProfile(
    request,
    profileId,
    'cascade_subject profile_fact cascade_object.',
  );

  const claim = await createAndVerifyClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'cascade_subject',
    object: 'cascade_object',
    fragmentId: frag.fragment_id,
  });

  const promoteRes = await request.post(`${BASE_URL}/api/v1/claims/${claim.id}/promote`, {
    headers: headers(profileId),
    data: { policy: 'single_supporter' },
  });
  expect(promoteRes.status()).toBe(201);
  const factId: string = (await promoteRes.json()).fact_id;

  // Retract the supporting fragment
  const retractRes = await request.post(`${BASE_URL}/api/v1/fragments/${frag.id}/retract`, {
    headers: headers(profileId),
  });
  expect(retractRes.status()).toBe(200);

  // The fact remains in the graph but needs revalidation after losing support.
  const factRes = await request.get(`${BASE_URL}/api/v1/facts/${factId}`, {
    headers: headers(profileId),
  });
  expect(factRes.status()).toBe(200);
  const body = await factRes.json();
  expect(body.status).toBe('needs_revalidation');
});
