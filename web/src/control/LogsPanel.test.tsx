import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ControlApi } from "../api";
import { LogsPanel } from "./LogsPanel";

describe("LogsPanel", () => {
  it("keeps conflict-review retry context in a compact lifecycle summary", async () => {
    const api = {
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [{
          id: "log-1",
          timestamp: "2026-08-18T01:05:00Z",
          severity: "WARN",
          severity_rank: 30,
          message: "conflict_review_retry_scheduled",
          source: "conflict-review",
          team_id: "team-1",
          profile_id: "owner-1",
          correlation_id: "corr-1",
          error: "",
          attrs: {
            reference_type: "conflict",
            reference_id: "conflict-1",
            stage: "review",
            reason_code: "provider_unavailable",
            from: "processing",
            to: "queued",
            worker_kind: "conflict-review",
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

  it("renders bounded conflict-review failure context in the visible summary", async () => {
    const api = {
      listOperationLogs: vi.fn().mockResolvedValue({
        data: [{
          id: "log-2",
          timestamp: "2026-08-18T01:05:00Z",
          severity: "ERROR",
          severity_rank: 50,
          message: "conflict_review_worker_failed",
          source: "conflict-review",
          team_id: "team-1",
          profile_id: null,
          correlation_id: "",
          error: "conflict review failed; conflict_id=conflict-1; stage=review; reason=provider_unavailable; class=timeout",
          attrs: {
            worker_kind: "conflict-review",
            conflict_id: "conflict-1",
            failure_stage: "review",
            failure_reason_code: "provider_unavailable",
            failure_class: "timeout",
          },
        }],
        pagination: { limit: 100, offset: 0, total: 1 },
      }),
    } as unknown as ControlApi;

    render(<LogsPanel api={api} teams={[]} />);

    expect(await screen.findByText("conflict_id=conflict-1", { exact: true })).toBeInTheDocument();
    expect(screen.getByText("failure_reason_code=provider_unavailable")).toBeInTheDocument();
    expect(screen.getByText("failure_class=timeout")).toBeInTheDocument();
  });
});
