import type { ControlTelemetryQuery, TelemetrySnapshot } from "./telemetry/types";
import { requestJson } from "./http";
export { ApiError } from "./http";

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

export type TeamProfile = {
  id: string;
  team_id: string;
  name: string;
  key_suffix: string | null;
  scopes: string[] | null;
  role: ProfileRole;
  rate_limit: number;
  last_used_at: string | null;
  expires_at: string | null;
  created_at: string;
};

export type ProfileRole = "manager" | "member";

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

export type CreateTeamProfileInput = {
  name: string;
  scopes?: string[];
  role?: ProfileRole;
  rate_limit: number;
  expires_at?: string;
};

export type UpdateTeamProfileInput =
  | { name: string; role?: never; scopes?: never }
  | { name?: never; role: ProfileRole; scopes?: never }
  | { name?: never; role?: never; scopes: string[] };

export type CreatedTeamProfile = {
  api_key: string;
  key: TeamProfile;
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
  message?: string;
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
  window: MetricsWindow;
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
  client_id: string;
  client_secret_env: string;
  scopes: string[];
  group_claims: string[];
  groups_endpoint: string;
  groups_scopes: string[];
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
  role: ProfileRole;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type SSOProviderInput = {
  name: string;
  kind: SSOProvider["kind"];
  issuer_url: string;
  client_id: string;
  client_secret_env: string;
  scopes: string[];
  group_claims: string[];
  groups_endpoint: string;
  groups_scopes: string[];
  enabled: boolean;
};

export type SSOGroupMappingInput = {
  team_id: string;
  group_id: string;
  scopes: string[];
  role: ProfileRole;
  enabled: boolean;
};

export type SSOConfigItem = {
  key: string;
  value: string;
  effective_value: string;
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
  reflect_enabled: boolean;
  reevaluate_enabled: boolean;
  dream_enabled: boolean;
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

export type EvaluationRuntimeConfig = {
  enabled: boolean;
  export_max_page_size: number;
};

export type EvaluationConfigItem = SSOConfigItem;

export type EvaluationConfig = {
  update_time: string;
  items: EvaluationConfigItem[];
  effective: EvaluationRuntimeConfig;
};

export type EvaluationConfigInput = {
  items: Array<{
    key: string;
    value: string;
  }>;
};

export type OperationLog = {
  id: string;
  timestamp: string;
  severity: "DEBUG" | "INFO" | "WARN" | "ERROR" | string;
  severity_rank: number;
  message: string;
  source: string;
  team_id: string | null;
  profile_id: string | null;
  correlation_id: string;
  error: string;
  attrs: Record<string, unknown> | null;
};

export type OperationLogQuery = {
  limit?: number;
  offset?: number;
  severity?: OperationLog["severity"] | "";
  sort?: "timestamp" | "severity";
  direction?: "asc" | "desc";
};

export type RecallFeedbackResultRef = {
  type: "fragment" | "claim" | "fact" | string;
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
  type: "fragment" | "claim" | "fact" | "dream" | string;
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
  status: "proposed" | "reinforced" | "stale" | "rejected" | "promoted" | string;
  cycle: string;
  cycle_run_id?: string;
  generator_model?: string;
  source_refs?: Array<{ type: string; id: string }>;
  invalidated_reason?: string;
  last_evaluated_at?: string;
  created_at: string;
  updated_at: string;
};

export type DreamSort = "updated_at" | "created_at" | "last_evaluated_at";
export type DreamDirection = "asc" | "desc";

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

export type ControlPortalMode = "normal" | "migration" | "cleanup";

export type ControlSession = {
  authenticated: boolean;
  portal_mode: ControlPortalMode;
  legacy_config_present: boolean;
};

export type MigrationRun = {
  run_id: string;
  migration_contract_version: string;
  corpus_version: string;
  source_kind: string;
  state: string;
  phase?: string;
  required: boolean;
  preflight_approved: boolean;
  backup_reference?: string;
  preflight_checks?: Record<string, unknown>;
  corpus_hash?: string;
  total_items: number;
  completed_items: number;
  failed_items: number;
  excluded_items: number;
  claim_epoch: number;
  last_error?: string;
  retryable: boolean;
  started_at?: string;
  completed_at?: string;
  cutover_at?: string;
  created_at: string;
  updated_at: string;
};

export type MigrationGateResult = {
  gate_name: string;
  outcome: "pass" | "fail" | "warning" | string;
  evidence_ref?: string;
  evidence_hash?: string;
  message?: string;
  metadata?: Record<string, unknown>;
};

export type MigrationRepairSummary = {
  required: boolean;
  legacy_predicate_reviews: number;
  orphan_reviews: number;
  abandoned_processing: number;
  retryable_failures: number;
  held_reviews: number;
  blocked_items: number;
  blocking_exclusions: number;
  repairable_exclusions?: number;
  hard_blocking_exclusions?: number;
  failure_groups?: Array<{
    stage: string;
    class: string;
    count: number;
  }>;
  repaired_items: number;
  claim_epoch_before?: number;
  claim_epoch_after?: number;
};

export type MigrationStatus = {
  state: string;
  required: boolean;
  data_plane_allowed: boolean;
  readiness_message: string;
  run?: MigrationRun;
  repair?: MigrationRepairSummary;
  gate_results?: MigrationGateResult[];
  recent_errors?: string[];
  restart_pending?: boolean;
};

export type MigrationPreflightInput = {
  backups_confirmed: boolean;
  reason?: string;
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
};

export class ControlApi {
  private readonly token: string;
  private readonly baseUrl: string;

  constructor(token: string, baseUrl = "/control/api") {
    this.token = token;
    this.baseUrl = baseUrl;
  }

  session(): Promise<ControlSession> {
    return this.requestEnvelope<ControlSession>("/session");
  }

  getMigrationStatus(): Promise<MigrationStatus> {
    return this.requestEnvelope<MigrationStatus>("/v2/migration");
  }

  approveMigrationPreflight(input: MigrationPreflightInput): Promise<MigrationStatus> {
    return this.requestEnvelope<MigrationStatus>("/v2/migration/preflight", { method: "POST", body: input });
  }

  startMigration(reason?: string): Promise<MigrationStatus> {
    return this.requestEnvelope<MigrationStatus>("/v2/migration/start", { method: "POST", body: { reason } });
  }

  pauseMigration(reason?: string): Promise<MigrationStatus> {
    return this.requestEnvelope<MigrationStatus>("/v2/migration/pause", { method: "POST", body: { reason } });
  }

  resumeMigration(reason?: string): Promise<MigrationStatus> {
    return this.requestEnvelope<MigrationStatus>("/v2/migration/resume", { method: "POST", body: { reason } });
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

  listTeamProfiles(teamId: string): Promise<Page<TeamProfile>> {
    return this.request<Page<TeamProfile>>(`/teams/${teamId}/profiles`);
  }

  createTeamProfile(teamId: string, input: CreateTeamProfileInput): Promise<CreatedTeamProfile> {
    return this.requestEnvelope<CreatedTeamProfile>(`/teams/${teamId}/profiles`, { method: "POST", body: input });
  }

  updateTeamProfile(teamId: string, profileId: string, input: UpdateTeamProfileInput): Promise<TeamProfile> {
    return this.requestEnvelope<TeamProfile>(`/teams/${teamId}/profiles/${profileId}`, { method: "PATCH", body: input });
  }

  regenerateTeamProfileKey(teamId: string, profileId: string, input: CreateTeamProfileInput): Promise<CreatedTeamProfile> {
    return this.requestEnvelope<CreatedTeamProfile>(`/teams/${teamId}/profiles/${profileId}/rotate`, { method: "POST", body: input });
  }

  deleteTeamProfile(teamId: string, profileId: string): Promise<{ status: string }> {
    return this.requestEnvelope<{ status: string }>(`/teams/${teamId}/profiles/${profileId}`, { method: "DELETE" });
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

  getMetrics(query: MetricsQuery = {}): Promise<ControlMetrics> {
    const params = new URLSearchParams();
    if (query.window_minutes !== undefined) {
      params.set("window_minutes", String(query.window_minutes));
    }
    if (query.team_id) {
      params.set("team_id", query.team_id);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    return this.requestEnvelope<ControlMetrics>(`/metrics${suffix}`);
  }

  getTelemetry(query: ControlTelemetryQuery = {}): Promise<TelemetrySnapshot> {
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
    return this.requestEnvelope<TelemetrySnapshot>(`/telemetry${suffix}`);
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

  getEvaluationConfig(): Promise<EvaluationConfig> {
    return this.requestEnvelope<EvaluationConfig>("/config/evaluation");
  }

  updateEvaluationConfig(input: EvaluationConfigInput): Promise<EvaluationConfig> {
    return this.requestEnvelope<EvaluationConfig>("/config/evaluation", { method: "PATCH", body: input });
  }

  listOperationLogs(query: OperationLogQuery = {}): Promise<Page<OperationLog>> {
    const params = new URLSearchParams();
    if (query.limit !== undefined) {
      params.set("limit", String(query.limit));
    }
    if (query.offset !== undefined) {
      params.set("offset", String(query.offset));
    }
    if (query.severity) {
      params.set("severity", query.severity);
    }
    if (query.sort) {
      params.set("sort", query.sort);
    }
    if (query.direction) {
      params.set("direction", query.direction);
    }
    const suffix = params.toString() ? `?${params.toString()}` : "";
    return this.request<Page<OperationLog>>(`/logs${suffix}`);
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
      token: this.token,
      body: options.body,
    });
  }
}
