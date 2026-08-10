import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
	TelemetryDashboard,
	formatTelemetryAxisTick,
  formatTelemetryCardValue,
  telemetryActivitySeries,
  telemetryChartPoints,
  telemetryCurrentCards,
  telemetryStateSeries,
  telemetryWindowedCards,
} from "./TelemetryDashboard";
import type { TelemetrySnapshot } from "./types";

describe("TelemetryDashboard helpers", () => {
  it("keeps small per-second axis labels distinct", () => {
    expect(formatTelemetryAxisTick(0, "events/s")).toBe("0");
    expect(formatTelemetryAxisTick(0.006, "events/s")).toBe("0.006");
    expect(formatTelemetryAxisTick(0.04, "rps")).toBe("0.04");
    expect(formatTelemetryAxisTick(1.5, "requests/s")).toBe("1.5");
  });

  it("formats percent axis labels with units", () => {
    expect(formatTelemetryAxisTick(91.2, "percent")).toBe("91%");
    expect(formatTelemetryAxisTick(8.5, "percent")).toBe("8.5%");
  });

  it("formats USD telemetry as currency", () => {
    expect(formatTelemetryCardValue({ unit: "USD", value: 0.0042, available: true })).toBe("$0.004200");
    expect(formatTelemetryAxisTick(12.5, "USD")).toBe("$12.5");
  });

  it("does not fabricate axes for an empty series", () => {
    const points = telemetryChartPoints([], "2026-05-02T12:00:00Z", "2026-05-02T13:00:00Z");

    expect(points).toEqual([]);
  });

  it("returns real samples without cloning them", () => {
    const samples = [{ timestamp: "2026-05-02T12:00:00Z", value: 0.02 }];

    expect(telemetryChartPoints(samples, "2026-05-02T11:00:00Z", "2026-05-02T13:00:00Z")).toBe(samples);
  });

  it("treats ungrouped telemetry as windowed activity", () => {
    const snapshot = telemetrySnapshot({
      cards: [
        { id: "http_requests", label: "HTTP requests", unit: "requests", value: 4 },
      ],
      series: [
        { id: "http_rps", label: "HTTP requests", unit: "rps", points: [] },
      ],
    });

    expect(telemetryWindowedCards(snapshot).map((card) => card.id)).toEqual(["http_requests"]);
    expect(telemetryCurrentCards(snapshot)).toEqual([]);
    expect(telemetryActivitySeries(snapshot).map((series) => series.id)).toEqual(["http_rps"]);
    expect(telemetryStateSeries(snapshot)).toEqual([]);
  });

  it("prefers explicit telemetry groups when the backend provides them", () => {
    const snapshot = telemetrySnapshot({
      cards: [{ id: "legacy", label: "Legacy", unit: "events", value: 1 }],
      windowed_cards: [{ id: "dream_feedbacks", label: "Dream feedback", unit: "events", value: 1 }],
      current_cards: [{ id: "active_evidence", label: "Active evidence", unit: "evidence", value: 3 }],
      series: [{ id: "legacy_series", label: "Legacy", unit: "events/s", points: [] }],
      activity_series: [{ id: "dream_feedbacks", label: "Dream feedback", unit: "events/s", points: [] }],
      state_series: [{ id: "active_evidence", label: "Active evidence", unit: "evidence", points: [] }],
    });

    expect(telemetryWindowedCards(snapshot).map((card) => card.id)).toEqual(["dream_feedbacks"]);
    expect(telemetryCurrentCards(snapshot).map((card) => card.id)).toEqual(["active_evidence"]);
    expect(telemetryActivitySeries(snapshot).map((series) => series.id)).toEqual(["dream_feedbacks"]);
    expect(telemetryStateSeries(snapshot).map((series) => series.id)).toEqual(["active_evidence"]);
  });

  it("formats unavailable telemetry cards as no data", () => {
    expect(formatTelemetryCardValue({ unit: "requests", value: 0, available: false })).toBe("No data");
    expect(formatTelemetryCardValue({ unit: "requests", value: 0, available: true })).toBe("0");
    expect(formatTelemetryCardValue({ unit: "percent", value: 12.2 })).toBe("12%");
  });

  it("renders ready items first and keeps unavailable items in the reason ledger", () => {
    const snapshot = telemetrySnapshot({
      status: "degraded",
      cards: [
        { id: "requests", label: "Requests", unit: "requests", value: 0, status: "ready", available: true },
        { id: "pricing", label: "AI cost", unit: "USD", value: 0, status: "unavailable", available: false, reason_code: "pricing_missing", reason: "Pricing is not configured for this usage." },
      ],
      windowed_cards: [
        { id: "requests", label: "Requests", unit: "requests", value: 0, status: "ready", available: true },
        { id: "pricing", label: "AI cost", unit: "USD", value: 0, status: "unavailable", available: false, reason_code: "pricing_missing", reason: "Pricing is not configured for this usage." },
      ],
      current_cards: [{ id: "relationships_active", label: "Relationships: active", unit: "relationships", value: 2, status: "ready", available: true }],
      series: [
        { id: "requests", label: "Requests", unit: "rps", points: [{ timestamp: "2026-05-02T13:00:00Z", value: 0 }], status: "ready" },
        { id: "broken", label: "Broken series", unit: "rps", points: [], status: "unavailable", reason_code: "query_failed", reason: "The telemetry query failed." },
      ],
      activity_series: [
        { id: "requests", label: "Requests", unit: "rps", points: [{ timestamp: "2026-05-02T13:00:00Z", value: 0 }], status: "ready" },
        { id: "broken", label: "Broken series", unit: "rps", points: [], status: "unavailable", reason_code: "query_failed", reason: "The telemetry query failed." },
      ],
    });

    render(
      <TelemetryDashboard
        title="Test telemetry"
        snapshot={snapshot}
        windowKey="1h"
        loading={false}
        error=""
        onWindowChange={() => undefined}
        onRefresh={() => undefined}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Some telemetry is unavailable");
    expect(screen.getByLabelText("Test telemetry totals")).toHaveTextContent("Requests");
    expect(screen.getByLabelText("Test telemetry current state")).toHaveTextContent("Relationships: active");
    expect(screen.getByLabelText("Test telemetry charts")).toHaveTextContent("Requests");
    expect(screen.getByLabelText("Test telemetry charts")).not.toHaveTextContent("Broken series");
    const disclosure = screen.getByText("Inactive / unavailable (2)");
    expect(disclosure).toBeVisible();
    fireEvent.click(disclosure);
    expect(screen.getByText("pricing_missing")).toBeVisible();
    expect(screen.getByText("query_failed")).toBeVisible();
  });

  it("shows an unavailable banner for legacy snapshots without status", () => {
    const snapshot = telemetrySnapshot({
      available: false,
      status: undefined,
      message: "Telemetry sources are unavailable",
    });

    render(
      <TelemetryDashboard
        title="Legacy telemetry"
        snapshot={snapshot}
        windowKey="1h"
        loading={false}
        error=""
        onWindowChange={() => undefined}
        onRefresh={() => undefined}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Telemetry is unavailable.");
  });
});

function telemetrySnapshot(overrides: Partial<TelemetrySnapshot>): TelemetrySnapshot {
  return {
    available: true,
    status: "ready",
    generated_at: "2026-05-02T13:00:00Z",
    window: {
      key: "1h",
      from: "2026-05-02T12:00:00Z",
      to: "2026-05-02T13:00:00Z",
      step_seconds: 60,
      retention_days: 30,
    },
    scope: { type: "system" },
    cards: [],
    series: [],
    ...overrides,
  };
}
