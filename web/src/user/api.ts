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

export type UserCredential = {
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
  memory_binding: "shared_only" | "profile_private" | "credential_private" | string;
  memory_space_kind: "team_shared" | "profile_private" | "credential_private" | string;
};

export type UserSession = {
  team: UserTeam;
  membership: UserMembership;
  credential: UserCredential | null;
  teams: UserTeamOption[];
  personal_credentials: UserCredential[];
  mcp_public_base_url: string;
};

export type UserTeamOption = {
  team: UserTeam;
  membership: UserMembership;
};

export type UserMembership = {
  team_id: string;
  name: string;
  grants: string[];
  role: "manager" | "member";
};

export type SSOProvider = {
  id: string;
  name: string;
  kind: string;
};

export type RotateResponse = {
  api_key: string;
  credential: UserCredential;
};

export type PrivateMemoryOperation = {
  operation_id: string;
  space_kind?: "profile_private" | "credential_private" | string;
  action: "erase_profile_private" | "erase_credential_private" | "retire_credential" | "retention_purge" | string;
  actor_class: string;
  reason_code: string;
  retire_space: boolean;
  status: "queued" | "processing" | "completed" | "failed" | string;
  deleted_counts: Record<string, number>;
  attempt_count?: number;
  next_attempt_at?: string;
  last_error_code?: string;
  requested_at: string;
  started_at?: string;
  completed_at?: string;
  updated_at: string;
};

export type CreatedCredential = RotateResponse;

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

export type CreateCredentialInput = {
  name: string;
  scopes?: string[];
  rate_limit: number;
  expires_at?: string;
  memory_binding?: "shared_only" | "profile_private" | "credential_private";
};

export type UpdateCredentialInput =
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
  signal?: AbortSignal;
  cache?: RequestCache;
  idempotencyKey?: string;
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

  async switchSSOTeam(teamId: string): Promise<UserSession> {
    const payload = await this.request<Envelope<UserSession>>("/ui/api/sso/team", { method: "POST", body: { team_id: teamId } });
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

  async createSSOCredential(input: CreateCredentialInput): Promise<CreatedCredential> {
    const payload = await this.request<Envelope<CreatedCredential>>("/ui/api/sso/credentials", { method: "POST", body: input });
    return payload.data;
  }

  async listSSOCredentials(): Promise<UserCredential[]> {
    const payload = await this.request<Envelope<UserCredential[]>>("/ui/api/sso/credentials");
    return payload.data;
  }

  async getSSOCredential(credentialId: string): Promise<UserCredential> {
    const payload = await this.request<Envelope<UserCredential>>(`/ui/api/sso/credentials/${credentialId}`);
    return payload.data;
  }

  async rotateSSOCredential(credentialId: string): Promise<RotateResponse> {
    const payload = await this.request<Envelope<RotateResponse>>(`/ui/api/sso/credentials/${credentialId}/rotate`, { method: "POST", body: {} });
    return payload.data;
  }

  async deleteSSOCredential(credentialId: string, idempotencyKey: string): Promise<PrivateMemoryOperation> {
    const payload = await this.request<Envelope<PrivateMemoryOperation>>(`/ui/api/sso/credentials/${credentialId}`, {
      method: "DELETE",
      body: { acknowledge_irreversible: true },
      idempotencyKey,
    });
    return payload.data;
  }

  async eraseSSOPrivateMemory(idempotencyKey: string): Promise<PrivateMemoryOperation> {
    const payload = await this.request<Envelope<PrivateMemoryOperation>>("/ui/api/sso/private-memory", {
      method: "DELETE",
      body: { acknowledge_irreversible: true },
      idempotencyKey,
    });
    return payload.data;
  }

  async eraseCredentialPrivateMemory(idempotencyKey: string): Promise<PrivateMemoryOperation> {
    const payload = await this.request<Envelope<PrivateMemoryOperation>>("/ui/api/credential/private-memory", {
      method: "DELETE",
      body: { acknowledge_irreversible: true },
      idempotencyKey,
    });
    return payload.data;
  }

  async getPrivateMemoryErasure(operationId: string, signal?: AbortSignal): Promise<PrivateMemoryOperation> {
    const payload = await this.request<Envelope<PrivateMemoryOperation>>(`/ui/api/private-memory/erasures/${encodeURIComponent(operationId)}`, { signal });
    return payload.data;
  }

  async rotateCredential(): Promise<RotateResponse> {
    const payload = await this.request<Envelope<RotateResponse>>("/ui/api/credential/rotate", { method: "POST", body: {} });
    return payload.data;
  }

  async updateTeam(input: UpdateTeamInput): Promise<UserTeam> {
    const payload = await this.request<Envelope<UserTeam>>("/ui/api/team", { method: "PATCH", body: input });
    return payload.data;
  }

  listTeamCredentials(): Promise<Page<UserCredential>> {
    return this.request<Page<UserCredential>>("/ui/api/team/credentials");
  }

  async createTeamCredential(input: CreateCredentialInput): Promise<CreatedCredential> {
    const payload = await this.request<Envelope<CreatedCredential>>("/ui/api/team/credentials", { method: "POST", body: input });
    return payload.data;
  }

  async updateTeamCredential(credentialId: string, input: UpdateCredentialInput): Promise<UserCredential> {
    const payload = await this.request<Envelope<UserCredential>>(`/ui/api/team/credentials/${credentialId}`, { method: "PATCH", body: input });
    return payload.data;
  }

  async rotateTeamCredential(credentialId: string, input: CreateCredentialInput): Promise<CreatedCredential> {
    const payload = await this.request<Envelope<CreatedCredential>>(`/ui/api/team/credentials/${credentialId}/rotate`, { method: "POST", body: input });
    return payload.data;
  }

  async deleteTeamCredential(credentialId: string): Promise<{ status: string }> {
    const payload = await this.request<Envelope<{ status: string }>>(`/ui/api/team/credentials/${credentialId}`, { method: "DELETE" });
    return payload.data;
  }

  async telemetry(query: UserTelemetryQuery = {}, signal?: AbortSignal): Promise<TelemetrySnapshot> {
    const params = new URLSearchParams();
    if (query.window) {
      params.set("window", query.window);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    const payload = await this.request<Envelope<TelemetrySnapshot>>(`/ui/api/telemetry${suffix}`, { signal });
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
        tier: relationship.tier,
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
    const payload = await this.request<Envelope<GraphSnapshot>>(`/ui/api/graph${suffix}`, { cache: "no-store" });
    return payload.data;
  }

  async nodeDetail(type: string, id: string): Promise<GraphNode> {
    const params = new URLSearchParams({ type, id });
    const payload = await this.request<Envelope<GraphNode>>(`/ui/api/node-detail?${params.toString()}`, { cache: "no-store" });
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
      cache: options.cache,
      signal: options.signal,
      idempotencyKey: options.idempotencyKey,
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
