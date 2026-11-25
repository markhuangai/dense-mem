export type TopicKind = "topic" | "assignment" | "evidence_group" | "override";
export type SourceKind = "evidence";
export type SourceHandle = { kind: SourceKind; id: string; version: number };
export type TopicSource = SourceHandle & { text: string; truncated: boolean; owner_id?: string };
export type TopicDefinition = { key: string; label: string; description: string; aliases?: string[]; parent_id?: string };
export type TopicRecord = {
  community_parent_id?: string; community_placement?: string;
  id: string; version: number; edit_version?: number; kind: TopicKind; retired: boolean; current: boolean; stale_reason?: string;
  definition?: TopicDefinition;
  assignment?: { source: SourceHandle; definition_id: string };
  group?: { members: SourceHandle[]; assessment_id?: string };
  override?: { action: "pin_definition" | "set_classification" | "group_together" | "keep_separate"; target_id?: string; definition_id?: string; members?: SourceHandle[] };
};
export type TopicEdit = {
  action: "define" | "classify" | "group_together" | "keep_separate" | "pin_definition" | "remove_override";
  record_id?: string; expected_version: number; kind?: TopicKind; definition?: TopicDefinition;
  definition_id?: string; source?: SourceHandle; members?: SourceHandle[];
};
export type TopicPublication = {
  id: string; revision: number; existing: boolean; records: Array<{ id: string; version: number }>;
  origin?: string; actor_id?: string; reason?: string; created_at?: string; rollback_of?: string; record_views?: TopicRecord[];
};
export type TopicDraft = {
  id: string; version: number; state: "open" | "published" | "discarded"; base_revision: number;
  reason: string; edits: TopicEdit[]; publication?: TopicPublication; updated_at: string;
};
export type SaveTopicEdits = { operation_key: string; reason: string; edits: TopicEdit[] };
export type TopicPreview = {
  draft_id: string; draft_version: number; revision: number; changed_records: number; affected_records: number;
  affected_sources: number; requires_reassessment: boolean; examples: TopicSource[]; examples_truncated: boolean; complete: boolean;
};
export type TopicCoverage = {
  observed_at: string; revision: number; discovery_complete: boolean; coverage_complete: boolean; pending_markers: number;
  counts: { eligible: number; organized: number; pending: number; ambiguous: number; failed: number; uncategorized?: number };
};
export type TopicRecordPage = { records: TopicRecord[]; revision: number; next_cursor?: string };
export type TopicSourcePage = { sources: TopicSource[]; revision: number; next_cursor?: string };
export type TopicDraftPage = { drafts: TopicDraft[]; next_cursor?: string };
export type TopicPublicationPage = { publications: TopicPublication[]; next_revision?: number };
