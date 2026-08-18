import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ControlApi } from "../api";
import { LogsPanel } from "./LogsPanel";

describe("LogsPanel", () => {
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
});
