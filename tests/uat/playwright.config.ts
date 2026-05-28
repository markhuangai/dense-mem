import { defineConfig } from '@playwright/test';

const baseURL = process.env.BASE_URL || 'http://localhost:8080';

export default defineConfig({
  testDir: '.',
  testMatch: ['**/*.spec.ts'],
  // Exclude Go test files
  testIgnore: ['**/*_test.go', '**/discoverability/**'],
  timeout: 90_000,
  retries: 0,
  workers: 1, // serial — tests share profile state
  reporter: [['list']],
  use: {
    baseURL,
    extraHTTPHeaders: {
      'Content-Type': 'application/json',
    },
    actionTimeout: 60_000,
  },
  // No webServer — tests expect a running instance at BASE_URL.
  // Set BASE_URL, API_KEY, PROFILE_ID, NEO4J_URI,
  // NEO4J_USER, NEO4J_PASSWORD env vars before running.
});
