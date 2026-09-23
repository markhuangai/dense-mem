import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, Dream, DreamDiagnostic, DreamRun, DreamStatus, Team } from "../api";
import { ControlDreamsPanel } from "./DreamsPanel";

const team: Team = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Dream Team",
  description: "",
  metadata: null,
  config: null,
  created_at: "2026-07-28T20:00:00Z",
  updated_at: "2026-07-28T20:00:00Z",
};

const status: DreamStatus = {
  effective_config: {
    enabled: true,
    force_enabled: false,
    start_time_local: "03:00",
    timezone: "UTC",
    max_outputs: 5,
    team_enabled: true,
    source: "team",
  },
  latest_run: null,
  pending_count: 1,
};

const dream: Dream = {
  dream_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  team_id: team.id,
  hypothesis: "A team-scoped control dream",
  what_if: "",
  possible_outcome: "",
  rationale: "",
  likelihood: 0.7,
  confidence: 0.8,
  status: "proposed",
  derivations: [
    {
      premise_position: 1,
      relationship_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      relationship_version: 2,
      source_group_key: "support-a",
      quote: "Dense-Mem uses Runtime.",
      authority: "primary",
    },
    {
      premise_position: 2,
      relationship_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
      relationship_version: 4,
      source_group_key: "support-b",
      quote: "Runtime uses PostgreSQL.",
      authority: "primary",
    },
  ],
  created_at: "2026-07-28T19:00:00Z",
  updated_at: "2026-07-28T20:00:00Z",
};

const failedProviderRun: DreamRun = {
  run_id: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
  team_id: team.id,
  run_date: "2026-07-28",
  started_at: "2026-07-28T03:00:00Z",
  completed_at: "2026-07-28T03:00:02Z",
  input_relationships: 2,
  attempted_paths: 1,
  provider_proposals: 0,
  created_dreams: 0,
  rejected_dreams: 0,
  outcome_summary: { provider_failed: 1, attempted_paths: 1 },
  status: "failed",
};

const emptyProviderRun: DreamRun = {
  ...failedProviderRun,
  run_id: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
  provider_proposals: 0,
  outcome_summary: {},
  status: "completed",
};

const diagnostic: DreamDiagnostic = {
  capture_id: "99999999-9999-4999-8999-999999999999",
  team_id: team.id,
  run_id: failedProviderRun.run_id,
  phase: "provider",
  outcome: "completed",
  capture_state: "captured",
  capture_reason: "",
  details: { provider_proposals: 1 },
  expires_at: "2026-09-28T03:00:00Z",
  created_at: "2026-07-28T03:00:01Z",
};

