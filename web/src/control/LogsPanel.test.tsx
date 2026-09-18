import { render, screen, within } from "@testing-library/react";
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
});
