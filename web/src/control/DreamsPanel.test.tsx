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
    reflect_enabled: true,
    reevaluate_enabled: true,
    dream_enabled: true,
    max_outputs: 5,
    team_enabled: true,
    source: "team",
  },
  latest_run: null,
  pending_count: 1,
};

const dream: Dream = {
  dream_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  team_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  hypothesis: "A team-scoped control dream",
  what_if: "",
  possible_outcome: "",
  rationale: "",
  likelihood: 0.7,
  confidence: 0.8,
  status: "proposed",
  cycle: "dream",
  created_at: "2026-07-28T19:00:00Z",
  updated_at: "2026-07-28T20:00:00Z",
};

describe("ControlDreamsPanel", () => {
  it("refreshes staleness before initial and manual reads but not filters or pagination", async () => {
    const refreshTeamDreams = vi.fn(async () => ({ updated_count: 0 }));
    const getTeamDreamingStatus = vi.fn(async () => status);
    const listTeamDreamingRuns = vi.fn(async () => []);
    const listTeamDreams = vi.fn(async () => ({ items: [dream], next_cursor: "next-page" }));
    const api = {
      refreshTeamDreams,
      getTeamDreamingStatus,
      listTeamDreamingRuns,
      listTeamDreams,
    } as unknown as ControlApi;

    render(<ControlDreamsPanel api={api} team={team} />);

    expect(await screen.findByText("A team-scoped control dream")).toBeInTheDocument();
    expect(refreshTeamDreams).toHaveBeenCalledTimes(1);
    expect(refreshTeamDreams).toHaveBeenCalledWith(team.id);
    expect(refreshTeamDreams.mock.invocationCallOrder[0]).toBeLessThan(getTeamDreamingStatus.mock.invocationCallOrder[0]);
    expect(refreshTeamDreams.mock.invocationCallOrder[0]).toBeLessThan(listTeamDreams.mock.invocationCallOrder[0]);

    await userEvent.click(screen.getByRole("button", { name: "Refresh dreams" }));
    await waitFor(() => expect(refreshTeamDreams).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(listTeamDreams).toHaveBeenCalledTimes(2));

    await userEvent.selectOptions(screen.getByLabelText("Status"), "proposed");
    await waitFor(() => expect(listTeamDreams).toHaveBeenCalledTimes(3));
    expect(refreshTeamDreams).toHaveBeenCalledTimes(2);

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(listTeamDreams).toHaveBeenCalledTimes(4));
    expect(refreshTeamDreams).toHaveBeenCalledTimes(2);
  });
});
