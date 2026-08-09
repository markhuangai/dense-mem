import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError, ConflictQueuePage, ControlApi, Team } from "../api";
import { ConflictQueuePanel } from "./ConflictQueuePanel";

describe("ConflictQueuePanel", () => {
  it("renders bounded queue data and keeps mutation controls absent", async () => {
    const getConflictQueue = vi.fn().mockResolvedValue(queuePage());
    const api = fakeApi(getConflictQueue);

    render(<ConflictQueuePanel api={api} team={team()} />);

    expect(await screen.findByText("Conflict question")).toBeInTheDocument();
    expect(screen.getByText("Alice")).toBeInTheDocument();
    expect(screen.getByText("Showing 1 of 2 supporters.")).toBeInTheDocument();
    expect(screen.getAllByText("Overdue").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("Assessment failures")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /resolve|retry|vote|delete|force winner|lease release/i })).not.toBeInTheDocument();
    expect(getConflictQueue).toHaveBeenCalledWith("team-1", { status: "", limit: 25, cursor: "" });
  });

  it("resets cursor pagination when the status filter changes", async () => {
    const getConflictQueue = vi.fn()
      .mockResolvedValueOnce(queuePage({ next_cursor: "cursor-2" }))
      .mockResolvedValue(queuePage());
    const api = fakeApi(getConflictQueue);
    const user = userEvent.setup();

    render(<ConflictQueuePanel api={api} team={team()} />);
    await screen.findByText("Conflict question");
    await user.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(getConflictQueue).toHaveBeenLastCalledWith("team-1", { status: "", limit: 25, cursor: "cursor-2" }));

    await user.selectOptions(screen.getByLabelText("Show"), "overdue");
    await waitFor(() => expect(getConflictQueue).toHaveBeenLastCalledWith("team-1", { status: "overdue", limit: 25, cursor: "" }));
  });

  it("resets cursor pagination when the selected team changes", async () => {
    const getConflictQueue = vi.fn().mockResolvedValue(queuePage({ next_cursor: "cursor-2" }));
    const api = fakeApi(getConflictQueue);
    const { rerender } = render(<ConflictQueuePanel api={api} team={team()} />);

    await screen.findByText("Conflict question");
    await userEvent.setup().click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(getConflictQueue).toHaveBeenLastCalledWith("team-1", { status: "", limit: 25, cursor: "cursor-2" }));

    rerender(<ConflictQueuePanel api={api} team={team({ id: "team-2" })} />);
    await waitFor(() => expect(getConflictQueue).toHaveBeenLastCalledWith("team-2", { status: "", limit: 25, cursor: "" }));
  });

  it("renders queue data before optional telemetry settles", async () => {
    let resolveTelemetry: ((value: unknown) => void) | undefined;
    const getTelemetry = vi.fn(() => new Promise((resolve) => {
      resolveTelemetry = resolve;
    }));
    const api = {
      getConflictQueue: vi.fn().mockResolvedValue(queuePage()),
      getTelemetry,
    } as unknown as ControlApi;

    render(<ConflictQueuePanel api={api} team={team()} />);
    expect(await screen.findByText("Conflict question")).toBeInTheDocument();
    expect(screen.queryByText("Loading conflict queue")).not.toBeInTheDocument();
    resolveTelemetry?.({ available: false, current_cards: [] });
  });

  it("distinguishes authorization failure from collector degradation", async () => {
    const api = fakeApi(vi.fn().mockRejectedValue(new ApiError(403, "forbidden")), {
      available: true,
      current_cards: [{ id: "conflict_queue_collection_success", label: "Conflict queue collection", unit: "state", value: 0, available: true }],
    });

    render(<ConflictQueuePanel api={api} team={team()} />);

    expect(await screen.findByText("Conflict queue access is not authorized.")).toBeInTheDocument();
    expect(screen.getByText(/Queue collector is degraded/)).toBeInTheDocument();
  });

  it("labels bounded question, predicate, and position projections", async () => {
    const bounded = queuePage();
    bounded.items[0].question_truncated = true;
    bounded.items[0].predicate_key_truncated = true;
    bounded.items[0].positions_truncated = true;
    const api = fakeApi(vi.fn().mockResolvedValue(bounded));

    render(<ConflictQueuePanel api={api} team={team()} />);

    expect(await screen.findByText("Question truncated.")).toBeInTheDocument();
    expect(screen.getByText("Predicate truncated.")).toBeInTheDocument();
    expect(screen.getByText("Some positions are not shown.")).toBeInTheDocument();
  });
});

function fakeApi(getConflictQueue: ReturnType<typeof vi.fn>, telemetryOverrides: Record<string, unknown> = {}) {
  return {
    getConflictQueue,
    getTelemetry: vi.fn().mockResolvedValue({
      available: true,
      current_cards: [{ id: "conflict_queue_collection_success", label: "Conflict queue collection", unit: "state", value: 1, available: true }],
      ...telemetryOverrides,
    }),
  } as unknown as ControlApi;
}

function team(overrides: Partial<Team> = {}): Team {
  return {
    id: "team-1",
    name: "Research",
    description: "",
    metadata: null,
    config: null,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...overrides,
  };
}

function queuePage(overrides: Partial<ConflictQueuePage> = {}): ConflictQueuePage {
  return {
    summary: {
      open_count: 1,
      overdue_count: 1,
      active_lease_count: 1,
      expired_lease_count: 0,
      failed_assessment_count_24h: 2,
      lww_resolution_count_24h: 1,
      pending_derived_task_count: 1,
      failed_derived_task_count: 0,
      oldest_open_age_seconds: 3600,
      oldest_overdue_age_seconds: 7200,
      collected_at: "2026-08-09T00:00:00Z",
    },
    items: [{
      conflict_id: "conflict-1",
      version: 2,
      status: "overdue",
      question: "Conflict question",
      question_truncated: false,
      predicate_key: "owns",
      predicate_key_truncated: false,
      positions_truncated: false,
      review_due_at: "2026-08-08T00:00:00Z",
      next_review_at: "2026-08-08T00:00:00Z",
      created_at: "2026-08-01T00:00:00Z",
      updated_at: "2026-08-09T00:00:00Z",
      attempt_count: 3,
      lease_state: "active",
      lease_until: "2026-08-09T01:00:00Z",
      last_failure_class: "none",
      positions: [{
        position_id: "position-1",
        position_key: "Alice owns the asset",
        disposition: "candidate",
        supporter_count: 2,
        support_group_count: 2,
        authoritative_group_count: 1,
        supporters_truncated: true,
        supporters: [{ profile_id: "profile-1", profile_name: "Alice", strongest_authority: "authoritative", accepted_at: "2026-08-01T00:00:00Z", source_group_count: 1 }],
      }],
    }],
    next_cursor: null,
    ...overrides,
  };
}
