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

  it("reads and updates SSO config", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({
      data: {
        update_time: "2026-06-09T12:00:00Z",
        items: [{ key: "SSO_PUBLIC_BASE_URL", value: "", effective_value: "", updated_at: "2026-06-09T12:00:00Z" }],
      },
    }), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);

    const api = new ControlApi("secret", "/control/api");
    await api.getSSOConfig();
    await api.updateSSOConfig({ items: [{ key: "SSO_PUBLIC_BASE_URL", value: "https://portal.example.com" }] });

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/control/api/config/sso", expect.objectContaining({ method: "GET" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/control/api/config/sso", expect.objectContaining({
      method: "PATCH",
      body: JSON.stringify({ items: [{ key: "SSO_PUBLIC_BASE_URL", value: "https://portal.example.com" }] }),
    }));
  });
});
