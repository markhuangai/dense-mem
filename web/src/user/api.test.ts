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
        membership: { team_id: "team-1", name: "Mine", grants: ["read"], role: "member" },
        credential: { id: "key-1", team_id: "team-1", name: "Mine", key_suffix: "abc123", scopes: ["read"], role: "member", rate_limit: 120, last_used_at: null, expires_at: null, created_at: "2026-05-01T12:00:00Z", memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: false } } },
        teams: [],
        personal_credentials: [],
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
        credential: { id: "key-1", team_id: "team-1", name: "Mine", key_suffix: "new123", scopes: ["read", "write"], role: "member", rate_limit: 120, last_used_at: null, expires_at: null, created_at: "2026-05-01T12:00:00Z", memory_access: { private: { read: true, write: true }, team_shared: { read: true, write: false } } },
      },
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await new UserApi("dm_key").rotateCredential();

    expect(fetchMock).toHaveBeenCalledWith("/ui/api/credential/rotate", expect.objectContaining({
      method: "POST",
      body: "{}",
    }));
  });

  it("reuses a credential revoke key after a failed response", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ message: "temporary failure" }), { status: 503 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { status: "revoked" } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const api = new UserApi("dm_key", "sso");

    await expect(api.revokeSSOCredential("credential-1")).rejects.toMatchObject(new ApiError(503, "temporary failure"));
    await api.revokeSSOCredential("credential-1");

    const firstHeaders = fetchMock.mock.calls[0][1]?.headers as Record<string, string>;
    const secondHeaders = fetchMock.mock.calls[1][1]?.headers as Record<string, string>;
    expect(firstHeaders["Idempotency-Key"]).toMatch(/^sso-credential-delete-/);
    expect(secondHeaders["Idempotency-Key"]).toBe(firstHeaders["Idempotency-Key"]);
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












  it("returns Evidence and direct topic hints with scoped follow-up parameters", async () => {
    const data={recall_id:"rec-1",results:[{evidence_id:"e-1",rank:1,context:"Exact café Evidence.",equivalent_evidence_ids:[],equivalents_truncated:false}],related_topics:[{topic_id:"11111111-1111-4111-8111-111111111111",name:"Storage",description:"Durable storage",eligible_evidence_count:2,rank:1}],topics_truncated:true,conflicts:[],degradations:[],search_states:{evidence:"current"}};
    const fetcher=vi.fn().mockResolvedValue(new Response(JSON.stringify({data}),{status:200}));
    vi.stubGlobal("fetch",fetcher);
    const topicIDs = [data.related_topics[0].topic_id, "22222222-2222-4222-8222-222222222222"];
    const knownEvidenceIDs = ["33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"];
    const result=await new UserApi("key").recall("",10,{topicIDs,topicLimit:0,knownEvidenceIDs,knownAt:"2026-11-01T00:00:00Z",validAt:"2026-10-01T00:00:00Z"});
    expect(result).toEqual(data);
    const url=new URL(fetcher.mock.calls[0][0],"http://localhost");
    expect(url.searchParams.getAll("topic_ids")).toEqual(topicIDs);
    expect(url.searchParams.get("topic_limit")).toBe("0");
    expect(url.searchParams.getAll("known_evidence_ids")).toEqual(knownEvidenceIDs);
    expect(url.searchParams.get("known_at")).toBe("2026-11-01T00:00:00Z");
    expect(url.searchParams.get("valid_at")).toBe("2026-10-01T00:00:00Z");
    expect(url.searchParams.has("relationship_limit")).toBe(false);
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
