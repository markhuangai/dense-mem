/**
 * UAT — Team profile API key permissions.
 *
 * Requires a running dense-mem server and a read/write API key for the target
 * team. The test creates a temporary read-only team profile key and verifies
 * that HTTP and MCP write surfaces reject it.
 */

import { test, expect, APIRequestContext } from '@playwright/test';
import {
  API_KEY,
  BASE_URL,
  PROFILE_ID,
  spawnMcp,
} from './helpers';

const TEAM_ID = process.env.TEAM_ID || PROFILE_ID;

function authHeaders(apiKey = API_KEY): Record<string, string> {
  return {
    'Authorization': `Bearer ${apiKey}`,
    'Content-Type': 'application/json',
  };
}

async function createReadOnlyProfile(
  request: APIRequestContext,
  testName: string,
): Promise<{ apiKey: string; profileId: string; scopes: string[] }> {
  const name = `uat-readonly-${testName.replace(/[^a-z0-9]+/gi, '-').toLowerCase()}-${Date.now()}`;
  const res = await request.post(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles`, {
    headers: authHeaders(),
    data: {
      name,
      scopes: ['read'],
      rate_limit: 120,
    },
  });
  const text = await res.text();
  expect(res.status(), text).toBe(201);

  const body = JSON.parse(text);
  expect(body.data.api_key).toEqual(expect.any(String));
  expect(body.data.key.id).toEqual(expect.any(String));
  expect(body.data.key.scopes).toEqual(['read']);

  return {
    apiKey: body.data.api_key as string,
    profileId: body.data.key.id as string,
    scopes: body.data.key.scopes as string[],
  };
}

async function deleteTeamProfile(
  request: APIRequestContext,
  profileId: string,
): Promise<void> {
  const res = await request.delete(`${BASE_URL}/api/v1/teams/${TEAM_ID}/profiles/${profileId}`, {
    headers: authHeaders(),
  });
  expect([200, 404], `cleanup profile ${profileId}: ${res.status()} ${await res.text()}`).toContain(res.status());
}

test('read-only profile key can read HTTP routes but cannot write', async ({ request }, testInfo) => {
  const created = await createReadOnlyProfile(request, testInfo.title);

  try {
    const listTools = await request.get(`${BASE_URL}/api/v1/tools`, {
      headers: authHeaders(created.apiKey),
    });
    expect(listTools.status(), await listTools.text()).toBe(200);

    const writeAttempt = await request.post(`${BASE_URL}/api/v1/fragments`, {
      headers: authHeaders(created.apiKey),
      data: {
        content: 'This write should be rejected for a read-only UAT key.',
        source_quality: 0.9,
        classification: { source: 'uat' },
        labels: ['uat', 'permissions'],
      },
    });
    expect(writeAttempt.status(), await writeAttempt.text()).toBe(403);
  } finally {
    await deleteTeamProfile(request, created.profileId);
  }
});

test('read-only profile key only sees and calls read-scoped MCP tools', async ({ request }, testInfo) => {
  const created = await createReadOnlyProfile(request, testInfo.title);
  const mcp = await spawnMcp({
    DENSE_MEM_URL: BASE_URL,
    DENSE_MEM_API_KEY: created.apiKey,
  });

  try {
    await mcp.call('initialize', {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: { name: 'profile-key-permissions-uat', version: '1.0.0' },
    });

    const listResponse = await mcp.call('tools/list', {});
    const list = listResponse as { result: { tools: Array<{ name: string }> } };
    const toolNames = list.result.tools.map((tool) => tool.name);

    expect(toolNames).toContain('recall_memory');
    expect(toolNames).not.toContain('remember');
    expect(toolNames).not.toContain('save_memory');
    expect(toolNames).not.toContain('confirm_memory');

    const writeCall = await mcp.call('tools/call', {
      name: 'remember',
      arguments: { content: 'This MCP write should be rejected.' },
    });
    expect(writeCall).toMatchObject({
      error: {
        message: 'insufficient scope for tool',
      },
    });
  } finally {
    await mcp.close();
    await deleteTeamProfile(request, created.profileId);
  }
});
