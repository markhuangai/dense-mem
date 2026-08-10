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

  it("exchanges a bearer key for a remembered portal session", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: { status: "signed_in" },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await new UserApi("dm_key").createPortalSession(true);

    expect(fetchMock).toHaveBeenCalledWith("/ui/api/session", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ remember: true }),
      credentials: "same-origin",
      headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
    }));
  });

  it("uses the portal CSRF cookie for cookie-authenticated writes", async () => {
    document.cookie = "dense_mem_ui_csrf=portal-csrf";
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: { status: "signed_out" },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await new UserApi("", "api_key_session").logoutPortalSession();

    expect(fetchMock).toHaveBeenCalledWith("/ui/api/session/logout", expect.objectContaining({
      method: "POST",
      credentials: "include",
      headers: expect.objectContaining({ "X-Dense-Mem-CSRF": "portal-csrf" }),
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
      sort: "updated_at",
      direction: "desc",
    });

    expect(result.next_cursor).toBe("next-dream");
    expect(fetchMock).toHaveBeenCalledWith("/ui/api/dreams?limit=50&status=proposed&cursor=current-dream&sort=updated_at&direction=desc", expect.objectContaining({
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
      types: ["entity", "value"],
      anchorType: "entity",
      anchorId: "entity-1",
      depth: 2,
      limit: 40,
    });

    expect(result.scope).toBe("local");
    expect(fetchMock).toHaveBeenCalledWith(
      "/ui/api/graph?scope=local&q=project+graph&types=entity%2Cvalue&anchor_type=entity&anchor_id=entity-1&depth=2&limit=40",
      expect.objectContaining({
        cache: "no-store",
        headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
      }),
    );
  });

  it("requests graph node details", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        key: "entity:entity-1",
        id: "entity-1",
        type: "entity",
        title: "entity title",
        body: "entity detail",
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").nodeDetail("entity", "entity-1");

    expect(result.body).toBe("entity detail");
    expect(fetchMock).toHaveBeenCalledWith(
      "/ui/api/node-detail?type=entity&id=entity-1",
      expect.objectContaining({
        cache: "no-store",
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
        related_relationships: [],
        related_communities: [
          {
            community_id: "community-1",
            logical_community_id: "logical-community-1",
            rank: 1,
            summary: "Dense-Mem community",
            top_entities: [{ entity_id: "33333333-3333-4333-8333-333333333333", name: "Dense-Mem" }],
            top_predicates: ["uses"],
            entity_count: 2,
            relationship_count: 1,
            relationships: [
              {
                relationship_id: "22222222-2222-4222-8222-222222222222",
                subject: { entity_id: "33333333-3333-4333-8333-333333333333", name: "Dense-Mem" },
                predicate: "uses",
                object: { name: "PostgreSQL" },
                polarity: "positive",
                evidence_ids: ["11111111-1111-4111-8111-111111111111"],
              },
            ],
            relationships_truncated: false,
          },
        ],
        related_hypotheses: [],
        search_states: { evidence: "current", relationships: "current" },
        degradations: [],
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").recall("postgres", 3);

    expect(result).toHaveLength(2);
    expect(result[0].community?.community_id).toBe("community-1");
    expect(result[0].community?.logical_community_id).toBe("logical-community-1");
    const evidenceHit = result.find((hit) => hit.evidence);
    expect(evidenceHit?.evidence?.evidence_id).toBe("11111111-1111-4111-8111-111111111111");
    expect(evidenceHit?.evidence?.context).toContain("PostgreSQL");
    expect(evidenceHit?.relationships?.[0].relationship_id).toBe("22222222-2222-4222-8222-222222222222");
    expect(evidenceHit?.semantic_rank).toBe(2);
    expect(evidenceHit?.final_score).toBe(0.5);
    expect(fetchMock).toHaveBeenCalledWith(
      "/ui/api/recall?query=postgres&limit=3",
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: "Bearer dm_key" }),
      }),
    );
  });

  it("does not associate community relationships without evidence IDs to every evidence hit", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        results: [{ evidence_id: "evidence-target", rank: 1, context: "target" }],
        related_relationships: [],
        related_communities: [{
          community_id: "community-no-evidence",
          logical_community_id: "logical-community-no-evidence",
          rank: 1,
          summary: "community",
          top_entities: [],
          top_predicates: [],
          entity_count: 0,
          relationship_count: 1,
          relationships: [{ relationship_id: "relationship-no-evidence" }],
          relationships_truncated: false,
        }],
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").recall("target", 1);

    const communityHit = result.find((hit) => hit.community);
    expect(communityHit?.community?.relationships?.[0].relationship_id).toBe("relationship-no-evidence");
    expect(result.find((hit) => hit.evidence)?.relationships).toEqual([]);
  });

  it("maps canonical related relationship recall hits", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: {
        recall_id: "rec_relationships",
        results: [],
        conflicts: [],
        related_relationships: [
          {
            relationship_id: "33333333-3333-4333-8333-333333333333",
            subject: { entity_id: "44444444-4444-4444-8444-444444444444", name: "Dense-Mem" },
            predicate: "uses",
            object: { name: "PostgreSQL" },
            polarity: "+",
            evidence_ids: ["11111111-1111-4111-8111-111111111111"],
            search_state: "current",
          },
        ],
        related_communities: [],
        related_hypotheses: [],
        search_states: { evidence: "current", relationships: "current" },
        degradations: [],
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new UserApi("dm_key").recall("postgres", 3);

    expect(result).toHaveLength(1);
    expect(result[0].relationship?.relationship_id).toBe("33333333-3333-4333-8333-333333333333");
    expect(result[0].relationships?.[0].evidence_ids).toEqual(["11111111-1111-4111-8111-111111111111"]);
    expect(result[0].semantic_rank).toBe(1);
    expect(result[0].final_score).toBe(1);
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
