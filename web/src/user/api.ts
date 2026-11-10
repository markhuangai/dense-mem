import type { MemoryAccess } from "../credentialPermissions";
import { ApiError, requestJson } from "../http";
import type { OntologyCoverage, OntologyDraft, OntologyDraftCommand, OntologyDraftPage, OntologyKind, OntologyPreview, OntologyPublication, OntologyPublicationPage, OntologyRecord, OntologyRecordPage, OntologySourcePage, SaveOntologyDraft, SourceKind } from "./ontology-types";
export { ApiError } from "../http";

export type UserTeam = {
  id: string;
  name: string;
  description: string;
  config?: Record<string, unknown> | null;
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
  memory_access: MemoryAccess;
};

export type UserSession = {
  memory_access: MemoryAccess;
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

export type RecallEvidenceContext = {
  evidence_id: string;
  rank: number;
  context: string;
  source?: string;
  source_type?: string;
  created_at?: string;
  space_kind?: "private" | "team_shared";
  equivalent_evidence_ids: string[];
  equivalents_truncated: boolean;
};

export type RelatedTopic = {
  topic_id: string;
  name: string;
  description: string;
  eligible_evidence_count: number;
  rank: number;
};

export type RecallPayload = {
  recall_id: string;
  results: RecallEvidenceContext[];
  related_topics: RelatedTopic[];
  topics_truncated: boolean;
  conflicts: Array<{ conflict_id: string; status: string }>;
  search_states: { evidence: string };
  degradations: Array<{ code: string; message: string }>;
};

export type RecallOptions = {
  topicIDs?: string[];
  topicLimit?: number;
  knownEvidenceIDs?: string[];
  knownAt?: string;
  validAt?: string;
};

type RequestOptions = {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  cache?: RequestCache;
};

export type UserAuthMode = "anonymous" | "api_key" | "api_key_session" | "sso";

type Envelope<T> = {
  data: T;
};

export class UserApi {
  private readonly token: string;
  private readonly authMode: UserAuthMode;
  private readonly pendingCredentialRevocations = new Map<string, string>();

  constructor(token: string, authMode: UserAuthMode = token ? "api_key" : "anonymous") {
    this.token = token;
    this.authMode = authMode;
  }

  async ontologyRecords(kind: OntologyKind | "" = "", cursor = ""): Promise<OntologyRecordPage> {
    return (await this.request<Envelope<OntologyRecordPage>>(`/ui/api/ontology/records?${new URLSearchParams({ kind, cursor, limit: "50" })}`)).data;
  }
  async ontologyRecord(id: string, version = 0): Promise<OntologyRecord> {
    return (await this.request<Envelope<OntologyRecord>>(`/ui/api/ontology/records/${encodeURIComponent(id)}?version=${version}`)).data;
  }
  async ontologySources(kind: SourceKind = "evidence", cursor = "", query = ""): Promise<OntologySourcePage> {
    return (await this.request<Envelope<OntologySourcePage>>(`/ui/api/ontology/sources?${new URLSearchParams({ kind, cursor, q: query, limit: "50" })}`)).data;
  }
  async ontologyStatus(): Promise<OntologyCoverage> { return (await this.request<Envelope<OntologyCoverage>>("/ui/api/ontology/status")).data; }
  async ontologyDrafts(cursor = ""): Promise<OntologyDraftPage> { return (await this.request<Envelope<OntologyDraftPage>>(`/ui/api/ontology/drafts?${new URLSearchParams({ cursor, limit: "50" })}`)).data; }
  async ontologyDraft(id: string): Promise<OntologyDraft> { return (await this.request<Envelope<OntologyDraft>>(`/ui/api/ontology/drafts/${encodeURIComponent(id)}`)).data; }
  async saveOntologyDraft(id: string, input: SaveOntologyDraft): Promise<OntologyDraft> {
    return (await this.request<Envelope<OntologyDraft>>(`/ui/api/ontology/drafts${id ? `/${encodeURIComponent(id)}` : ""}`, { method: id ? "PUT" : "POST", body: input })).data;
  }
  async previewOntologyDraft(id: string, version: number): Promise<OntologyPreview> {
    return (await this.request<Envelope<OntologyPreview>>(`/ui/api/ontology/drafts/${encodeURIComponent(id)}/preview`, { method: "POST", body: { expected_version: version } })).data;
  }
  async commandOntologyDraft(id: string, action: "publish" | "discard", input: OntologyDraftCommand): Promise<OntologyDraft> {
    return (await this.request<Envelope<OntologyDraft>>(`/ui/api/ontology/drafts/${encodeURIComponent(id)}/${action}`, { method: "POST", body: input })).data;
  }
  async ontologyPublications(after = 0): Promise<OntologyPublicationPage> {
    return (await this.request<Envelope<OntologyPublicationPage>>(`/ui/api/ontology/publications?after=${after}&limit=50`)).data;
  }
  async ontologyPublication(id: string): Promise<OntologyPublication> { return (await this.request<Envelope<OntologyPublication>>(`/ui/api/ontology/publications/${encodeURIComponent(id)}`)).data; }
  async rollbackOntologyPublication(id: string, input: Omit<OntologyDraftCommand, "expected_version">): Promise<OntologyPublication> {
    return (await this.request<Envelope<OntologyPublication>>(`/ui/api/ontology/publications/${encodeURIComponent(id)}/rollback`, { method: "POST", body: input })).data;
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

  async revokeSSOCredential(credentialId: string, idempotencyKey?: string): Promise<unknown> {
    const key = idempotencyKey ?? this.pendingCredentialRevocations.get(credentialId) ?? newIdempotencyKey("sso-credential-delete");
    this.pendingCredentialRevocations.set(credentialId, key);
    const payload = await this.request<Envelope<unknown>>(`/ui/api/sso/credentials/${credentialId}`, {
      method: "DELETE",
      body: { acknowledge_irreversible: true },
      headers: { "Idempotency-Key": key },
    });
    this.pendingCredentialRevocations.delete(credentialId);
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

  async recall(query: string, limit = 10, options: RecallOptions = {}): Promise<RecallPayload> {
    const params = new URLSearchParams({ query, limit: String(limit) });
    if (options.topicIDs) params.set("topic_ids", options.topicIDs.join(","));
    if (options.topicLimit !== undefined) params.set("topic_limit", String(options.topicLimit));
    if (options.knownEvidenceIDs) params.set("known_evidence_ids", options.knownEvidenceIDs.join(","));
    if (options.knownAt) params.set("known_at", options.knownAt);
    if (options.validAt) params.set("valid_at", options.validAt);
    const payload = await this.request<Envelope<unknown>>(`/ui/api/recall?${params.toString()}`);
    if (!isRecallPayload(payload.data)) throw new ApiError(500, "Unexpected recall response format");
    return payload.data;
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const method = options.method ?? "GET";
    return requestJson<T>(path, {
      method,
      token: this.token || undefined,
      credentials: this.token ? "same-origin" : "include",
      body: options.body,
      headers: options.headers,
      cache: options.cache,
      signal: options.signal,
      csrf: this.token ? undefined : {
        cookieName: this.authMode === "api_key_session" ? "dense_mem_ui_csrf" : "dense_mem_sso_csrf",
        headerName: "X-Dense-Mem-CSRF",
      },
    });
  }
}

export function newIdempotencyKey(prefix: string): string {
  const randomUUID = globalThis.crypto?.randomUUID?.();
  return `${prefix}-${randomUUID ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`}`;
}

function isRecallPayload(value: unknown): value is RecallPayload {
  if (!value || typeof value !== "object") return false;
  const payload = value as Partial<RecallPayload>;
  return typeof payload.recall_id === "string" && Array.isArray(payload.results)
    && Array.isArray(payload.related_topics) && typeof payload.topics_truncated === "boolean"
    && Array.isArray(payload.conflicts) && Array.isArray(payload.degradations);
}
