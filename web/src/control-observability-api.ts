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
  next_action: "retry_same_request" | "resubmit_remember" | "retry_correction" | "contact_operator" | "none";
  remediation: string;
};

export type SubmissionDiagnosticSummary = {
  team_id: string;
  team_name: string;
  owner_profile_id: string;
  submission_id: string;
  processing_state: string;
  correlation_id?: string;
  failed_phase?: string;
  error_code?: string;
  evidence_count: number;
  relationship_count: number;
  document_count: number;
  assessor_turns: number;
  duration_ms: number;
  created_at: string;
  completed_at?: string | null;
};

export type SubmissionEvidenceStatus = {
  disposition: "stored" | "not_stored";
  evidence_id?: string;
  evidence_index: number;
  superseded_evidence_ids: string[];
  search_state: "current" | "not_required";
  reason?: string;
  error?: SubmissionStatusError | null;
};

export type SubmissionRelationshipResult = {
  ref: string;
  disposition: string;
  reason?: string;
  splits?: Array<{ split_index: number; relationship_id: string; relationship_version: number; status: string }>;
};

export type SubmissionDiagnosticEvent = {
  sequence_no: number;
  phase: string;
  event_kind: string;
  outcome: string;
  metadata: Record<string, unknown> | null;
  created_at: string;
};

export type SubmissionDiagnosticDetail = {
  team_id: string;
  team_name: string;
  owner_profile_id: string;
  submission_id: string;
  submission_kind: "remember";
  processing_state: string;
  search_state: string;
  correlation_id?: string;
  failed_phase?: string;
  error_code?: string;
  evidence_count: number;
  relationship_count: number;
  document_count: number;
  assessor_turns: number;
  duration_ms: number;
  created_at: string;
  completed_at?: string | null;
  evidence: SubmissionEvidenceStatus[];
  relationship_results: SubmissionRelationshipResult[];
  errors: SubmissionStatusError[];
  events: SubmissionDiagnosticEvent[];
  failure_artifacts: RememberFailureArtifactDescriptor[];
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
  return pathWithQuery("/remember-attempts", params);
}

export function buildSubmissionDiagnosticPath(teamId: string, submissionId: string): string {
  return "/teams/" + encodeURIComponent(teamId) + "/remember-attempts/" + encodeURIComponent(submissionId);
}

export function buildRememberFailureArtifactPath(teamId: string, submissionId: string, artifactId: string): string {
  return "/teams/" + encodeURIComponent(teamId) + "/remember-attempts/"
    + encodeURIComponent(submissionId) + "/artifacts/" + encodeURIComponent(artifactId);
}

export async function fetchRememberFailureArtifact(baseUrl: string, token: string, teamId: string, submissionId: string, artifactId: string): Promise<string> {
  const response = await fetch(`${baseUrl}${buildRememberFailureArtifactPath(teamId, submissionId, artifactId)}`, {
    method: "GET",
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    credentials: token ? undefined : "include",
    cache: "no-store",
  });
  const text = await response.text();
  if (!response.ok) {
    throw new Error(text || response.statusText || "Request failed");
  }
  return text;
}

function appendParam(params: URLSearchParams, key: string, value: string | number | undefined): void {
  if (value !== undefined && value !== "") {
    params.set(key, String(value));
  }
}

function pathWithQuery(path: string, params: URLSearchParams): string {
  const query = params.toString();
  return query ? path + "?" + query : path;
}
