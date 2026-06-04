import type { TelemetrySnapshot, UserTelemetryQuery } from "../telemetry/types";

export type UserTeam = {
  id: string;
  name: string;
  description: string;
  created_at: string;
  updated_at: string;
};

export type UserKey = {
  id: string;
  team_id: string;
  name: string;
  key_suffix: string;
  scopes: string[];
  rate_limit: number;
  last_used_at: string | null;
  expires_at: string | null;
  created_at: string;
};

export type UserSession = {
  team: UserTeam;
  key: UserKey;
  can_rotate: boolean;
};

export type RotateResponse = {
  api_key: string;
  key: UserKey;
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

  async rotateKey(): Promise<RotateResponse> {
    const payload = await this.request<Envelope<RotateResponse>>("/ui/api/key/rotate", { method: "POST", body: {} });
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

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const response = await fetch(path, {
      method: options.method ?? "GET",
      headers: {
        Authorization: `Bearer ${this.token}`,
        "Content-Type": "application/json",
      },
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
