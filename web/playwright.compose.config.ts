import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests-compose",
  retries: 0,
  reporter: "list",
  use: {
    trace: "retain-on-failure",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile", use: { ...devices["Pixel 7"] } },
  ],
});
