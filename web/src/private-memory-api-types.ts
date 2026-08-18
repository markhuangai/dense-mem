export type PrivateMemoryRuntimeConfig = {
  retention_days: number;
};

export type PrivateMemoryConfigItem = {
  key: string;
  value: string;
  effective_value: string;
  validation_error?: string;
  updated_at: string;
};

export type PrivateMemoryConfig = {
  update_time: string;
  items: PrivateMemoryConfigItem[];
  effective: PrivateMemoryRuntimeConfig;
};

export type PrivateMemoryConfigInput = {
  items: Array<{ key: string; value: string }>;
};

export type PrivateMemoryLegalHold = {
  id: string;
  team_id: string;
  space_id: string;
  reason_code: string;
  actor_class: string;
  placed_at: string;
  released_at?: string;
};

export type PrivateMemorySpace = {
  id: string;
  team_id: string;
  kind: "profile_private" | "credential_private" | string;
  owner_profile_id?: string;
  owner_credential_id?: string;
  generation: number;
  lifecycle_state: "active" | "sealed" | "retired" | string;
  private_content_at?: string;
  sealed_at?: string;
  retired_at?: string;
  created_at: string;
  updated_at: string;
  active_hold?: PrivateMemoryLegalHold;
};

export type PrivateMemoryOperation = {
  operation_id: string;
  team_id: string;
  space_id?: string;
  space_kind?: string;
  target_credential_id?: string;
  action: string;
  actor_class: string;
  reason_code: string;
  target_generation?: number;
  retire_space: boolean;
  status: "queued" | "processing" | "completed" | "failed" | string;
  deleted_counts: Record<string, number>;
  attempt_count?: number;
  next_attempt_at?: string;
  last_error_code?: string;
  requested_at: string;
  started_at?: string;
  completed_at?: string;
  updated_at: string;
};

export type PrivateMemoryRetentionRun = {
  id: string;
  actor_class: string;
  cutoff: string;
  retention_days: number;
  queued_count: number;
  status: string;
  started_at: string;
  completed_at?: string;
};

export type PrivateMemoryPage<T> = {
  data: T[];
  pagination: { limit: number; offset: number };
};
