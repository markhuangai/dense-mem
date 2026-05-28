/**
 * UAT - Team profile control-plane lifecycle.
 *
 * Requires a running dense-mem server and a read/write API key for TEAM_ID.
 * Covers profile key create/list/get/rotate/delete, expiry rejection, per-key
 * rate limits, and audit entries for API key lifecycle operations.
 */

import { test, expect, APIRequestContext } from '@playwright/test';
import { API_KEY, BASE_URL, PROFILE_ID } from './helpers';

const TEAM_ID = process.env.TEAM_ID || PROFILE_ID;

type CreatedTeamProfile = {
  apiKey: string;
  profileId: string;
  scopes: string[];
};

type AuditEntry = {
  operation: string;
  entity_type: string;
  entity_id: string;
  after_payload?: Record<string, unknown>;
  before_payload?: Record<string, unknown>;
};

function authHeaders(apiKey = API_KEY): Record<string, string> {
  return {
    Authorization: `Bearer ${apiKey}`,
    'Content-Type': 'application/json',
  };
}

function uniqueName(prefix: string): string {
  return `${prefix}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

async function createTeamProfile(
  request: APIRequestContext,
  input: {
    name: string;
    scopes: string[];
    rateLimit?: number;
    expiresAt?: string;
  },
): Promise<CreatedTeamProfile> {
  const res = await request.post(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles`, {
    headers: authHeaders(),
    data: {
      name: input.name,
      scopes: input.scopes,
      rate_limit: input.rateLimit ?? 120,
      expires_at: input.expiresAt,
    },
  });
  expect(res.status()).toBe(201);

  const body = await res.json();
  expect(body.data.key.id).toEqual(expect.any(String));
  expect(body.data.key.scopes).toEqual(input.scopes);
  expect(body.data.api_key).toEqual(expect.any(String));

  return {
    apiKey: body.data.api_key as string,
    profileId: body.data.key.id as string,
    scopes: body.data.key.scopes as string[],
  };
}

async function deleteTeamProfile(request: APIRequestContext, profileId: string): Promise<void> {
  const res = await request.delete(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles/${profileId}`, {
    headers: authHeaders(),
  });
  expect([200, 404]).toContain(res.status());
}

async function getToolsStatus(request: APIRequestContext, apiKey: string): Promise<number> {
  const res = await request.get(`${BASE_URL}/api/v1/tools`, {
    headers: authHeaders(apiKey),
  });
  return res.status();
}

test('team profile keys cover lifecycle, expiry, rate-limit, and audit logs', async ({ request }) => {
  const cleanupProfileIds = new Set<string>();

  try {
    const expired = await createTeamProfile(request, {
      name: uniqueName('uat-expired'),
      scopes: ['read'],
      expiresAt: new Date(Date.now() - 60_000).toISOString(),
    });
    cleanupProfileIds.add(expired.profileId);

    const expiredUse = await request.get(`${BASE_URL}/api/v1/tools`, {
      headers: authHeaders(expired.apiKey),
    });
    expect(expiredUse.status()).toBe(401);
    const expiredBody = await expiredUse.json();
    expect(['AUTH_INVALID', 'AUTH_EXPIRED']).toContain(expiredBody.code);

    const managed = await createTeamProfile(request, {
      name: uniqueName('uat-lifecycle'),
      scopes: ['read', 'write'],
      rateLimit: 120,
    });
    cleanupProfileIds.add(managed.profileId);

    const listRes = await request.get(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles`, {
      headers: authHeaders(),
    });
    expect(listRes.status()).toBe(200);
    const listBody = await listRes.json();
    const listed = listBody.data.find((item: { id: string }) => item.id === managed.profileId);
    expect(listed).toMatchObject({
      id: managed.profileId,
      scopes: ['read', 'write'],
      rate_limit: 120,
    });

    const getRes = await request.get(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles/${managed.profileId}`, {
      headers: authHeaders(),
    });
    expect(getRes.status()).toBe(200);
    const getBody = await getRes.json();
    expect(getBody.data).toMatchObject({
      id: managed.profileId,
      scopes: ['read', 'write'],
    });
    expect(getBody.data.api_key).toBeUndefined();
    expect(getBody.data.key_hash).toBeUndefined();

    const rotateWithScopes = await request.post(
      `${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles/${managed.profileId}/rotate`,
      {
        headers: authHeaders(),
        data: {
          name: uniqueName('uat-rotate-invalid'),
          scopes: ['read'],
          rate_limit: 120,
        },
      },
    );
    expect(rotateWithScopes.status()).toBe(422);

    const rotateRes = await request.post(
      `${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles/${managed.profileId}/rotate`,
      {
        headers: authHeaders(),
        data: {
          name: uniqueName('uat-rotated'),
          rate_limit: 120,
        },
      },
    );
    expect(rotateRes.status()).toBe(200);
    const rotateBody = await rotateRes.json();
    const rotatedKey = rotateBody.data.api_key as string;
    expect(rotatedKey).toEqual(expect.any(String));
    expect(rotateBody.data.key).toMatchObject({
      id: managed.profileId,
      scopes: ['read', 'write'],
    });

    expect(await getToolsStatus(request, managed.apiKey)).toBe(401);
    expect(await getToolsStatus(request, rotatedKey)).toBe(200);

    const rateLimited = await createTeamProfile(request, {
      name: uniqueName('uat-rate-limit'),
      scopes: ['read'],
      rateLimit: 2,
    });
    cleanupProfileIds.add(rateLimited.profileId);

    const firstRead = await request.get(`${BASE_URL}/api/v1/tools`, {
      headers: authHeaders(rateLimited.apiKey),
    });
    const secondRead = await request.get(`${BASE_URL}/api/v1/tools`, {
      headers: authHeaders(rateLimited.apiKey),
    });
    const thirdRead = await request.get(`${BASE_URL}/api/v1/tools`, {
      headers: authHeaders(rateLimited.apiKey),
    });
    expect(firstRead.status()).toBe(200);
    expect(secondRead.status()).toBe(200);
    expect(thirdRead.status()).toBe(429);
    const rateBody = await thirdRead.json();
    expect(rateBody.code).toBe('RATE_LIMITED');

    const deleteRes = await request.delete(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles/${managed.profileId}`, {
      headers: authHeaders(),
    });
    expect(deleteRes.status()).toBe(200);
    cleanupProfileIds.delete(managed.profileId);
    expect(await getToolsStatus(request, rotatedKey)).toBe(401);

    const auditRes = await request.get(`${BASE_URL}/api/v1/teams/${TEAM_ID}/audit-log`, {
      headers: authHeaders(),
      params: { limit: '100' },
    });
    expect(auditRes.status()).toBe(200);
    const auditBody = await auditRes.json();
    const entries = auditBody.data as AuditEntry[];

    expect(entries).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          operation: 'CREATE',
          entity_type: 'api_key',
          entity_id: managed.profileId,
        }),
        expect.objectContaining({
          operation: 'ROTATE_KEY',
          entity_type: 'team_profile',
          entity_id: managed.profileId,
        }),
        expect.objectContaining({
          operation: 'DELETE',
          entity_type: 'api_key',
          entity_id: managed.profileId,
        }),
      ]),
    );
  } finally {
    for (const profileId of cleanupProfileIds) {
      await deleteTeamProfile(request, profileId);
    }
  }
});
