import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, Dream, DreamRun, DreamStatus, Team } from "../api";
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

describe("ControlDreamsPanel", () => {
  it("loads team-owned outputs and paginates without a re-evaluation request", async () => {
    const getTeamDreamingStatus = vi.fn(async () => status);
    const listTeamDreamingRuns = vi.fn(async () => [failedProviderRun]);
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
    expect(screen.getByText("failed")).toHaveClass("error");
    expect(getTeamDreamingStatus).toHaveBeenCalledWith(team.id);
    expect(listTeamDreamingRuns).toHaveBeenCalledWith(team.id, 10);

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(listTeamDreams).toHaveBeenCalledTimes(2));
    expect(listTeamDreams).toHaveBeenLastCalledWith(team.id, expect.objectContaining({ cursor: "next-page" }));
  });
});
