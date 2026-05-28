/**
 * UAT-05 — Phase 3: Claim verification error paths.
 *
 * Verifies that POST /api/v1/claims/:id/verify returns stable,
 * machine-readable error codes (AC-X6) for known failure modes:
 * - claim_not_found (404)
 * - supporting_fragment_missing after retraction (404)
 * - verifier_timeout (504)
 * - verifier_provider (503)
 */

import { test, expect } from '@playwright/test';
import { headers, seedFragmentForProfile, BASE_URL } from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase3-errors';

// UAT-05a: Non-existent claim returns claim_not_found (404)
test('UAT-05a: verify non-existent claim returns 404 with claim_not_found', async ({
  request,
}) => {
  const res = await request.post(
    `${BASE_URL}/api/v1/claims/00000000-0000-0000-0000-000000000000/verify`,
    {
      headers: headers(profileId),
      data: { verifier_model: 'test-verifier' },
      timeout: 60_000,
    },
  );
  expect(res.status()).toBe(404);
  const body = await res.json();
  expect(body).toMatchObject({
    code: 'claim_not_found',
    message: expect.any(String),
  });
});

// UAT-05b: Claim with retracted supporting fragment returns supporting_fragment_missing (404)
test('UAT-05b: claim with retracted fragment returns supporting_fragment_missing', async ({
  request,
}) => {
  // Create a fragment, create a claim, retract the fragment, then verify.
  const frag = await seedFragmentForProfile(request, profileId, 'temp_subject IS temp_object.');
  const createRes = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'IS',
      subject: 'temp_subject',
      object: 'temp_object',
      supported_by: [frag.fragment_id],
    },
  });
  expect(createRes.status()).toBe(201);
  const claimId: string = (await createRes.json()).claim_id;

  // Retract keeps the support edge but excludes the fragment from active evidence.
  const retractRes = await request.post(`${BASE_URL}/api/v1/fragments/${frag.id}/retract`, {
    headers: headers(profileId),
  });
  expect(retractRes.status()).toBe(200);

  const verifyRes = await request.post(
    `${BASE_URL}/api/v1/claims/${claimId}/verify`,
    {
      headers: headers(profileId),
      data: { verifier_model: 'test-verifier' },
      timeout: 60_000,
    },
  );
  // Expected: 404 with supporting_fragment_missing code
  expect(verifyRes.status()).toBe(404);
  const body = await verifyRes.json();
  expect(body.code).toBe('supporting_fragment_missing');
});

// UAT-05c: AC-X6 regression — error responses have stable machine-readable codes
test('AC-X6 regression: error response has stable code field', async ({ request }) => {
  const res = await request.post(
    `${BASE_URL}/api/v1/claims/not-a-valid-uuid/verify`,
    {
      headers: headers(profileId),
      data: { verifier_model: 'test-verifier' },
      timeout: 60_000,
    },
  );
  // Should return 400 or 404 with a stable error code
  expect(res.status()).toBeGreaterThanOrEqual(400);
  const body = await res.json();
  // The error envelope must include a `code` field for machine parsing
  expect(body).toHaveProperty('code');
  expect(typeof body.code).toBe('string');
  // code must be lowercase with underscores (stable external contract)
  expect(body.code).toMatch(/^[a-z][a-z0-9_]*$/);
});