describe("ControlDreamsPanel", () => {
  it("loads team-owned outputs and paginates without a re-evaluation request", async () => {
    const getTeamDreamingStatus = vi.fn(async () => status);
    const listTeamDreamingRuns = vi.fn(async () => [failedProviderRun, emptyProviderRun]);
    const listTeamDreams = vi.fn(async () => ({ items: [dream], next_cursor: "next-page" }));
    const api = {
      getTeamDreamingStatus,
      listTeamDreamingRuns,
      listTeamDreams,
    } as unknown as ControlApi;

    render(<ControlDreamsPanel api={api} team={team} />);

    expect(await screen.findByText("A team-scoped control dream")).toBeInTheDocument();
    expect(screen.getByText("2 cited excerpts")).toBeInTheDocument();
    expect(screen.getByText("Provider call failed")).toBeInTheDocument();
    expect(screen.getByText("Provider returned no supported relationship")).toBeInTheDocument();
    expect(screen.getByText("failed")).toHaveClass("error");
    expect(getTeamDreamingStatus).toHaveBeenCalledWith(team.id);
    expect(listTeamDreamingRuns).toHaveBeenCalledWith(team.id, 10);

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(listTeamDreams).toHaveBeenCalledTimes(2));
    expect(listTeamDreams).toHaveBeenLastCalledWith(team.id, expect.objectContaining({ cursor: "next-page" }));
  });

  it("loads hypothesis diagnostics and expands the linked run capture", async () => {
    const listTeamDreamingRuns = vi.fn(async () => [failedProviderRun, emptyProviderRun]);
    const listTeamDreams = vi.fn(async () => ({ items: [dream], next_cursor: "" }));
    const listTeamDreamDiagnosticsForHypothesis = vi.fn(async () => ({ items: [diagnostic], next_cursor: "" }));
    const getTeamDreamDiagnostic = vi.fn(async () => ({ ...diagnostic, payload: { response_body: "protected" } }));
    const api = {
      getTeamDreamingStatus: vi.fn(async () => status),
      listTeamDreamingRuns,
      listTeamDreams,
      listTeamDreamDiagnosticsForHypothesis,
      getTeamDreamDiagnostic,
    } as unknown as ControlApi;

    render(<ControlDreamsPanel api={api} team={team} />);
    const dreamRow = (await screen.findByText("A team-scoped control dream")).closest("tr");
    expect(dreamRow).toBeTruthy();
    await userEvent.click(within(dreamRow as HTMLElement).getByRole("button", { name: "Inspect" }));
    expect(await screen.findByText("provider")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "View capture" }));
    await waitFor(() => expect(getTeamDreamDiagnostic).toHaveBeenCalledWith(team.id, failedProviderRun.run_id, diagnostic.capture_id));
    expect(await screen.findByText(/protected/)).toBeInTheDocument();
  });

  it("ignores a stale diagnostic response after a newer run selection", async () => {
    let resolveFirst: ((value: { items: DreamDiagnostic[]; next_cursor: string }) => void) | undefined;
    const firstPage = new Promise<{ items: DreamDiagnostic[]; next_cursor: string }>((resolve) => { resolveFirst = resolve; });
    const newerDiagnostic = { ...diagnostic, capture_id: "88888888-8888-4888-8888-888888888888", run_id: emptyProviderRun.run_id, phase: "validation" as const, outcome: "newer" };
    const listTeamDreamDiagnostics = vi.fn()
      .mockReturnValueOnce(firstPage)
      .mockResolvedValueOnce({ items: [newerDiagnostic], next_cursor: "" });
    const api = {
      getTeamDreamingStatus: vi.fn(async () => status),
      listTeamDreamingRuns: vi.fn(async () => [failedProviderRun, emptyProviderRun]),
      listTeamDreams: vi.fn(async () => ({ items: [dream], next_cursor: "" })),
      listTeamDreamDiagnostics,
    } as unknown as ControlApi;

    render(<ControlDreamsPanel api={api} team={team} />);
    expect(await screen.findByText("A team-scoped control dream")).toBeInTheDocument();
    const inspectButtons = screen.getAllByRole("button", { name: "Inspect" });
    await userEvent.click(inspectButtons[1]);
    await userEvent.click(inspectButtons[2]);
    expect(await screen.findByText("newer")).toBeInTheDocument();
    resolveFirst?.({ items: [diagnostic], next_cursor: "" });
    await waitFor(() => expect(within(screen.getByRole("region", { name: "Dream diagnostics" })).queryByText("completed")).not.toBeInTheDocument());
  });

  it("keeps expired capture state visible while hiding its payload", async () => {
    const expired = { ...diagnostic, capture_state: "expired" as const, capture_reason: "retention_expired" };
    const api = {
      getTeamDreamingStatus: vi.fn(async () => status),
      listTeamDreamingRuns: vi.fn(async () => [failedProviderRun]),
      listTeamDreams: vi.fn(async () => ({ items: [dream], next_cursor: "" })),
      listTeamDreamDiagnostics: vi.fn(async () => ({ items: [expired], next_cursor: "" })),
    } as unknown as ControlApi;

    render(<ControlDreamsPanel api={api} team={team} />);
    expect(await screen.findByText("A team-scoped control dream")).toBeInTheDocument();
    const runRow = document.querySelector(".dream-runs-table tbody tr");
    expect(runRow).toBeTruthy();
    await userEvent.click(within(runRow as HTMLElement).getByRole("button", { name: "Inspect" }));
    expect(await screen.findByText(/expired · retention_expired/)).toBeInTheDocument();
  });
});
