import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ControlApi, type ProviderTestTarget, type ProviderTestResult } from "../api";
import { jsonResponse } from "../App.test-helpers";
import { ProviderTestsPanel } from "./ProviderTestsPanel";

const target: ProviderTestTarget = { target_id: "a".repeat(64), endpoint: "https://provider.test/v1/chat/completions", model: "configured-model", roles: ["Remember", "Conflict review"], operation: "assess", credential_configuration: "Generative credentials", configuration_revision: "env", available: true, timeout_ms: 60000 };
const success: ProviderTestResult = { test_id: "test-123", correlation_id: "correlation-123", server_instance: "hosting-server", started_at: "2026-11-04T12:00:00Z", duration_ms: 200, target, sample_version: "1", outcome: "succeeded", reason: "sample_validated", guidance: "The sampled provider operation completed successfully.", http_status: 200, network_state: "http_response_received", exchanges: [{ exchange_id: "exchange-1", status_code: 200, duration_ms: 120, dns_ms: null, connect_ms: null, tls_ms: null, first_byte_ms: 100, connection_reused: true, request_capture_state: "captured", response_capture_state: "captured" }], capture_state: "captured", detailed_logs_available: true };
const api = () => new ControlApi("control-fixture", "http://localhost/control/api");

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("Provider tests", () => {
  it("groups configured models by endpoint and only sends a request after a click", async () => {
    const second = { ...target, target_id: "b".repeat(64), model: "another-model" };
    const disabled = { ...target, target_id: "c".repeat(64), endpoint: "", model: "", operation: "decide", available: false, unavailable_reason: "Decision provider is not configured." };
    const fetcher = vi.fn(async (_url: unknown, options?: RequestInit) => jsonResponse({ data: options?.method === "POST" ? success : [target, second, disabled] }));
    vi.stubGlobal("fetch", fetcher);
    render(<ProviderTestsPanel api={api()} />);
    expect(await screen.findByText("configured-model")).toBeVisible();
    expect(screen.getAllByRole("heading", { name: target.endpoint })).toHaveLength(1);
    expect(screen.getAllByText("Remember · Conflict review")[0]).toBeVisible();
    expect(screen.getByRole("button", { name: /unconfigured model/ })).toBeDisabled();
    expect(fetcher).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: /configured-model/ }));
    expect(await screen.findByText("Sample succeeded")).toBeVisible();
    expect(screen.getByText("hosting-server")).toBeVisible();
    expect(screen.getByText("correlation-123")).toBeVisible();
    expect(JSON.parse(String(fetcher.mock.calls[1][1]?.body))).toEqual({ target_id: target.target_id });
    expect(fetcher.mock.calls[1][1]?.headers).toMatchObject({ Authorization: "Bearer control-fixture" });
    const exchange = screen.getByText("Exchange 1: HTTP 200").closest("details")!;
    fireEvent.click(within(exchange).getByText("Exchange 1: HTTP 200"));
    expect(within(exchange).getAllByText("Not observed")).toHaveLength(3);
    fireEvent.click(screen.getByRole("button", { name: "Refresh AI providers" }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3));
    expect(fetcher.mock.calls.filter((call) => call[1]?.method === "POST")).toHaveLength(1);
  });

  it("disables duplicate submission and distinguishes client cancellation from a provider receipt", async () => {
    const fetcher = vi.fn((_url: unknown, options?: RequestInit) => {
      if (options?.method !== "POST") return Promise.resolve(jsonResponse({ data: [target] }));
      return new Promise<Response>((_resolve, reject) => options.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError"))));
    });
    vi.stubGlobal("fetch", fetcher);
    render(<ProviderTestsPanel api={api()} />);
    const button = await screen.findByRole("button", { name: /configured-model/ });
    fireEvent.click(button);
    expect(button).toBeDisabled();
    fireEvent.click(button);
    fireEvent.click(screen.getByRole("button", { name: "Cancel request" }));
    expect(await screen.findByText(/Request cancelled. Provider completion is unknown/)).toBeVisible();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(button).not.toBeDisabled();
  });

  it("reports HTTP rejection, missing capture, and diagnostic level without calling them a network outage", async () => {
    const failed: ProviderTestResult = { ...success, outcome: "failed", reason: "authentication_failed", failure_phase: "response_status", http_status: 401, guidance: "An HTTP response arrived. Check the configured provider credential and model permissions.", capture_state: "incomplete", detailed_logs_available: false };
    vi.stubGlobal("fetch", vi.fn(async (_url: unknown, options?: RequestInit) => jsonResponse({ data: options?.method === "POST" ? failed : [target] })));
    render(<ProviderTestsPanel api={api()} />);
    fireEvent.click(await screen.findByRole("button", { name: /configured-model/ }));
    expect(await screen.findByText("Sample failed")).toBeVisible();
    expect(screen.getByText("401")).toBeVisible();
    expect(screen.getByText(/HTTP response received/)).toBeVisible();
    expect(screen.getByText(/Diagnostic capture is incomplete/)).toBeVisible();
    expect(screen.getByText(/Full exchange logs are unavailable/)).toBeVisible();
  });

  it("shows loading, configuration failures, and stale-target errors", async () => {
    let release: (response: Response) => void = () => {};
    const fetcher = vi.fn().mockImplementationOnce(() => new Promise<Response>((resolve) => { release = resolve; })).mockResolvedValueOnce(new Response(JSON.stringify({ message: "Refresh the provider list" }), { status: 409 }));
    vi.stubGlobal("fetch", fetcher);
    render(<ProviderTestsPanel api={api()} />);
    expect(screen.getByText("Loading AI providers")).toBeVisible();
    release(jsonResponse({ data: [target] }));
    fireEvent.click(await screen.findByRole("button", { name: /configured-model/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Refresh the provider list");
    fetcher.mockRejectedValueOnce(new Error("Configuration unavailable"));
    fireEvent.click(screen.getByRole("button", { name: "Refresh AI providers" }));
    expect(await screen.findByText("Configuration unavailable")).toBeVisible();
  });
});
