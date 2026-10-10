import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ControlApi, type OntologyMaintenanceStatus } from "../api";
import { generalConfigSnapshot, jsonResponse } from "../App.test-helpers";
import { ConfigPanel } from "./ConfigPanel";
import { OntologyMaintenancePanel } from "./OntologyMaintenancePanel";

const policy = { enabled: true, cadence_hours: 12 as const, start_time_local: "03:00", timezone: "UTC", model: "configured-model", max_concurrency: 1, input_tokens: 250000, output_tokens: 100000, settings_version: "version" };
const status: OntologyMaintenanceStatus = { observed_at: "2026-10-06T12:00:00Z", enabled: true, paused: false, discovery_complete: false, coverage_complete: false, counts: { eligible: 4, organized: 1, pending: 1, failed: 1, ambiguous: 0, budget_deferred: 1 }, pending_policy: { ...policy, model: "pending-model" }, window: { id: "window", starts_at: "2026-10-06T03:00:00Z", ends_at: "2026-10-06T15:00:00Z", policy, charged_input_tokens: 100, charged_output_tokens: 200, reported_input_tokens: 60, reported_output_tokens: 0, reserved_input_tokens: 40, reserved_output_tokens: 200, overrun: false } };
const run = { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", window_id: "window", kind: "run", status: "incomplete", retryable: true, max_batches: 1, completed_batches: 1, failure_code: "provider_unavailable", created_at: status.observed_at, updated_at: status.observed_at };

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

function fixture() {
  const api = new ControlApi("http://localhost", "synthetic-control");
  vi.spyOn(api, "getOntologyMaintenanceStatus").mockResolvedValue(status);
  vi.spyOn(api, "listOntologyMaintenanceRuns").mockResolvedValue({ runs: [run] });
  return api;
}

describe("Ontology maintenance controls", () => {
  it("shows incomplete coverage, active policy, pending policy, and retained reservations", async () => {
    render(<OntologyMaintenancePanel api={fixture()} />);
    expect(await screen.findByText(/Coverage incomplete/)).toBeVisible();
    expect(screen.getByText(/Eligible counts cover discovered records/)).toBeVisible();
    expect(screen.getByText(/configured-model/)).toBeVisible();
    expect(screen.getByText(/Unreported reservation/)).toBeVisible();
    expect(screen.getByText(/250,000 input/)).toBeVisible();
    expect(screen.getByText("provider_unavailable")).toBeVisible();
  });

  it("reuses the operation key after a command failure and keeps that error during refresh", async () => {
    const api = fixture();
    const command = vi.spyOn(api, "runOntologyMaintenance").mockRejectedValueOnce(new Error("accounting unavailable")).mockResolvedValueOnce(run);
    render(<OntologyMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Run bounded maintenance" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("accounting unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Refresh ontology coverage" }));
    await waitFor(() => expect(api.getOntologyMaintenanceStatus).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("alert")).toHaveTextContent("accounting unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Run bounded maintenance" }));
    await screen.findByRole("status");
    expect(command.mock.calls[1][0]).toBe(command.mock.calls[0][0]);
    expect(command.mock.calls[0][1]).toBe(1);
  });

  it("offers retry only for the run that owns retryable failures", async () => {
    const api = fixture();
    const unrelated = { ...run, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", retryable: false, failure_code: "budget_deferred" };
    vi.mocked(api.listOntologyMaintenanceRuns).mockResolvedValue({ runs: [unrelated, run] });
    const command = vi.spyOn(api, "runOntologyMaintenance").mockResolvedValue(run);
    render(<OntologyMaintenancePanel api={api} />);
    await screen.findByText("budget_deferred");
    const buttons = screen.getAllByRole("button", { name: "Retry failed work" });
    expect(buttons).toHaveLength(1);
    fireEvent.click(buttons[0]);
    await screen.findByRole("status");
    expect(command).toHaveBeenCalledWith(expect.any(String), 1, run.id);
  });

  it("keeps older run pages after polling and disables invalid batch limits", async () => {
    const api = fixture();
    const older = { ...run, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", failure_code: "lease_lost" };
    vi.mocked(api.listOntologyMaintenanceRuns).mockImplementation(async (cursor) => cursor ? { runs: [older] } : { runs: [run], next_cursor: run.id });
    render(<OntologyMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older runs" }));
    await screen.findByText("lease_lost");
    fireEvent.click(screen.getByRole("button", { name: "Refresh ontology coverage" }));
    await waitFor(() => expect(api.listOntologyMaintenanceRuns).toHaveBeenCalledTimes(4));
    expect(screen.getByText("lease_lost")).toBeVisible();
    fireEvent.change(screen.getByLabelText("Maximum batches"), { target: { value: "101" } });
    expect(screen.getByRole("button", { name: "Run bounded maintenance" })).toBeDisabled();
  });

  it.each(["command", "poll", "manual"])("refreshes older eligibility and pagination after %s", async (trigger) => {
    const api = fixture();
    const first = Array.from({ length: 50 }, (_, index) => ({ ...run, id: `${(index + 1).toString(16).padStart(8, "0")}-aaaa-4aaa-8aaa-aaaaaaaaaaaa`, retryable: false, failure_code: "" }));
    const older = { ...run, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" };
    let changed = false;
    let poll: () => void = () => {};
    const interval = window.setInterval.bind(window);
    vi.spyOn(window, "setInterval").mockImplementation((callback, timeout, ...args) => {
      if (timeout === 10000) poll = callback as () => void;
      return interval(callback, timeout, ...args);
    });
    vi.mocked(api.listOntologyMaintenanceRuns).mockImplementation(async (cursor) => {
      if (!cursor) return { runs: changed ? [{ ...run, id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", retryable: false }, ...first.slice(0, 49)] : first, next_cursor: changed ? first[48].id : first[49].id };
      return { runs: changed ? [first[49], { ...older, retryable: false }] : [older] };
    });
    const command = vi.spyOn(api, "runOntologyMaintenance").mockImplementation(async () => { changed = true; return run; });
    render(<OntologyMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older runs" }));
    const retry = await screen.findByRole("button", { name: "Retry failed work" });
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh ontology coverage" })).toBeEnabled());
    if (trigger === "command") fireEvent.click(retry);
    else {
      changed = true;
      if (trigger === "manual") fireEvent.click(screen.getByRole("button", { name: "Refresh ontology coverage" }));
      else await act(async () => poll());
    }
    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry failed work" })).not.toBeInTheDocument());
    expect(screen.getByText("bbbbbbbb")).toBeVisible();
    expect(screen.getByText("cccccccc")).toBeVisible();
    expect(screen.getByText(first[49].id.slice(0, 8))).toBeVisible();
    expect(screen.getAllByRole("row").filter((row) => row.textContent?.includes("bbbbbbbb"))).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Load older runs" })).not.toBeInTheDocument();
    if (trigger === "command") expect(command).toHaveBeenCalledWith(expect.any(String), 1, older.id);
  });

  it("retains history and disables stale retries when an older-page refresh fails", async () => {
    const api = fixture();
    const older = { ...run, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" };
    const oldest = { ...run, id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", retryable: false };
    let failure = false;
    let recovered = false;
    vi.mocked(api.listOntologyMaintenanceRuns).mockImplementation(async (cursor) => {
      if (!cursor) return { runs: [{ ...run, retryable: false }], next_cursor: run.id };
      if (cursor === older.id) return { runs: [oldest] };
      if (failure) throw new Error("older history unavailable");
      return { runs: [{ ...older, retryable: !recovered }], next_cursor: older.id };
    });
    render(<OntologyMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older runs" }));
    await screen.findByRole("button", { name: "Retry failed work" });
    failure = true;
    fireEvent.click(screen.getByRole("button", { name: "Refresh ontology coverage" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("older history unavailable");
    expect(screen.getByText("bbbbbbbb")).toBeVisible();
    expect(screen.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Load older runs" }));
    await screen.findByText("cccccccc");
    expect(screen.getByRole("alert")).toHaveTextContent("older history unavailable");
    expect(screen.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    failure = false;
    recovered = true;
    fireEvent.click(screen.getByRole("button", { name: "Refresh ontology coverage" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry failed work" })).not.toBeInTheDocument());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("coalesces polling during a loaded-page refresh", async () => {
    const api = fixture();
    const older = { ...run, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", retryable: false };
    let resolve!: (value: { runs: typeof run[] }) => void;
    const pending = new Promise<{ runs: typeof run[] }>((done) => { resolve = done; });
    let blocked = false;
    let poll: () => void = () => {};
    const interval = window.setInterval.bind(window);
    vi.spyOn(window, "setInterval").mockImplementation((callback, timeout, ...args) => {
      if (timeout === 10000) poll = callback as () => void;
      return interval(callback, timeout, ...args);
    });
    vi.mocked(api.listOntologyMaintenanceRuns).mockImplementation(async (cursor) => cursor ? blocked ? pending : { runs: [older] } : { runs: [run], next_cursor: run.id });
    render(<OntologyMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older runs" }));
    await screen.findByText("bbbbbbbb");
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh ontology coverage" })).toBeEnabled());
    blocked = true;
    fireEvent.click(screen.getByRole("button", { name: "Refresh ontology coverage" }));
    await waitFor(() => expect(api.listOntologyMaintenanceRuns).toHaveBeenCalledTimes(4));
    act(() => { poll(); poll(); });
    expect(api.listOntologyMaintenanceRuns).toHaveBeenCalledTimes(4);
    expect(screen.getByRole("button", { name: "Refresh ontology coverage" })).toBeDisabled();
    await act(async () => resolve({ runs: [older] }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh ontology coverage" })).toBeEnabled());
    expect(screen.getByText("bbbbbbbb")).toBeVisible();
  });

  it("saves operator policy through the configuration API and shows the saved values", async () => {
    const items = [
      { key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "false", effective_value: "false" },
      { key: "ONTOLOGY_MAINTENANCE_CADENCE_HOURS", value: "12", effective_value: "12" },
      { key: "ONTOLOGY_MAINTENANCE_MODEL", value: "", effective_value: policy.model },
    ].map((item) => ({ ...item, updated_at: status.observed_at }));
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/config/general")) return jsonResponse({ data: generalConfigSnapshot });
      if (url.endsWith("/config/ontology-maintenance")) {
        const saved = init?.method === "PATCH"
          ? items.map((item) => ({ ...item, value: JSON.parse(String(init.body)).items.find((entry: { key: string }) => entry.key === item.key).value }))
          : items;
        return jsonResponse({ data: { update_time: status.observed_at, items: saved, effective: policy } });
      }
      if (url.endsWith("/ontology/status")) return jsonResponse({ data: status });
      if (url.endsWith("/ontology/runs?limit=50")) return jsonResponse({ data: { runs: [run] } });
      throw new Error(`Unexpected request ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<ConfigPanel api={new ControlApi("synthetic-control", "/control/api")} />);
    fireEvent.click(screen.getByRole("tab", { name: "Ontology" }));
    const enabled = await screen.findByLabelText("Enable ontology maintenance");
    expect(enabled).not.toBeChecked();
    fireEvent.click(enabled);
    fireEvent.change(screen.getByLabelText("Cadence (hours)"), { target: { value: "24" } });
    fireEvent.change(screen.getByLabelText("Maintenance model"), { target: { value: "operator-model" } });
    fireEvent.click(screen.getByRole("button", { name: "Save config" }));
    await screen.findByText("Saved");
    expect(enabled).toBeChecked();
    expect(screen.getByLabelText("Cadence (hours)")).toHaveValue("24");
    expect(screen.getByLabelText("Maintenance model")).toHaveValue("operator-model");
    const patch = fetchMock.mock.calls.find(([, init]) => init?.method === "PATCH");
    expect(patch?.[0]).toBe("/control/api/config/ontology-maintenance");
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ items: [
      { key: "ONTOLOGY_MAINTENANCE_ENABLED", value: "true" },
      { key: "ONTOLOGY_MAINTENANCE_CADENCE_HOURS", value: "24" },
      { key: "ONTOLOGY_MAINTENANCE_MODEL", value: "operator-model" },
    ] });
    expect(screen.getByText(/Policy changes apply at the next window/)).toBeVisible();
  });

  it.each([true, false])("pauses, resumes, and retries through distinct HTTP commands (randomUUID available: %s)", async (randomUUIDAvailable) => {
    if (!randomUUIDAvailable) vi.stubGlobal("crypto", { randomUUID: undefined });
    let paused = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/ontology/status")) return jsonResponse({ data: { ...status, paused } });
      if (url.endsWith("/ontology/runs?limit=50")) return jsonResponse({ data: { runs: [run] } });
      if (init?.method === "POST") {
        if (url.endsWith("/pause")) paused = true;
        else if (url.endsWith("/resume")) paused = false;
        else if (!url.endsWith(`/runs/${run.id}/retry`)) throw new Error(`Unexpected command ${url}`);
        return jsonResponse({ data: run });
      }
      throw new Error(`Unexpected request ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<OntologyMaintenancePanel api={new ControlApi("synthetic-control", "/control/api")} />);
    fireEvent.click(await screen.findByRole("button", { name: "Pause maintenance" }));
    await screen.findByRole("button", { name: "Resume maintenance" });
    expect(screen.getByRole("status")).toHaveTextContent("Maintenance paused; dispatched work will finish.");
    expect(screen.getByRole("button", { name: "Run bounded maintenance" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Resume maintenance" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Retry failed work" })).toBeEnabled());
    expect(screen.getByRole("status")).toHaveTextContent("Maintenance resumed.");
    fireEvent.change(screen.getByLabelText("Maximum batches"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Retry failed work" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(`Run ${run.id.slice(0, 8)} accepted`));
    const commands = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(commands.map(([url]) => url)).toEqual([
      "/control/api/ontology/pause", "/control/api/ontology/resume", `/control/api/ontology/runs/${run.id}/retry`,
    ]);
    const bodies = commands.map(([, init]) => JSON.parse(String(init?.body)));
    expect(bodies[2]).toEqual({ operation_key: expect.any(String), max_batches: 3 });
    expect(new Set(bodies.map((body) => body.operation_key)).size).toBe(3);
  });
});
