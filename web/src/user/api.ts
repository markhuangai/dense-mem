import type { TelemetrySnapshot, UserTelemetryQuery } from "../telemetry/types";
import { ApiError, requestJson } from "../http";
export { ApiError } from "../http";

export type UserTeam = {
  id: string;
  name: string;
  description: string;
  config?: Record<string, unknown> | null;
  dreaming_effective?: DreamingEffectiveConfig | null;
  created_at: string;
  updated_at: string;
};

export type UserKey = {
  id: string;
  team_id: string;
  name: string;
  key_suffix: string;
  scopes: string[];
  role: "manager" | "member";
  rate_limit: number;
  last_used_at: string | null;
  expires_at: string | null;
  created_at: string;
};

export type UserSession = {
  team: UserTeam;
  key: UserKey;
  teams?: UserTeamOption[];
  auth_method?: "api_key" | "api_key_session" | "sso";
  can_rotate: boolean;
  can_manage_team: boolean;
  personal_key: UserKey | null;
  can_create_personal_key: boolean;
  can_rotate_personal_key: boolean;
  personal_key_max_scopes?: string[];
};

export type UserTeamOption = {
  team: UserTeam;
  key: UserKey;
  can_rotate: boolean;
  can_manage_team: boolean;
};

export type SSOProvider = {
  id: string;
  name: string;
  kind: string;
};

export type RotateResponse = {
  api_key: string;
  key: UserKey;
};

export type CreatedTeamProfile = RotateResponse;

export type Page<T> = {
  data: T[];
  pagination: {
    limit: number;
    offset: number;
    total: number;
  };
};

export type UpdateTeamInput = {
  name: string;
  description: string;
  config?: Record<string, unknown>;
};

export type CreateTeamProfileInput = {
  name: string;
  scopes?: string[];
  rate_limit: number;
  expires_at?: string;
};

export type UpdateTeamProfileInput =
  | { name: string; scopes?: never }
  | { name?: never; scopes: string[] };

export type ListResponse<T> = {
  items: T[];
  next_cursor?: string;
  has_more?: boolean;
  total?: number;
};

export type DreamingRuntimeConfig = {
  enabled: boolean;
  force_enabled: boolean;
  start_time_local: string;
  timezone: string;
  max_outputs: number;
};

export type DreamingEffectiveConfig = DreamingRuntimeConfig & {
  team_enabled: boolean;
  source: "global" | "team" | "global_force" | string;
};

export type Dream = {
  dream_id: string;
  team_id: string;
  hypothesis: string;
  what_if: string;
  possible_outcome: string;
  rationale: string;
  likelihood: number;
  confidence: number;
  status: "proposed" | "reinforced" | "stale" | "rejected" | "submitted" | string;
  cycle_run_id?: string;
  generator_model?: string;
  source_refs?: Array<{ type: string; id: string }>;
  derivations?: Array<{
    premise_position: number;
    relationship_id: string;
    relationship_version: number;
    source_group_key: string;
    quote: string;
    authority: string;
  }>;
  invalidated_reason?: string;
  created_at: string;
  updated_at: string;
};

export type DreamSort = "updated_at" | "created_at";
export type DreamDirection = "asc" | "desc";

export type DreamQuery = {
  limit?: number;
  status?: Dream["status"] | "";
  cursor?: string;
  sort?: DreamSort;
  direction?: DreamDirection;
};

export type DreamRun = {
  run_id: string;
  team_id: string;
  run_date: string;
  started_at: string;
  completed_at: string;
  input_relationships: number;
  created_dreams: number;
  rejected_dreams: number;
  scheduled_for?: string;
  attempt_count?: number;
  provider_model?: string;
  provider_turns?: number;
  provider_input_tokens?: number;
  provider_output_tokens?: number;
  attempted_paths?: number;
  provider_proposals?: number;
  outcome_summary?: Record<string, number>;
  status: string;
  error?: string;
};

export type DreamStatus = {
  effective_config: DreamingEffectiveConfig;
  latest_run?: DreamRun | null;
  pending_count: number;
};

