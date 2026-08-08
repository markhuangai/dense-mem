export type CommunityStatus = {
  effective_config: {
    enabled: boolean;
    start_time_local: string;
    timezone: string;
    max_concurrency: number;
    jitter_seconds: number;
  };
  latest_run?: {
    run_id: string;
    team_id: string;
    window_key: string;
    status: string;
    node_count: number;
    edge_count: number;
    community_count: number;
    source_fingerprint?: string;
    provider_model?: string;
    provider_attempts?: number;
    error?: string;
    started_at?: string;
    completed_at?: string;
  } | null;
  current_community_count: number;
};
