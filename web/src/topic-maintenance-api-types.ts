import type { GeneralConfigItem } from "./configuration-api-types";

export type TopicMaintenancePolicy = {
  enabled: boolean;
  cadence_hours: 12 | 24;
  start_time_local: string;
  timezone: string;
  model: string;
  max_concurrency: number;
  team_max_concurrency?: number;
  team_concurrency_overrides?: Array<{ team_id: string; max_concurrency: number }>;
  context_window_tokens: number;
  settings_version: string;
};
export type TopicMaintenanceConfig = {
  update_time: string;
  items: GeneralConfigItem[];
  effective: TopicMaintenancePolicy;
};
export type TopicMaintenanceRun = {
  id: string;
  window_id: string;
  kind: string;
  status: string;
  retryable: boolean;
  operation_key?: string;
  max_batches: number;
  completed_batches: number;
  failure_code?: string;
  created_at: string;
  updated_at: string;
  input_tokens?: number;
  output_tokens?: number;
};
export type TopicMaintenanceStatus = {
  observed_at: string;
  paused: boolean;
  enabled: boolean;
  discovery_complete: boolean;
  coverage_complete: boolean;
  communities?: { placed: number; standalone: number; pending: number; failed: number };
  equivalence?: { pending: number; completed: number; failed: number };
  counts: Record<"eligible" | "organized" | "pending" | "ambiguous" | "failed", number> & { uncategorized?: number };
  oldest_pending_at?: string;
  last_successful_progress?: string;
  pending_policy: TopicMaintenancePolicy;
  window?: {
    id: string;
    starts_at: string;
    ends_at: string;
    policy: TopicMaintenancePolicy;
    usage: TopicMaintenanceUsage;
  };
  configuration_error?: string;
  active_work: number;
  usage: TopicMaintenanceUsage;
  historical_usage: TopicMaintenanceUsage;
  latest_run?: TopicMaintenanceRun;
};
export type TopicMaintenanceRunPage = { runs: TopicMaintenanceRun[]; next_cursor?: string };

export type TopicMaintenanceUsage = {
  attempts: number;
  unreconciled_attempts: number;
  unavailable_usage_attempts: number;
  partial_usage_attempts: number;
  reported_input_tokens: number;
  reported_output_tokens: number;
  reported_total_tokens: number;
};
