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

export type SubmissionStatusError = {
  code: string;
  message: string;
  retryable: boolean;
  next_action: "poll_status" | "resubmit_submission" | "submit_replacement" | "retry_correction" | "contact_operator" | "none";
  remediation: string;
};

export type SubmissionDiagnosticSummary = {
  team_id: string;
  team_name: string;
  owner_profile_id: string;
  submission_id: string;
  processing_state: string;
  correlation_id?: string;
  source_summary: string;
  source_summary_truncated: boolean;
  attempts: number;
  max_attempts: number;
  evidence_count: number;
  submitted_at: string;
  next_attempt_at?: string | null;
  started_at?: string | null;
  updated_at?: string | null;
  completed_at?: string | null;
  error?: SubmissionStatusError | null;
  operator_diagnostic?: SubmissionOperatorDiagnostic | null;
};

export type SubmissionOperatorDiagnostic = {
  id?: string;
  placement_item_id?: string;
  outcome_kind?: string;
  status?: string;
  occurred_at?: string | null;
  failure_reason_code?: string;
  failure_stage?: string;
  failure_class?: string;
  validation_stage?: string;
  validation_field_families?: string[];
  failure_measurement?: {
    unit: string;
    observed?: number;
    observed_at_least?: number;
    limit: number;
  } | null;
  provider_status?: number;
  assessor_turns?: number;
  assessor_provider_attempted?: boolean;
  message?: string;
};

export type SubmissionEvidenceStatus = {
  evidence_id: string;
  evidence_index: number;
  superseded_evidence_ids: string[];
  search_state: string;
  error?: SubmissionStatusError | null;
};

export type SubmissionSemanticHold = {
  state: string;
  issues: Array<{ code: string; relationship_ref?: string; component: string; message: string }>;
  issues_truncated: boolean;
  replacement: {
    tool: string;
    replaces_submission_id: string;
    expires_at?: string | null;
    instruction: string;
  };
};

export type SubmissionDiagnosticDetail = {
  team_id: string;
  team_name: string;
  owner_profile_id: string;
  evidence_count: number;
  submission_id: string;
  submission_kind: "remember";
  processing_state: string;
  search_state: string;
  check_after_seconds: number;
  correlation_id?: string;
  source_summary: string;
  source_summary_truncated: boolean;
  attempts?: number;
  max_attempts?: number;
  submitted_at?: string;
  next_attempt_at?: string | null;
  started_at?: string | null;
  updated_at?: string | null;
  completed_at?: string | null;
  evidence: SubmissionEvidenceStatus[];
  errors: SubmissionStatusError[];
  quarantine_expires_at?: string | null;
  replacement_window_expires_at?: string | null;
  semantic_hold?: SubmissionSemanticHold | null;
  operator_diagnostic?: SubmissionOperatorDiagnostic | null;
  operator_diagnostics: SubmissionOperatorDiagnostic[];
};

export type SubmissionDiagnosticQuery = {
  team_id?: string;
  processing_state?: string;
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

export function buildSubmissionDiagnosticsPath(query: SubmissionDiagnosticQuery): string {
  const params = new URLSearchParams();
  appendParam(params, "team_id", query.team_id);
  appendParam(params, "processing_state", query.processing_state);
  appendParam(params, "limit", query.limit);
  appendParam(params, "offset", query.offset);
  return pathWithQuery("/submissions", params);
}

export function buildSubmissionDiagnosticPath(teamId: string, submissionId: string): string {
  return `/teams/${encodeURIComponent(teamId)}/submissions/${encodeURIComponent(submissionId)}`;
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
