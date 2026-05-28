/**
 * UAT-03 — Phase 2: Claim deduplication.
 *
 * Verifies that:
 * - creating a claim with the same (subject, predicate, object) returns the
 *   same claim_id regardless of which fragment backs it
 * - the dedupe is profile-scoped: same triple in profile B is a different claim
 */

import { test, expect } from '@playwright/test';
import {
  API_KEY_B,
  headers,
  headersForApiKey,
  seedFragmentForProfile,
  seedFragmentWithHeaders,
  BASE_URL,
} from './helpers';

const profileId = process.env.PROFILE_ID || 'uat-profile-phase2-dedupe';

// UAT-03a: Same triple from different fragments returns same claim_id
test('UAT-03a: same triple returns existing claim_id (dedupe)', async ({ request }) => {
  const frag1 = await seedFragmentForProfile(request, profileId, 'First source: fire is hot.');
  const frag2 = await seedFragmentForProfile(request, profileId, 'Second source: fire is hot.');

  const payload = (fragmentId: string) => ({
    predicate: 'IS',
    subject: 'fire',
    object: 'hot',
    supported_by: [fragmentId],
  });

  const res1 = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: payload(frag1.fragment_id),
  });
  const res2 = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: payload(frag2.fragment_id),
  });

  expect(res1.status()).toBe(201);
  expect(res2.status()).toBe(200);
  expect(res2.headers()['x-idempotent-replay']).toBe('true');
  const id1: string = (await res1.json()).claim_id;
  const id2: string = (await res2.json()).claim_id;
  expect(id1).toBe(id2);
});

// UAT-03b: Dedupe is profile-scoped — same triple in another profile is distinct
test('UAT-03b: dedupe is profile-scoped', async ({ request }) => {
  test.skip(!API_KEY_B, 'API_KEY_B is required for cross-profile isolation');
  if (!API_KEY_B) {
    return;
  }

  const fragA = await seedFragmentForProfile(
    request,
    profileId,
    'Snow is white — profile A source.',
  );
  const fragB = await seedFragmentWithHeaders(
    request,
    headersForApiKey(API_KEY_B),
    'Snow is white — profile B source.',
  );

  const res1 = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headers(profileId),
    data: {
      predicate: 'IS',
      subject: 'snow',
      object: 'white',
      supported_by: [fragA.fragment_id],
    },
  });
  const res2 = await request.post(`${BASE_URL}/api/v1/claims`, {
    headers: headersForApiKey(API_KEY_B),
    data: {
      predicate: 'IS',
      subject: 'snow',
      object: 'white',
      supported_by: [fragB.fragment_id],
    },
  });

  expect(res1.status()).toBe(201);
  expect(res2.status()).toBe(201);
  const id1: string = (await res1.json()).claim_id;
  const id2: string = (await res2.json()).claim_id;
  // Different profiles → different claim IDs even for identical triples
  expect(id1).not.toBe(id2);
});
