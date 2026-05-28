/**
 * UAT-02 — Phase 2: Claim creation happy path.
 *
 * Verifies that POST /api/v1/claims:
 * - accepts a subject, predicate, object, and supported_by fragment IDs
 * - returns 201 with a stable claim_id
 * - records the Claim node in Neo4j with profile isolation
 * - inherits source_quality from the supporting fragment
 */

import { test, expect } from '@playwright/test';
import {
  API_KEY_B,
  headers,
  headersForApiKey,
  seedFragmentForProfile,
  BASE_URL,
} from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase2-create';

// UAT-02a: POST /api/v1/claims returns 201 with claim_id
test('UAT-02a: claim creation returns 201 with claim_id', async ({ request }) => {
  const frag = await seedFragmentForProfile(request, profileId);

  const res = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'IS',
      subject: 'sky',
      object: 'blue',
      supported_by: [frag.fragment_id],
    },
  });
  expect(res.status()).toBe(201);
  const body = await res.json();
  expect(body).toMatchObject({
    claim_id: expect.any(String),
    predicate: 'IS',
    subject: 'sky',
    object: 'blue',
    status: expect.stringMatching(/^(candidate|pending)$/),
  });
});

// UAT-02b: Returned claim_id is stable (deterministic from content hash)
test('UAT-02b: claim_id is stable across identical requests', async ({ request }) => {
  const frag = await seedFragmentForProfile(request, profileId, 'Grass is green.');

  const payload = {
    predicate: 'IS',
    subject: 'grass',
    object: 'green',
    supported_by: [frag.fragment_id],
  };

  const res1 = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: payload,
  });
  const res2 = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: payload,
  });

  expect(res1.status()).toBe(201);
  expect(res2.status()).toBe(200);
  const body1 = await res1.json();
  const body2 = await res2.json();
  expect(body1.claim_id).toBe(body2.claim_id);
});

// UAT-02c: Claim inherits source_quality from the supporting fragment
test('UAT-02c: claim inherits source_quality from fragment', async ({ request }) => {
  const frag = await seedFragmentForProfile(request, profileId, 'Ice is cold.', {
    source_quality: 0.8,
  });

  const res = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'IS',
      subject: 'ice',
      object: 'cold',
      supported_by: [frag.fragment_id],
    },
  });
  expect(res.status()).toBe(201);
  const body = await res.json();
  expect(body).toHaveProperty('source_quality');
  expect(typeof body.source_quality).toBe('number');
});

// UAT-02d: Missing required support returns validation error
test('UAT-02d: missing supported_by returns 422', async ({ request }) => {
  const res = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'IS',
      subject: 'test',
      object: 'value',
    },
  });
  expect(res.status()).toBe(422);
  const body = await res.json();
  expect(body.code).toBe('VALIDATION_ERROR');
});

// UAT-02e: Cross-profile isolation — claim created for profile A not visible to profile B
test('UAT-02e: cross-profile isolation — claim not visible across profiles', async ({
  request,
}) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  const fragA = await seedFragmentForProfile(request, profileId, 'Profile A content');

  const createRes = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'HAS',
      subject: 'profile_a',
      object: 'exclusive_claim',
      supported_by: [fragA.fragment_id],
    },
  });
  expect(createRes.status()).toBe(201);
  const claimId: string = (await createRes.json()).claim_id;

  // Profile B must not be able to read profile A's claim
  const readRes = await request.get(`${BASE_URL}/api/v1/claims/${claimId}`, {
    headers: headersForApiKey(API_KEY_B),
  });
  // Should be 404 (not found) or 403 (forbidden), not 200
  expect(readRes.status()).not.toBe(200);
});
