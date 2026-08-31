export type SearchConvergence = {
  observed_at: string;
  status: "converged" | "recovering" | "attention_required" | string;
  expected_documents: number;
  current_documents: number;
  drifted_documents: number;
  affected_team_count: number;
  oldest_drift_age_seconds: number;
  drift_classes: Array<{ class: string; count: number }>;
  contract?: {
    provider: string;
    model: string;
    dimensions: number;
    index_generation: number;
    index_strategy: string;
  };
  latest_run?: {
    run_id: string;
    local_run_date: string;
    status: string;
    selected_count: number;
    embedded_count: number;
    updated_count: number;
    drifted_count: number;
    last_error?: string;
    updated_at: string;
  };
};
