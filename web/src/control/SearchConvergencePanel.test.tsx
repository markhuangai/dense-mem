import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, SearchConvergence } from "../api";
import { SearchConvergencePanel } from "./SearchConvergencePanel";

describe("SearchConvergencePanel", () => {
  it("renders canonical document drift and refreshes read-only state", async () => {
    const getSearchConvergence = vi.fn().mockResolvedValue(failureSnapshot());
    const api = { getSearchConvergence } as unknown as ControlApi;

    render(<SearchConvergencePanel api={api} />);

    expect(await screen.findByText("attention required")).toBeInTheDocument();
    expect(screen.getByText("Document drift")).toBeInTheDocument();
    expect(screen.getByText("2 / 2")).toBeInTheDocument();
    expect(screen.queryByText(/Legacy embedding jobs/)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /retry|resolve|delete|requeue/i }),
    ).not.toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("button", { name: "Refresh search convergence" }),
    );
    await waitFor(() => expect(getSearchConvergence).toHaveBeenCalledTimes(2));
  });

  it("shows convergence without retaining resolved drift state", async () => {
    const snapshot = failureSnapshot();
    snapshot.status = "converged";
    snapshot.drift_classes = [];
    snapshot.drifted_documents = 0;
    snapshot.oldest_drift_age_seconds = 0;
    const api = {
      getSearchConvergence: vi.fn().mockResolvedValue(snapshot),
    } as unknown as ControlApi;

    render(<SearchConvergencePanel api={api} />);

    expect(await screen.findByText("converged")).toBeInTheDocument();
    expect(screen.getByText("Document drift")).toBeInTheDocument();
    expect(screen.getByText("No outstanding drift")).toBeInTheDocument();
    expect(screen.queryByText(/Legacy embedding jobs/)).not.toBeInTheDocument();
  });
});

function failureSnapshot(): SearchConvergence {
  return {
    observed_at: "2026-08-11T04:30:00Z",
    status: "attention_required",
    expected_documents: 4,
    current_documents: 2,
    drifted_documents: 2,
    affected_team_count: 1,
    oldest_drift_age_seconds: 120,
    drift_classes: [
      { class: "missing_document", count: 1 },
      { class: "missing_vector", count: 1 },
    ],
    contract: {
      provider: "openai",
      model: "embedding-model",
      dimensions: 3,
      index_generation: 2,
      index_strategy: "exact",
    },
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
