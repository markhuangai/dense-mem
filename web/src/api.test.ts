import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, ControlApi } from "./api";

describe("ControlApi", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("sends the portal token as a bearer token", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: { authenticated: true } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const api = new ControlApi("secret", "/control/api");
    await api.session();

    expect(fetchMock).toHaveBeenCalledWith("/control/api/session", expect.objectContaining({
      headers: expect.objectContaining({ Authorization: "Bearer secret" }),
    }));
  });

  it("throws ApiError with server message", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "invalid token" }), { status: 401 })));

    const api = new ControlApi("bad", "/control/api");

    await expect(api.session()).rejects.toMatchObject(new ApiError(401, "invalid token"));
  });

  it("requests metrics with window and team filters", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        window: { from: "2026-05-02T12:00:00Z", to: "2026-05-02T13:00:00Z", bucket_seconds: 60, retention_days: 30 },
        system: { requests: 0, errors: 0, avg_latency_ms: 0, max_latency_ms: 0 },
        dependencies: [],
        teams: [],
        keys: [],
        routes: [],
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const api = new ControlApi("secret", "/control/api");
    await api.getMetrics({ window_minutes: 60, team_id: "team-1" });

    expect(fetchMock).toHaveBeenCalledWith("/control/api/metrics?window_minutes=60&team_id=team-1", expect.any(Object));
  });

  it("requests telemetry with window and profile filters", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        available: true,
        window: { key: "1h", from: "2026-05-02T12:00:00Z", to: "2026-05-02T13:00:00Z", step_seconds: 60, retention_days: 30 },
        scope: { type: "profile", team_id: "team-1", profile_id: "profile-1" },
        cards: [],
        series: [],
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const api = new ControlApi("secret", "/control/api");
    await api.getTelemetry({ window: "1h", scope: "profile", team_id: "team-1", profile_id: "profile-1" });

    expect(fetchMock).toHaveBeenCalledWith("/control/api/telemetry?window=1h&scope=profile&team_id=team-1&profile_id=profile-1", expect.any(Object));
  });
});
