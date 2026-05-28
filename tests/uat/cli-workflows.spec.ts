/**
 * UAT - Container operator CLI workflow.
 *
 * Requires the docker compose stack to be running from the repository root.
 * Uses the server container's operator binaries against the live Postgres DB.
 */

import { test, expect } from '@playwright/test';
import { execFile } from 'node:child_process';
import path from 'node:path';
import { promisify } from 'node:util';
import { BASE_URL } from './helpers';

const execFileAsync = promisify(execFile);
const REPO_ROOT = path.resolve(__dirname, '../..');

type ProvisionOutput = {
  team_id: string;
  profile_id: string;
  scopes: string[];
  api_key: string;
};

type ListOutput = {
  items: Array<{
    profile_id: string;
    name: string;
    scopes: string[];
  }>;
};

type RotateOutput = {
  team_id: string;
  profile_id: string;
  scopes: string[];
  api_key: string;
};

type DeleteOutput = {
  team_id: string;
  profile_id?: string;
  status: string;
};

function authHeaders(apiKey: string): Record<string, string> {
  return {
    Authorization: `Bearer ${apiKey}`,
    'Content-Type': 'application/json',
  };
}

async function composeJSON<T>(binary: string, args: string[]): Promise<T> {
  const { stdout } = await execFileAsync(
    'docker',
    ['compose', 'exec', '-T', 'server', `/app/${binary}`, ...args],
    {
      cwd: REPO_ROOT,
      env: process.env,
      maxBuffer: 1024 * 1024,
    },
  );
  return JSON.parse(stdout) as T;
}

async function tryDeleteTeam(teamId: string): Promise<void> {
  try {
    await composeJSON<DeleteOutput>('delete-team', ['-team-id', teamId]);
  } catch {
    // Best-effort cleanup only; the primary test assertions already failed.
  }
}

test('container CLI provisions, lists, rotates, and deletes a team profile', async ({ request }) => {
  const provisioned = await composeJSON<ProvisionOutput>('provision-team', [
    '-name',
    `uat-cli-${Date.now()}`,
    '-rate-limit',
    '120',
  ]);

  try {
    expect(provisioned.team_id).toEqual(expect.any(String));
    expect(provisioned.profile_id).toEqual(expect.any(String));
    expect(provisioned.scopes).toEqual(['read', 'write']);
    expect(typeof provisioned.api_key).toBe('string');
    expect(provisioned.api_key.length).toBeGreaterThan(16);

    const list = await composeJSON<ListOutput>('list-team-profiles', [
      '-team-id',
      provisioned.team_id,
    ]);
    expect(list.items).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          profile_id: provisioned.profile_id,
          scopes: ['read', 'write'],
        }),
      ]),
    );

    const rotated = await composeJSON<RotateOutput>('rotate-team-profile-key', [
      '-team-id',
      provisioned.team_id,
      '-profile-id',
      provisioned.profile_id,
    ]);
    expect(rotated.team_id).toBe(provisioned.team_id);
    expect(rotated.profile_id).toBe(provisioned.profile_id);
    expect(rotated.scopes).toEqual(['read', 'write']);
    expect(typeof rotated.api_key).toBe('string');
    expect(rotated.api_key.length).toBeGreaterThan(16);

    const oldKeyUse = await request.get(`${BASE_URL}/api/v1/tools`, {
      headers: authHeaders(provisioned.api_key),
    });
    expect(oldKeyUse.status()).toBe(401);

    const newKeyUse = await request.get(`${BASE_URL}/api/v1/tools`, {
      headers: authHeaders(rotated.api_key),
    });
    expect(newKeyUse.status()).toBe(200);

    const deleted = await composeJSON<DeleteOutput>('delete-team-profile', [
      '-team-id',
      provisioned.team_id,
      '-profile-id',
      provisioned.profile_id,
    ]);
    expect(deleted).toMatchObject({
      team_id: provisioned.team_id,
      profile_id: provisioned.profile_id,
      status: 'deleted',
    });

    const deletedKeyUse = await request.get(`${BASE_URL}/api/v1/tools`, {
      headers: authHeaders(rotated.api_key),
    });
    expect(deletedKeyUse.status()).toBe(401);
  } finally {
    await tryDeleteTeam(provisioned.team_id);
  }
});
