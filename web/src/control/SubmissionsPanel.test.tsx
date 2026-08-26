import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi } from "../api";
import { SubmissionsPanel } from "./SubmissionsPanel";

describe("SubmissionsPanel", () => {
  it("lists terminal Remember attempts and renders their event transcript", async () => {
    const listSubmissionDiagnostics = vi.fn().mockResolvedValue({
      data: [{
        team_id: "team-1", team_name: "Payments", owner_profile_id: "owner-1",
        submission_id: "attempt-1", processing_state: "completed", evidence_count: 2,
        relationship_count: 1, document_count: 2, assessor_turns: 1, duration_ms: 42,
        created_at: "2026-08-11T04:30:00Z", completed_at: "2026-08-11T04:30:01Z",
      }],
      pagination: { limit: 50, offset: 0, total: 1 },
    });
    const getSubmissionDiagnostic = vi.fn().mockResolvedValue({
      team_id: "team-1", team_name: "Payments", owner_profile_id: "owner-1",
      submission_id: "attempt-1", submission_kind: "remember", processing_state: "completed",
      search_state: "current", evidence_count: 2, relationship_count: 1, document_count: 2,
      assessor_turns: 1, duration_ms: 42, created_at: "2026-08-11T04:30:00Z",
      completed_at: "2026-08-11T04:30:01Z", evidence: [], relationship_results: [], errors: [],
      events: [{ sequence_no: 1, phase: "remember", event_kind: "commit_completed", outcome: "completed", metadata: {}, created_at: "2026-08-11T04:30:01Z" }],
    });
    const api = { listSubmissionDiagnostics, getSubmissionDiagnostic } as unknown as ControlApi;
    render(<SubmissionsPanel api={api} team={{ id: "team-1", name: "Payments" } as any} />);

    expect(await screen.findByText("Remember Attempt Detail")).toBeInTheDocument();
    expect(screen.getByText("Commit Completed")).toBeInTheDocument();
    expect(screen.getByText("2 / 2")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /refresh Remember attempts/i }));
    await waitFor(() => expect(listSubmissionDiagnostics).toHaveBeenCalledTimes(2));
  });
});
