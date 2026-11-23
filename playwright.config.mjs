import { defineConfig, devices } from "@playwright/test";

const baseURL = "http://127.0.0.1:4173/dense-mem/";
const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE;

export default defineConfig({
  testDir: "./tests",
  timeout: 40_000,
  workers: 2,
  use: {
    ...devices["Desktop Chrome"], baseURL, reducedMotion: "reduce",
    launchOptions: executablePath ? {executablePath} : undefined
  },
  webServer: {
    command: 'python3 -m http.server --bind 127.0.0.1 --directory "$SITE_PREVIEW_DIR" 4173',
    url: baseURL,
    env: {SITE_PREVIEW_DIR: process.env.SITE_PREVIEW_DIR || "_preview"}
  }
});
