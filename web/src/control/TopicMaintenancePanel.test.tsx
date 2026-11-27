import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ControlApi, type TopicMaintenanceStatus } from "../api";
import { generalConfigSnapshot, jsonResponse } from "../App.test-helpers";
import { TopicMaintenanceConfigPanel } from "./ConfigPanel";
import { TopicMaintenancePanel } from "./TopicMaintenancePanel";

const policy = { enabled: true, cadence_hours: 12 as const, start_time_local: "03:00", timezone: "UTC", model: "configured-model", max_concurrency: 1, context_window_tokens: 300000, settings_version: "version" };
const usage = { attempts: 3, unreconciled_attempts: 1, unavailable_usage_attempts: 1, partial_usage_attempts: 1, reported_input_tokens: 60, reported_output_tokens: 0, reported_total_tokens: 60 };
const status: TopicMaintenanceStatus = { observed_at: "2026-10-06T12:00:00Z", enabled: true, paused: false, discovery_complete: false, coverage_complete: false, active_work: 1, usage, historical_usage: { ...usage, unreconciled_attempts: 0 }, counts: { eligible: 3, organized: 1, pending: 1, failed: 1, ambiguous: 0 }, pending_policy: { ...policy, model: "pending-model" }, window: { id: "window", starts_at: "2026-10-06T03:00:00Z", ends_at: "2026-10-06T15:00:00Z", policy, usage } };
const run = { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", window_id: "window", kind: "run", status: "incomplete", retryable: true, max_batches: 1, completed_batches: 1, failure_code: "provider_unavailable", created_at: status.observed_at, updated_at: status.observed_at };

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

function fixture() {
  const api = new ControlApi("http://localhost", "synthetic-control");
  vi.spyOn(api, "getTopicMaintenanceStatus").mockResolvedValue(status);
  vi.spyOn(api, "listTopicMaintenanceRuns").mockResolvedValue({ runs: [run] });
  return api;
}

describe("Topic maintenance controls", () => {
  it("submits selected uncategorized Evidence for one team", async () => {
    const api = fixture();
    const reassess = vi.spyOn(api, "reassessUncategorizedEvidence").mockResolvedValue({ ...run, kind: "reassess", status: "pending" });
    render(<TopicMaintenancePanel api={api} />);
    const team = "11111111-1111-4111-8111-111111111111";
    const first = "22222222-2222-4222-8222-222222222222";
    const second = "33333333-3333-4333-8333-333333333333";
    fireEvent.change(await screen.findByLabelText("Team ID"), { target: { value: team } });
    fireEvent.change(screen.getByLabelText("Evidence IDs"), { target: { value: `${first}\n${second}` } });
    fireEvent.click(screen.getByRole("button", { name: "Reassess selected Evidence" }));
    await waitFor(() => expect(reassess).toHaveBeenCalledWith(team, expect.any(String), [first, second]));
    expect(await screen.findByText(/Reassessment .* accepted/)).toBeVisible();
    fireEvent.change(screen.getByLabelText("Evidence IDs"), { target: { value: `${first}, ${first}` } });
    expect(screen.getByRole("button", { name: "Reassess selected Evidence" })).toBeDisabled();
  });

  it("offers captured exchanges only for runs with a failure", async () => {
    const api = fixture();
    vi.mocked(api.listTopicMaintenanceRuns).mockResolvedValue({ runs: [{ ...run, failure_code: "", status: "completed", retryable: false }, { ...run, id: "failed-run" }] });
    vi.spyOn(api, "listOperationLogs").mockResolvedValue({ data: [], pagination: { limit: 20, offset: 0, total: 0 } });
    render(<TopicMaintenancePanel api={api} />);
    const buttons = await screen.findAllByRole("button", { name: "View failure diagnostics" });
    expect(buttons).toHaveLength(1);
    const retryButton = screen.getByRole("button", { name: "Retry failed work" });
    expect(retryButton.parentElement).toHaveClass("button-row");
    expect(buttons[0].parentElement).toBe(retryButton.parentElement);
    fireEvent.click(buttons[0]);
    expect(buttons[0].closest("tr")).toHaveClass("selected-row");
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("region", { name: "Topic maintenance failure diagnostics" })).toBeVisible();
    expect(await within(dialog).findByText(/No retained failure captures/)).toBeVisible();
    expect(api.listOperationLogs).toHaveBeenCalledWith(expect.objectContaining({ reference_id: "failed-run", event: "topic_failure_capture" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Close failure diagnostics" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(buttons[0]).toHaveFocus());
  });

  it("keeps Retry failed work enabled during background polling refreshes", async () => {
    const api = fixture();
    let resolvePoll!: (value: TopicMaintenanceStatus) => void;
    let calls = 0;
    vi.spyOn(api, "getTopicMaintenanceStatus").mockImplementation(() => {
      calls += 1;
      if (calls === 1) return Promise.resolve(status);
      return new Promise<TopicMaintenanceStatus>((resolve) => { resolvePoll = resolve; });
    });
    let poll: () => void = () => {};
    const interval = window.setInterval.bind(window);
    vi.spyOn(window, "setInterval").mockImplementation((callback, timeout, ...args) => {
      if (timeout === 10000) poll = callback as () => void;
      return interval(callback, timeout, ...args);
    });
    render(<TopicMaintenancePanel api={api} />);
    const retry = await screen.findByRole("button", { name: "Retry failed work" });
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh topic coverage" })).toBeEnabled());
    act(() => { poll(); });
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh topic coverage" })).toBeDisabled());
    expect(retry).toBeEnabled();
    await act(async () => { resolvePoll(status); });
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh topic coverage" })).toBeEnabled());
    expect(retry).toBeEnabled();
  });
  it("keeps empty discovery distinct from completed coverage", async () => {
    const api = fixture();
    vi.mocked(api.getTopicMaintenanceStatus).mockResolvedValue({ ...status, window: undefined, counts: { eligible: 0, organized: 0, pending: 0, failed: 0, ambiguous: 0 } });
    render(<TopicMaintenancePanel api={api} />);
    const coverage = await screen.findByRole("region", { name: "Topic assignment coverage" });
    expect(within(coverage).getByText("Discovery in progress")).toBeVisible();
    expect(within(coverage).getByText("No eligible records discovered")).toBeVisible();
    expect(within(coverage).queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.getByText("No maintenance window has opened.")).toBeVisible();
  });

  it("reports unavailable usage independently from work still in flight", async () => {
    render(<TopicMaintenancePanel api={fixture()} />);
    const current = await screen.findByRole("region", { name: "Current topic window" });
    expect(within(current).getByText("Reported token totals exclude unavailable usage.")).toBeVisible();
    expect(within(current).queryByRole("progressbar")).not.toBeInTheDocument();
    const coverage = screen.getByRole("region", { name: "Topic assignment coverage" });
    expect(within(coverage).getByText("Active work")).toBeVisible();
    expect(within(coverage).getByText("Unfinished attempts")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Add allowance now" })).not.toBeInTheDocument();
  });

  it("blocks model commands when context capacity needs configuration and keeps pause available", async () => {
    const api = fixture();
    vi.mocked(api.getTopicMaintenanceStatus).mockResolvedValue({ ...status, configuration_error: "configuration_invalid", window: undefined, pending_policy: { ...policy, context_window_tokens: 0 } });
    render(<TopicMaintenancePanel api={api} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Model processing is blocked");
    expect(screen.getByText("Not configured")).toBeVisible();
    expect(screen.getByRole("button", { name: "Run bounded maintenance" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Pause maintenance" })).toBeEnabled();
  });

  it("retains historical allowance amounts without offering a grant action", async () => {
    const api = fixture();
    vi.mocked(api.listTopicMaintenanceRuns).mockResolvedValue({ runs: [{ ...run, kind: "allowance", status: "completed", retryable: false, input_tokens: 1000, output_tokens: 2000 }] });
    render(<TopicMaintenancePanel api={api} />);
    expect(await screen.findByText("Historical allowance")).toBeVisible();
    expect(screen.getByText("1,000 input / 2,000 output")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Add allowance now" })).not.toBeInTheDocument();
  });

  it("shows incomplete coverage, active policy, pending policy, and unknown usage", async () => {
    render(<TopicMaintenancePanel api={fixture()} />);
    expect(await screen.findByText(/Coverage incomplete/)).toBeVisible();
    expect(screen.getByText(/Eligible counts cover discovered records/)).toBeVisible();
    expect(screen.getByText(/configured-model/)).toBeVisible();
    expect(screen.getByText(/Reported token totals exclude unavailable usage/)).toBeVisible();
    expect(screen.getAllByText(/300,000 tokens per call/)).toHaveLength(2);
    expect(screen.getByText("provider_unavailable")).toBeVisible();
  });

  it("reuses the operation key after a command failure and keeps that error during refresh", async () => {
    const api = fixture();
    const command = vi.spyOn(api, "runTopicMaintenance").mockRejectedValueOnce(new Error("accounting unavailable")).mockResolvedValueOnce(run);
    render(<TopicMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Run bounded maintenance" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("accounting unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Refresh topic coverage" }));
    await waitFor(() => expect(api.getTopicMaintenanceStatus).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("alert")).toHaveTextContent("accounting unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Run bounded maintenance" }));
    await screen.findByRole("status");
    expect(command.mock.calls[1][0]).toBe(command.mock.calls[0][0]);
    expect(command.mock.calls[0][1]).toBe(1);
  });

  it("offers retry only for the run that owns retryable failures", async () => {
    const api = fixture();
    const unrelated = { ...run, id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", retryable: false, failure_code: "budget_deferred" };
    vi.mocked(api.listTopicMaintenanceRuns).mockResolvedValue({ runs: [unrelated, run] });
    const command = vi.spyOn(api, "runTopicMaintenance").mockResolvedValue(run);
    render(<TopicMaintenancePanel api={api} />);
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
    vi.mocked(api.listTopicMaintenanceRuns).mockImplementation(async (cursor) => cursor ? { runs: [older] } : { runs: [run], next_cursor: run.id });
    render(<TopicMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older runs" }));
    await screen.findByText("lease_lost");
    fireEvent.click(screen.getByRole("button", { name: "Refresh topic coverage" }));
    await waitFor(() => expect(api.listTopicMaintenanceRuns).toHaveBeenCalledTimes(4));
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
    vi.mocked(api.listTopicMaintenanceRuns).mockImplementation(async (cursor) => {
      if (!cursor) return { runs: changed ? [{ ...run, id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", retryable: false }, ...first.slice(0, 49)] : first, next_cursor: changed ? first[48].id : first[49].id };
      return { runs: changed ? [first[49], { ...older, retryable: false }] : [older] };
    });
    const command = vi.spyOn(api, "runTopicMaintenance").mockImplementation(async () => { changed = true; return run; });
    render(<TopicMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older runs" }));
    const retry = await screen.findByRole("button", { name: "Retry failed work" });
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh topic coverage" })).toBeEnabled());
    if (trigger === "command") fireEvent.click(retry);
    else {
      changed = true;
      if (trigger === "manual") fireEvent.click(screen.getByRole("button", { name: "Refresh topic coverage" }));
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
    vi.mocked(api.listTopicMaintenanceRuns).mockImplementation(async (cursor) => {
      if (!cursor) return { runs: [{ ...run, retryable: false }], next_cursor: run.id };
      if (cursor === older.id) return { runs: [oldest] };
      if (failure) throw new Error("older history unavailable");
      return { runs: [{ ...older, retryable: !recovered }], next_cursor: older.id };
    });
    render(<TopicMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older runs" }));
    await screen.findByRole("button", { name: "Retry failed work" });
    failure = true;
    fireEvent.click(screen.getByRole("button", { name: "Refresh topic coverage" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("older history unavailable");
    expect(screen.getByText("bbbbbbbb")).toBeVisible();
    expect(screen.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Load older runs" }));
    await screen.findByText("cccccccc");
    expect(screen.getByRole("alert")).toHaveTextContent("older history unavailable");
    expect(screen.getByRole("button", { name: "Retry failed work" })).toBeDisabled();
    failure = false;
    recovered = true;
    fireEvent.click(screen.getByRole("button", { name: "Refresh topic coverage" }));
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
    vi.mocked(api.listTopicMaintenanceRuns).mockImplementation(async (cursor) => cursor ? blocked ? pending : { runs: [older] } : { runs: [run], next_cursor: run.id });
    render(<TopicMaintenancePanel api={api} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load older runs" }));
    await screen.findByText("bbbbbbbb");
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh topic coverage" })).toBeEnabled());
    blocked = true;
    fireEvent.click(screen.getByRole("button", { name: "Refresh topic coverage" }));
    await waitFor(() => expect(api.listTopicMaintenanceRuns).toHaveBeenCalledTimes(4));
    act(() => { poll(); poll(); });
    expect(api.listTopicMaintenanceRuns).toHaveBeenCalledTimes(4);
    expect(screen.getByRole("button", { name: "Refresh topic coverage" })).toBeDisabled();
    await act(async () => resolve({ runs: [older] }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh topic coverage" })).toBeEnabled());
    expect(screen.getByText("bbbbbbbb")).toBeVisible();
  });

  it("saves operator policy through the configuration API and shows the saved values", async () => {
    const items = [
      { key: "TOPIC_MAINTENANCE_ENABLED", value: "false", effective_value: "false" },
      { key: "TOPIC_MAINTENANCE_CADENCE_HOURS", value: "12", effective_value: "12" },
      { key: "TOPIC_MAINTENANCE_MODEL", value: "", effective_value: policy.model },
      { key: "TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "", effective_value: "" },
    ].map((item) => ({ ...item, updated_at: status.observed_at }));
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/config/general")) return jsonResponse({ data: generalConfigSnapshot });
      if (url.endsWith("/config/topic-maintenance")) {
        const saved = init?.method === "PATCH"
          ? items.map((item) => ({ ...item, value: JSON.parse(String(init.body)).items.find((entry: { key: string }) => entry.key === item.key).value }))
          : items;
        return jsonResponse({ data: { update_time: status.observed_at, items: saved, effective: policy } });
      }
      if (url.endsWith("/topic-maintenance/status")) return jsonResponse({ data: status });
      if (url.endsWith("/topic-maintenance/runs?limit=50")) return jsonResponse({ data: { runs: [run] } });
      throw new Error(`Unexpected request ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<TopicMaintenanceConfigPanel api={new ControlApi("synthetic-control", "/control/api")} />);
    const enabled = await screen.findByLabelText("Enable topic maintenance");
    expect(enabled).not.toBeChecked();
    fireEvent.click(enabled);
    fireEvent.change(screen.getByLabelText("Cadence (hours)"), { target: { value: "24" } });
    fireEvent.change(screen.getByLabelText("Maintenance model"), { target: { value: "operator-model" } });
    fireEvent.change(screen.getByLabelText("Model context window (tokens)"), { target: { value: "262144" } });
    fireEvent.click(screen.getByRole("button", { name: "Save config" }));
    await screen.findByText("Changes saved");
    expect(enabled).toBeChecked();
    expect(screen.getByLabelText("Cadence (hours)")).toHaveValue("24");
    expect(screen.getByLabelText("Maintenance model")).toHaveValue("operator-model");
    const patch = fetchMock.mock.calls.find(([, init]) => init?.method === "PATCH");
    expect(patch?.[0]).toBe("/control/api/config/topic-maintenance");
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ items: [
      { key: "TOPIC_MAINTENANCE_ENABLED", value: "true" },
      { key: "TOPIC_MAINTENANCE_CADENCE_HOURS", value: "24" },
      { key: "TOPIC_MAINTENANCE_MODEL", value: "operator-model" },
      { key: "TOPIC_MAINTENANCE_CONTEXT_WINDOW_TOKENS", value: "262144" },
    ] });
    expect(screen.getByText(/Policy changes apply at the next window/)).toBeVisible();
  });

  it.each([true, false])("pauses, resumes, and retries through distinct HTTP commands (randomUUID available: %s)", async (randomUUIDAvailable) => {
    if (!randomUUIDAvailable) vi.stubGlobal("crypto", { randomUUID: undefined });
    let paused = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/topic-maintenance/status")) return jsonResponse({ data: { ...status, paused } });
      if (url.endsWith("/topic-maintenance/runs?limit=50")) return jsonResponse({ data: { runs: [run] } });
      if (init?.method === "POST") {
        if (url.endsWith("/pause")) paused = true;
        else if (url.endsWith("/resume")) paused = false;
        else if (!url.endsWith(`/runs/${run.id}/retry`)) throw new Error(`Unexpected command ${url}`);
        return jsonResponse({ data: run });
      }
      throw new Error(`Unexpected request ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<TopicMaintenancePanel api={new ControlApi("synthetic-control", "/control/api")} />);
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
      "/control/api/topic-maintenance/pause", "/control/api/topic-maintenance/resume", `/control/api/topic-maintenance/runs/${run.id}/retry`,
    ]);
    const bodies = commands.map(([, init]) => JSON.parse(String(init?.body)));
    expect(bodies[2]).toEqual({ operation_key: expect.any(String), max_batches: 3 });
    expect(new Set(bodies.map((body) => body.operation_key)).size).toBe(3);
  });
  it("edits team limits with pagination, preserves failed drafts, and separates active from saved policy", async () => {
    const team = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    const other = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    let saved = { ...policy, max_concurrency: 3, team_max_concurrency: 2, team_concurrency_overrides: [] as Array<{team_id:string;max_concurrency:number}> };
    const active = { ...policy, max_concurrency: 3, team_max_concurrency: 1, team_concurrency_overrides: [] };
    let attempts = 0;
    const items = () => [
      { key: "TOPIC_MAINTENANCE_TEAM_MAX_CONCURRENCY", value: String(saved.team_max_concurrency), effective_value: String(saved.team_max_concurrency), updated_at: status.observed_at },
      { key: "TOPIC_MAINTENANCE_TEAM_OVERRIDES_JSON", value: JSON.stringify(saved.team_concurrency_overrides), effective_value: JSON.stringify(saved.team_concurrency_overrides), updated_at: status.observed_at },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/config/topic-maintenance")) {
        if (init?.method === "PATCH") {
          if (++attempts === 1) return jsonResponse({ message: "save unavailable" }, 503);
          const values = JSON.parse(String(init.body)).items;
          saved = { ...saved, team_max_concurrency: Number(values.find((item:{key:string}) => item.key.endsWith("TEAM_MAX_CONCURRENCY")).value), team_concurrency_overrides: JSON.parse(values.find((item:{key:string}) => item.key.endsWith("OVERRIDES_JSON")).value) };
        }
        return jsonResponse({ data: { update_time: status.observed_at, items: items(), effective: saved } });
      }
      if (url.includes("/teams?")) {
        const offset = Number(new URL(url, "http://local").searchParams.get("offset"));
        return jsonResponse({ data: [{ id: offset ? other : team, name: offset ? "Other team" : "Parallel team" }], pagination: { offset, limit: 10, total: 11 } });
      }
      if (url.endsWith("/topic-maintenance/status")) return jsonResponse({ data: { ...status, pending_policy: saved, window: { ...status.window, policy: active } } });
      if (url.includes("/topic-maintenance/runs?")) return jsonResponse({ data: { runs: [] } });
      throw new Error(`Unexpected request ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<TopicMaintenanceConfigPanel api={new ControlApi("synthetic", "/control/api")} />);
    await screen.findByRole("option", { name: "Parallel team" });
    fireEvent.change(screen.getByLabelText("Team for override"), { target: { value: team } });
    expect(screen.getByText(/Current window limit: 1.*Saved next-window limit: 2/)).toBeVisible();
    fireEvent.change(screen.getByLabelText("Concurrency override"), { target: { value: "9" } });
    expect(screen.getByRole("button", { name: "Stage team override" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Concurrency override"), { target: { value: "8" } });
    fireEvent.click(screen.getByRole("button", { name: "Stage team override" }));
    fireEvent.click(screen.getByRole("button", { name: "Save config" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("save unavailable");
    expect(screen.getByText(/Staged: 8/)).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Save config" }));
    await screen.findByText("Changes saved");
    fireEvent.click(screen.getByRole("button", { name: "Refresh topic coverage" }));
    await waitFor(() => expect(screen.getByText(/Staged: 8.*Saved: 8.*Current effective: 1.*Pending effective: 3/)).toBeVisible());
    fireEvent.click(screen.getByRole("button", { name: "Next teams" }));
    await screen.findByRole("option", { name: "Other team" });
    fireEvent.click(screen.getByRole("button", { name: "Use default for Parallel team" }));
    fireEvent.click(screen.getByRole("button", { name: "Save config" }));
    await waitFor(() => expect(attempts).toBe(3));
    expect(saved.team_concurrency_overrides).toEqual([]);
  });

});
