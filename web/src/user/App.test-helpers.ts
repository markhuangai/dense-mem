import { vi } from "vitest";
import { RecallHit, RecallPayload, UserCredential, UserSession } from "./api";

export const baseSession: UserSession = {
  mcp_public_base_url: "https://memory.example.test",
  team: {
    id: "11111111-1111-4111-8111-111111111111",
    name: "Research Team",
    description: "",
    created_at: "2026-05-01T12:00:00Z",
    updated_at: "2026-05-01T12:00:00Z",
  },
  membership: {
    team_id: "11111111-1111-4111-8111-111111111111",
    name: "Mine",
    grants: ["read"],
    role: "member",
  },
  credential: {
    id: "22222222-2222-4222-8222-222222222222",
    team_id: "11111111-1111-4111-8111-111111111111",
    name: "Mine",
    key_suffix: "abc123",
    scopes: ["read"],
    role: "member",
    rate_limit: 120,
    last_used_at: null,
    expires_at: null,
    created_at: "2026-05-01T12:00:00Z",
    memory_binding: "shared_only",
    memory_space_kind: "team_shared",
  },
  teams: [],
  personal_credentials: [],
};

export const memberCredential: UserCredential = {
  id: "33333333-3333-4333-8333-333333333333",
  team_id: baseSession.team.id,
  name: "Reader credential",
  key_suffix: "def456",
  scopes: ["read"],
  role: "member",
  rate_limit: 120,
  last_used_at: null,
  expires_at: null,
  created_at: "2026-05-01T12:00:00Z",
  memory_binding: "shared_only",
  memory_space_kind: "team_shared",
};

export function optionValue<T>(value: T | T[], index: number): T {
  return Array.isArray(value) ? value[Math.min(index, value.length - 1)] : value;
}

export function isRecallSequence(value: RecallHit[] | RecallHit[][]): value is RecallHit[][] {
  return Array.isArray(value[0]);
}

export function authorizationHeader(init?: RequestInit): string {
  const headers = init?.headers;
  if (!headers) {
    return "";
  }
  if (headers instanceof Headers) {
    return headers.get("Authorization") ?? "";
  }
  if (Array.isArray(headers)) {
    return headers.find(([name]) => name.toLowerCase() === "authorization")?.[1] ?? "";
  }
  return (headers as Record<string, string>).Authorization ?? (headers as Record<string, string>).authorization ?? "";
}

export function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export function recallPayloadForHits(hits: RecallHit[]): RecallPayload {
  return {
    recall_id: "recall-test",
    results: hits.map((hit, index) => {
      const evidence = evidenceForHit(hit);
      return {
        ...evidence,
        relationship_ids: relationshipsForHit(hit).map((relationship) => relationship.relationship_id),
        rank: hit.semantic_rank ?? index + 1,
      };
    }),
    conflicts: [],
    related_communities: [],
    discovery_paths: hits.map((hit) => {
      const evidenceID = evidenceForHit(hit).evidence_id;
      return {
        community_id: `community-${evidenceID}`,
        logical_community_id: `logical-community-${evidenceID}`,
        rank: 1,
        summary: "test community",
        top_entities: [],
        top_predicates: [],
        entity_count: 0,
        relationship_count: relationshipsForHit(hit).length,
        relationships: relationshipsForHit(hit).map((relationship) => ({
          ...relationship,
          evidence_ids: relationship.evidence_ids?.length ? relationship.evidence_ids : [evidenceID],
        })),
        relationships_truncated: false,
      };
    }),
    related_relationships: [],
    related_hypotheses: [],
    search_states: { evidence: "current", relationships: "current" },
    degradations: [],
  };
}

export function ssoSessions() {
  const secondTeam = {
    ...baseSession.team,
    id: "55555555-5555-4555-8555-555555555555",
    name: "Analytics Team",
  };
  const firstMembership = { ...baseSession.membership };
  const secondMembership = {
    ...baseSession.membership,
    team_id: secondTeam.id,
    name: "Analytics SSO",
    grants: ["read", "write"],
    role: "manager" as const,
  };
  const initial: UserSession = {
    ...baseSession,
    credential: null,
    membership: firstMembership,
    personal_credentials: [],
    teams: [
      { team: baseSession.team, membership: firstMembership },
      { team: secondTeam, membership: secondMembership },
    ],
  };
  const switched: UserSession = {
    ...initial,
    team: secondTeam,
    membership: secondMembership,
  };
  return { initial, switched, secondTeam, secondMembership };
}

