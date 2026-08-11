import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, SearchConvergence } from "../api";
import { SearchConvergencePanel } from "./SearchConvergencePanel";

describe("SearchConvergencePanel", () => {
  it("renders bounded job-derived failure groups and refreshes read-only state", async () => {
    const getSearchConvergence = vi.fn().mockResolvedValue(failureSnapshot());
    const api = { getSearchConvergence } as unknown as ControlApi;

    render(<SearchConvergencePanel api={api} />);

    expect(await screen.findByText("Payments")).toBeInTheDocument();
    expect(screen.getByText("provider_quota_exhausted")).toBeInTheDocument();
    expect(screen.getByText("2 / 1")).toBeInTheDocument();
    expect(screen.getByText("Add provider credit or repair billing before the next daily canary.")).toBeInTheDocument();
    expect(screen.getByText("Showing the most recent 1 of 101 failure groups.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /retry|resolve|delete|requeue/i })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Refresh search convergence" }));
    await waitFor(() => expect(getSearchConvergence).toHaveBeenCalledTimes(2));
  });

  it("shows convergence without retaining resolved failure state", async () => {
    const snapshot = failureSnapshot();
    snapshot.status = "converged";
    snapshot.queue.failed = 0;
    snapshot.queue.affected_team_count = 0;
    snapshot.failures = [];
    snapshot.failure_groups = [];
    snapshot.failure_group_count = 0;
    snapshot.failure_groups_truncated = false;
    const api = { getSearchConvergence: vi.fn().mockResolvedValue(snapshot) } as unknown as ControlApi;

    render(<SearchConvergencePanel api={api} />);

    expect(await screen.findByText("No unresolved failure groups.")).toBeInTheDocument();
    expect(screen.queryByText("Payments")).not.toBeInTheDocument();
    expect(screen.queryByText("provider_quota_exhausted")).not.toBeInTheDocument();
  });
});

function failureSnapshot(): SearchConvergence {
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
    queue: {
      queued: 1,
      processing: 0,
      failed: 2,
      expired_leases: 0,
      affected_team_count: 1,
      oldest_pending_age_seconds: 60,
      oldest_failure_age_seconds: 120,
    },
    failures: [{
      source_kind: "evidence",
      failure_class: "provider_action_required",
      failure_code: "provider_quota_exhausted",
      count: 2,
    }],
    failure_groups: [{
      team_id: "11111111-1111-4111-8111-111111111111",
      team_name: "Payments",
      source_kind: "evidence",
      failure_class: "provider_action_required",
      failure_code: "provider_quota_exhausted",
      status: "attention_required",
      failed_job_count: 2,
      queued_job_count: 1,
      processing_job_count: 0,
      affected_job_count: 3,
      first_failed_at: "2026-08-11T04:28:00Z",
      last_failed_at: "2026-08-11T04:29:00Z",
      age_seconds: 60,
      guidance: "Add provider credit or repair billing before the next daily canary.",
    }],
    failure_group_count: 101,
    failure_groups_truncated: true,
    latest_run: {
      run_id: "22222222-2222-4222-8222-222222222222",
      local_run_date: "2026-08-11",
      status: "deferred",
      canary_outcome: "failed",
      canary_failure_class: "provider_action_required",
      canary_failure_code: "provider_quota_exhausted",
      requeued_count: 0,
      recovered_count: 0,
      updated_at: "2026-08-11T04:30:00Z",
    },
  };
}
