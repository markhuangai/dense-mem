export type SearchConvergence = {
  observed_at: string;
  status: "converged" | "recovering" | "attention_required" | string;
  contract?: {
    provider: string;
    model: string;
    dimensions: number;
    index_generation: number;
    index_strategy: string;
  };
  queue: {
    queued: number;
    processing: number;
    failed: number;
    expired_leases: number;
    affected_team_count: number;
    oldest_pending_age_seconds: number;
    oldest_failure_age_seconds: number;
  };
  failures: Array<{ source_kind: string; failure_class: string; failure_code: string; count: number }>;
  incidents: Array<{
    team_id: string;
    team_name: string;
    incident_id: string;
    source_kind: string;
    failure_class: string;
    failure_code: string;
    status: string;
    affected_job_count: number;
    first_seen_at: string;
    last_seen_at: string;
    age_seconds: number;
    guidance: string;
  }>;
  latest_run?: {
    run_id: string;
    local_run_date: string;
    status: string;
    canary_job_id?: string;
    canary_attempted_at?: string;
    canary_outcome: string;
    canary_failure_class?: string;
    canary_failure_code?: string;
    requeued_count: number;
    recovered_count: number;
    last_error?: string;
    updated_at: string;
  };
};
