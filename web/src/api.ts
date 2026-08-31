import type { ControlTelemetryQuery, TelemetrySnapshot } from "./telemetry/types";
import type { CommunityStatus } from "./community-api-types";
import type { OAuthProtectedResourceConfig } from "./oauth-protected-resource-types";
import type { SearchConvergence } from "./search-convergence-types";
import { requestBytes, requestJson } from "./http";
import {
  buildRememberAttemptDiagnosticPath, buildRememberAttemptDiagnosticsPath, buildRememberFailureArtifactPath,
  buildOperationLogsPath,
  type RememberAttemptDiagnosticDetail, type RememberAttemptDiagnosticQuery, type RememberAttemptDiagnosticSummary,
  type OperationLog, type OperationLogQuery,
} from "./control-observability-api";
export { ApiError } from "./http";
export { listControlIdentityProviders, type ControlIdentityProvider } from "./control-auth-api";
export type {
  OperationLog, OperationLogQuery,
  RememberError,
  RememberAttemptDiagnosticDetail, RememberAttemptDiagnosticEvent, RememberAttemptDiagnosticQuery,
  RememberAttemptDiagnosticSummary, RememberAttemptOutcome, RememberAttemptPublicResult, RememberFailureArtifactDescriptor,
} from "./control-observability-api";
export type {
  ConflictQueueItem,
  ConflictQueueLeaseState,
  ConflictQueuePage,
  ConflictQueuePosition,
  ConflictQueueQuery,
  ConflictQueueStatus,
  ConflictQueueSummary,
  ConflictQueueSupporter,
} from "./conflict-queue-api-types";
import type { ConflictQueuePage, ConflictQueueQuery } from "./conflict-queue-api-types";
export type { OAuthProtectedResourceConfig, OAuthScopeMapping } from "./oauth-protected-resource-types";
export type { SearchConvergence } from "./search-convergence-types";
export type Team = {
  id: string;
  name: string;
  description: string;
  metadata: Record<string, unknown> | null;
  config: Record<string, unknown> | null;
  dreaming_effective?: DreamingEffectiveConfig | null;
  created_at: string;
  updated_at: string;
};

export type Credential = {
  id: string;
  team_id: string;
  name: string;
  key_suffix: string | null;
  scopes: string[] | null;
  role: CredentialRole;
  rate_limit: number;
  last_used_at: string | null;
  expires_at: string | null;
  created_at: string;
};

export type CredentialRole = "manager" | "member";
export type MembershipRole = "manager" | "member";

export type Pagination = {
  limit: number;
  offset: number;
  total: number;
};

export type Page<T> = {
  data: T[];
  pagination: Pagination;
};

export type CreateTeamInput = {
  name: string;
  description: string;
  config?: Record<string, unknown>;
};

export type UpdateTeamInput = CreateTeamInput;

export type CreateCredentialInput = {
  name: string;
  scopes?: string[];
  role?: CredentialRole;
  rate_limit: number;
  expires_at?: string;
};

export type UpdateCredentialInput =
  | { name: string; role?: never; scopes?: never }
  | { name?: never; role: CredentialRole; scopes?: never }
  | { name?: never; role?: never; scopes: string[] };

export type CreatedCredential = {
  api_key: string;
  credential: Credential;
};

export type SecuritySettings = {
  enabled: boolean;
  failure_threshold: number;
  failure_window_seconds: number;
  ban_duration_seconds: number;
  updated_at: string;
};

export type SecurityBan = {
  ip: string;
  reason: string;
  source: "auto" | "manual";
  failure_count: number;
  banned_at: string;
  expires_at: string | null;
  last_failed_at: string | null;
  metadata: Record<string, unknown> | null;
  revoked_at: string | null;
};

export type CreateSecurityBanInput = {
  ip: string;
  reason: string;
  expires_at?: string;
};

export type MetricsTotal = {
  requests: number;
  errors: number;
  avg_latency_ms: number;
  max_latency_ms: number;
};

export type MetricsWindow = {
  from: string;
  to: string;
  bucket_seconds: number;
  retention_days: number;
};

export type MetricsDependency = {
  name: string;
  status: "ok" | "error" | "degraded";
  latency_ms: number | null;
  message?: string; reason_code?: string;
};

