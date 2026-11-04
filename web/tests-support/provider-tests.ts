import { expect, type Page, type TestInfo } from "@playwright/test";
import { navigate, expectPageContained } from "./portal";
import type { ProviderTestTarget, ProviderTestResult } from "../src/api";

export async function checkProviderTestsPanel(page: Page, testInfo: TestInfo, width: number) {
  const target: ProviderTestTarget = { target_id: "a".repeat(64), endpoint: "https://configured-provider.example/v1/chat/completions", model: "configured-model", operation: "assess", roles: ["Remember and session ingestion", "Conflict review"], credential_configuration: "Generative credentials", configuration_revision: "environment", timeout_ms: 60000, available: true };
  let status = 200, requests = 0, release: () => void = () => {};
  let held = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/control/api/diagnostics/**", async (route) => {
    if (route.request().method() === "GET") return route.fulfill({ json: { data: [target, { ...target, target_id: "b".repeat(64), model: "other-model", roles: ["Ontology maintenance"] }] } });
    expect(route.request().postDataJSON()).toEqual({ target_id: target.target_id });
    requests += 1;
    await held;
    const result: ProviderTestResult = { test_id: "provider-test-123", correlation_id: "provider-correlation-123", server_instance: "hosting-server", target, started_at: "2026-11-04T12:00:00Z", duration_ms: 120, sample_version: "1", outcome: status === 200 ? "succeeded" : "failed", reason: status === 200 ? "sample_validated" : "authentication_failed", guidance: status === 200 ? "The sampled operation completed successfully." : "An HTTP response arrived. Check the configured provider credential.", http_status: status, failure_phase: status === 200 ? "" : "response_status", network_state: "http_response_received", exchanges: [{ exchange_id: "exchange-123", status_code: status, duration_ms: 100, dns_ms: null, connect_ms: null, tls_ms: null, first_byte_ms: 90, connection_reused: true, request_capture_state: "captured", response_capture_state: "captured" }], capture_state: "captured", detailed_logs_available: true };
    return route.fulfill({ json: { data: result } });
  });
  await navigate(page, "Settings");
  await page.getByRole("tab", { name: "AI providers", exact: true }).click();
  await expect(page.getByRole("heading", { name: target.endpoint })).toHaveCount(1);
  await expect(page.getByText("other-model", { exact: true })).toBeVisible();
  expect(requests).toBe(0);
  const button = page.getByRole("button", { name: "Send test request for configured-model (assess)", exact: true });
  await button.focus();
  await page.keyboard.press("Enter");
  await expect(button).toBeDisabled();
  await expect(page.getByText("Waiting for the server’s test result…")).toBeVisible();
  await expectPageContained(page);
  await page.screenshot({ path: testInfo.outputPath(`providers-${width}-running.png`), fullPage: true });
  release();
  await expect(page.getByText("Sample succeeded", { exact: true })).toBeVisible();
  await expect(page.getByText("hosting-server", { exact: true })).toBeVisible();
  await expectPageContained(page);
  await page.screenshot({ path: testInfo.outputPath(`providers-${width}-success.png`), fullPage: true });
  status = 401;
  held = Promise.resolve();
  await button.click();
  await expect(page.getByText("Sample failed", { exact: true })).toBeVisible();
  await expect(page.getByText(/HTTP response received/)).toBeVisible();
  await expect(page.getByText("401", { exact: true })).toBeVisible();
  await expectPageContained(page);
  await page.screenshot({ path: testInfo.outputPath(`providers-${width}-failure.png`), fullPage: true });
  await page.getByRole("button", { name: "Refresh AI providers", exact: true }).click();
  await expect(button).toBeEnabled();
  expect(requests).toBe(2);
}
