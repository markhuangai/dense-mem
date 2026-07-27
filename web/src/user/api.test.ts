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
    expect(fetchMock).toHaveBeenCalledWith("/ui/api/dreams?limit=50&status=proposed&cursor=current-dream&sort=last_evaluated_at&direction=desc", expect.objectContaining({
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

  it("maps canonical recall payloads to evidence display hits", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        recall_id: "rec_canonical",
        results: [
          {
            evidence_id: "11111111-1111-4111-8111-111111111111",
            relationship_ids: ["22222222-2222-4222-8222-222222222222"],
            rank: 2,
            context: "Dense-Mem uses PostgreSQL as the durable authority.",
          },
        ],
        conflicts: [],
        discovery_paths: [
          {
            evidence_ids: ["11111111-1111-4111-8111-111111111111"],
            relationships: [
              {
                relationship_id: "22222222-2222-4222-8222-222222222222",
                subject: { entity_id: "33333333-3333-4333-8333-333333333333", name: "Dense-Mem" },
                predicate: "uses",
                object: { name: "PostgreSQL" },
                polarity: "positive",
              },
            ],
          },
        ],
        discovery_guidance: "No additional discovery guidance.",
        related_hypotheses: [],
        search_state: "current",
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").recall("postgres", 3);

    expect(result).toHaveLength(1);
    expect(result[0].evidence?.evidence_id).toBe("11111111-1111-4111-8111-111111111111");
    expect(result[0].evidence?.context).toContain("PostgreSQL");
    expect(result[0].relationships?.[0].relationship_id).toBe("22222222-2222-4222-8222-222222222222");
    expect(result[0].semantic_rank).toBe(2);
    expect(result[0].final_score).toBe(0.5);
    expect(fetchMock).toHaveBeenCalledWith(
      "/ui/api/recall?query=postgres&limit=3",
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
      }),
    );
  });

  it("keeps legacy recall hit array compatibility", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: [
        {
          tier: "1",
          score: 0.94,
          semantic_rank: 1,
          final_score: 0.94,
          fact: {
            fact_id: "fact-1",
            subject: "Dense-Mem",
            predicate: "uses",
            object: "PostgreSQL",
            status: "active",
            truth_score: 0.99,
            recorded_at: "2026-05-01T12:00:00Z",
          },
        },
      ],
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").recall("postgres", 3);

    expect(result).toHaveLength(1);
    expect(result[0].fact?.fact_id).toBe("fact-1");
    expect(result[0].semantic_rank).toBe(1);
  });

  it("keeps legacy relationship and evidence recall compatibility", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        results: [
          {
            relationship_id: "relationship-1",
            subject: { name: "Dense-Mem" },
            predicate: "uses",
            object: { name: "PostgreSQL" },
            tier: "fact",
            evidence_ids: ["evidence-1"],
          },
        ],
        evidences: [
          {
            evidence_id: "evidence-1",
            context: "Dense-Mem uses PostgreSQL.",
            source: "legacy",
          },
        ],
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").recall("postgres", 3);

    expect(result).toHaveLength(1);
    expect(result[0].relationship?.relationship_id).toBe("relationship-1");
    expect(result[0].evidences?.[0].evidence_id).toBe("evidence-1");
    expect(result[0].semantic_rank).toBe(1);
  });

  it("throws a typed error for unknown successful recall payloads", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: { unexpected: true },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(new UserApi("dm_key").recall("postgres", 3)).rejects.toMatchObject(
      new ApiError(500, "Unexpected recall response format"),
    );
  });

  it("throws ApiError with server message", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "invalid api key" }), { status: 401 })));

    await expect(new UserApi("bad").session()).rejects.toMatchObject(new ApiError(401, "invalid api key"));
  });
});
