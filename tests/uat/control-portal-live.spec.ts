/**
 * UAT - Live control portal browser flow.
 *
 * Requires CONTROL_PORTAL_TOKEN and the control portal at CONTROL_BASE_URL
 * (default http://127.0.0.1:8090). This test uses the real browser UI and
 * backend, with API cleanup for the disposable team.
 */

import { test, expect } from '@playwright/test';

const CONTROL_BASE_URL = process.env.CONTROL_BASE_URL || 'http://127.0.0.1:8090';
const CONTROL_PORTAL_TOKEN = process.env.CONTROL_PORTAL_TOKEN || '';

type Team = {
  id: string;
  name: string;
};

function controlHeaders(): Record<string, string> {
  return {
    Authorization: `Bearer ${CONTROL_PORTAL_TOKEN}`,
    'Content-Type': 'application/json',
  };
}

async function findTeamIdByName(name: string): Promise<string | null> {
  const res = await fetch(`${CONTROL_BASE_URL}/control/api/teams`, {
    headers: controlHeaders(),
  });
  if (!res.ok) {
    return null;
  }
  const body = await res.json();
  const found = (body.data as Team[]).find((team) => team.name === name);
  return found?.id ?? null;
}

async function deleteTeam(teamId: string): Promise<void> {
  await fetch(`${CONTROL_BASE_URL}/control/api/teams/${teamId}`, {
    method: 'DELETE',
    headers: controlHeaders(),
  });
}

test.skip(!CONTROL_PORTAL_TOKEN, 'CONTROL_PORTAL_TOKEN is required for live control portal UAT');

test('control portal creates, displays, and deletes a read-only team profile', async ({ page }) => {
  const teamName = `UAT Portal ${Date.now()}`;
  const profileName = `portal-readonly-${Date.now()}`;
  let teamId: string | null = null;

  try {
    await page.goto(CONTROL_BASE_URL);
    await page.getByLabel('Control token').fill(CONTROL_PORTAL_TOKEN);
    await page.getByRole('button', { name: 'Unlock' }).click();
    await expect(page.getByRole('heading', { name: 'Teams' })).toBeVisible();

    await page.getByLabel('Name').first().fill(teamName);
    await page.getByLabel('Description').first().fill('live portal UAT team');
    await page.getByRole('button', { name: /^Create$/ }).click();
    await expect(page.getByRole('heading', { name: teamName })).toBeVisible();

    teamId = await findTeamIdByName(teamName);
    expect(teamId).toEqual(expect.any(String));

    await page.getByRole('button', { name: /Profiles & API Keys/ }).click();
    await expect(page.getByRole('heading', { name: 'Profiles' })).toBeVisible();
    await page.getByLabel('Profile name').fill(profileName);
    await page.getByLabel('Permission').selectOption('read');
    await page.getByLabel('Rate limit').fill('120');
    await page.getByRole('button', { name: 'Create profile' }).click();

    await expect(page.locator('.secret-box')).toBeVisible();
    const row = page.getByRole('row', { name: new RegExp(profileName) });
    await expect(row).toContainText('Read only');
    await page.getByRole('button', { name: 'Dismiss API key' }).click();
    await expect(page.locator('.secret-box')).toBeHidden();

    page.once('dialog', (dialog) => dialog.accept());
    await page.getByRole('button', { name: new RegExp(`Delete profile ${profileName}`) }).click();
    await expect(page.getByRole('row', { name: new RegExp(profileName) })).toBeHidden();

    await page.getByRole('button', { name: /Teams/ }).click();
    page.once('dialog', (dialog) => dialog.accept());
    await page.getByRole('button', { name: /^Delete$/ }).click();
    await expect(page.getByRole('button', { name: new RegExp(teamName) })).toBeHidden();
    teamId = null;
  } finally {
    if (teamId) {
      await deleteTeam(teamId);
    }
  }
});
