import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

beforeEach(() => {
  sessionStorage.clear();
  vi.restoreAllMocks();
});

describe("App migration portal", () => {
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
        expect(body.postgres_backup_reference).toBe("pg-backup-1");
        expect(body.postgres_backup_created).toBe(true);
        expect(body.neo4j_snapshot_reference).toBe("neo4j-snapshot-1");
        expect(body.neo4j_snapshot_created).toBe(true);
        return jsonResponse({
          data: {
            state: "ready",
            required: true,
            data_plane_allowed: false,
            readiness_message: "migration preflight approved",
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

    await userEvent.type(screen.getByLabelText("PostgreSQL backup reference"), "pg-backup-1");
    await userEvent.click(screen.getByLabelText("PostgreSQL backup created"));
    await userEvent.type(screen.getByLabelText("Neo4j snapshot reference"), "neo4j-snapshot-1");
    await userEvent.click(screen.getByLabelText("Neo4j snapshot created"));
    await userEvent.click(screen.getByRole("button", { name: /start migration/i }));

    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => (
      String(url).endsWith("/v2/migration/start")
    ))).toBe(true));
  });

  it("renews old migration preflight before resuming an rc11 run", async () => {
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
      migration_contract_version: "dense-mem.v2.1.migration-control.v2",
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
            readiness_message: "renew preflight before resuming legacy migration contract",
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
            readiness_message: "migration preflight approved",
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
    await userEvent.type(screen.getByLabelText("PostgreSQL backup reference"), "pg-backup-2");
    await userEvent.click(screen.getByLabelText("PostgreSQL backup created"));
    await userEvent.type(screen.getByLabelText("Neo4j snapshot reference"), "neo4j-snapshot-2");
    await userEvent.click(screen.getByLabelText("Neo4j snapshot created"));
    await userEvent.click(screen.getByRole("button", { name: /start migration/i }));

    await waitFor(() => expect(preflightCalls).toBe(1));
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith("/v2/migration/start"))).toBe(true);
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
    migration_contract_version: "dense-mem.v2.1.migration-control.v2",
    corpus_version: "dense-mem.v2.1.legacy-corpus.v1",
    source_kind: "neo4j",
    required: true,
    created_at: "2026-07-22T00:00:00Z",
    updated_at: "2026-07-22T00:00:00Z",
    ...overrides,
  };
}

function jsonResponse(payload: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  }));
}
