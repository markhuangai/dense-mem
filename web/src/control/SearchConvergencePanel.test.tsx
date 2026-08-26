import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, SearchConvergence } from "../api";
import { SearchConvergencePanel } from "./SearchConvergencePanel";

describe("SearchConvergencePanel", () => {
  it("renders document drift and refreshes read-only state", async () => {
    const getSearchConvergence = vi.fn().mockResolvedValue(driftSnapshot());
    const api = { getSearchConvergence } as unknown as ControlApi;

    render(<SearchConvergencePanel api={api} />);

    expect(await screen.findByText("vector missing")).toBeInTheDocument();
    expect(screen.getAllByText("2").length).toBeGreaterThan(0);
    expect(screen.getByText("1 affected teams")).toBeInTheDocument();
    expect(screen.getByText(/no queue or background embedding worker/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /requeue|retry|resolve/i })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Refresh search convergence" }));
    await waitFor(() => expect(getSearchConvergence).toHaveBeenCalledTimes(2));
  });

  it("shows a converged document projection without drift", async () => {
    const snapshot = driftSnapshot();
    snapshot.status = "converged";
    snapshot.current_documents = snapshot.expected_documents;
    snapshot.drifted_documents = 0;
    snapshot.affected_team_count = 0;
    snapshot.drift_classes = [];
    const api = { getSearchConvergence: vi.fn().mockResolvedValue(snapshot) } as unknown as ControlApi;

    render(<SearchConvergencePanel api={api} />);

    expect(await screen.findByText("No unresolved document drift.")).toBeInTheDocument();
    expect(screen.queryByText("vector missing")).not.toBeInTheDocument();
  });
});

function driftSnapshot(): SearchConvergence {
  return {
    observed_at: "2026-08-11T04:30:00Z",
    status: "attention_required",
    contract: {
      provider: "openai",
      model: "embedding-model",
      dimensions: 3,
      index_generation: 2,
      index_strategy: "exact",
    },
    expected_documents: 5,
    current_documents: 3,
    drifted_documents: 2,
    affected_team_count: 1,
    oldest_drift_age_seconds: 120,
    drift_classes: [{ class: "vector_missing", count: 2 }],
    latest_run: {
      run_id: "22222222-2222-4222-8222-222222222222",
      local_run_date: "2026-08-11",
      status: "completed",
      selected_count: 2,
      embedded_count: 2,
      updated_count: 2,
      drifted_count: 0,
      updated_at: "2026-08-11T04:30:00Z",
    },
  };
}
