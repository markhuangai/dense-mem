import { describe, expect, it } from "vitest";
import { formatTelemetryAxisTick, telemetryChartPoints } from "./TelemetryDashboard";

describe("TelemetryDashboard helpers", () => {
  it("keeps small per-second axis labels distinct", () => {
    expect(formatTelemetryAxisTick(0, "promotions/s")).toBe("0");
    expect(formatTelemetryAxisTick(0.006, "promotions/s")).toBe("0.006");
    expect(formatTelemetryAxisTick(0.04, "rps")).toBe("0.04");
    expect(formatTelemetryAxisTick(1.5, "requests/s")).toBe("1.5");
  });

  it("uses the selected telemetry window as empty chart axis data", () => {
    const points = telemetryChartPoints([], "2026-05-02T12:00:00Z", "2026-05-02T13:00:00Z");

    expect(points).toEqual([
      { timestamp: "2026-05-02T12:00:00Z", value: 0 },
      { timestamp: "2026-05-02T13:00:00Z", value: 0 },
    ]);
  });

  it("returns real samples without cloning them", () => {
    const samples = [{ timestamp: "2026-05-02T12:00:00Z", value: 0.02 }];

    expect(telemetryChartPoints(samples, "2026-05-02T11:00:00Z", "2026-05-02T13:00:00Z")).toBe(samples);
  });
});
