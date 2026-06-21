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

  it("throws ApiError with status text for non-JSON error responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("<html>bad gateway</html>", {
      status: 502,
      statusText: "Bad Gateway",
    })));

    const api = new ControlApi("bad", "/control/api");

    await expect(api.session()).rejects.toMatchObject({
      name: "ApiError",
      status: 502,
      message: "Bad Gateway",
    });
  });

  it("throws ApiError for malformed JSON success responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{", { status: 200 })));

    const api = new ControlApi("secret", "/control/api");

    await expect(api.session()).rejects.toMatchObject({
      name: "ApiError",
      status: 200,
      message: "Invalid JSON response",
    });
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

  it("reads and updates general config", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({
      data: {
        update_time: "2026-06-16T09:00:00Z",
        items: [{ key: "APP_TIMEZONE", value: "Local", effective_value: "Local", updated_at: "2026-06-16T09:00:00Z" }],
        effective: { timezone: "Local" },
      },
    }), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);

    const api = new ControlApi("secret", "/control/api");
    await api.getGeneralConfig();
    await api.updateGeneralConfig({ items: [{ key: "APP_TIMEZONE", value: "America/New_York" }] });

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/control/api/config/general", expect.objectContaining({ method: "GET" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/control/api/config/general", expect.objectContaining({
      method: "PATCH",
      body: JSON.stringify({ items: [{ key: "APP_TIMEZONE", value: "America/New_York" }] }),
    }));
  });

  it("reads and updates dreaming config", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({
      data: {
        update_time: "2026-06-11T03:00:00Z",
        items: [{ key: "DREAMING_START_TIME_LOCAL", value: "03:00", effective_value: "03:00", updated_at: "2026-06-11T03:00:00Z" }],
        effective: {
          enabled: false,
          force_enabled: false,
          start_time_local: "03:00",
          timezone: "UTC",
          reflect_enabled: true,
          reevaluate_enabled: true,
          dream_enabled: true,
          max_outputs: 5,
        },
      },
    }), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);

    const api = new ControlApi("secret", "/control/api");
    await api.getDreamingConfig();
    await api.updateDreamingConfig({ items: [{ key: "DREAMING_START_TIME_LOCAL", value: "02:30" }] });

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/control/api/config/dreaming", expect.objectContaining({ method: "GET" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/control/api/config/dreaming", expect.objectContaining({
      method: "PATCH",
      body: JSON.stringify({ items: [{ key: "DREAMING_START_TIME_LOCAL", value: "02:30" }] }),
    }));
  });

  it("reads and updates community detection config", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({
      data: {
        update_time: "2026-06-15T03:30:00Z",
        items: [{ key: "COMMUNITY_DETECTION_ENABLED", value: "false", effective_value: "false", updated_at: "2026-06-15T03:30:00Z" }],
        effective: {
          enabled: false,
          start_time_local: "03:30",
          timezone: "Local",
          max_concurrency: 1,
          jitter_seconds: 600,
        },
      },
    }), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);

    const api = new ControlApi("secret", "/control/api");
    await api.getCommunityDetectionConfig();
    await api.updateCommunityDetectionConfig({ items: [{ key: "COMMUNITY_DETECTION_ENABLED", value: "true" }] });

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/control/api/config/community-detection", expect.objectContaining({ method: "GET" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/control/api/config/community-detection", expect.objectContaining({
      method: "PATCH",
      body: JSON.stringify({ items: [{ key: "COMMUNITY_DETECTION_ENABLED", value: "true" }] }),
    }));
  });

  it("requests team dreams with cursor pagination", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        items: [],
        next_cursor: "next-dream",
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const api = new ControlApi("secret", "/control/api");
    const result = await api.listTeamDreams("team-1", {
      limit: 50,
      status: "proposed",
      cursor: "current-dream",
      sort: "created_at",
      direction: "asc",
    });

    expect(result.next_cursor).toBe("next-dream");
    expect(fetchMock).toHaveBeenCalledWith("/control/api/teams/team-1/dreams?limit=50&status=proposed&cursor=current-dream&sort=created_at&direction=asc", expect.any(Object));
  });
});
