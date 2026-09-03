import type {
  EvidenceConflict,
  EvidenceConflictDetail,
  EvidenceConflictListPage,
  EvidenceConflictListQuery,
} from "./evidence-conflict-api-types";

type RequestEnvelope = <T>(path: string, options?: { method?: string; body?: unknown }) => Promise<T>;

export function listEvidenceConflicts(request: RequestEnvelope, teamId: string, query: EvidenceConflictListQuery = {}): Promise<EvidenceConflictListPage> {
  const params = new URLSearchParams();
  if (query.status) params.set("status", query.status);
  if (query.limit !== undefined) params.set("limit", String(query.limit));
  if (query.cursor) params.set("cursor", query.cursor);
  const suffix = params.toString() ? `?${params.toString()}` : "";
  return request<EvidenceConflictListPage>(`/teams/${encodeURIComponent(teamId)}/evidence-conflicts${suffix}`);
}

export function getEvidenceConflict(request: RequestEnvelope, teamId: string, conflictId: string, eventLimit?: number, eventCursor?: string): Promise<EvidenceConflictDetail> {
  const params = new URLSearchParams();
  if (eventLimit !== undefined) params.set("event_limit", String(eventLimit));
  if (eventCursor) params.set("event_cursor", eventCursor);
  const suffix = params.toString() ? `?${params.toString()}` : "";
  return request<EvidenceConflictDetail>(`/teams/${encodeURIComponent(teamId)}/evidence-conflicts/${encodeURIComponent(conflictId)}${suffix}`);
}

export function resolveEvidenceConflict(request: RequestEnvelope, teamId: string, conflictId: string, input: { expected_version: number; decision: "resolve" | "dismiss"; reason: string; preferred_position_id?: string }): Promise<{ conflict: EvidenceConflict }> {
  return request<{ conflict: EvidenceConflict }>(`/teams/${encodeURIComponent(teamId)}/evidence-conflicts/${encodeURIComponent(conflictId)}/resolution`, { method: "POST", body: input });
}
