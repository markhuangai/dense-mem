import type { OntologyMaintenancePolicy } from "./ontology-maintenance-api-types";

export type CommunityStatus = {
  effective_config: OntologyMaintenancePolicy;
  legacy_processing_retired: boolean;
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
