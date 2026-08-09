export type ConflictQueueStatus = "open" | "overdue";
export type ConflictQueueLeaseState = "idle" | "active" | "expired";

export type ConflictQueueSummary = {
  open_count: number;
  overdue_count: number;
  active_lease_count: number;
  expired_lease_count: number;
  failed_assessment_count_24h: number;
  lww_resolution_count_24h: number;
  pending_derived_task_count: number;
  failed_derived_task_count: number;
  oldest_open_age_seconds: number;
  oldest_overdue_age_seconds: number;
  collected_at: string;
};

export type ConflictQueueSupporter = {
  profile_id: string;
  profile_name: string;
  strongest_authority: string;
  accepted_at: string;
};

export type ConflictQueuePosition = {
  position_id: string;
  position_key: string;
  disposition: string;
  supporter_count: number;
  supporters_truncated: boolean;
  supporters: ConflictQueueSupporter[];
};

export type ConflictQueueItem = {
  conflict_id: string;
  version: number;
  status: ConflictQueueStatus;
  question: string;
  question_truncated: boolean;
  predicate_key: string;
  predicate_key_truncated: boolean;
  positions_truncated: boolean;
  review_due_at: string;
  next_review_at: string;
  created_at: string;
  updated_at: string;
  attempt_count: number;
  lease_state: ConflictQueueLeaseState;
  lease_until?: string;
  last_failure_class: string;
  positions: ConflictQueuePosition[];
};

export type ConflictQueuePage = {
  summary: ConflictQueueSummary;
  items: ConflictQueueItem[];
  next_cursor: string | null;
};

export type ConflictQueueQuery = {
  status?: "" | ConflictQueueStatus;
  limit?: number;
  cursor?: string;
};