export function mockSSOUserFetch(initial: UserSession, switched: UserSession, options: { logoutStatus?: number } = {}) {
  let current = initial;
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url === "/ui/api/sso/providers" && method === "GET") {
      return jsonResponse({ data: [] });
    }
    if (url === "/ui/api/session" && method === "GET") {
      return jsonResponse({ data: current });
    }
    if (url === "/ui/api/sso/team" && method === "POST") {
      current = switched;
      return jsonResponse({ data: current });
    }
    if (url.startsWith("/ui/api/telemetry") && method === "GET") {
      return jsonResponse({ data: telemetryForSession(current) });
    }
    if (url === "/ui/api/sso/credentials" && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const createdID = current.personal_credentials.length === 0
        ? "77777777-7777-4777-8777-777777777777"
        : "88888888-8888-4888-8888-888888888888";
      const created: UserCredential = {
        ...memberCredential,
        team_id: current.team.id,
        id: createdID,
        name: body.name,
        key_suffix: "own123",
        scopes: body.scopes,
        role: "member",
        memory_binding: body.memory_binding ?? "profile_private",
        memory_space_kind: body.memory_binding === "credential_private" ? "credential_private" : body.memory_binding === "shared_only" ? "team_shared" : "profile_private",
      };
      current = {
        ...current,
        personal_credentials: [...current.personal_credentials, created],
      };
      return jsonResponse({ data: { api_key: "dm_sso_personal_plaintext", credential: created } }, 201);
    }
    if (url.includes("/ui/api/sso/credentials/") && url.endsWith("/rotate") && method === "POST") {
      const credentialID = url.split("/").at(-2);
      const rotated = { ...(current.personal_credentials.find((credential) => credential.id === credentialID) ?? memberCredential), team_id: current.team.id, key_suffix: "rot321" };
      current = { ...current, personal_credentials: current.personal_credentials.map((credential) => credential.id === rotated.id ? rotated : credential) };
      return jsonResponse({ data: { api_key: "dm_sso_personal_rotated", credential: rotated } });
    }
    if (url.includes("/ui/api/sso/credentials/") && method === "DELETE") {
      const credentialID = url.split("/").at(-1);
      current = { ...current, personal_credentials: current.personal_credentials.filter((credential) => credential.id !== credentialID) };
      const now = "2026-08-18T00:00:00Z";
      return jsonResponse({ data: {
        operation_id: "99999999-9999-4999-8999-999999999999",
        action: "retire_credential",
        actor_class: "owner_sso",
        reason_code: "credential_deleted",
        retire_space: true,
        status: "completed",
        deleted_counts: {},
        requested_at: now,
        completed_at: now,
        updated_at: now,
      } }, 202);
    }
    if (url === "/ui/api/sso/private-memory" && method === "DELETE") {
      const now = "2026-08-18T00:00:00Z";
      return jsonResponse({ data: {
        operation_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        action: "erase_profile_private",
        actor_class: "owner_sso",
        reason_code: "owner_request",
        retire_space: false,
        status: "queued",
        deleted_counts: {},
        requested_at: now,
        updated_at: now,
      } }, 202);
    }
    if (url === "/ui/api/private-memory/erasures/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" && method === "GET") {
      const now = "2026-08-18T00:00:01Z";
      return jsonResponse({ data: {
        operation_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        action: "erase_profile_private",
        actor_class: "owner_sso",
        reason_code: "owner_request",
        retire_space: false,
        status: "completed",
        deleted_counts: { knowledge_ingests: 1 },
        requested_at: "2026-08-18T00:00:00Z",
        completed_at: now,
        updated_at: now,
      } });
    }
    if (url === "/ui/api/sso/logout" && method === "POST") {
      if (options.logoutStatus) {
        return jsonResponse({ message: "logout failed" }, options.logoutStatus);
      }
      return jsonResponse({ data: { status: "signed_out" } });
    }
    if (url.startsWith("/ui/api/recall")) {
      return jsonResponse({ data: [] });
    }
    return jsonResponse({ message: "not found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

export function telemetryForSession(session: UserSession) {
  const teamScope = session.membership.role === "manager";
  return {
    available: true,
    window: {
      key: "1h",
      from: "2026-05-02T12:00:00Z",
      to: "2026-05-02T13:00:00Z",
      step_seconds: 60,
      retention_days: 30,
    },
    scope: teamScope
      ? { type: "team", team_id: session.team.id }
      : { type: "self", team_id: session.team.id, profile_id: session.credential?.id ?? "sso-owner" },
    cards: [{ id: "http_requests", label: "HTTP requests", unit: "requests", value: teamScope ? 9 : 4 }],
    series: [],
  };
}

function evidenceForHit(hit: RecallHit) {
	if (!hit.evidence) {
		throw new Error("test recall hit must include evidence");
	}
	return hit.evidence;
}

function relationshipsForHit(hit: RecallHit) {
  if (hit.relationships?.length) {
    return hit.relationships;
  }
  if (hit.relationship) {
    return [hit.relationship];
  }
	return [];
}