export type RecallCommunity = {
  community_id: string;
  logical_community_id: string;
  rank: number;
  summary: string;
  top_entities: RecallEntity[];
  top_predicates: string[];
  entity_count: number;
  relationship_count: number;
  relationships: RecallRelationship[];
  relationships_truncated: boolean;
};

export type RecallHit = {
  tier?: string;
  score?: number;
  evidence?: RecallEvidenceContext;
  relationship?: RecallRelationship;
  relationships?: RecallRelationship[];
  evidences?: RecallEvidenceContext[];
  semantic_rank?: number;
  keyword_rank?: number;
  final_score?: number;
  discovery_paths?: RecallDiscoveryPath[];
  related_hypotheses?: unknown[];
  community?: RecallCommunity;
};

export type RecallEntity = {
  entity_id?: string;
  name?: string;
  kind?: string;
};

export type RecallObject = {
  entity_id?: string;
  value_id?: string;
  name?: string;
  kind?: string;
  value?: string;
  type?: string;
};

export type RecallRelationship = {
  relationship_id: string;
  tier?: string;
  equivalent_relationship_ids?: string[];
  subject?: RecallEntity;
  predicate?: string;
  object?: RecallObject;
  polarity?: string;
  valid_from?: string;
  valid_to?: string;
  evidence_ids?: string[];
  search_state?: string;
  corroborating_relationship_ids?: string[];
  conflicting_relationship_ids?: string[];
};

export type RecallEvidenceContext = {
  evidence_id: string;
  relationship_ids?: string[];
  rank?: number;
  context: string;
  source?: string;
  source_type?: string;
  created_at?: string;
};

export type RecallDiscoveryPath = RecallCommunity;

export type RecallPayload = {
  recall_id?: string;
  results: RecallEvidenceContext[];
  conflicts?: unknown[];
  related_relationships?: RecallRelationship[];
  related_communities?: RecallCommunity[];
  discovery_paths?: RecallDiscoveryPath[];
  discovery_guidance?: string;
  related_hypotheses?: unknown[];
  search_states?: {
    evidence?: string;
    relationships?: string;
  };
  degradations?: unknown[];
};

export type GraphNodeType = "entity" | "value";

export type GraphNode = {
  key: string;
  id: string;
  type: GraphNodeType | string;
  title: string;
  body?: string;
  status?: string;
  community_id?: string;
  source?: string;
  score?: number;
  recorded_at?: string;
};

export type GraphEdge = {
  id: string;
  source: string;
  target: string;
  relationship: string;
  directed: boolean;
};

export type GraphSnapshot = {
  scope: "overview" | "local" | string;
  query?: string;
  anchor?: { type: GraphNodeType | string; id: string; key: string };
  depth: number;
  limit: number;
  truncated: boolean;
  nodes: GraphNode[];
  edges: GraphEdge[];
};

export type GraphQuery = {
  scope?: "overview" | "local";
  q?: string;
  types?: GraphNodeType[];
  anchorType?: GraphNodeType;
  anchorId?: string;
  depth?: number;
  limit?: number;
};

type RequestOptions = {
  method?: string;
  body?: unknown;
};

export type UserAuthMode = "anonymous" | "api_key" | "api_key_session" | "sso";

type Envelope<T> = {
  data: T;
};

export class UserApi {
  private readonly token: string;
  private readonly authMode: UserAuthMode;

  constructor(token: string, authMode: UserAuthMode = token ? "api_key" : "anonymous") {
    this.token = token;
    this.authMode = authMode;
  }

  async session(): Promise<UserSession> {
    const payload = await this.request<Envelope<UserSession>>("/ui/api/session");
    return payload.data;
  }

  async ssoProviders(): Promise<SSOProvider[]> {
    const payload = await this.request<Envelope<SSOProvider[]>>("/ui/api/sso/providers");
    return payload.data;
  }

  ssoStartUrl(providerId: string): string {
    return `/ui/api/sso/start/${encodeURIComponent(providerId)}`;
  }

  async switchSSOTeam(profileId: string): Promise<UserSession> {
    const payload = await this.request<Envelope<UserSession>>("/ui/api/sso/team", { method: "POST", body: { profile_id: profileId } });
    return payload.data;
  }

