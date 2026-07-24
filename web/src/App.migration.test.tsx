import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

beforeEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("App migration portal", () => {
  it("waits for each migration status request before scheduling the next poll", async () => {
    vi.useFakeTimers();
    const pendingStatus: Array<(response: Response) => void> = [];
    let statusCalls = 0;
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/session")) {
        return jsonResponse({ data: { authenticated: true, portal_mode: "migration", legacy_config_present: true } });
      }
      if (url.endsWith("/v2/migration") && method === "GET") {
        statusCalls++;
        return new Promise((resolve) => pendingStatus.push(resolve));
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    const view = render(<App />);
    await act(async () => {
      for (let index = 0; index < 10; index++) {
        await Promise.resolve();
      }
    });
    expect(statusCalls).toBe(1);

    await act(async () => {
      vi.advanceTimersByTime(10_000);
      await Promise.resolve();
    });
    expect(statusCalls).toBe(1);

    await act(async () => {
      pendingStatus.shift()?.(new Response(JSON.stringify({
        data: {
          state: "paused_retryable",
          required: true,
          data_plane_allowed: false,
          readiness_message: "migration is paused",
          run: migrationRun({ state: "paused_retryable" }),
        },
      }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }));
      for (let index = 0; index < 5; index++) {
        await Promise.resolve();
      }
    });

    act(() => vi.advanceTimersByTime(1_999));
    expect(statusCalls).toBe(1);
    await act(async () => {
      vi.advanceTimersByTime(1);
      await Promise.resolve();
    });
    expect(statusCalls).toBe(2);
    view.unmount();
  });

  it("shows migration mode without loading normal teams", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/session")) {
        return jsonResponse({ data: { authenticated: true, portal_mode: "migration", legacy_config_present: true } });
      }
      if (url.endsWith("/v2/migration") && method === "GET") {
        return jsonResponse({
          data: {
            state: "required",
            required: true,
            data_plane_allowed: false,
            readiness_message: "legacy migration is required",
          },
        });
      }
      if (url.endsWith("/v2/migration/preflight") && method === "POST") {
        const body = JSON.parse(String(init?.body));
        expect(body).toEqual({
          backups_confirmed: true,
          reason: "operator confirmed external PostgreSQL and Neo4j backups",
        });
        return jsonResponse({
          data: {
            state: "ready",
            required: true,
            data_plane_allowed: false,
            readiness_message: "backup confirmation recorded",
            run: migrationRun({ state: "ready" }),
          },
        });
      }
      if (url.endsWith("/v2/migration/start") && method === "POST") {
        return jsonResponse({
          data: {
            state: "running",
            required: true,
            data_plane_allowed: false,
            readiness_message: "migration is running",
            run: migrationRun({ state: "running", updated_at: "2026-07-22T00:00:01Z" }),
          },
        });
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    expect(await screen.findByRole("heading", { name: "Legacy migration is required" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/teams"))).toBe(false);
    expect(screen.queryByLabelText("PostgreSQL backup reference")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Neo4j snapshot reference")).not.toBeInTheDocument();
    expect(screen.getByText("Dense-Mem does not create, inspect, or restore these backups.")).toBeInTheDocument();

    const startButton = screen.getByRole("button", { name: /confirm and start migration/i });
    expect(startButton).toBeDisabled();
    await userEvent.click(screen.getByLabelText("I confirm that I have backed up both the PostgreSQL and Neo4j databases."));
    expect(startButton).toBeEnabled();
    await userEvent.click(startButton);

    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => (
      String(url).endsWith("/v2/migration/start")
    ))).toBe(true));
  });

  it("renews old migration backup confirmation before resuming an rc11 run", async () => {
    let preflightCalls = 0;
    const legacyRun = migrationRun({
      run_id: "run-rc11",
      state: "running",
      total_items: 4,
      completed_items: 2,
      migration_contract_version: "dense-mem.v2.1.migration-control.v1",
      updated_at: "2026-07-22T00:00:01Z",
    });
    const currentRun = {
      ...legacyRun,
      state: "ready",
      migration_contract_version: "dense-mem.v2.1.migration-control.v3",
      preflight_checks: confirmationChecks(),
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/session")) {
        return jsonResponse({ data: { authenticated: true, portal_mode: "migration", legacy_config_present: true } });
      }
      if (url.endsWith("/v2/migration") && method === "GET") {
        return jsonResponse({
          data: {
            state: "running",
            required: true,
            data_plane_allowed: false,
            readiness_message: "renew backup confirmation before resuming legacy migration contract",
            run: legacyRun,
          },
        });
      }
      if (url.endsWith("/v2/migration/preflight") && method === "POST") {
        preflightCalls++;
        return jsonResponse({
          data: {
            state: "ready",
            required: true,
            data_plane_allowed: false,
            readiness_message: "backup confirmation recorded",
            run: currentRun,
          },
        });
      }
      if (url.endsWith("/v2/migration/start") && method === "POST") {
        return jsonResponse({
          data: {
            state: "running",
            required: true,
            data_plane_allowed: false,
            readiness_message: "migration is running",
            run: { ...currentRun, state: "running" },
          },
        });
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    expect(await screen.findByRole("heading", { name: "Legacy migration is required" })).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("I confirm that I have backed up both the PostgreSQL and Neo4j databases."));
    await userEvent.click(screen.getByRole("button", { name: /confirm and start migration/i }));

    await waitFor(() => expect(preflightCalls).toBe(1));
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/v2/migration/start"))).toBe(true);
  });

  it("renews incomplete current backup confirmation before restarting an active run", async () => {
    let preflightCalls = 0;
    const activeRun = migrationRun({
      state: "running",
      total_items: 4,
      completed_items: 2,
      preflight_checks: { operator_backup_confirmation: true },
      updated_at: "2026-07-22T00:00:01Z",
    });
    const currentRun = {
      ...activeRun,
      state: "ready",
      preflight_checks: confirmationChecks(),
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/session")) {
        return jsonResponse({ data: { authenticated: true, portal_mode: "migration", legacy_config_present: true } });
      }
      if (url.endsWith("/v2/migration") && method === "GET") {
        return jsonResponse({
          data: {
            state: "running",
            required: true,
            data_plane_allowed: false,
            readiness_message: "backup confirmation must be renewed before migration can continue",
            run: activeRun,
          },
        });
      }
      if (url.endsWith("/v2/migration/preflight") && method === "POST") {
        preflightCalls++;
        return jsonResponse({
          data: {
            state: "ready",
            required: true,
            data_plane_allowed: false,
            readiness_message: "backup confirmation recorded",
            run: currentRun,
          },
        });
      }
      if (url.endsWith("/v2/migration/start") && method === "POST") {
        return jsonResponse({
          data: {
            state: "running",
            required: true,
            data_plane_allowed: false,
            readiness_message: "migration is running",
            run: { ...currentRun, state: "running" },
          },
        });
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    expect(await screen.findByRole("heading", { name: "Legacy migration is required" })).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("I confirm that I have backed up both the PostgreSQL and Neo4j databases."));
    await userEvent.click(screen.getByRole("button", { name: /confirm and start migration/i }));

    await waitFor(() => expect(preflightCalls).toBe(1));
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/v2/migration/start"))).toBe(true);
  });

  it("does not offer backup confirmation renewal for a non-retryable failed run", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/session")) {
        return jsonResponse({ data: { authenticated: true, portal_mode: "migration", legacy_config_present: true } });
      }
      if (url.endsWith("/v2/migration") && method === "GET") {
        return jsonResponse({
          data: {
            state: "failed",
            required: true,
            data_plane_allowed: false,
            readiness_message: "migration failed; inspect errors and resume only if retryable",
            run: migrationRun({
              state: "failed",
              retryable: false,
              migration_contract_version: "dense-mem.v2.1.migration-control.v2",
            }),
          },
        });
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    expect(await screen.findByText("migration failed; inspect errors and resume only if retryable")).toBeInTheDocument();
    const startButton = screen.getByRole("button", { name: /confirm and start migration/i });
    expect(startButton).toBeDisabled();
    await userEvent.click(screen.getByLabelText("I confirm that I have backed up both the PostgreSQL and Neo4j databases."));
    expect(startButton).toBeDisabled();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/v2/migration/preflight"))).toBe(false);
  });

  it("retries start without reconfirming when confirmation succeeds but start fails", async () => {
    let preflightCalls = 0;
    let startCalls = 0;
    const readyRun = migrationRun({ state: "ready" });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/session")) {
        return jsonResponse({ data: { authenticated: true, portal_mode: "migration", legacy_config_present: true } });
      }
      if (url.endsWith("/v2/migration") && method === "GET") {
        return jsonResponse({
          data: {
            state: "required",
            required: true,
            data_plane_allowed: false,
            readiness_message: "legacy migration is required",
          },
        });
      }
      if (url.endsWith("/v2/migration/preflight") && method === "POST") {
        preflightCalls++;
        return jsonResponse({
          data: {
            state: "ready",
            required: true,
            data_plane_allowed: false,
            readiness_message: "backup confirmation recorded",
            run: readyRun,
          },
        });
      }
      if (url.endsWith("/v2/migration/start") && method === "POST") {
        startCalls++;
        if (startCalls === 1) {
          return jsonResponse({ message: "start failed" }, 500);
        }
        return jsonResponse({
          data: {
            state: "running",
            required: true,
            data_plane_allowed: false,
            readiness_message: "migration is running",
            run: { ...readyRun, state: "running" },
          },
        });
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    expect(await screen.findByRole("heading", { name: "Legacy migration is required" })).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("I confirm that I have backed up both the PostgreSQL and Neo4j databases."));
    await userEvent.click(screen.getByRole("button", { name: /confirm and start migration/i }));

    expect(await screen.findByText("start failed")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /start migration/i }));

    await waitFor(() => expect(startCalls).toBe(2));
    expect(preflightCalls).toBe(1);
  });

  it("shows blocked repair details and disables resume", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/session")) {
        return jsonResponse({ data: { authenticated: true, portal_mode: "migration", legacy_config_present: true } });
      }
      if (url.endsWith("/v2/migration")) {
        return jsonResponse({
          data: {
            state: "paused_retryable",
            required: true,
            data_plane_allowed: false,
            readiness_message: "migration is paused at a durable checkpoint",
            run: migrationRun({ state: "paused_retryable", total_items: 4421, completed_items: 4414, failed_items: 7 }),
            repair: {
              required: false,
              legacy_predicate_reviews: 0,
              orphan_reviews: 0,
              abandoned_processing: 0,
              retryable_failures: 0,
              held_reviews: 3452,
              blocked_items: 7,
              blocking_exclusions: 1,
              repaired_items: 0,
              claim_epoch_before: 3,
              failure_groups: [{ stage: "verification", class: "timeout", count: 7 }],
            },
          },
        });
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    expect(await screen.findByText("Resume is blocked")).toBeInTheDocument();
    expect(screen.getByText("verification/timeout: 7")).toBeInTheDocument();
    expect(screen.getByText("Blocking exclusions")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /resume/i })).toBeDisabled();
  });

  it("shows cleanup mode without normal control routes", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/session")) {
        return jsonResponse({ data: { authenticated: true, portal_mode: "cleanup", legacy_config_present: true } });
      }
      if (url.endsWith("/v2/migration")) {
        return jsonResponse({
          data: {
            state: "cut_over",
            required: false,
            data_plane_allowed: true,
            readiness_message: "compatible V2 migration marker present; neo4j_disconnect_required",
            restart_pending: false,
          },
        });
      }
      return jsonResponse({ message: "not found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
    sessionStorage.setItem("denseMem.controlToken", "secret");

    render(<App />);
    expect(await screen.findByRole("heading", { name: "PostgreSQL V2 is active" })).toBeInTheDocument();
    expect(screen.getByText(/Remove NEO4J_URI/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /start migration/i })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/teams"))).toBe(false);
  });
});

function migrationRun(overrides: Record<string, unknown> = {}) {
  return {
    run_id: "run-1",
    state: "ready",
    preflight_approved: true,
    total_items: 0,
    completed_items: 0,
    failed_items: 0,
    excluded_items: 0,
    retryable: true,
    migration_contract_version: "dense-mem.v2.1.migration-control.v3",
    preflight_checks: confirmationChecks(),
    corpus_version: "dense-mem.v2.1.legacy-corpus.v1",
    source_kind: "neo4j",
    required: true,
    created_at: "2026-07-22T00:00:00Z",
    updated_at: "2026-07-22T00:00:00Z",
    ...overrides,
  };
}

function confirmationChecks() {
  return {
    operator_backup_confirmation: true,
    postgres_backup_confirmed: true,
    neo4j_backup_confirmed: true,
    confirmation_scope: "operator",
    backup_verification: "not_performed",
  };
}

function jsonResponse(payload: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  }));
}
