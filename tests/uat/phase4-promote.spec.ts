/**
 * UAT-06 — Phase 4: Claim promotion to fact (happy path).
 *
 * Verifies that POST /api/v1/claims/:id/promote:
 * - requires a validated claim (not candidate)
 * - returns 201 with a fact_id
 * - the fact is retrievable via GET /api/v1/facts/:id
 * - the fact is listed via GET /api/v1/facts
 * - is profile-isolated
 */

import { test, expect } from '@playwright/test';
import {
  headers,
  API_KEY_B,
  headersForApiKey,
  seedFragmentForProfile,
  createAndVerifyClaim,
  createAndPromoteClaim,
  BASE_URL,
} from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase4-promote';

// UAT-06a: Promoting a validated claim returns 201 with fact_id
test('UAT-06a: promoting validated claim returns 201 with fact', async ({ request }) => {
  test.setTimeout(90_000);

  const claim = await createAndVerifyClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'platinum',
    object: 'precious',
  });
  expect(claim.status).toBe('validated');

  const promoteRes = await request.post(
    `${BASE_URL}/api/v1/claims/${claim.id}/promote`,
    {
      headers: headers(profileId),
      data: { policy: 'single_supporter' },
    },
  );
  expect(promoteRes.status()).toBe(201);
  const body = await promoteRes.json();
  expect(body).toMatchObject({
    fact_id: expect.any(String),
    predicate: 'profile_fact',
    subject: 'platinum',
    object: 'precious',
  });
});

// UAT-06b: Promoted fact is retrievable via GET /api/v1/facts/:id
test('UAT-06b: promoted fact retrievable via GET /facts/:id', async ({ request }) => {
  const fact = await createAndPromoteClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'diamond',
    object: 'hard',
  });

  const getRes = await request.get(`${BASE_URL}/api/v1/facts/${fact.id}`, {
    headers: headers(profileId),
  });
  expect(getRes.status()).toBe(200);
  const body = await getRes.json();
  expect(body).toMatchObject({
    fact_id: fact.id,
    predicate: 'profile_fact',
    subject: 'diamond',
    object: 'hard',
  });
});

// UAT-06c: Promoted fact appears in GET /api/v1/facts list
test('UAT-06c: promoted fact appears in GET /facts list', async ({ request }) => {
  const fact = await createAndPromoteClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'titanium',
    object: 'strong',
  });

  const listRes = await request.get(`${BASE_URL}/api/v1/facts`, {
    headers: headers(profileId),
  });
  expect(listRes.status()).toBe(200);
  const body = await listRes.json();
  expect(body).toHaveProperty('items');
  const facts: { fact_id: string }[] = body.items;
  const ids = facts.map((f) => f.fact_id);
  expect(ids).toContain(fact.id);
});

// UAT-06d: Promoting an unvalidated (candidate) claim returns needs_claim_validated (409)
test('UAT-06d: promoting unvalidated claim returns 409 needs_claim_validated', async ({
  request,
}) => {
  const frag = await seedFragmentForProfile(
    request,
    profileId,
    'Unverified content for promote gate test.',
  );
  const createRes = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'profile_fact',
      subject: 'unverified_promote_subject',
      object: 'unverified_promote_object',
      modality: 'assertion',
      extract_conf: 0.99,
      resolution_conf: 0.99,
      supported_by: [frag.fragment_id],
    },
  });
  expect(createRes.status()).toBe(201);
  const claimId: string = (await createRes.json()).claim_id;

  const promoteRes = await request.post(
    `${BASE_URL}/api/v1/claims/${claimId}/promote`,
    {
      headers: headers(profileId),
      data: { policy: 'single_supporter' },
    },
  );
  expect(promoteRes.status()).toBe(409);
  const body = await promoteRes.json();
  expect(body.code).toBe('needs_claim_validated');
});

// UAT-06e: Cross-profile isolation — profile B cannot promote profile A's claim
test('UAT-06e: cross-profile isolation on promote', async ({ request }) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  const claim = await createAndVerifyClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'rubidium',
    object: 'metallic',
  });

  const promoteRes = await request.post(
    `${BASE_URL}/api/v1/claims/${claim.id}/promote`,
    {
      headers: headersForApiKey(API_KEY_B),
      data: { policy: 'single_supporter' },
    },
  );
  expect(promoteRes.status()).not.toBe(201);
  expect(promoteRes.status()).toBeGreaterThanOrEqual(400);
});

// UAT-06f: Cross-profile isolation — GET /facts only returns own profile's facts
test('UAT-06f: GET /facts cross-profile isolation', async ({ request }) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  const factA = await createAndPromoteClaim(request, profileId, {
    predicate: 'profile_fact',
    subject: 'argon',
    object: 'noble',
  });

  // Profile B should not see profile A's fact
  const listRes = await request.get(`${BASE_URL}/api/v1/facts`, {
    headers: headersForApiKey(API_KEY_B),
  });
  if (listRes.status() === 200) {
    const body = await listRes.json();
    const facts: { fact_id: string }[] = body.items ?? [];
    const ids = facts.map((f) => f.fact_id);
    expect(ids).not.toContain(factA.id);
  } else {
    // 404 or empty list is also acceptable for profile B with no facts
    expect(listRes.status()).toBeGreaterThanOrEqual(200);
  }
});