  async logoutSSO(): Promise<{ status: string }> {
    const payload = await this.request<Envelope<{ status: string }>>("/ui/api/sso/logout", { method: "POST" });
    return payload.data;
  }

  async createPortalSession(remember: boolean): Promise<{ status: string }> {
    const payload = await this.request<Envelope<{ status: string }>>("/ui/api/session", {
      method: "POST",
      body: { remember },
    });
    return payload.data;
  }

  async logoutPortalSession(): Promise<{ status: string }> {
    const payload = await this.request<Envelope<{ status: string }>>("/ui/api/session/logout", { method: "POST" });
    return payload.data;
  }

  async createSSOKey(input: CreateTeamProfileInput): Promise<CreatedTeamProfile> {
    const payload = await this.request<Envelope<CreatedTeamProfile>>("/ui/api/sso/key", { method: "POST", body: input });
    return payload.data;
  }

  async rotateSSOKey(): Promise<RotateResponse> {
    const payload = await this.request<Envelope<RotateResponse>>("/ui/api/sso/key/rotate", { method: "POST", body: {} });
    return payload.data;
  }

  async rotateKey(): Promise<RotateResponse> {
    const payload = await this.request<Envelope<RotateResponse>>("/ui/api/key/rotate", { method: "POST", body: {} });
    return payload.data;
  }

  async updateTeam(input: UpdateTeamInput): Promise<UserTeam> {
    const payload = await this.request<Envelope<UserTeam>>("/ui/api/team", { method: "PATCH", body: input });
    return payload.data;
  }

  listTeamProfiles(): Promise<Page<UserKey>> {
    return this.request<Page<UserKey>>("/ui/api/team/profiles");
  }

  async createTeamProfile(input: CreateTeamProfileInput): Promise<CreatedTeamProfile> {
    const payload = await this.request<Envelope<CreatedTeamProfile>>("/ui/api/team/profiles", { method: "POST", body: input });
    return payload.data;
  }

  async updateTeamProfile(profileId: string, input: UpdateTeamProfileInput): Promise<UserKey> {
    const payload = await this.request<Envelope<UserKey>>(`/ui/api/team/profiles/${profileId}`, { method: "PATCH", body: input });
    return payload.data;
  }

  async rotateTeamProfile(profileId: string, input: CreateTeamProfileInput): Promise<CreatedTeamProfile> {
    const payload = await this.request<Envelope<CreatedTeamProfile>>(`/ui/api/team/profiles/${profileId}/rotate`, { method: "POST", body: input });
    return payload.data;
  }

  async deleteTeamProfile(profileId: string): Promise<{ status: string }> {
    const payload = await this.request<Envelope<{ status: string }>>(`/ui/api/team/profiles/${profileId}`, { method: "DELETE" });
    return payload.data;
  }

