import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ControlApi, ControlMetrics } from "../api";
import { MetricsPanel } from "./MetricsPanel";

const baseMetrics: ControlMetrics = {
  window: { from: "2026-09-13T11:00:00Z", to: "2026-09-13T12:00:00Z", bucket_seconds: 60, retention_days: 30 },
  system: { requests: 5, errors: 0, mcp_tool_calls: 2, mcp_tool_failures: 1, avg_latency_ms: 52, max_latency_ms: 134 },
  dependencies: [],
  teams: [],
  keys: [],
  routes: [],
};

function apiFor(metrics: ControlMetrics): ControlApi {
  return {
    getMetrics: vi.fn(async () => metrics),
    getTelemetry: vi.fn(async () => ({ available: false, window: { key: "1h", from: "2026-09-13T11:00:00Z", to: "2026-09-13T12:00:00Z", step_seconds: 60, retention_days: 30 }, scope: { type: "system" }, cards: [], series: [] })),
  } as unknown as ControlApi;
}

describe("MetricsPanel", () => {
  it("labels HTTP totals separately and renders MCP outcomes", async () => {
    render(<MetricsPanel api={apiFor(baseMetrics)} teams={[]} />);

    await waitFor(() => expect(screen.getByText("HTTP requests")).toBeInTheDocument());
    expect(screen.getByText("MCP tool calls")).toBeInTheDocument();
    expect(screen.getByText("MCP tool failures")).toBeInTheDocument();
    expect(screen.getByText("MCP failure rate")).toBeInTheDocument();
    expect(screen.getByText("50%")).toBeInTheDocument();
  });

  it("marks omitted MCP fields unavailable instead of inventing values", async () => {
    render(<MetricsPanel api={apiFor({ ...baseMetrics, system: { ...baseMetrics.system, mcp_tool_calls: undefined, mcp_tool_failures: undefined } })} teams={[]} />);

    await waitFor(() => expect(screen.getByText("MCP tool calls")).toBeInTheDocument());
    expect(screen.getAllByText("Unavailable").length).toBeGreaterThanOrEqual(3);
  });
});
