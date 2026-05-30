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
        key: { id: "key-1", team_id: "team-1", name: "Mine", key_suffix: "abc123", scopes: ["read"], rate_limit: 120, last_used_at: null, expires_at: null, created_at: "2026-05-01T12:00:00Z" },
        can_rotate: false,
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
        key: { id: "key-1", team_id: "team-1", name: "Mine", key_suffix: "new123", scopes: ["read", "write"], rate_limit: 120, last_used_at: null, expires_at: null, created_at: "2026-05-01T12:00:00Z" },
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await new UserApi("dm_key").rotateKey();

    expect(fetchMock).toHaveBeenCalledWith("/ui/api/key/rotate", expect.objectContaining({
      method: "POST",
      body: "{}",
    }));
  });

  it("throws ApiError with server message", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "invalid api key" }), { status: 401 })));

    await expect(new UserApi("bad").session()).rejects.toMatchObject(new ApiError(401, "invalid api key"));
  });
});