export type MetricsTeam = MetricsTotal & {
  team_id: string;
  team_name: string;
};

export type MetricsKey = MetricsTotal & {
  team_id: string;
  team_name: string;
  key_id: string;
  key_name: string;
  key_suffix: string;
};

export type MetricsRoute = MetricsTotal & {
  route: string;
  method: string;
  status_class: string;
};

export type ControlMetrics = {
  window: MetricsWindow; dependencies_checked_at?: string;
  system: MetricsTotal;
  dependencies: MetricsDependency[];
  teams: MetricsTeam[];
  keys: MetricsKey[];
  routes: MetricsRoute[];
};

export type MetricsQuery = {
  window_minutes?: number;
  team_id?: string;
};

export type SSOProvider = {
  id: string;
  name: string;
  kind: "azure_ad" | "pingone" | "generic_oidc";
  issuer_url: string;
  tenant_id: string;
  identity_claim: string;
  client_id: string;
  client_secret_env: string;
  scopes: string[];
  group_claims: string[];
  groups_endpoint: string;
  groups_scopes: string[];
  protected_resource: OAuthProtectedResourceConfig;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type SSOGroupMapping = {
  id: string;
  provider_id: string;
  team_id: string;
  team_name: string;
  group_id: string;
  group_name: string;
  scopes: string[];
  role: MembershipRole;
  enabled: boolean;
  origin: string;
  retired_at: string | null;
  created_at: string;
  updated_at: string;
};

export type SSOProviderInput = {
  name: string;
  kind: SSOProvider["kind"];
  issuer_url: string;
  tenant_id: string;
  identity_claim: string;
  client_id: string;
  client_secret_env: string;
  scopes: string[];
  group_claims: string[];
  groups_endpoint: string;
  groups_scopes: string[];
  protected_resource: OAuthProtectedResourceConfig;
  enabled: boolean;
};

export type SSOGroupMappingInput = {
  team_id: string;
  group_id: string;
  scopes: string[];
  role: MembershipRole;
  enabled: boolean;
};

export type DirectoryRoleEntitlement = {
  role: MembershipRole;
  scopes: string[];
};

export type DirectoryConnector = {
  id: string;
  provider_id: string;
  status: "disabled" | "observe" | "active";
  group_pattern: string;
  role_entitlements: Record<string, DirectoryRoleEntitlement>;
  max_auto_teams: number;
  credential_version: number;
  scim_path: string;
  last_activation_at: string | null;
  created_at: string;
  updated_at: string;
};

export type DirectoryConnectorInput = {
  group_pattern: string;
  role_entitlements: Record<string, DirectoryRoleEntitlement>;
  max_auto_teams: number;
};

export type DirectoryCredential = {
  connector_id: string;
  credential_version: number;
  bearer_token: string;
  oauth_client_id: string;
  oauth_client_secret: string;
};

export type DirectoryConnectorCreateResult = {
  connector: DirectoryConnector;
  credential: DirectoryCredential;
};

export type DirectoryPreview = {
  version: string;
  candidates: Array<{
    group_id: string;
    external_id: string;
    display_name: string;
    team_id: string;
    team_name: string;
    entitlement: DirectoryRoleEntitlement;
    binding_origin: string;
  }>;
  issues: Array<{
    kind: string;
    detail: string;
    active: boolean;
  }>;
};

export type ControlAdminGroup = {
  id: string;
  provider_id: string;
  group_id: string;
  group_name: string;
  enabled: boolean;
  retired_at: string | null;
  created_at: string;
  updated_at: string;
};

export type ControlAdminGroupInput = {
  group_id: string;
  group_name: string;
  enabled: boolean;
};

export type ControlSession = {
  authenticated: boolean;
  auth_method: "token" | "sso";
};

export type SSOConfigItem = {
  key: string;
  value: string;
  effective_value: string;
  validation_error?: string;
  updated_at: string;
};

export type SSOConfig = {
  update_time: string;
  items: SSOConfigItem[];
};

export type GeneralRuntimeConfig = {
  timezone: string;
};

export type GeneralConfigItem = SSOConfigItem;

export type GeneralConfig = {
  update_time: string;
  items: GeneralConfigItem[];
  effective: GeneralRuntimeConfig;
};

export type GeneralConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type SSOConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
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

export type DreamingConfigItem = SSOConfigItem;

export type DreamingConfig = {
  update_time: string;
  items: DreamingConfigItem[];
  effective: DreamingRuntimeConfig;
};

export type DreamingConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type CommunityDetectionRuntimeConfig = {
  enabled: boolean;
  start_time_local: string;
  timezone: string;
  max_concurrency: number;
  jitter_seconds: number;
};

export type CommunityDetectionConfigItem = SSOConfigItem;

export type CommunityDetectionConfig = {
  update_time: string;
  items: CommunityDetectionConfigItem[];
  effective: CommunityDetectionRuntimeConfig;
};

export type CommunityDetectionConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type OperationLogRuntimeConfig = {
  retention_days: number;
};

export type OperationLogConfigItem = SSOConfigItem;

export type OperationLogConfig = {
  update_time: string;
  items: OperationLogConfigItem[];
  effective: OperationLogRuntimeConfig;
};

export type OperationLogConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type RecallFeedbackRuntimeConfig = {
  enabled: boolean;
  retention_days: number;
};

export type RecallFeedbackConfigItem = SSOConfigItem;

export type RecallFeedbackConfig = {
  update_time: string;
  items: RecallFeedbackConfigItem[];
  effective: RecallFeedbackRuntimeConfig;
};

export type RecallFeedbackConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type TelemetryPricingRuntimeConfig = {
  verifier_model: string;
  embedding_model: string;
  verifier_input_usd_per_million_tokens: number | null;
  verifier_output_usd_per_million_tokens: number | null;
  embedding_input_usd_per_million_tokens: number | null;
};

export type TelemetryPricingConfigItem = SSOConfigItem;

export type TelemetryPricingConfig = {
  update_time: string;
  items: TelemetryPricingConfigItem[];
  effective: TelemetryPricingRuntimeConfig;
};

export type TelemetryPricingConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type RecallFeedbackResultRef = {
  type: string;
  id: string;
  rank: number;
  tier?: string;
  score?: number;
  final_score?: number;
  semantic_rank?: number;
  keyword_rank?: number;
  status_at_recall?: string;
  recorded_at?: string;
  created_at?: string;
  updated_at?: string;
  valid_from?: string;
  valid_to?: string;
  retracted_at?: string;
};

export type RecallFeedbackJudgedResultRef = {
  type: string;
  id: string;
  rank?: number;
};

export type RecallFeedbackDreamFeedback = {
  dream_id: string;
  used: boolean;
  quality: "high" | "medium" | "low" | string;
  contradicted: boolean;
  feedback_comment?: string;
};

export type RecallFeedbackResolvedResult = {
  type: string;
  id: string;
  rank: number;
  resolution_status: "found" | "missing" | string;
  current_status?: string;
  current?: Record<string, unknown>;
  ref: RecallFeedbackResultRef;
};

export type RecallFeedbackEvent = {
  recall_id: string;
  created_at: string;
  updated_at: string;
  feedback_at?: string | null;
  team_id?: string | null;
  profile_id?: string | null;
  key_id?: string | null;
  auth_method: string;
  tool_name: string;
  query: string;
  tool_args: Record<string, unknown> | null;
  result_refs: RecallFeedbackResultRef[] | null;
  result_count: number;
  snapshot_state: "captured" | "feedback_only" | string;
  used?: boolean | null;
  answer_supported?: boolean | null;
  quality?: "high" | "medium" | "low" | string;
  missing_context?: boolean | null;
  irrelevant?: boolean | null;
  feedback_comment?: string;
  irrelevant_result_refs?: RecallFeedbackJudgedResultRef[] | null;
  dream_feedback?: RecallFeedbackDreamFeedback[] | null;
  resolved_results?: RecallFeedbackResolvedResult[] | null;
};

export type RecallFeedbackEventQuery = {
  limit?: number;
  offset?: number;
  team_id?: string;
  profile_id?: string;
  quality?: RecallFeedbackEvent["quality"] | "";
  include_pending?: boolean;
  missing_context?: boolean | "";
  irrelevant?: boolean | "";
  from?: string;
  to?: string;
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

export type DreamQuery = {
  limit?: number;
  status?: Dream["status"] | "";
  cursor?: string;
  sort?: DreamSort;
  direction?: DreamDirection;
};

export type DreamListResponse = {
  items: Dream[];
  next_cursor?: string;
};
type RequestOptions = {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
};

export class ControlApi {
  private readonly token: string;
  private readonly baseUrl: string;

  constructor(token = "", baseUrl = "/control/api") {
    this.token = token;
    this.baseUrl = baseUrl;
  }

  session(): Promise<ControlSession> {
    return this.requestEnvelope<ControlSession>("/session");
  }

  listTeams(): Promise<Page<Team>> {
    return this.request<Page<Team>>("/teams");
  }

  createTeam(input: CreateTeamInput): Promise<Team> {
    return this.requestEnvelope<Team>("/teams", { method: "POST", body: input });
  }

  updateTeam(teamId: string, input: UpdateTeamInput): Promise<Team> {
    return this.requestEnvelope<Team>(`/teams/${teamId}`, { method: "PATCH", body: input });
  }

  deleteTeam(teamId: string): Promise<{ status: string }> {
    return this.requestEnvelope<{ status: string }>(`/teams/${teamId}`, { method: "DELETE" });
  }

  listTeamCredentials(teamId: string): Promise<Page<Credential>> {
    return this.request<Page<Credential>>(`/teams/${teamId}/credentials`);
  }

  createTeamCredential(teamId: string, input: CreateCredentialInput): Promise<CreatedCredential> {
    return this.requestEnvelope<CreatedCredential>(`/teams/${teamId}/credentials`, { method: "POST", body: input });
  }

  updateTeamCredential(teamId: string, credentialId: string, input: UpdateCredentialInput): Promise<Credential> {
    return this.requestEnvelope<Credential>(`/teams/${teamId}/credentials/${credentialId}`, { method: "PATCH", body: input });
  }

  rotateTeamCredential(teamId: string, credentialId: string, input: CreateCredentialInput): Promise<CreatedCredential> {
    return this.requestEnvelope<CreatedCredential>(`/teams/${teamId}/credentials/${credentialId}/rotate`, { method: "POST", body: input });
  }

  deleteTeamCredential(teamId: string, credentialId: string): Promise<{ status: string }> {
    return this.requestEnvelope<{ status: string }>(`/teams/${teamId}/credentials/${credentialId}`, { method: "DELETE" });
  }

  getSecuritySettings(): Promise<SecuritySettings> {
    return this.requestEnvelope<SecuritySettings>("/security/settings");
  }

  updateSecuritySettings(input: SecuritySettings): Promise<SecuritySettings> {
    return this.requestEnvelope<SecuritySettings>("/security/settings", { method: "PATCH", body: input });
  }

  listSecurityBans(includeExpired = false): Promise<Page<SecurityBan>> {
    const suffix = includeExpired ? "?include_expired=true" : "";
    return this.request<Page<SecurityBan>>(`/security/bans${suffix}`);
  }

  createSecurityBan(input: CreateSecurityBanInput): Promise<SecurityBan> {
    return this.requestEnvelope<SecurityBan>("/security/bans", { method: "POST", body: input });
  }

  deleteSecurityBan(ip: string): Promise<{ status: string }> {
    return this.requestEnvelope<{ status: string }>(`/security/bans/${encodeURIComponent(ip)}`, { method: "DELETE" });
  }

  getMetrics(query: MetricsQuery = {}, signal?: AbortSignal): Promise<ControlMetrics> {
    const params = new URLSearchParams();
    if (query.window_minutes !== undefined) {
      params.set("window_minutes", String(query.window_minutes));
    }
    if (query.team_id) {
      params.set("team_id", query.team_id);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    return this.requestEnvelope<ControlMetrics>(`/metrics${suffix}`, { signal });
  }

  getConflictQueue(teamId: string, query: ConflictQueueQuery = {}): Promise<ConflictQueuePage> {
    const params = new URLSearchParams();
    if (query.status) {
      params.set("status", query.status);
    }
    if (query.limit !== undefined) {
      params.set("limit", String(query.limit));
    }
    if (query.cursor) {
      params.set("cursor", query.cursor);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    return this.requestEnvelope<ConflictQueuePage>(`/teams/${encodeURIComponent(teamId)}/conflicts/queue${suffix}`);
  }

  getSearchConvergence(): Promise<SearchConvergence> {
    return this.requestEnvelope<SearchConvergence>("/search/convergence");
  }

  getTelemetry(query: ControlTelemetryQuery = {}, signal?: AbortSignal): Promise<TelemetrySnapshot> {
    const params = new URLSearchParams();
    if (query.window) {
      params.set("window", query.window);
    }
    if (query.scope) {
      params.set("scope", query.scope);
    }
    if (query.team_id) {
      params.set("team_id", query.team_id);
    }
    if (query.profile_id) {
      params.set("profile_id", query.profile_id);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    return this.requestEnvelope<TelemetrySnapshot>(`/telemetry${suffix}`, { signal });
  }

  listSSOProviders(): Promise<SSOProvider[]> {
    return this.requestEnvelope<SSOProvider[]>("/sso/providers");
  }

  createSSOProvider(input: SSOProviderInput): Promise<SSOProvider> {
    return this.requestEnvelope<SSOProvider>("/sso/providers", { method: "POST", body: input });
  }

  updateSSOProvider(providerId: string, input: SSOProviderInput): Promise<SSOProvider> {
    return this.requestEnvelope<SSOProvider>(`/sso/providers/${providerId}`, { method: "PATCH", body: input });
  }

  deleteSSOProvider(providerId: string): Promise<{ status: string }> {
    return this.requestEnvelope<{ status: string }>(`/sso/providers/${providerId}`, { method: "DELETE" });
  }

  listSSOGroupMappings(providerId: string): Promise<SSOGroupMapping[]> {
    return this.requestEnvelope<SSOGroupMapping[]>(`/sso/providers/${providerId}/mappings`);
  }

  createSSOGroupMapping(providerId: string, input: SSOGroupMappingInput): Promise<SSOGroupMapping> {
    return this.requestEnvelope<SSOGroupMapping>(`/sso/providers/${providerId}/mappings`, { method: "POST", body: input });
  }

  updateSSOGroupMapping(providerId: string, mappingId: string, input: SSOGroupMappingInput): Promise<SSOGroupMapping> {
    return this.requestEnvelope<SSOGroupMapping>(`/sso/providers/${providerId}/mappings/${mappingId}`, { method: "PATCH", body: input });
  }

  deleteSSOGroupMapping(providerId: string, mappingId: string): Promise<{ status: string }> {
    return this.requestEnvelope<{ status: string }>(`/sso/providers/${providerId}/mappings/${mappingId}`, { method: "DELETE" });
  }

  listDirectoryConnectors(): Promise<DirectoryConnector[]> {
    return this.requestEnvelope<DirectoryConnector[]>("/sso/directory/connectors");
  }

  getDirectoryConnector(providerId: string): Promise<DirectoryConnector> {
    return this.requestEnvelope<DirectoryConnector>(`/sso/providers/${providerId}/directory-connector`);
  }

  createDirectoryConnector(providerId: string, input: DirectoryConnectorInput): Promise<DirectoryConnectorCreateResult> {
    return this.requestEnvelope<DirectoryConnectorCreateResult>(`/sso/providers/${providerId}/directory-connector`, { method: "POST", body: input });
  }

  updateDirectoryConnector(connectorId: string, input: DirectoryConnectorInput): Promise<DirectoryConnector> {
    return this.requestEnvelope<DirectoryConnector>(`/sso/directory/connectors/${connectorId}`, { method: "PATCH", body: input });
  }

  rotateDirectoryCredentials(connectorId: string): Promise<DirectoryCredential> {
    return this.requestEnvelope<DirectoryCredential>(`/sso/directory/connectors/${connectorId}/credentials/rotate`, { method: "POST" });
  }

  previewDirectoryConnector(connectorId: string): Promise<DirectoryPreview> {
    return this.requestEnvelope<DirectoryPreview>(`/sso/directory/connectors/${connectorId}/preview`);
  }

  setDirectoryConnectorStatus(connectorId: string, status: DirectoryConnector["status"], previewVersion = ""): Promise<DirectoryConnector> {
    return this.requestEnvelope<DirectoryConnector>(`/sso/directory/connectors/${connectorId}/status`, { method: "POST", body: { status, preview_version: previewVersion } });
  }

  adoptDirectoryGroupTeam(connectorId: string, groupId: string, teamId: string): Promise<{ status: string }> {
    return this.requestEnvelope<{ status: string }>(`/sso/directory/connectors/${connectorId}/groups/${groupId}/adopt`, { method: "POST", body: { team_id: teamId } });
  }

  listControlAdminGroups(providerId: string): Promise<ControlAdminGroup[]> {
    return this.requestEnvelope<ControlAdminGroup[]>(`/sso/providers/${providerId}/control-admin-groups`);
  }

  createControlAdminGroup(providerId: string, input: ControlAdminGroupInput): Promise<ControlAdminGroup> {
    return this.requestEnvelope<ControlAdminGroup>(`/sso/providers/${providerId}/control-admin-groups`, { method: "POST", body: input });
  }

  deleteControlAdminGroup(providerId: string, groupId: string): Promise<{ status: string }> {
    return this.requestEnvelope<{ status: string }>(`/sso/providers/${providerId}/control-admin-groups/${groupId}`, { method: "DELETE" });
  }

  logoutControlSSO(): Promise<void> {
    return requestJson<void>("/control/auth/logout", {
      method: "POST",
      credentials: "include",
      csrf: { cookieName: "dense_mem_control_csrf", headerName: "X-Dense-Mem-Control-CSRF" },
    });
  }

  getGeneralConfig(): Promise<GeneralConfig> {
    return this.requestEnvelope<GeneralConfig>("/config/general");
  }

  updateGeneralConfig(input: GeneralConfigInput): Promise<GeneralConfig> {
    return this.requestEnvelope<GeneralConfig>("/config/general", { method: "PATCH", body: input });
  }

  getSSOConfig(): Promise<SSOConfig> {
    return this.requestEnvelope<SSOConfig>("/config/sso");
  }

  updateSSOConfig(input: SSOConfigInput): Promise<SSOConfig> {
    return this.requestEnvelope<SSOConfig>("/config/sso", { method: "PATCH", body: input });
  }

  getDreamingConfig(): Promise<DreamingConfig> {
    return this.requestEnvelope<DreamingConfig>("/config/dreaming");
  }

  updateDreamingConfig(input: DreamingConfigInput): Promise<DreamingConfig> {
    return this.requestEnvelope<DreamingConfig>("/config/dreaming", { method: "PATCH", body: input });
  }

  getCommunityDetectionConfig(): Promise<CommunityDetectionConfig> {
    return this.requestEnvelope<CommunityDetectionConfig>("/config/community-detection");
  }

  updateCommunityDetectionConfig(input: CommunityDetectionConfigInput): Promise<CommunityDetectionConfig> {
    return this.requestEnvelope<CommunityDetectionConfig>("/config/community-detection", { method: "PATCH", body: input });
  }

  getOperationLogConfig(): Promise<OperationLogConfig> {
    return this.requestEnvelope<OperationLogConfig>("/config/operation-logs");
  }

  updateOperationLogConfig(input: OperationLogConfigInput): Promise<OperationLogConfig> {
    return this.requestEnvelope<OperationLogConfig>("/config/operation-logs", { method: "PATCH", body: input });
  }

  getRecallFeedbackConfig(): Promise<RecallFeedbackConfig> {
    return this.requestEnvelope<RecallFeedbackConfig>("/config/recall-feedback");
  }

  updateRecallFeedbackConfig(input: RecallFeedbackConfigInput): Promise<RecallFeedbackConfig> {
    return this.requestEnvelope<RecallFeedbackConfig>("/config/recall-feedback", { method: "PATCH", body: input });
  }

  getTelemetryPricingConfig(): Promise<TelemetryPricingConfig> {
    return this.requestEnvelope<TelemetryPricingConfig>("/config/telemetry-pricing");
  }

  updateTelemetryPricingConfig(input: TelemetryPricingConfigInput): Promise<TelemetryPricingConfig> {
    return this.requestEnvelope<TelemetryPricingConfig>("/config/telemetry-pricing", { method: "PATCH", body: input });
  }

  listOperationLogs(query: OperationLogQuery = {}): Promise<Page<OperationLog>> {
    return this.request<Page<OperationLog>>(buildOperationLogsPath(query));
  }

  listRememberAttemptDiagnostics(query: RememberAttemptDiagnosticQuery = {}): Promise<Page<RememberAttemptDiagnosticSummary>> {
    return this.request<Page<RememberAttemptDiagnosticSummary>>(buildRememberAttemptDiagnosticsPath(query));
  }

  getRememberAttemptDiagnostic(teamId: string, attemptId: string): Promise<RememberAttemptDiagnosticDetail> {
    return this.requestEnvelope<RememberAttemptDiagnosticDetail>(buildRememberAttemptDiagnosticPath(teamId, attemptId));
  }

  getRememberFailureArtifact(teamId: string, attemptId: string, artifactId: string): Promise<Uint8Array> {
    return requestBytes(`${this.baseUrl}${buildRememberFailureArtifactPath(teamId, attemptId, artifactId)}`, {
      token: this.token || undefined,
      credentials: this.token ? undefined : "include",
      cache: "no-store",
    });
  }

  listRecallFeedbackEvents(query: RecallFeedbackEventQuery = {}): Promise<Page<RecallFeedbackEvent>> {
    const params = new URLSearchParams();
    if (query.limit !== undefined) {
      params.set("limit", String(query.limit));
    }
    if (query.offset !== undefined) {
      params.set("offset", String(query.offset));
    }
    if (query.team_id) {
      params.set("team_id", query.team_id);
    }
    if (query.profile_id) {
      params.set("profile_id", query.profile_id);
    }
    if (query.quality) {
      params.set("quality", query.quality);
    }
    if (query.include_pending !== undefined) {
      params.set("include_pending", String(query.include_pending));
    }
    if (query.missing_context !== undefined && query.missing_context !== "") {
      params.set("missing_context", String(query.missing_context));
    }
    if (query.irrelevant !== undefined && query.irrelevant !== "") {
      params.set("irrelevant", String(query.irrelevant));
    }
    if (query.from) {
      params.set("from", query.from);
    }
    if (query.to) {
      params.set("to", query.to);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    return this.request<Page<RecallFeedbackEvent>>(`/recall-feedback-events${suffix}`);
  }

  getRecallFeedbackEvent(recallId: string): Promise<RecallFeedbackEvent> {
    return this.requestEnvelope<RecallFeedbackEvent>(`/recall-feedback-events/${encodeURIComponent(recallId)}`);
  }

  getTeamDreamingStatus(teamId: string): Promise<DreamStatus> {
    return this.requestEnvelope<DreamStatus>(`/teams/${teamId}/dreaming/status`);
  }

  getTeamCommunityStatus(teamId: string): Promise<CommunityStatus> {
    return this.requestEnvelope<CommunityStatus>(`/teams/${teamId}/community/status`);
  }

  listTeamDreamingRuns(teamId: string, limit = 20): Promise<DreamRun[]> {
    return this.requestEnvelope<DreamRun[]>(`/teams/${teamId}/dreaming/runs?limit=${limit}`);
  }

  listTeamDreams(teamId: string, query: DreamQuery = {}): Promise<DreamListResponse> {
    const params = new URLSearchParams();
    if (query.limit !== undefined) {
      params.set("limit", String(query.limit));
    }
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
    const suffix = params.toString() ? `?${params.toString()}` : "";
    return this.requestEnvelope<DreamListResponse>(`/teams/${teamId}/dreams${suffix}`);
  }

  private async requestEnvelope<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const payload = await this.request<{ data: T }>(path, options);
    return payload.data;
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    return requestJson<T>(`${this.baseUrl}${path}`, {
      method: options.method ?? "GET",
      token: this.token || undefined,
      body: options.body,
      signal: options.signal,
      credentials: this.token ? undefined : "include",
      csrf: this.token ? undefined : { cookieName: "dense_mem_control_csrf", headerName: "X-Dense-Mem-Control-CSRF" },
    });
  }
}
