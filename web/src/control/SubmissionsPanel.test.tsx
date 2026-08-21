import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, Team } from "../api";
import { SubmissionsPanel } from "./SubmissionsPanel";

describe("SubmissionsPanel", () => {
  it("shows actionable failure guidance and a submission-scoped lifecycle timeline", async () => {
    const listSubmissionDiagnostics = vi.fn().mockResolvedValue({
      data: [{
        team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1",
        submission_id: "submission-1", processing_state: "failed", correlation_id: "corr-1",
        attempts: 5, max_attempts: 5, evidence_count: 1, submitted_at: "2026-08-18T01:00:00Z",
        error: { code: "assessor_unavailable", message: "submission assessment was unavailable after bounded retries", retryable: true, next_action: "resubmit_submission", remediation: "Submit the complete batch again with remember." },
      }],
      pagination: { limit: 50, offset: 0, total: 1 },
    });
    const getSubmissionDiagnostic = vi.fn().mockResolvedValue({
      team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1", evidence_count: 1,
      submission_id: "submission-1", submission_kind: "remember", processing_state: "failed", search_state: "not_required",
      check_after_seconds: 60, correlation_id: "corr-1", attempts: 5, max_attempts: 5,
      submitted_at: "2026-08-18T01:00:00Z", updated_at: "2026-08-18T01:05:00Z",
      evidence: [{ evidence_id: "evidence-1", evidence_index: 0, superseded_evidence_ids: [], search_state: "not_required" }],
      errors: [{ code: "assessor_unavailable", message: "submission assessment was unavailable after bounded retries", retryable: true, next_action: "resubmit_submission", remediation: "Submit the complete batch again with remember." }],
    });
    const listOperationLogs = vi.fn().mockResolvedValue({
      data: [{ id: "log-1", timestamp: "2026-08-18T01:05:00Z", severity: "WARN", severity_rank: 30, message: "submission_failed", source: "worker", team_id: "team-1", profile_id: "owner-1", correlation_id: "corr-1", error: "", attrs: { from: "processing", to: "failed", stage: "assessment", reason_code: "terminal_failure" } }],
      pagination: { limit: 100, offset: 0, total: 1 },
    });
    const api = { listSubmissionDiagnostics, getSubmissionDiagnostic, listOperationLogs } as unknown as ControlApi;

    render(<SubmissionsPanel api={api} team={team()} />);

    expect(await screen.findByText("assessor_unavailable")).toBeInTheDocument();
    expect(screen.getByText("Submit the complete batch again with remember.")).toBeInTheDocument();
    expect(screen.getByText("Resubmit Submission")).toBeInTheDocument();
    expect(screen.getAllByText("Failed").length).toBeGreaterThan(0);
    expect(screen.getByText(/Processing → Failed/)).toBeInTheDocument();
    expect(screen.queryByText(/private evidence/i)).not.toBeInTheDocument();
    expect(listOperationLogs).toHaveBeenCalledWith({
      team_id: "team-1", reference_type: "submission", reference_id: "submission-1",
      limit: 100, offset: 0, sort: "timestamp", direction: "asc",
    });

    await userEvent.selectOptions(screen.getByLabelText("Processing state"), "failed");
    await waitFor(() => expect(listSubmissionDiagnostics).toHaveBeenLastCalledWith({
      team_id: "team-1", processing_state: "failed", limit: 50, offset: 0,
    }));
  });

  it("keeps authoritative detail visible when supplemental logs are unavailable", async () => {
    const summary = {
      team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1",
      submission_id: "submission-2", processing_state: "completed", correlation_id: "corr-2",
      attempts: 1, max_attempts: 5, evidence_count: 1, submitted_at: "2026-08-18T02:00:00Z",
    };
    const api = {
      listSubmissionDiagnostics: vi.fn().mockResolvedValue({
        data: [summary], pagination: { limit: 50, offset: 0, total: 1 },
      }),
      getSubmissionDiagnostic: vi.fn().mockResolvedValue({
        ...summary,
        submission_kind: "remember", search_state: "current", check_after_seconds: 60,
        updated_at: "2026-08-18T02:01:00Z", completed_at: "2026-08-18T02:01:00Z",
        evidence: [{ evidence_id: "evidence-2", evidence_index: 0, superseded_evidence_ids: [], search_state: "current" }],
        errors: [],
      }),
      listOperationLogs: vi.fn().mockRejectedValue(new Error("operation log database detail")),
    } as unknown as ControlApi;

    render(<SubmissionsPanel api={api} team={team()} />);

    expect(await screen.findByRole("region", { name: "Submission details" })).toHaveTextContent("Completed");
    expect(screen.getByText("Operational timeline unavailable. Durable placement state remains authoritative.")).toBeInTheDocument();
    expect(screen.queryByText(/database detail/i)).not.toBeInTheDocument();
  });

  it("navigates every submission page", async () => {
    const summaries = ["submission-1", "submission-51"].map((submissionID) => ({
      team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1",
      submission_id: submissionID, processing_state: "completed", correlation_id: `corr-${submissionID}`,
      attempts: 1, max_attempts: 5, evidence_count: 0, submitted_at: "2026-08-18T02:00:00Z",
    }));
    const listSubmissionDiagnostics = vi.fn().mockImplementation(({ offset }: { offset: number }) => Promise.resolve({
      data: [offset === 0 ? summaries[0] : summaries[1]],
      pagination: { limit: 50, offset, total: 51 },
    }));
    const api = {
      listSubmissionDiagnostics,
      getSubmissionDiagnostic: vi.fn().mockImplementation((_teamID: string, submissionID: string) => Promise.resolve({
        ...summaries.find((item) => item.submission_id === submissionID),
        submission_kind: "remember", search_state: "current", check_after_seconds: 60,
        evidence: [], errors: [],
      })),
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [], pagination: { limit: 100, offset: 0, total: 0 },
      }),
    } as unknown as ControlApi;

    const { rerender } = render(<SubmissionsPanel api={api} team={team()} />);

    expect(await screen.findByText("1-1 of 51")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(listSubmissionDiagnostics).toHaveBeenLastCalledWith({
      team_id: "team-1", processing_state: "", limit: 50, offset: 50,
    }));
    expect(await screen.findByText("51-51 of 51")).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Submission details" })).toHaveTextContent("submission-51");

    rerender(<SubmissionsPanel api={api} team={{ ...team(), id: "team-2" }} />);
    await waitFor(() => expect(listSubmissionDiagnostics).toHaveBeenLastCalledWith({
      team_id: "team-2", processing_state: "", limit: 50, offset: 0,
    }));
  });

  it("clears previous-team submissions while the next team loads or fails", async () => {
    const summary = {
      team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1",
      submission_id: "submission-team-1", processing_state: "completed", correlation_id: "corr-team-1",
      attempts: 1, max_attempts: 5, evidence_count: 0, submitted_at: "2026-08-18T02:00:00Z",
    };
    let rejectTeamTwo!: (reason?: unknown) => void;
    const teamTwoPage = new Promise<never>((_resolve, reject) => {
      rejectTeamTwo = reject;
    });
    const listSubmissionDiagnostics = vi.fn().mockImplementation(({ team_id }: { team_id: string }) => {
      if (team_id === "team-2") {
        return teamTwoPage;
      }
      return Promise.resolve({
        data: [summary], pagination: { limit: 50, offset: 0, total: 1 },
      });
    });
    const api = {
      listSubmissionDiagnostics,
      getSubmissionDiagnostic: vi.fn().mockResolvedValue({
        ...summary, submission_kind: "remember", search_state: "current", check_after_seconds: 60,
        evidence: [], errors: [],
      }),
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [], pagination: { limit: 100, offset: 0, total: 0 },
      }),
    } as unknown as ControlApi;

    const { rerender } = render(<SubmissionsPanel api={api} team={team()} />);

    expect(await screen.findByRole("button", { name: "Inspect submission submission-team-1" })).toBeInTheDocument();
    rerender(<SubmissionsPanel api={api} team={{ ...team(), id: "team-2" }} />);
    await waitFor(() => expect(listSubmissionDiagnostics).toHaveBeenLastCalledWith({
      team_id: "team-2", processing_state: "", limit: 50, offset: 0,
    }));
    expect(screen.queryByRole("button", { name: "Inspect submission submission-team-1" })).not.toBeInTheDocument();
    expect(screen.getByText("0-0 of 0")).toBeInTheDocument();

    await act(async () => rejectTeamTwo(new Error("new team unavailable")));

    expect(await screen.findByRole("alert")).toHaveTextContent("new team unavailable");
    expect(screen.queryByRole("button", { name: "Inspect submission submission-team-1" })).not.toBeInTheDocument();
    expect(screen.getByText("0-0 of 0")).toBeInTheDocument();
  });

  it("clears submissions and detail while a new processing-state filter loads or fails", async () => {
    const summary = {
      team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1",
      submission_id: "submission-completed", processing_state: "completed", correlation_id: "corr-completed",
      attempts: 1, max_attempts: 5, evidence_count: 0, submitted_at: "2026-08-18T02:00:00Z",
    };
    let rejectFilteredPage!: (reason?: unknown) => void;
    const filteredPage = new Promise<never>((_resolve, reject) => {
      rejectFilteredPage = reject;
    });
    const listSubmissionDiagnostics = vi.fn().mockImplementation(({ processing_state }: { processing_state: string }) => {
      if (processing_state === "failed") {
        return filteredPage;
      }
      return Promise.resolve({
        data: [summary], pagination: { limit: 50, offset: 0, total: 1 },
      });
    });
    const api = {
      listSubmissionDiagnostics,
      getSubmissionDiagnostic: vi.fn().mockResolvedValue({
        ...summary, submission_kind: "remember", search_state: "current", check_after_seconds: 60,
        evidence: [], errors: [],
      }),
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [], pagination: { limit: 100, offset: 0, total: 0 },
      }),
    } as unknown as ControlApi;

    render(<SubmissionsPanel api={api} team={team()} />);

    expect(await screen.findByRole("button", { name: "Inspect submission submission-completed" })).toBeInTheDocument();
    expect(await screen.findByRole("region", { name: "Submission details" })).toHaveTextContent("submission-completed");

    await userEvent.selectOptions(screen.getByLabelText("Processing state"), "failed");
    await waitFor(() => expect(listSubmissionDiagnostics).toHaveBeenLastCalledWith({
      team_id: "team-1", processing_state: "failed", limit: 50, offset: 0,
    }));
    expect(screen.queryByRole("button", { name: "Inspect submission submission-completed" })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Submission details" })).not.toBeInTheDocument();
    expect(screen.getByText("0-0 of 0")).toBeInTheDocument();

    await act(async () => rejectFilteredPage(new Error("filtered submissions unavailable")));

    expect(await screen.findByRole("alert")).toHaveTextContent("filtered submissions unavailable");
    expect(screen.queryByRole("button", { name: "Inspect submission submission-completed" })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Submission details" })).not.toBeInTheDocument();
    expect(screen.getByText("0-0 of 0")).toBeInTheDocument();
  });

  it("shows complete resubmission guidance and evidence-specific remediation", async () => {
    const summary = {
      team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1",
      submission_id: "submission-failed", processing_state: "failed", correlation_id: "corr-failed",
      attempts: 1, max_attempts: 5, evidence_count: 1, submitted_at: "2026-08-18T02:00:00Z",
    };
    const api = {
      listSubmissionDiagnostics: vi.fn().mockResolvedValue({
        data: [summary], pagination: { limit: 50, offset: 0, total: 1 },
      }),
      getSubmissionDiagnostic: vi.fn().mockResolvedValue({
        ...summary,
        submission_kind: "remember", search_state: "failed", check_after_seconds: 60,
        evidence: [{
          evidence_id: "evidence-held", evidence_index: 0, superseded_evidence_ids: [], search_state: "failed",
          error: {
            code: "search_projection_failed", message: "search projection failed", retryable: true,
            next_action: "contact_operator", remediation: "Inspect the embedding worker and retry the submission.",
          },
        }],
        errors: [{
          code: "submission_requires_resubmission",
          message: "the complete submission must be sent again",
          retryable: true,
          next_action: "resubmit_submission",
          remediation: "Submit the complete batch again with remember and a new idempotency_key after correcting the input.",
        }],
      }),
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [], pagination: { limit: 100, offset: 0, total: 0 },
      }),
    } as unknown as ControlApi;

    render(<SubmissionsPanel api={api} team={team()} />);

    expect(await screen.findByText("search_projection_failed")).toBeInTheDocument();
    expect(screen.getByText("Inspect the embedding worker and retry the submission.")).toBeInTheDocument();
    expect(screen.getByText("Submit the complete batch again with remember and a new idempotency_key after correcting the input.")).toBeInTheDocument();
  });

  it("does not leave a previous submission detail visible after a new detail request fails", async () => {
    const summaries = ["submission-1", "submission-2"].map((submissionID) => ({
      team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1",
      submission_id: submissionID, processing_state: "completed", correlation_id: `corr-${submissionID}`,
      attempts: 1, max_attempts: 5, evidence_count: 0, submitted_at: "2026-08-18T02:00:00Z",
    }));
    const api = {
      listSubmissionDiagnostics: vi.fn().mockResolvedValue({
        data: summaries, pagination: { limit: 50, offset: 0, total: 2 },
      }),
      getSubmissionDiagnostic: vi.fn().mockImplementation((_teamID: string, submissionID: string) => {
        if (submissionID === "submission-2") {
          return Promise.reject(new Error("submission detail unavailable"));
        }
        return Promise.resolve({
          ...summaries[0], submission_kind: "remember", search_state: "not_required", check_after_seconds: 60,
          evidence: [], errors: [],
        });
      }),
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [], pagination: { limit: 100, offset: 0, total: 0 },
      }),
    } as unknown as ControlApi;

    render(<SubmissionsPanel api={api} team={team()} />);

    expect(await screen.findByRole("region", { name: "Submission details" })).toHaveTextContent("submission-1");
    await userEvent.click(screen.getByRole("button", { name: "Inspect submission submission-2" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("submission detail unavailable");
    expect(screen.queryByRole("region", { name: "Submission details" })).not.toBeInTheDocument();
  });
});

function team(): Team {
  return {
    id: "team-1", name: "Staging", description: "", metadata: null, config: null,
    created_at: "2026-08-01T00:00:00Z", updated_at: "2026-08-01T00:00:00Z",
  };
}
