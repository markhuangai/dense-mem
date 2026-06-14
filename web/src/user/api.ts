import type { TelemetrySnapshot, UserTelemetryQuery } from "../telemetry/types";

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
  auth_method?: "api_key" | "sso";
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

export type UpdateTeamProfileInput = {
  name: string;
};

export type Fragment = {
  fragment_id: string;
  id: string;
  content: string;
  source_type: string;
  source?: string;
  labels?: string[];
  status?: string;
  created_at: string;
  updated_at: string;
};

export type Claim = {
  claim_id: string;
  subject: string;
  predicate: string;
  object: string;
  modality: string;
  polarity: string;
  status: string;
  entailment_verdict: string;
  extract_conf: number;
  resolution_conf: number;
  recorded_at: string;
};

export type Fact = {
  fact_id: string;
  subject: string;
  predicate: string;
  object: string;
  status: string;
  truth_score: number;
  recorded_at: string;
  labels?: string[];
};

export type Community = {
  community_id: string;
  level: number;
  summary: string;
  member_count: number;
  top_entities?: string[];
  top_predicates?: string[];
  last_summarized_at: string;
};

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
  reflect_enabled: boolean;
  reevaluate_enabled: boolean;
  dream_enabled: boolean;
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
  status: "proposed" | "reinforced" | "stale" | "rejected" | "promoted" | string;
  cycle: string;
  cycle_run_id?: string;
  generator_model?: string;
  source_refs?: Array<{ type: string; id: string }>;
  invalidated_reason?: string;
  created_at: string;
  updated_at: string;
};

export type DreamRun = {
  run_id: string;
  team_id: string;
  run_date: string;
  started_at: string;
  completed_at: string;
  reflect_ran: boolean;
  reevaluate_ran: boolean;
  dream_ran: boolean;
  stale_facts: number;
  candidate_claims: number;
  disputed_claims: number;
  clarifications: number;
  reevaluated_dreams: number;
  created_dreams: number;
  status: string;
  error?: string;
};

export type DreamStatus = {
  effective_config: DreamingEffectiveConfig;
  latest_run?: DreamRun | null;
  pending_count: number;
};

export type RecallHit = {
  tier?: string;
  score?: number;
  fragment?: Fragment;
  claim?: Claim;
  fact?: Fact;
  semantic_rank: number;
  keyword_rank: number;
  final_score: number;
};

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

type RequestOptions = {
  method?: string;
  body?: unknown;
};

type Envelope<T> = {
  data: T;
};

export class UserApi {
  private readonly token: string;

  constructor(token: string) {
    this.token = token;
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

  async updateTeam(teamId: string, input: UpdateTeamInput): Promise<UserTeam> {
    const payload = await this.request<Envelope<UserTeam>>(`/api/v1/teams/${teamId}`, { method: "PATCH", body: input });
    return payload.data;
  }

  listTeamProfiles(teamId: string): Promise<Page<UserKey>> {
    return this.request<Page<UserKey>>(`/api/v1/teams/${teamId}/profiles`);
  }

  async createTeamProfile(teamId: string, input: CreateTeamProfileInput): Promise<CreatedTeamProfile> {
    const payload = await this.request<Envelope<CreatedTeamProfile>>(`/api/v1/teams/${teamId}/profiles`, { method: "POST", body: input });
    return payload.data;
  }

  async updateTeamProfile(teamId: string, profileId: string, input: UpdateTeamProfileInput): Promise<UserKey> {
    const payload = await this.request<Envelope<UserKey>>(`/api/v1/teams/${teamId}/profiles/${profileId}`, { method: "PATCH", body: input });
    return payload.data;
  }

  async rotateTeamProfile(teamId: string, profileId: string, input: CreateTeamProfileInput): Promise<CreatedTeamProfile> {
    const payload = await this.request<Envelope<CreatedTeamProfile>>(`/api/v1/teams/${teamId}/profiles/${profileId}/rotate`, { method: "POST", body: input });
    return payload.data;
  }

  async deleteTeamProfile(teamId: string, profileId: string): Promise<{ status: string }> {
    const payload = await this.request<Envelope<{ status: string }>>(`/api/v1/teams/${teamId}/profiles/${profileId}`, { method: "DELETE" });
    return payload.data;
  }

  async telemetry(query: UserTelemetryQuery = {}): Promise<TelemetrySnapshot> {
    const params = new URLSearchParams();
    if (query.window) {
      params.set("window", query.window);
    }
    if (query.scope) {
      params.set("scope", query.scope);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    const payload = await this.request<Envelope<TelemetrySnapshot>>(`/ui/api/telemetry${suffix}`);
    return payload.data;
  }

  async recall(query: string, limit = 10): Promise<RecallHit[]> {
    const params = new URLSearchParams({ query, limit: String(limit) });
    const payload = await this.request<Envelope<RecallHit[]>>(`/api/v1/recall?${params.toString()}`);
    return payload.data;
  }

  listFacts(limit = 20): Promise<ListResponse<Fact>> {
    return this.request<ListResponse<Fact>>(`/api/v1/facts?limit=${limit}`);
  }

  listClaims(limit = 20): Promise<ListResponse<Claim>> {
    return this.request<ListResponse<Claim>>(`/api/v1/claims?limit=${limit}`);
  }

  listFragments(limit = 20): Promise<ListResponse<Fragment>> {
    return this.request<ListResponse<Fragment>>(`/api/v1/fragments?limit=${limit}`);
  }

  listCommunities(limit = 20): Promise<ListResponse<Community>> {
    return this.request<ListResponse<Community>>(`/api/v1/communities?limit=${limit}`);
  }

  async dreamingStatus(): Promise<DreamStatus> {
    const payload = await this.request<Envelope<DreamStatus>>("/api/v1/dreaming/status");
    return payload.data;
  }

  async listDreamingRuns(limit = 20): Promise<DreamRun[]> {
    const payload = await this.request<Envelope<DreamRun[]>>(`/api/v1/dreaming/runs?limit=${limit}`);
    return payload.data;
  }

  async listDreams(status = "", limit = 20): Promise<Dream[]> {
    const params = new URLSearchParams({ limit: String(limit) });
    if (status) {
      params.set("status", status);
    }
    const payload = await this.request<Envelope<Dream[]>>(`/api/v1/dreams?${params.toString()}`);
    return payload.data;
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const method = options.method ?? "GET";
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
    };
    if (this.token) {
      headers.Authorization = `Bearer ${this.token}`;
    } else if (method !== "GET" && method !== "HEAD") {
      const csrf = readCookie("dense_mem_sso_csrf");
      if (csrf) {
        headers["X-Dense-Mem-CSRF"] = csrf;
      }
    }
    const response = await fetch(path, {
      method,
      headers,
      credentials: this.token ? "same-origin" : "include",
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    });

    const text = await response.text();
    const payload = text ? JSON.parse(text) : null;

    if (!response.ok) {
      throw new ApiError(response.status, errorMessage(payload, response.statusText));
    }

    return payload as T;
  }
}

function readCookie(name: string): string {
  const prefix = `${name}=`;
  return document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(prefix))
    ?.slice(prefix.length) ?? "";
}

function errorMessage(payload: unknown, fallback: string): string {
  if (payload && typeof payload === "object") {
    const record = payload as Record<string, unknown>;
    if (typeof record.message === "string") {
      return record.message;
    }
    if (typeof record.error === "string") {
      return record.error;
    }
  }
  return fallback || "Request failed.";
}
