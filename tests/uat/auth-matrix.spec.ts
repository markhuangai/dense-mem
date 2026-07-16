/**
 * UAT - Protected route authentication and authorization matrix.
 *
 * Exercises auth failures across every protected route family, then verifies a
 * read-only profile key cannot reach write surfaces.
 */

import { test, expect, APIRequestContext } from '@playwright/test';
import { API_KEY, BASE_URL, PROFILE_ID } from './helpers';

const TEAM_ID = process.env.TEAM_ID || PROFILE_ID;
const ID = '00000000-0000-0000-0000-000000000000';

type RouteCase = {
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE';
  path: string;
  data?: unknown;
  headers?: Record<string, string>;
};

function authHeaders(apiKey = API_KEY): Record<string, string> {
  return {
    Authorization: `Bearer ${apiKey}`,
    'Content-Type': 'application/json',
  };
}

function protectedRoutes(): RouteCase[] {
  return [
    { method: 'GET', path: `/api/v1/teams/${TEAM_ID}` },
    { method: 'PATCH', path: `/api/v1/teams/${TEAM_ID}`, data: {} },
    { method: 'GET', path: `/api/v1/teams/${TEAM_ID}/audit-log` },
    { method: 'GET', path: `/api/v1/teams/${TEAM_ID}/profiles` },
    { method: 'POST', path: `/api/v1/teams/${TEAM_ID}/profiles`, data: {} },
    { method: 'GET', path: `/api/v1/teams/${TEAM_ID}/profiles/${ID}` },
    { method: 'POST', path: `/api/v1/teams/${TEAM_ID}/profiles/${ID}/rotate`, data: {} },
    { method: 'DELETE', path: `/api/v1/teams/${TEAM_ID}/profiles/${ID}` },
    { method: 'GET', path: '/api/v1/tools' },
    { method: 'GET', path: '/api/v1/tools/recall_memory' },
    { method: 'POST', path: '/api/v1/tools/recall_memory', data: { query: 'noop' } },
    { method: 'POST', path: '/api/v1/tools/remember', data: { evidence: [{ content: 'noop' }] } },
    { method: 'POST', path: '/api/v1/tools/get_memory_placement', data: { ingest_id: ID } },
    { method: 'POST', path: '/api/v1/tools/resolve_memory_placement', data: { ingest_id: ID, message: 'noop' } },
    { method: 'GET', path: '/api/v1/recall?query=noop' },
    { method: 'POST', path: '/mcp', data: { jsonrpc: '2.0', id: 1, method: 'tools/list', params: {} } },
    { method: 'GET', path: '/mcp' },
    { method: 'GET', path: '/api/v1/openapi.json' },
  ];
}

function readOnlyWriteRoutes(profileId: string): RouteCase[] {
  return [
    { method: 'PATCH', path: `/api/v1/teams/${TEAM_ID}`, data: { name: 'denied team rename' } },
    { method: 'POST', path: `/api/v1/teams/${TEAM_ID}/profiles`, data: { name: 'denied profile', scopes: ['read'], rate_limit: 120 } },
    { method: 'POST', path: `/api/v1/teams/${TEAM_ID}/profiles/${profileId}/rotate`, data: { name: 'denied rotate', rate_limit: 120 } },
    { method: 'DELETE', path: `/api/v1/teams/${TEAM_ID}/profiles/${profileId}` },
    { method: 'POST', path: '/api/v1/tools/remember', data: { evidence: [{ content: 'denied' }] } },
    { method: 'POST', path: '/api/v1/tools/resolve_memory_placement', data: { ingest_id: ID, message: 'denied' } },
  ];
}

async function send(
  request: APIRequestContext,
  route: RouteCase,
  headers: Record<string, string>,
) {
  const options = {
    headers: { ...headers, ...(route.headers ?? {}) },
    data: route.data,
  };
  const url = `${BASE_URL}${route.path}`;
  switch (route.method) {
    case 'GET':
      return request.get(url, options);
    case 'POST':
      return request.post(url, options);
    case 'PATCH':
      return request.patch(url, options);
    case 'DELETE':
      return request.delete(url, options);
  }
}

async function createReadOnlyProfile(request: APIRequestContext): Promise<{ apiKey: string; profileId: string }> {
  const res = await request.post(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles`, {
    headers: authHeaders(),
    data: {
      name: `uat-auth-readonly-${Date.now()}`,
      scopes: ['read'],
      rate_limit: 120,
    },
  });
  expect(res.status()).toBe(201);
  const body = await res.json();
  return {
    apiKey: body.data.api_key as string,
    profileId: body.data.key.id as string,
  };
}

async function deleteTeamProfile(request: APIRequestContext, profileId: string): Promise<void> {
  const res = await request.delete(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles/${profileId}`, {
    headers: authHeaders(),
  });
  expect([200, 404]).toContain(res.status());
}

test('protected route families reject missing, malformed, and invalid auth', async ({ request }) => {
  for (const route of protectedRoutes()) {
    const missing = await send(request, route, { 'Content-Type': 'application/json' });
    expect(missing.status(), `${route.method} ${route.path} should reject missing auth`).toBe(401);
    expect((await missing.json()).code).toBe('AUTH_MISSING');

    const malformed = await send(request, route, {
      Authorization: 'Token not-bearer',
      'Content-Type': 'application/json',
    });
    expect(malformed.status(), `${route.method} ${route.path} should reject malformed auth`).toBe(401);
    expect((await malformed.json()).code).toBe('AUTH_INVALID');

    const invalid = await send(request, route, {
      Authorization: 'Bearer not-a-valid-dense-mem-key',
      'Content-Type': 'application/json',
    });
    expect(invalid.status(), `${route.method} ${route.path} should reject invalid auth`).toBe(401);
    expect((await invalid.json()).code).toBe('AUTH_INVALID');
  }
});

test('read-only profile key receives 403 from write route families', async ({ request }) => {
  const created = await createReadOnlyProfile(request);

  try {
    for (const route of readOnlyWriteRoutes(created.profileId)) {
      const res = await send(request, route, authHeaders(created.apiKey));
      expect(res.status(), `${route.method} ${route.path} should reject read-only key`).toBe(403);
      expect((await res.json()).code).toBe('FORBIDDEN');
    }
  } finally {
    await deleteTeamProfile(request, created.profileId);
  }
});
