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
  event?: string;
  team_id?: string;
  reference_type?: string;
  reference_id?: string;
  from?: string;
  to?: string;
};

export type RememberError = {
  code: string;
  message: string;
  retryable: boolean;
  next_action: "retry_same_request" | "resubmit_remember" | "retry_dream_feedback" | "retry_correction" | "contact_operator" | "none";
  remediation: string;
};

export type RememberAttemptOutcome = "completed" | "rejected" | "quarantined" | "failed" | "replayed";

export type RememberAttemptDiagnosticSummary = {
  team_id: string;
  team_name: string;
  owner_profile_id: string;
  attempt_id: string;
  space_id?: string;
  space_generation?: number;
  canonical_attempt_id?: string;
  contract_version: string;
  submission_kind: string;
  outcome: RememberAttemptOutcome;
  failed_phase?: string;
  error_code?: string;
  correlation_id?: string;
  evidence_count: number;
  relationship_count: number;
  document_count: number;
  assessor_turns: number;
  duration_ms: number;
  created_at: string;
  completed_at?: string | null;
};

export type RememberAttemptDiagnosticEvent = {
  sequence_no: number;
  phase: string;
  event_kind: string;
  outcome: string;
  metadata: Record<string, unknown>;
  created_at: string;
};

export type RememberFailureArtifactDescriptor = {
  artifact_id: string;
  artifact_kind: string;
  content_type: string;
  byte_count: number;
  content_sha256: string;
  captured_at: string;
  expires_at: string;
};

export type RememberAttemptPublicResult = {
  contract_version: string;
  submission_id: string;
  submission_kind: string;
  processing_state: string;
  search_state: string;
  correlation_id: string;
  evidence: Array<{
    disposition: string;
    evidence_id?: string;
    evidence_index: number;
    superseded_evidence_ids: string[];
    search_state: string;
    reason?: string;
  }>;
  relationship_results: Array<{
    ref: string;
    disposition: string;
    reason?: string;
    splits: Array<{ split_index: number; relationship_id: string; relationship_version: number; status: string }>;
  }>;
  errors: RememberError[];
};

export type RememberAttemptDiagnosticDetail = RememberAttemptDiagnosticSummary & {
  public_result: RememberAttemptPublicResult;
  events: RememberAttemptDiagnosticEvent[];
  artifacts: RememberFailureArtifactDescriptor[];
};

export type RememberAttemptDiagnosticQuery = {
  team_id?: string;
  outcome?: RememberAttemptOutcome | "";
  limit?: number;
  offset?: number;
};

export function buildOperationLogsPath(query: OperationLogQuery): string {
  const params = new URLSearchParams();
  appendParam(params, "limit", query.limit);
  appendParam(params, "offset", query.offset);
  appendParam(params, "severity", query.severity);
  appendParam(params, "sort", query.sort);
  appendParam(params, "direction", query.direction);
  appendParam(params, "event", query.event);
  appendParam(params, "team_id", query.team_id);
  appendParam(params, "reference_type", query.reference_type);
  appendParam(params, "reference_id", query.reference_id);
  appendParam(params, "from", query.from);
  appendParam(params, "to", query.to);
  return pathWithQuery("/logs", params);
}

export function buildRememberAttemptDiagnosticsPath(query: RememberAttemptDiagnosticQuery): string {
  const params = new URLSearchParams();
  appendParam(params, "team_id", query.team_id);
  appendParam(params, "outcome", query.outcome);
  appendParam(params, "limit", query.limit);
  appendParam(params, "offset", query.offset);
  return pathWithQuery("/remember-attempts", params);
}

export function buildRememberAttemptDiagnosticPath(teamId: string, attemptId: string): string {
  return `/teams/${encodeURIComponent(teamId)}/remember-attempts/${encodeURIComponent(attemptId)}`;
}

export function buildRememberFailureArtifactPath(teamId: string, attemptId: string, artifactId: string): string {
  return `${buildRememberAttemptDiagnosticPath(teamId, attemptId)}/artifacts/${encodeURIComponent(artifactId)}`;
}

function appendParam(params: URLSearchParams, key: string, value: string | number | undefined): void {
  if (value !== undefined && value !== "") {
    params.set(key, String(value));
  }
}

function pathWithQuery(path: string, params: URLSearchParams): string {
  const query = params.toString();
  return query ? `${path}?${query}` : path;
}
