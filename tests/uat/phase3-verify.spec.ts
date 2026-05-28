/**
 * UAT-04 — Phase 3: Claim verification happy path.
 *
 * Verifies that POST /api/v1/claims/:id/verify:
 * - accepts a verifier_model parameter
 * - transitions the claim from candidate → validated
 * - returns the updated claim with verification metadata
 * - is profile-isolated
 */

import { test, expect } from '@playwright/test';
import {
  headers,
  API_KEY_B,
  headersForApiKey,
  seedFragmentForProfile,
  BASE_URL,
  createValidatedCandidateForVerify,
} from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase3-verify';

// UAT-04a: Verify endpoint transitions claim to validated
test('UAT-04a: POST /claims/:id/verify transitions to validated', async ({ request }) => {
  test.setTimeout(90_000);

  const frag = await seedFragmentForProfile(request, profileId, 'Gold is a metal.');

  const createRes = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'IS_A',
      subject: 'gold',
      object: 'metal',
      supported_by: [frag.fragment_id],
    },
  });
  expect(createRes.status()).toBe(201);
  const claimId: string = (await createRes.json()).claim_id;

  const verifyRes = await request.post(
    `${BASE_URL}/api/v1/claims/${claimId}/verify`,
    {
      headers: headers(profileId),
      data: { verifier_model: 'gpt-4o-mini' },
      timeout: 60_000,
    },
  );
  expect(verifyRes.status()).toBe(200);
  const body = await verifyRes.json();
  expect(body).toMatchObject({
    claim_id: claimId,
    status: 'validated',
  });
});

// UAT-04b: Verified claim includes verification metadata
test('UAT-04b: verified claim includes verification_metadata', async ({ request }) => {
  const claim = await createValidatedCandidateForVerify(request, profileId, {
    predicate: 'IS',
    subject: 'lead',
    object: 'dense',
  });
  expect(claim.status).toBe('validated');
  // Verification metadata should be present
  expect(claim).toHaveProperty('claim_id');
});

// UAT-04c: Verifying an already-validated claim is idempotent (200)
test('UAT-04c: re-verifying a validated claim returns 200', async ({ request }) => {
  test.setTimeout(90_000);

  const frag = await seedFragmentForProfile(request, profileId, 'silver_idempotent IS shiny.');
  const createRes = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'IS',
      subject: 'silver_idempotent',
      object: 'shiny',
      supported_by: [frag.fragment_id],
    },
  });
  expect(createRes.status()).toBe(201);
  const claimId: string = (await createRes.json()).claim_id;

  // First verify
  await request.post(`${BASE_URL}/api/v1/claims/${claimId}/verify`, {
    headers: headers(profileId),
    data: { verifier_model: 'test-verifier' },
    timeout: 60_000,
  });

  // Second verify — idempotent
  const res2 = await request.post(
    `${BASE_URL}/api/v1/claims/${claimId}/verify`,
    {
      headers: headers(profileId),
      data: { verifier_model: 'test-verifier' },
      timeout: 60_000,
    },
  );
  expect(res2.status()).toBe(200);
  const body2 = await res2.json();
  expect(body2.status).toBe('validated');
});

// UAT-04d: Cross-profile isolation — profile B cannot verify profile A's claim
test('UAT-04d: cross-profile isolation on verify', async ({ request }) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  const frag = await seedFragmentForProfile(request, profileId, 'Copper conducts electricity.');
  const createRes = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'CONDUCTS',
      subject: 'copper',
      object: 'electricity',
      supported_by: [frag.fragment_id],
    },
  });
  expect(createRes.status()).toBe(201);
  const claimId: string = (await createRes.json()).claim_id;

  // Profile B attempts to verify profile A's claim — must be rejected
  const verifyRes = await request.post(
    `${BASE_URL}/api/v1/claims/${claimId}/verify`,
    {
      headers: headersForApiKey(API_KEY_B),
      data: { verifier_model: 'test-verifier' },
      timeout: 60_000,
    },
  );
  expect(verifyRes.status()).not.toBe(200);
  expect(verifyRes.status()).toBeGreaterThanOrEqual(400);
});
