import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests-user",
  retries: 0,
  reporter: "list",
  use: {
    baseURL: "http://127.0.0.1:5174",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "npm run dev:user -- --port 5174",
    url: "http://127.0.0.1:5174/ui/",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile", use: { ...devices["Pixel 7"] } },
  ],
});
