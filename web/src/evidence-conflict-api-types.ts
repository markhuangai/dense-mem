export type EvidenceConflictStatus = "open" | "resolved" | "dismissed";

export type EvidenceConflictPosition = {
  position_id: string;
  position_key?: string;
  evidence_id: string;
  canonical_owner_profile_id?: string;
  occurrence_id: string;
  occurrence_owner_profile_id?: string;
  quote: string;
  span_start: number;
  span_end: number;
  authority: string;
  submitted: boolean;
  created_at: string;
};

export type EvidenceConflictEvent = {
  event_id: string;
  conflict_id: string;
  ordinal: number;
  action: string;
  status_after: EvidenceConflictStatus;
  case_version: number;
  actor_kind: string;
  actor_id?: string;
  reason?: string;
  preferred_position_id?: string;
  citation_snapshot: EvidenceConflictPosition[];
  created_at: string;
};

export type EvidenceConflict = {
  team_id: string;
  conflict_id: string;
  space_id: string;
  space_generation: number;
  kind: "evidence_conflict";
  status: EvidenceConflictStatus;
  version: number;
  preferred_position_id?: string;
  resolved_at?: string;
  resolution_reason?: string;
  created_at: string;
  updated_at: string;
  positions: EvidenceConflictPosition[];
  events?: EvidenceConflictEvent[];
};

export type EvidenceConflictListPage = {
  items: EvidenceConflict[];
  next_cursor: string | null;
};

export type EvidenceConflictListQuery = {
  status?: "" | EvidenceConflictStatus;
  limit?: number;
  cursor?: string;
};

export type EvidenceConflictDetail = {
  conflict: EvidenceConflict;
  next_event_cursor: string | null;
};
