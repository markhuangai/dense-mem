import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ControlApi } from "../api";
import { jsonResponse, mockPortalFetch, profileA, telemetrySnapshot } from "../App.test-helpers";
import { TeamOverviewPanel } from "./TeamWorkspace";

function renderOverview() {
  return render(<TeamOverviewPanel api={new ControlApi("test-control-token")} team={profileA} />);
}

function telemetryRegion() {
  return within(screen.getByRole("region", { name: "Team telemetry" }));
}

afterEach(() => vi.unstubAllGlobals());

describe("Team overview telemetry", () => {
  it("fetches scoped telemetry independently and changes the range", async () => {
    const requests = mockPortalFetch({ teams: [profileA], keys: [] });
    renderOverview();
    await telemetryRegion().findByRole("group", { name: "HTTP requests samples" });
    expect(telemetryRegion().getByRole("button", { name: "1h" })).toHaveAttribute("aria-pressed", "true");
    expect(within(screen.getByRole("region", { name: "Top signals" })).getByText("90 ms")).toBeVisible();

    for (const window of ["15m", "12h", "1d"]) {
      fireEvent.click(telemetryRegion().getByRole("button", { name: window }));
      await telemetryRegion().findByRole("group", { name: "HTTP requests samples" });
      const telemetryRequest = requests.mock.calls.find(([url]) => String(url).includes(`/telemetry?window=${window}`));
      const params = new URL(String(telemetryRequest?.[0]), "http://localhost").searchParams;
      expect(Object.fromEntries(params)).toEqual({ scope: "team", team_id: profileA.id, window });
    }
    expect(requests.mock.calls.filter(([url]) => String(url).includes("/metrics?"))).toHaveLength(1);
  });

  it.each([404, 503])("keeps metrics and alerts visible on telemetry HTTP %s", async (status) => {
    mockPortalFetch({ teams: [profileA], keys: [], telemetry: () => jsonResponse({ message: "unavailable" }, status) });
    renderOverview();
    expect(await telemetryRegion().findByText("Telemetry unavailable")).toBeVisible();
    expect(within(screen.getByRole("region", { name: "Recent alerts" })).getByText("Request errors detected")).toBeVisible();
    expect(telemetryRegion().queryByRole("group", { name: "HTTP requests samples" })).not.toBeInTheDocument();
  });

  it("reports an explicitly unavailable snapshot", async () => {
    mockPortalFetch({ teams: [profileA], keys: [], telemetry: () => jsonResponse({ data: { ...telemetrySnapshot, available: false } }) });
    renderOverview();
    expect(await telemetryRegion().findByText("Telemetry unavailable")).toBeVisible();
  });

  it("reports degradation and an empty series without making up samples", async () => {
    mockPortalFetch({ teams: [profileA], keys: [], telemetry: () => jsonResponse({
      data: { ...telemetrySnapshot, status: "degraded", message: "Some queries failed", series: [{ ...telemetrySnapshot.series[0], points: [] }] },
    }) });
    renderOverview();
    expect(await telemetryRegion().findByText("Telemetry degraded · Some queries failed")).toBeVisible();
    expect(telemetryRegion().getByText("No samples recorded in this window.")).toBeVisible();
    expect(telemetryRegion().queryByRole("group", { name: "HTTP requests samples" })).not.toBeInTheDocument();
  });

  it("keeps telemetry visible when the independent metrics request fails", async () => {
    mockPortalFetch({ teams: [profileA], keys: [], metrics: "error" });
    renderOverview();
    await telemetryRegion().findByRole("group", { name: "HTTP requests samples" });
    expect(await within(screen.getByRole("region", { name: "Recent alerts" })).findByText("Metrics unavailable")).toBeVisible();
  });

  it("aborts old requests and ignores a late result and an AbortError", async () => {
    let previousSignal: AbortSignal | null | undefined;
    let finishPrevious!: (response: Response) => void;
    const pendingResponse = new Promise<Response>((resolve) => { finishPrevious = resolve; });
    mockPortalFetch({ teams: [profileA], keys: [], telemetry: (url, signal) => {
      if (url.searchParams.get("window") === "1h") {
        previousSignal = signal;
        return pendingResponse;
      }
      if (url.searchParams.get("window") === "15m") return Promise.reject(new DOMException("aborted", "AbortError"));
      return jsonResponse({ data: telemetrySnapshot });
    } });
    renderOverview();
    await waitFor(() => expect(previousSignal).toBeDefined());
    await act(async () => fireEvent.click(telemetryRegion().getByRole("button", { name: "15m" })));
    expect(previousSignal?.aborted).toBe(true);
    expect(telemetryRegion().queryByText("Telemetry unavailable")).not.toBeInTheDocument();
    fireEvent.click(telemetryRegion().getByRole("button", { name: "12h" }));
    await telemetryRegion().findByRole("group", { name: "HTTP requests samples" });

    await act(async () => finishPrevious(jsonResponse({ message: "late error" }, 503)));
    expect(telemetryRegion().getByRole("group", { name: "HTTP requests samples" })).toBeVisible();
    expect(telemetryRegion().queryByText("Telemetry unavailable")).not.toBeInTheDocument();
  });
});
