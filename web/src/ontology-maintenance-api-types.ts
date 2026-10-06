import type { GeneralConfigItem } from "./configuration-api-types";

export type OntologyMaintenancePolicy = {
  enabled: boolean;
  cadence_hours: 12 | 24;
  start_time_local: string;
  timezone: string;
  model: string;
  max_concurrency: number;
  input_tokens: number;
  output_tokens: number;
  settings_version: string;
};
export type OntologyMaintenanceConfig = {
  update_time: string;
  items: GeneralConfigItem[];
  effective: OntologyMaintenancePolicy;
};
export type OntologyMaintenanceRun = {
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
};
export type OntologyMaintenanceStatus = {
  observed_at: string;
  paused: boolean;
  enabled: boolean;
  discovery_complete: boolean;
  coverage_complete: boolean;
  counts: Record<"eligible" | "organized" | "pending" | "ambiguous" | "failed" | "budget_deferred", number>;
  oldest_pending_at?: string;
  last_successful_progress?: string;
  pending_policy: OntologyMaintenancePolicy;
  window?: {
    id: string;
    starts_at: string;
    ends_at: string;
    policy: OntologyMaintenancePolicy;
    charged_input_tokens: number;
    charged_output_tokens: number;
    reported_input_tokens: number;
    reported_output_tokens: number;
    reserved_input_tokens: number;
    reserved_output_tokens: number;
    overrun: boolean;
  };
  latest_run?: OntologyMaintenanceRun;
};
export type OntologyMaintenanceRunPage = { runs: OntologyMaintenanceRun[]; next_cursor?: string };
