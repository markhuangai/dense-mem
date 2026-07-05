import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, UserApi } from "./api";

describe("UserApi", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("sends the API key as a bearer token", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        team: { id: "team-1", name: "Team", description: "", created_at: "2026-05-01T12:00:00Z", updated_at: "2026-05-01T12:00:00Z" },
        key: { id: "key-1", team_id: "team-1", name: "Mine", key_suffix: "abc123", scopes: ["read"], role: "member", rate_limit: 120, last_used_at: null, expires_at: null, created_at: "2026-05-01T12:00:00Z" },
        can_rotate: false,
        can_manage_team: false,
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await new UserApi("dm_key").session();

    expect(fetchMock).toHaveBeenCalledWith("/ui/api/session", expect.objectContaining({
      headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
    }));
  });

  it("rotates without editable fields", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        api_key: "dm_new",
        key: { id: "key-1", team_id: "team-1", name: "Mine", key_suffix: "new123", scopes: ["read", "write"], role: "member", rate_limit: 120, last_used_at: null, expires_at: null, created_at: "2026-05-01T12:00:00Z" },
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await new UserApi("dm_key").rotateKey();

    expect(fetchMock).toHaveBeenCalledWith("/ui/api/key/rotate", expect.objectContaining({
      method: "POST",
      body: "{}",
    }));
  });

  it("requests role-derived telemetry", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        available: true,
        window: { key: "30m", from: "2026-05-02T12:30:00Z", to: "2026-05-02T13:00:00Z", step_seconds: 60, retention_days: 30 },
        scope: { type: "self", team_id: "team-1", profile_id: "key-1" },
        cards: [],
        series: [],
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await new UserApi("dm_key").telemetry({ window: "30m" });

    expect(fetchMock).toHaveBeenCalledWith("/ui/api/telemetry?window=30m", expect.objectContaining({
      headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
    }));
  });

  it("requests dreams with cursor pagination", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        items: [],
        next_cursor: "next-dream",
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").listDreams({
      status: "proposed",
      limit: 50,
      cursor: "current-dream",
      sort: "last_evaluated_at",
      direction: "desc",
    });

    expect(result.next_cursor).toBe("next-dream");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/dreams?limit=50&status=proposed&cursor=current-dream&sort=last_evaluated_at&direction=desc", expect.objectContaining({
      headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
    }));
  });

  it("requests graph snapshots with scoped query params", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        scope: "local",
        depth: 2,
        limit: 40,
        truncated: false,
        nodes: [],
        edges: [],
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").graph({
      scope: "local",
      q: "project graph",
      types: ["fact", "claim"],
      anchorType: "fact",
      anchorId: "fact-1",
      depth: 2,
      limit: 40,
      includeSuperseded: true,
    });

    expect(result.scope).toBe("local");
    expect(fetchMock).toHaveBeenCalledWith(
      "/ui/api/graph?scope=local&q=project+graph&types=fact%2Cclaim&anchor_type=fact&anchor_id=fact-1&depth=2&limit=40&include_superseded=true",
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
      }),
    );
  });

  it("requests graph node details", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        key: "claim:claim-1",
        id: "claim-1",
        type: "claim",
        title: "claim title",
        body: "claim detail",
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").nodeDetail("claim", "claim-1");

    expect(result.body).toBe("claim detail");
    expect(fetchMock).toHaveBeenCalledWith(
      "/ui/api/node-detail?type=claim&id=claim-1",
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
      }),
    );
  });

  it("throws ApiError with server message", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "invalid api key" }), { status: 401 })));

    await expect(new UserApi("bad").session()).rejects.toMatchObject(new ApiError(401, "invalid api key"));
  });
});
