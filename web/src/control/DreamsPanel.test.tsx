import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, Dream, DreamStatus, Team } from "../api";
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
  created_at: "2026-07-28T19:00:00Z",
  updated_at: "2026-07-28T20:00:00Z",
};

describe("ControlDreamsPanel", () => {
  it("loads team-owned outputs and paginates without a re-evaluation request", async () => {
    const getTeamDreamingStatus = vi.fn(async () => status);
    const listTeamDreamingRuns = vi.fn(async () => []);
    const listTeamDreams = vi.fn(async () => ({ items: [dream], next_cursor: "next-page" }));
    const api = {
      getTeamDreamingStatus,
      listTeamDreamingRuns,
      listTeamDreams,
    } as unknown as ControlApi;

    render(<ControlDreamsPanel api={api} team={team} />);

    expect(await screen.findByText("A team-scoped control dream")).toBeInTheDocument();
    expect(getTeamDreamingStatus).toHaveBeenCalledWith(team.id);
    expect(listTeamDreamingRuns).toHaveBeenCalledWith(team.id, 10);

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(listTeamDreams).toHaveBeenCalledTimes(2));
    expect(listTeamDreams).toHaveBeenLastCalledWith(team.id, expect.objectContaining({ cursor: "next-page" }));
  });
});
