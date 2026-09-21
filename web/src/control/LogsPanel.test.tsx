import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ControlApi } from "../api";
import { LogsPanel } from "./LogsPanel";

describe("LogsPanel", () => {
  it("offers every supported operation-log severity", async () => {
    const api = {
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [],
        pagination: { limit: 100, offset: 0, total: 0 },
      }),
    } as unknown as ControlApi;

    render(<LogsPanel api={api} teams={[]} />);

    const severity = await screen.findByLabelText("Severity");
    for (const level of ["TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"]) {
      expect(within(severity).getByRole("option", { name: level })).toBeInTheDocument();
    }
  });

  it("distinguishes fatal and trace log rows", async () => {
    const api = {
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [
          {
            id: "fatal-log",
            timestamp: "2026-08-18T01:05:00Z",
            severity: "FATAL",
            severity_rank: 60,
            message: "fatal event",
            source: "worker",
            team_id: null,
            profile_id: null,
            correlation_id: "",
            error: "",
            attrs: {},
          },
          {
            id: "trace-log",
            timestamp: "2026-08-18T01:06:00Z",
            severity: "TRACE",
            severity_rank: 5,
            message: "trace event",
            source: "worker",
            team_id: null,
            profile_id: null,
            correlation_id: "",
            error: "",
            attrs: {},
          },
        ],
        pagination: { limit: 100, offset: 0, total: 2 },
      }),
    } as unknown as ControlApi;

    render(<LogsPanel api={api} teams={[]} />);

    const fatalRow = (await screen.findByText("fatal event")).closest("tr");
    const traceRow = screen.getByText("trace event").closest("tr");
    expect(fatalRow).not.toBeNull();
    expect(traceRow).not.toBeNull();
    expect(within(fatalRow as HTMLElement).getByText("FATAL")).toHaveClass("status-pill", "error");
    expect(within(traceRow as HTMLElement).getByText("TRACE")).toHaveClass("status-pill", "neutral");
  });

  it("keeps the next retry time in a full compact lifecycle summary", async () => {
    const api = {
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [{
          id: "log-1",
          timestamp: "2026-08-18T01:05:00Z",
          severity: "WARN",
          severity_rank: 30,
          message: "submission_retry_scheduled",
          source: "worker",
          team_id: "team-1",
          profile_id: "owner-1",
          correlation_id: "corr-1",
          error: "",
          attrs: {
            reference_type: "submission",
            reference_id: "submission-1",
            stage: "assessment",
            reason_code: "provider_unavailable",
            from: "processing",
            to: "queued",
            attempts: 2,
            max_attempts: 5,
            next_attempt_at: "2026-08-18T01:06:00Z",
          },
        }],
        pagination: { limit: 100, offset: 0, total: 1 },
      }),
    } as unknown as ControlApi;

    render(<LogsPanel api={api} teams={[]} />);

    expect(await screen.findByText("next_attempt_at=2026-08-18T01:06:00Z")).toBeInTheDocument();
  });

  it("renders bounded worker failure context in the visible summary", async () => {
    const api = {
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [{
          id: "log-2",
          timestamp: "2026-08-18T01:05:00Z",
          severity: "ERROR",
          severity_rank: 50,
          message: "active team worker failed",
          source: "worker",
          team_id: "team-1",
          profile_id: null,
          correlation_id: "",
          error: "semantic placement worker failed; submission_id=submission-1; stage=assessment; reason=assessor_provider_failed; class=timeout",
          attrs: {
            worker_kind: "semantic-placement",
            submission_id: "submission-1",
            failure_stage: "assessment",
            failure_reason_code: "assessor_provider_failed",
            failure_class: "timeout",
          },
        }],
        pagination: { limit: 100, offset: 0, total: 1 },
      }),
    } as unknown as ControlApi;

    render(<LogsPanel api={api} teams={[]} />);

    expect(await screen.findByText("submission_id=submission-1", { exact: true })).toBeInTheDocument();
    expect(screen.getByText("failure_reason_code=assessor_provider_failed")).toBeInTheDocument();
    expect(screen.getByText("failure_class=timeout")).toBeInTheDocument();
  });

  it("applies Remember identity filters and expands the raw record", async () => {
    const user = userEvent.setup();
    const listOperationLogs = vi.fn().mockResolvedValue({
      data: [{
        id: "http-log",
        timestamp: "2026-08-18T01:05:00Z",
        severity: "INFO",
        severity_rank: 20,
        message: "http_request",
        source: "control",
        team_id: "team-1",
        profile_id: null,
        correlation_id: "corr-1",
        error: "",
        attrs: {
          method: "GET", uri: "/control/api/remember-invocations", status: 200,
          invocation_id: "invocation-1", request_hash: "hash-1", attempt_id: "attempt-1",
          classification: "replay", retryable: true, delivery_stage: "write_observed",
        },
      }],
      pagination: { limit: 100, offset: 0, total: 101 },
    });
    const api = { listOperationLogs } as unknown as ControlApi;

    render(<LogsPanel api={api} teams={[{ id: "team-1", name: "Staging", description: "", metadata: null, config: null, created_at: "", updated_at: "" }]} />);
    expect(await screen.findByText("GET /control/api/remember-invocations status 200")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Correlation ID"), "corr-1");
    await user.type(screen.getByLabelText("Request hash"), "hash-1");
    await user.type(screen.getByLabelText("Invocation ID"), "invocation-1");
    await user.type(screen.getByLabelText("Attempt ID"), "attempt-1");
    await user.selectOptions(screen.getByLabelText("Call classification"), "replay");
    await user.selectOptions(screen.getByLabelText("Retryable"), "true");
    await waitFor(() => expect(listOperationLogs).toHaveBeenLastCalledWith(expect.objectContaining({
      correlation_id: "corr-1", request_hash: "hash-1", invocation_id: "invocation-1", attempt_id: "attempt-1",
      classification: "replay", retryable: true, offset: 0,
    })));
    await user.click(screen.getByRole("button", { name: /View raw log/ }));
    expect(screen.getByLabelText(/Raw log body/)).toHaveTextContent("delivery_stage");
  });

  it("debounces free-text identity filters", async () => {
    vi.useFakeTimers();
    try {
      const listOperationLogs = vi.fn().mockResolvedValue({
        data: [],
        pagination: { limit: 100, offset: 0, total: 0 },
      });
      const api = { listOperationLogs } as unknown as ControlApi;

      render(<LogsPanel api={api} teams={[]} />);
      fireEvent.change(screen.getByLabelText("Correlation ID"), { target: { value: "corr" } });
      expect(listOperationLogs).toHaveBeenCalledTimes(1);

      act(() => vi.advanceTimersByTime(299));
      expect(listOperationLogs).toHaveBeenCalledTimes(1);
      act(() => vi.advanceTimersByTime(1));
      expect(listOperationLogs).toHaveBeenCalledTimes(2);
      expect(listOperationLogs).toHaveBeenLastCalledWith(expect.objectContaining({ correlation_id: "corr", offset: 0 }));
    } finally {
      vi.useRealTimers();
    }
  });
});
