import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, RememberAttemptDiagnosticDetail, RememberAttemptDiagnosticSummary, Team } from "../api";
import { RememberAttemptsPanel } from "./RememberAttemptsPanel";

describe("RememberAttemptsPanel", () => {
  it("opens on calls and links a call to filtered logs and its canonical attempt", async () => {
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "invocation-1", canonical_attempt_id: "attempt-1",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "replay", outcome: "replayed",
      phase: "replay", protected_cause: "provider unavailable", delivery_stage: "write_observed", retryable: true,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const listRememberInvocationDiagnostics = vi.fn().mockResolvedValue({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } });
    const getRememberInvocationDiagnostic = vi.fn().mockResolvedValue({ ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "not_delivered" });
    const onOpenLogs = vi.fn();
    const api = { listRememberInvocationDiagnostics, getRememberInvocationDiagnostic, listRememberAttemptDiagnostics: vi.fn().mockResolvedValue({ data: [], pagination: { limit: 50, offset: 0, total: 0 } }) } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} onOpenLogs={onOpenLogs} />);

    expect(await screen.findByRole("heading", { name: "Remember Calls" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Inspect Remember call invocation-1" }));
    expect(screen.getByText(/Caller receipt unknown/)).toBeInTheDocument();
    expect(screen.getByText("write_observed (Caller receipt unknown)")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "View related logs" }));
    expect(onOpenLogs).toHaveBeenCalledWith({ team_id: "team-1", correlation_id: "corr-1", invocation_id: "invocation-1" });
    await userEvent.click(screen.getByRole("button", { name: /Attempt attempt-/ }));
    expect(await screen.findByRole("heading", { name: "Remember Attempts" })).toBeInTheDocument();
  });

  it("disables call selection while the calls list is refreshing", async () => {
    let resolveRefresh!: (value: unknown) => void;
    const refresh = new Promise((resolve) => { resolveRefresh = resolve; });
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "refresh-call", canonical_attempt_id: "",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "execution", outcome: "completed",
      phase: "commit", protected_cause: "", delivery_stage: "write_observed", retryable: false,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const listRememberInvocationDiagnostics = vi.fn()
      .mockResolvedValueOnce({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } })
      .mockReturnValueOnce(refresh);
    const api = {
      listRememberInvocationDiagnostics,
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({ ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured" }),
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    await screen.findByRole("button", { name: "Inspect Remember call refresh-call" });
    await userEvent.click(screen.getByRole("button", { name: "Refresh Remember calls" }));
    expect(screen.getByRole("button", { name: "Inspect Remember call refresh-call" })).toBeDisabled();
    await act(async () => resolveRefresh({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } }));
    expect(screen.getByRole("button", { name: "Inspect Remember call refresh-call" })).not.toBeDisabled();
  });

  it("hides related logs when no log handler is provided", async () => {
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "no-logs-call", canonical_attempt_id: "",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "execution", outcome: "completed",
      phase: "commit", protected_cause: "", delivery_stage: "write_observed", retryable: false,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const api = {
      listRememberInvocationDiagnostics: vi.fn().mockResolvedValue({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } }),
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({ ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured" }),
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    await userEvent.click(await screen.findByRole("button", { name: "Inspect Remember call no-logs-call" }));
    expect(screen.queryByRole("button", { name: "View related logs" })).not.toBeInTheDocument();
  });

  it("does not load hidden attempts until the Attempts view is opened", async () => {
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "calls-only", canonical_attempt_id: "attempt-1",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "execution", outcome: "completed",
      phase: "commit", protected_cause: "", delivery_stage: "write_observed", retryable: false,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const listRememberInvocationDiagnostics = vi.fn().mockResolvedValue({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } });
    const listRememberAttemptDiagnostics = vi.fn().mockResolvedValue({ data: [], pagination: { limit: 50, offset: 0, total: 0 } });
    const api = {
      listRememberInvocationDiagnostics,
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({ ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured" }),
      listRememberAttemptDiagnostics,
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);

    expect(await screen.findByRole("heading", { name: "Remember Calls" })).toBeInTheDocument();
    expect(listRememberAttemptDiagnostics).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: /^Attempts$/ }));
    await waitFor(() => expect(listRememberAttemptDiagnostics).toHaveBeenCalledWith({ team_id: "team-1", outcome: "", limit: 50, offset: 0 }));
  });

  it("clears the previous call detail while a new selection is loading", async () => {
    let resolveSecond!: (value: unknown) => void;
    const secondDetail = new Promise((resolve) => { resolveSecond = resolve; });
    const first = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "call-a", canonical_attempt_id: "",
      request_hash: "hash-a", correlation_id: "corr-a", classification: "execution", outcome: "failed",
      phase: "assessment", protected_cause: "old-cause", delivery_stage: "write_observed", retryable: true,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const second = { ...first, invocation_id: "call-b", correlation_id: "corr-b", protected_cause: "new-cause" } as const;
    const listRememberInvocationDiagnostics = vi.fn().mockResolvedValue({ data: [first, second], pagination: { limit: 50, offset: 0, total: 2 } });
    const getRememberInvocationDiagnostic = vi.fn().mockImplementation((_teamID: string, invocationID: string) => invocationID === "call-a"
      ? Promise.resolve({ ...first, request_capture_state: "captured", request_body: "old-request", provider_exchanges: [], caller_response_capture_state: "captured" })
      : secondDetail);
    const api = { listRememberInvocationDiagnostics, getRememberInvocationDiagnostic } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);

    expect(await screen.findByText("old-request")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Inspect Remember call call-b" }));
    expect(screen.queryByText("old-request")).not.toBeInTheDocument();
    expect(await screen.findByText("Loading Remember call details")).toBeInTheDocument();
    await act(async () => resolveSecond({ ...second, request_capture_state: "captured", request_body: "new-request", provider_exchanges: [], caller_response_capture_state: "captured" }));
    expect(await screen.findByText("new-request")).toBeInTheDocument();
  });

  it("does not surface a hidden attempt failure after switching back to Calls", async () => {
    let rejectAttempt!: (reason?: unknown) => void;
    const pendingAttempt = new Promise((_, reject) => { rejectAttempt = reject; });
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "call-1", canonical_attempt_id: "",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "execution", outcome: "completed",
      phase: "commit", protected_cause: "", delivery_stage: "unknown_receipt", retryable: false,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const api = {
      listRememberInvocationDiagnostics: vi.fn().mockResolvedValue({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } }),
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({ ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured" }),
      listRememberAttemptDiagnostics: vi.fn().mockResolvedValue({ data: [summary("attempt-1")], pagination: { limit: 50, offset: 0, total: 1 } }),
      getRememberAttemptDiagnostic: vi.fn().mockReturnValue(pendingAttempt),
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    await screen.findByRole("heading", { name: "Remember Calls" });
    await userEvent.click(screen.getByRole("button", { name: /^Attempts$/ }));
    expect(await screen.findByRole("heading", { name: "Remember Attempts" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /^Calls$/ }));
    expect(await screen.findByRole("heading", { name: "Remember Calls" })).toBeInTheDocument();
    await act(async () => rejectAttempt(new Error("hidden attempt failed")));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("reloads Calls after switching away from a pending initial request", async () => {
    let resolveInitial!: (value: unknown) => void;
    const initialList = new Promise((resolve) => { resolveInitial = resolve; });
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "call-1", canonical_attempt_id: "",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "execution", outcome: "completed",
      phase: "commit", protected_cause: "", delivery_stage: "unknown_receipt", retryable: false,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const listRememberInvocationDiagnostics = vi.fn()
      .mockReturnValueOnce(initialList)
      .mockResolvedValue({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } });
    const api = {
      listRememberInvocationDiagnostics,
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({ ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured" }),
      listRememberAttemptDiagnostics: vi.fn().mockResolvedValue({ data: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    await screen.findByRole("heading", { name: "Remember Calls" });
    await userEvent.click(screen.getByRole("button", { name: /^Attempts$/ }));
    await userEvent.click(screen.getByRole("button", { name: /^Calls$/ }));
    await waitFor(() => expect(listRememberInvocationDiagnostics).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole("button", { name: "Inspect Remember call call-1" })).toBeInTheDocument();
    await act(async () => resolveInitial({ data: [], pagination: { limit: 50, offset: 0, total: 0 } }));
    expect(screen.getByRole("button", { name: "Inspect Remember call call-1" })).toBeInTheDocument();
  });

  it("resets to Calls when the team changes while Attempts is active", async () => {
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "call-1", canonical_attempt_id: "",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "execution", outcome: "completed",
      phase: "commit", protected_cause: "", delivery_stage: "write_observed", retryable: false,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const listRememberInvocationDiagnostics = vi.fn().mockImplementation(({ team_id }: { team_id: string }) => Promise.resolve({
      data: [{ ...invocation, team_id, invocation_id: `${team_id}-call` }], pagination: { limit: 50, offset: 0, total: 1 },
    }));
    const listRememberAttemptDiagnostics = vi.fn().mockResolvedValue({ data: [summary("attempt-1")], pagination: { limit: 50, offset: 0, total: 1 } });
    const api = {
      listRememberInvocationDiagnostics,
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({ ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured" }),
      listRememberAttemptDiagnostics,
      getRememberAttemptDiagnostic: vi.fn().mockResolvedValue(detailFor("attempt-1")),
    } as unknown as ControlApi;

    const { rerender } = render(<RememberAttemptsPanel api={api} team={team()} />);
    await screen.findByRole("heading", { name: "Remember Calls" });
    await userEvent.click(screen.getByRole("button", { name: /^Attempts$/ }));
    expect(await screen.findByRole("heading", { name: "Remember Attempts" })).toBeInTheDocument();
    rerender(<RememberAttemptsPanel api={api} team={{ ...team(), id: "team-2" }} />);
    expect(await screen.findByRole("heading", { name: "Remember Calls" })).toBeInTheDocument();
  });

  it("preserves an explicitly linked canonical attempt during lazy loading", async () => {
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "call-1", canonical_attempt_id: "attempt-51",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "replay", outcome: "replayed",
      phase: "replay", protected_cause: "", delivery_stage: "write_observed", retryable: false,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const listRememberAttemptDiagnostics = vi.fn().mockResolvedValue({ data: [summary("attempt-1"), summary("attempt-2")], pagination: { limit: 50, offset: 0, total: 51 } });
    const getRememberAttemptDiagnostic = vi.fn().mockImplementation((_teamID: string, attemptID: string) => Promise.resolve(detailFor(attemptID)));
    const api = {
      listRememberInvocationDiagnostics: vi.fn().mockResolvedValue({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } }),
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({ ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured" }),
      listRememberAttemptDiagnostics,
      getRememberAttemptDiagnostic,
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    await screen.findByRole("button", { name: /Attempt attempt-/ });
    await userEvent.click(screen.getByRole("button", { name: /Attempt attempt-/ }));
    expect(await screen.findByRole("heading", { name: "Remember Attempts" })).toBeInTheDocument();
    await screen.findByText("attempt-51");
    expect(getRememberAttemptDiagnostic).toHaveBeenLastCalledWith("team-1", "attempt-51");
  });

  it("warns when top-level invocation bodies are truncated", async () => {
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "truncated-call", canonical_attempt_id: "",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "execution", outcome: "failed",
      phase: "assessment", protected_cause: "", delivery_stage: "write_observed", retryable: true,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const api = {
      listRememberInvocationDiagnostics: vi.fn().mockResolvedValue({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } }),
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({
        ...invocation, request_body: "truncated-request", request_capture_state: "truncated",
        provider_exchanges: [], caller_response: "truncated-response", caller_response_capture_state: "truncated",
      }),
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    await userEvent.click(await screen.findByRole("button", { name: "Inspect Remember call truncated-call" }));
    expect(await screen.findAllByText("The capture exceeded the diagnostic size limit; the displayed body is truncated.")).toHaveLength(2);
  });

  it("renders unknown delivery and operation-log degradation explicitly", async () => {
    const invocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "degraded-call", canonical_attempt_id: "",
      request_hash: "hash-1", correlation_id: "corr-1", classification: "execution", outcome: "failed",
      phase: "assessment", protected_cause: "", delivery_stage: "unknown_receipt", retryable: true,
      duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const api = {
      listRememberInvocationDiagnostics: vi.fn().mockResolvedValue({ data: [invocation], pagination: { limit: 50, offset: 0, total: 1 } }),
      getRememberInvocationDiagnostic: vi.fn().mockResolvedValue({
        ...invocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "captured", enrichment_unavailable: true,
      }),
    } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);
    await userEvent.click(await screen.findByRole("button", { name: "Inspect Remember call degraded-call" }));
    expect(await screen.findByText("Unknown (Caller receipt unknown)")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Related operation-log context is unavailable");
  });

  it("clears prior-team calls and ignores late list and detail responses", async () => {
    let resolveTeamOneList!: (value: unknown) => void;
    let resolveTeamOneDetail!: (value: unknown) => void;
    const teamOneList = new Promise((resolve) => { resolveTeamOneList = resolve; });
    const teamOneDetail = new Promise((resolve) => { resolveTeamOneDetail = resolve; });
    const teamOneInvocation = {
      team_id: "team-1", owner_profile_id: "owner-1", invocation_id: "team-one-invocation", request_hash: "hash-1",
      correlation_id: "corr-1", classification: "execution", outcome: "failed", phase: "assessment", delivery_stage: "unknown_receipt",
      retryable: true, duration_ms: 3, created_at: "2026-08-18T01:00:00Z", expires_at: "2026-08-25T01:00:00Z", retained_by_legal_hold: false,
    } as const;
    const teamTwoInvocation = {
      ...teamOneInvocation, team_id: "team-2", invocation_id: "team-two-invocation", correlation_id: "corr-2", retryable: false,
    } as const;
    const listRememberInvocationDiagnostics = vi.fn().mockImplementation(({ team_id }: { team_id: string }) => (
      team_id === "team-1" ? teamOneList : Promise.resolve({ data: [teamTwoInvocation], pagination: { limit: 50, offset: 0, total: 1 } })
    ));
    const getRememberInvocationDiagnostic = vi.fn().mockImplementation((_teamID: string, invocationID: string) => (
      invocationID === "team-one-invocation"
        ? teamOneDetail
        : Promise.resolve({ ...teamTwoInvocation, request_capture_state: "captured", provider_exchanges: [], caller_response_capture_state: "unknown_receipt" })
    ));
    const api = {
      listRememberInvocationDiagnostics,
      getRememberInvocationDiagnostic,
      listRememberAttemptDiagnostics: vi.fn().mockResolvedValue({ data: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    } as unknown as ControlApi;

    const { rerender } = render(<RememberAttemptsPanel api={api} team={team()} />);
    await act(async () => resolveTeamOneList({ data: [teamOneInvocation], pagination: { limit: 50, offset: 0, total: 1 } }));
    expect(await screen.findByRole("button", { name: "Inspect Remember call team-one-invocation" })).toBeInTheDocument();
    rerender(<RememberAttemptsPanel api={api} team={{ ...team(), id: "team-2" }} />);
    expect(screen.queryByRole("button", { name: "Inspect Remember call team-one-invocation" })).not.toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Inspect Remember call team-two-invocation" })).toBeInTheDocument();

    await act(async () => resolveTeamOneDetail({ ...teamOneInvocation, request_capture_state: "captured", request_body: "team-one-secret", provider_exchanges: [], caller_response_capture_state: "unknown_receipt" }));
    expect(screen.queryByText("team-one-secret")).not.toBeInTheDocument();
  });

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
      events: [{ sequence_no: 1, phase: "assessment", event_kind: "assessment_failed", outcome: "failed", metadata: { markup: "<script>bad()</script>", assessor_validation: { failure_class: "malformed_exhausted", turns: [{ attempt: 3, stage: "response_contract", fields: ["relationship_results.object"], field_families: ["relationship_results.object"], error_count: 1, truncated: false }] } }, created_at: "2026-08-18T01:00:01Z" }],
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
    expect(screen.getAllByText(/Expires/)).toHaveLength(3);
    expect(screen.getByText("<script>alert(1)</script>")).toBeInTheDocument();
    expect(document.querySelector("script")).toBeNull();
    expect(detailRegion.querySelector(".remember-event-metadata")?.textContent).toContain('"markup": "<script>bad()</script>"');
    expect(within(detailRegion).getByRole("heading", { name: "Assessor validation" })).toBeInTheDocument();
    expect(within(detailRegion).getAllByText("relationship_results.object").length).toBeGreaterThanOrEqual(1);
    expect(within(detailRegion).getByText('{"name":"remember","arguments":{"evidence":[]}}')).toBeInTheDocument();
    expect(within(detailRegion).getByText('{"error":"unavailable"}')).toBeInTheDocument();
    expect(within(detailRegion).getByText('{"isError":true}')).toBeInTheDocument();
    await userEvent.click(within(detailRegion).getAllByRole("button", { name: "Copy" })[0]);
    expect(await within(detailRegion).findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("renders explicit expired, unavailable, and interrupted capture states", async () => {
    const listRememberAttemptDiagnostics = vi.fn().mockResolvedValue({
      data: [summary("states-attempt", "failed")],
      pagination: { limit: 50, offset: 0, total: 1 },
    });
    const getRememberAttemptDiagnostic = vi.fn().mockResolvedValue({
      ...baseDetail("states-attempt"),
      outcome: "failed",
      error_code: "provider_unavailable",
      diagnostics: {
        original_request: { diagnostic_id: "expired", sequence_no: 1, kind: "original_request", component: "remember", outcome: "captured", capture_state: "expired", captured_at: "2026-08-01T00:00:00Z", expires_at: "2026-08-08T00:00:00Z", retained_by_legal_hold: false },
        provider_exchanges: [
          { diagnostic_id: "not-called", sequence_no: 2, kind: "provider_exchange", component: "assessor", outcome: "provider_not_called", capture_state: "provider_not_called", captured_at: "2026-08-01T00:00:00Z", expires_at: "2026-08-08T00:00:00Z", retained_by_legal_hold: false },
          { diagnostic_id: "no-response", sequence_no: 3, kind: "provider_exchange", component: "embedding", outcome: "no_response", capture_state: "no_response", captured_at: "2026-08-01T00:00:00Z", expires_at: "2026-08-08T00:00:00Z", retained_by_legal_hold: false },
          { diagnostic_id: "interrupted", sequence_no: 4, kind: "provider_exchange", component: "assessor", outcome: "response_read_failed", capture_state: "interrupted", captured_at: "2026-08-01T00:00:00Z", expires_at: "2026-08-08T00:00:00Z", retained_by_legal_hold: false },
        ],
        caller_response: { diagnostic_id: "caller", sequence_no: 5, kind: "caller_response", component: "mcp", outcome: "not_delivered", capture_state: "not_delivered", captured_at: "2026-08-01T00:00:00Z", expires_at: "2026-08-08T00:00:00Z", retained_by_legal_hold: false },
      },
    });
    const api = { listRememberAttemptDiagnostics, getRememberAttemptDiagnostic } as unknown as ControlApi;

    render(<RememberAttemptsPanel api={api} team={team()} />);

    const detailRegion = await screen.findByRole("region", { name: "Remember attempt details" });
    expect(within(detailRegion).getByText("This capture expired after seven days and its body is no longer available.")).toBeInTheDocument();
    expect(within(detailRegion).getByText("The provider was not called for this failed attempt.")).toBeInTheDocument();
    expect(within(detailRegion).getByText("The provider call did not produce an HTTP response.")).toBeInTheDocument();
    expect(within(detailRegion).getByText("Capture was interrupted before the provider response was fully read.")).toBeInTheDocument();
    expect(within(detailRegion).getByText("No response was delivered to the caller because the request ended before the server could return it.")).toBeInTheDocument();
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
