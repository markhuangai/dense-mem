import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, RememberAttemptDiagnosticDetail, RememberAttemptDiagnosticSummary, Team } from "../api";
import { RememberAttemptsPanel } from "./RememberAttemptsPanel";

describe("RememberAttemptsPanel", () => {
  it("renders safe result/event data and inline diagnostics with copy controls", async () => {
    const listRememberAttemptDiagnostics = vi.fn().mockResolvedValue({
      data: [{
        team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1", attempt_id: "attempt-1",
        contract_version: "dense-mem.v2.6", submission_kind: "remember", outcome: "failed",
        failed_phase: "assessment", error_code: "provider_unavailable", evidence_count: 1,
        retryable: true, relationship_count: 1, document_count: 0, assessor_turns: 1, duration_ms: 24,
        created_at: "2026-08-18T01:00:00Z",
      }],
      pagination: { limit: 50, offset: 0, total: 1 },
    });
    const getRememberAttemptDiagnostic = vi.fn().mockResolvedValue({
      team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1", attempt_id: "attempt-1",
      contract_version: "remember_request_hash_v1", submission_kind: "remember", outcome: "failed",
      failed_phase: "assessment", error_code: "provider_unavailable", evidence_count: 1,
      retryable: true, relationship_count: 1, document_count: 0, assessor_turns: 1, duration_ms: 24,
      created_at: "2026-08-18T01:00:00Z", public_result: {
        contract_version: "dense-mem.v2.6", submission_id: "attempt-1", submission_kind: "remember",
        processing_state: "failed", search_state: "not_required", correlation_id: "corr-1",
        evidence: [{ disposition: "not_stored", evidence_index: 0, superseded_evidence_ids: [], search_state: "not_required", reason: "<script>alert(1)</script>" }],
        relationship_results: [{ ref: "r1", disposition: "not_stored", splits: [], reason: "provider unavailable" }], errors: [],
      },
      events: [{ sequence_no: 1, phase: "assessment", event_kind: "assessment_failed", outcome: "failed", metadata: { markup: "<script>bad()</script>" }, created_at: "2026-08-18T01:00:01Z" }],
      diagnostics: {
        original_request: { diagnostic_id: "request-1", sequence_no: 1, kind: "original_request", component: "remember", request_body: `{"name":"remember","arguments":{"evidence":[]}}`, outcome: "captured", capture_state: "captured", captured_at: "2026-08-18T01:00:01Z", expires_at: "2026-08-25T01:00:01Z", retained_by_legal_hold: false },
        provider_exchanges: [{ diagnostic_id: "provider-1", sequence_no: 2, kind: "provider_exchange", component: "assessor", request_body: `{"model":"test"}`, response_body: `{"error":"unavailable"}`, outcome: "captured", capture_state: "captured", captured_at: "2026-08-18T01:00:01Z", expires_at: "2026-08-25T01:00:01Z", retained_by_legal_hold: false }],
        caller_response: { diagnostic_id: "response-1", sequence_no: 3, kind: "caller_response", component: "mcp", response_body: `{"isError":true}`, outcome: "captured", capture_state: "captured", captured_at: "2026-08-18T01:00:01Z", expires_at: "2026-08-25T01:00:01Z", retained_by_legal_hold: false },
      },
    });
    const api = { listRememberAttemptDiagnostics, getRememberAttemptDiagnostic } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);

    const detailRegion = await screen.findByRole("region", { name: "Remember attempt details" });
    expect(within(detailRegion).getByText("provider_unavailable")).toBeInTheDocument();
    expect(screen.getByText("Migrated history")).toBeInTheDocument();
    expect(screen.getByText(/Expires/)).toBeInTheDocument();
    expect(screen.getByText("<script>alert(1)</script>")).toBeInTheDocument();
    expect(document.querySelector("script")).toBeNull();
    expect(detailRegion.querySelector(".remember-event-metadata")?.textContent).toContain('"markup": "<script>bad()</script>"');
    expect(within(detailRegion).getByText('{"name":"remember","arguments":{"evidence":[]}}')).toBeInTheDocument();
    expect(within(detailRegion).getByText('{"error":"unavailable"}')).toBeInTheDocument();
    expect(within(detailRegion).getByText('{"isError":true}')).toBeInTheDocument();
    await userEvent.click(within(detailRegion).getAllByRole("button", { name: "Copy" })[0]);
    expect(await within(detailRegion).findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("clears the previous team while the next list request is pending", async () => {
    let resolveTeamTwo!: (value: unknown) => void;
    const teamTwo = new Promise((resolve) => { resolveTeamTwo = resolve; });
    const listRememberAttemptDiagnostics = vi.fn().mockImplementation(({ team_id }: { team_id: string }) => team_id === "team-2" ? teamTwo : Promise.resolve({
      data: [{ team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1", attempt_id: "attempt-1", contract_version: "dense-mem.v2.6", submission_kind: "remember", outcome: "completed", evidence_count: 0, relationship_count: 0, document_count: 0, assessor_turns: 0, duration_ms: 1, created_at: "2026-08-18T01:00:00Z" }],
      pagination: { limit: 50, offset: 0, total: 1 },
    }));
    const api = {
      listRememberAttemptDiagnostics,
      getRememberAttemptDiagnostic: vi.fn().mockResolvedValue({ ...baseDetail("attempt-1"), team_id: "team-1" }),
    } as unknown as ControlApi;
    const { rerender } = render(<RememberAttemptsPanel api={api} team={team()} />);
    expect(await screen.findByRole("button", { name: "Inspect Remember attempt attempt-1" })).toBeInTheDocument();
    rerender(<RememberAttemptsPanel api={api} team={{ ...team(), id: "team-2" }} />);
    await waitFor(() => expect(listRememberAttemptDiagnostics).toHaveBeenLastCalledWith({ team_id: "team-2", outcome: "", limit: 50, offset: 0 }));
    expect(screen.queryByRole("button", { name: "Inspect Remember attempt attempt-1" })).not.toBeInTheDocument();
    await act(async () => resolveTeamTwo({ data: [], pagination: { limit: 50, offset: 0, total: 0 } }));
  });

  it("filters outcomes and paginates the scalar attempt list", async () => {
    const listRememberAttemptDiagnostics = vi.fn().mockImplementation(({ outcome, offset }: { outcome: string; offset: number }) => Promise.resolve(
      outcome === "failed"
        ? { data: [summary("failed-attempt", "failed")], pagination: { limit: 50, offset: 0, total: 1 } }
        : offset === 0
          ? { data: [summary("attempt-a"), summary("attempt-b")], pagination: { limit: 50, offset: 0, total: 51 } }
          : { data: [summary("attempt-z")], pagination: { limit: 50, offset: 50, total: 51 } },
    ));
    const api = {
      listRememberAttemptDiagnostics,
      getRememberAttemptDiagnostic: vi.fn().mockImplementation((teamID: string, attemptID: string) => Promise.resolve(detailFor(attemptID))),
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    expect(await screen.findByRole("button", { name: "Inspect Remember attempt attempt-a" })).toBeInTheDocument();
    expect(within(screen.getByLabelText("Remember attempt outcome")).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "All outcomes", "Completed", "Rejected", "Quarantined", "Failed", "Replayed",
    ]);
    await userEvent.selectOptions(screen.getByLabelText("Remember attempt outcome"), "failed");
    await waitFor(() => expect(listRememberAttemptDiagnostics).toHaveBeenLastCalledWith({ team_id: "team-1", outcome: "failed", limit: 50, offset: 0 }));
    expect(await screen.findByRole("button", { name: "Inspect Remember attempt failed-attempt" })).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Remember attempt outcome"), "");
    await waitFor(() => expect(listRememberAttemptDiagnostics).toHaveBeenLastCalledWith({ team_id: "team-1", outcome: "", limit: 50, offset: 0 }));
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(listRememberAttemptDiagnostics).toHaveBeenLastCalledWith({ team_id: "team-1", outcome: "", limit: 50, offset: 50 }));
    expect(await screen.findByRole("button", { name: "Inspect Remember attempt attempt-z" })).toBeInTheDocument();
    expect(screen.getByText("51-51 of 51")).toBeInTheDocument();
  });

  it("suppresses stale detail responses after selection, filter, and page changes", async () => {
    let resolveDetailA!: (value: RememberAttemptDiagnosticDetail) => void;
    let resolveFiltered!: (value: unknown) => void;
    let resolvePageTwo!: (value: unknown) => void;
    const detailA = new Promise<RememberAttemptDiagnosticDetail>((resolve) => { resolveDetailA = resolve; });
    const filteredPage = new Promise((resolve) => { resolveFiltered = resolve; });
    const pageTwo = new Promise((resolve) => { resolvePageTwo = resolve; });
    const listRememberAttemptDiagnostics = vi.fn().mockImplementation(({ outcome, offset }: { outcome: string; offset: number }) => {
      if (outcome === "failed") return filteredPage;
      return offset === 0
        ? Promise.resolve({ data: [summary("attempt-a"), summary("attempt-b")], pagination: { limit: 50, offset: 0, total: 51 } })
        : pageTwo;
    });
    const getRememberAttemptDiagnostic = vi.fn().mockImplementation((_teamID: string, attemptID: string) => attemptID === "attempt-a" ? detailA : Promise.resolve(detailFor(attemptID)));
    const api = { listRememberAttemptDiagnostics, getRememberAttemptDiagnostic } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    expect(await screen.findByRole("button", { name: "Inspect Remember attempt attempt-a" })).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Remember attempt outcome"), "failed");
    resolveDetailA(detailFor("attempt-a", "failed"));
    await act(async () => await Promise.resolve());
    expect(screen.queryByText("detail-a-error")).not.toBeInTheDocument();
    resolveFiltered({ data: [summary("failed-attempt", "failed")], pagination: { limit: 50, offset: 0, total: 1 } });
    expect(await screen.findByRole("button", { name: "Inspect Remember attempt failed-attempt" })).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Remember attempt outcome"), "");
    expect(await screen.findByRole("button", { name: "Inspect Remember attempt attempt-a" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Inspect Remember attempt attempt-b" }));
    expect(await screen.findByText("attempt-b")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    resolvePageTwo({ data: [summary("attempt-z")], pagination: { limit: 50, offset: 50, total: 51 } });
    expect(await screen.findByRole("button", { name: "Inspect Remember attempt attempt-z" })).toBeInTheDocument();
    expect(screen.queryByText("detail-a-error")).not.toBeInTheDocument();
  });

  it("keeps the latest selection when a refresh resolves late", async () => {
    let refresh = false;
    let resolveRefresh!: (value: unknown) => void;
    const refreshPage = new Promise((resolve) => { resolveRefresh = resolve; });
    const listRememberAttemptDiagnostics = vi.fn().mockImplementation(() => {
      if (refresh) return refreshPage;
      refresh = true;
      return Promise.resolve({ data: [summary("attempt-a"), summary("attempt-b")], pagination: { limit: 50, offset: 0, total: 2 } });
    });
    const api = {
      listRememberAttemptDiagnostics,
      getRememberAttemptDiagnostic: vi.fn().mockImplementation((_teamID: string, attemptID: string) => Promise.resolve(detailFor(attemptID))),
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    expect(await screen.findByText("attempt-a")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh Remember attempts" }));
    await userEvent.click(screen.getByRole("button", { name: "Inspect Remember attempt attempt-b" }));
    expect(await screen.findByText("attempt-b")).toBeInTheDocument();
    resolveRefresh({ data: [summary("attempt-a"), summary("attempt-b")], pagination: { limit: 50, offset: 0, total: 2 } });
    await waitFor(() => expect(screen.getByRole("button", { name: "Inspect Remember attempt attempt-b" }).closest("tr")).toHaveClass("selected-row"));
    expect(screen.getByText("attempt-b")).toBeInTheDocument();
  });


});

function team(): Team {
  return { id: "team-1", name: "Staging", description: "", metadata: null, config: null, created_at: "2026-08-01T00:00:00Z", updated_at: "2026-08-01T00:00:00Z" };
}

function summary(attemptID: string, outcome: RememberAttemptDiagnosticSummary["outcome"] = "completed"): RememberAttemptDiagnosticSummary {
  return {
    team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1", attempt_id: attemptID,
    contract_version: "dense-mem.v2.6", submission_kind: "remember", outcome,
    retryable: outcome === "failed", evidence_count: 0, relationship_count: 0, document_count: 0, assessor_turns: 0, duration_ms: 1,
    created_at: "2026-08-18T01:00:00Z",
  };
}

function detailFor(attemptID: string, outcome: RememberAttemptDiagnosticSummary["outcome"] = "completed"): RememberAttemptDiagnosticDetail {
  return {
    ...baseDetail(attemptID),
    outcome,
    error_code: attemptID === "attempt-a" && outcome === "failed" ? "detail-a-error" : undefined,
    diagnostics: { original_request: null, provider_exchanges: [], caller_response: null },
  };
}

function baseDetail(attemptID: string): RememberAttemptDiagnosticDetail {
  return {
    team_id: "team-1", team_name: "Staging", owner_profile_id: "owner-1", attempt_id: attemptID,
    contract_version: "remember_request_hash_v1", submission_kind: "remember", outcome: "completed",
    retryable: false, evidence_count: 0, relationship_count: 0, document_count: 0, assessor_turns: 0, duration_ms: 1,
    created_at: "2026-08-18T01:00:00Z", public_result: { contract_version: "dense-mem.v2.6", submission_id: attemptID, submission_kind: "remember", processing_state: "completed", search_state: "current", correlation_id: "corr", evidence: [], relationship_results: [], errors: [] }, events: [], diagnostics: { original_request: null, provider_exchanges: [], caller_response: null },
  };
}