  async telemetry(query: UserTelemetryQuery = {}): Promise<TelemetrySnapshot> {
    const params = new URLSearchParams();
    if (query.window) {
      params.set("window", query.window);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    const payload = await this.request<Envelope<TelemetrySnapshot>>(`/ui/api/telemetry${suffix}`);
    return payload.data;
  }

  async recall(query: string, limit = 10): Promise<RecallHit[]> {
    const params = new URLSearchParams({ query, limit: String(limit) });
    const payload = await this.request<Envelope<unknown>>(`/ui/api/recall?${params.toString()}`);
    const data = payload.data;
    if (isRecallPayload(data)) {
      const communityPaths = data.related_communities?.length ? data.related_communities : data.discovery_paths ?? [];
      const communityHits = (data.related_communities ?? []).filter((community): community is RecallCommunity => Boolean(community.community_id)).map((community, index) => ({
        community,
        relationships: community.relationships ?? [],
        discovery_paths: communityPaths,
        related_hypotheses: data.related_hypotheses ?? [],
        semantic_rank: community.rank || index + 1,
        final_score: community.rank ? 1 / community.rank : undefined,
      }));
      const relationshipHits = (data.related_relationships ?? []).map((relationship, index) => ({
        relationship,
        relationships: [relationship],
        evidences: [],
        discovery_paths: communityPaths,
        related_hypotheses: data.related_hypotheses ?? [],
        semantic_rank: index + 1,
        final_score: relationship.search_state === "current" ? 1 : undefined,
      }));
      const evidenceHits = data.results.map((evidence) => ({
        evidence,
        evidences: [evidence],
        relationships: relationshipsForEvidence(communityPaths, evidence.evidence_id),
        discovery_paths: communityPaths,
        related_hypotheses: data.related_hypotheses ?? [],
        final_score: evidence.rank ? 1 / evidence.rank : undefined,
        semantic_rank: evidence.rank,
      }));
      return [...communityHits, ...evidenceHits, ...relationshipHits];
    }
    throw new ApiError(500, "Unexpected recall response format");
  }

  async graph(query: GraphQuery = {}): Promise<GraphSnapshot> {
    const params = new URLSearchParams();
    if (query.scope) {
      params.set("scope", query.scope);
    }
    if (query.q) {
      params.set("q", query.q);
    }
    if (query.types?.length) {
      params.set("types", query.types.join(","));
    }
    if (query.anchorType) {
      params.set("anchor_type", query.anchorType);
    }
    if (query.anchorId) {
      params.set("anchor_id", query.anchorId);
    }
    if (query.depth !== undefined) {
      params.set("depth", String(query.depth));
    }
    if (query.limit !== undefined) {
      params.set("limit", String(query.limit));
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    const payload = await this.request<Envelope<GraphSnapshot>>(`/ui/api/graph${suffix}`);
    return payload.data;
  }

  async nodeDetail(type: string, id: string): Promise<GraphNode> {
    const params = new URLSearchParams({ type, id });
    const payload = await this.request<Envelope<GraphNode>>(`/ui/api/node-detail?${params.toString()}`);
    return payload.data;
  }

  async dreamingStatus(): Promise<DreamStatus> {
    const payload = await this.request<Envelope<DreamStatus>>("/ui/api/dreaming/status");
    return payload.data;
  }

  async listDreamingRuns(limit = 20): Promise<DreamRun[]> {
    const payload = await this.request<Envelope<DreamRun[]>>(`/ui/api/dreaming/runs?limit=${limit}`);
    return payload.data;
  }

  async listDreams(query: DreamQuery = {}): Promise<ListResponse<Dream>> {
    const params = new URLSearchParams({ limit: String(query.limit ?? 20) });
    if (query.status) {
      params.set("status", query.status);
    }
    if (query.cursor) {
      params.set("cursor", query.cursor);
    }
    if (query.sort) {
      params.set("sort", query.sort);
    }
    if (query.direction) {
      params.set("direction", query.direction);
    }
    const payload = await this.request<Envelope<ListResponse<Dream>>>(`/ui/api/dreams?${params.toString()}`);
    return payload.data;
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const method = options.method ?? "GET";
    return requestJson<T>(path, {
      method,
      token: this.token || undefined,
      credentials: this.token ? "same-origin" : "include",
      body: options.body,
      csrf: this.token ? undefined : {
        cookieName: this.authMode === "api_key_session" ? "dense_mem_ui_csrf" : "dense_mem_sso_csrf",
        headerName: "X-Dense-Mem-CSRF",
      },
    });
  }
}

function isRecallPayload(value: RecallPayload | unknown): value is RecallPayload {
  if (!value || typeof value !== "object") {
    return false;
  }
  const payload = value as Partial<RecallPayload> & { evidences?: unknown };
  return Array.isArray(payload.results) && !Array.isArray(payload.evidences);
}

function relationshipsForEvidence(paths: RecallDiscoveryPath[], evidenceID: string): RecallRelationship[] {
  const seen = new Set<string>();
  const out: RecallRelationship[] = [];
  for (const path of paths) {
    for (const relationship of path.relationships) {
      if (path.community_id && !relationship.evidence_ids?.length) {
        continue;
      }
      if (relationship.evidence_ids?.length && !relationship.evidence_ids.includes(evidenceID)) {
        continue;
      }
      if (!relationship.relationship_id || seen.has(relationship.relationship_id)) {
        continue;
      }
      seen.add(relationship.relationship_id);
      out.push(relationship);
    }
  }
  return out;
}
