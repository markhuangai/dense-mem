import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ControlApi } from "../api";
import type { CommunityStatus } from "../community-api-types";
import { metricsSnapshot, profileA } from "../App.test-helpers";
import { TeamOverviewPanel } from "./TeamWorkspace";

const status: CommunityStatus = {
  effective_config: { enabled: true, cadence_hours: 24, start_time_local: "00:00", timezone: "UTC", model: "", max_concurrency: 1, context_window_tokens: 0, settings_version: "1" },
  legacy_processing_retired: true,
  current_community_count: 3,
  projection_coverage: { total_topics: 3, current_topics: 3, pending_topics: 0, stale_topics: 0, failed_topics: 0, coverage_complete: true },
};

describe("team overview topic state", () => {
  it.each([
    ["current", status, "3 current · 0 pending · 0 stale · 0 failed", "3", false],
    ["pending", { ...status, projection_coverage: { ...status.projection_coverage!, total_topics: 6, pending_topics: 2, stale_topics: 1, coverage_complete: false } }, "3 current · 2 pending · 1 stale · 0 failed", "3", false],
    ["failed", { ...status, projection_coverage: { ...status.projection_coverage!, total_topics: 6, pending_topics: 2, failed_topics: 1, coverage_complete: false } }, "3 current · 2 pending · 0 stale · 1 failed", "3", true],
    ["empty", { ...status, current_community_count: 0, projection_coverage: { total_topics: 0, current_topics: 0, pending_topics: 0, stale_topics: 0, failed_topics: 0, coverage_complete: true } }, "No current topics", "0", false],
    ["discovery pending", { ...status, current_community_count: 0, projection_coverage: { total_topics: 0, current_topics: 0, pending_topics: 0, stale_topics: 0, failed_topics: 0, coverage_complete: false } }, "Pending discovery", "0", false],
    ["missing coverage", { ...status, projection_coverage: undefined }, "Projection status unavailable", "3", true],
    ["disabled history", { ...status, current_community_count: 0, effective_config: { ...status.effective_config, enabled: false }, latest_run: { run_id: "historical-run", team_id: profileA.id, window_key: "old", status: "completed", node_count: 1, edge_count: 1, community_count: 99 } }, "Historical run: completed", "99", false],
    ["disabled zero history", { ...status, effective_config: { ...status.effective_config, enabled: false }, latest_run: { run_id: "empty-run", team_id: profileA.id, window_key: "old", status: "completed", node_count: 0, edge_count: 0, community_count: 0 } }, "Historical run: completed", "0", false],
    ["disabled without history", { ...status, current_community_count: 0, effective_config: { ...status.effective_config, enabled: false } }, "No historical runs", "n/a", false],
    ["disabled null history", { ...status, current_community_count: 0, effective_config: { ...status.effective_config, enabled: false }, latest_run: null }, "No historical runs", "n/a", false],
    ["enabled history ignored", { ...status, latest_run: { run_id: "historical-run", team_id: profileA.id, window_key: "old", status: "completed", node_count: 1, edge_count: 1, community_count: 99 } }, "3 current · 0 pending · 0 stale · 0 failed", "3", false],
    ["unavailable", null, "Status unavailable", "n/a", true],
  ] as const)("renders %s through the overview component", async (_name, response, expected, expectedValue, warning) => {
    const api = {
      listTeamCredentials: vi.fn(async () => ({ data: [] })),
      getMetrics: vi.fn(async () => metricsSnapshot),
      getTeamCommunityStatus: vi.fn(async () => {
        if (!response) throw new Error("unavailable");
        return response;
      }),
    } as unknown as ControlApi;
    render(<TeamOverviewPanel api={api} team={profileA} />);
    const detail = await within(screen.getByRole("region", { name: "Team activity" })).findByText(expected);
    const row = detail.closest(".metric-row")!;
    expect(row.querySelector("strong")?.textContent).toBe(expectedValue);
    expect(row.classList.contains("warning")).toBe(warning);
    expect(within(screen.getByRole("region", { name: "Team activity" })).queryByText("not run")).not.toBeInTheDocument();
    if (response?.effective_config.enabled) expect(screen.queryByText(/Historical run:/)).not.toBeInTheDocument();
  });
});
