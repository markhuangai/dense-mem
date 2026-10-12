export type OntologyKind = "entity_class" | "predicate_concept" | "topic" | "assignment" | "evidence_group" | "relationship_group" | "override";
export type SourceKind = "entity" | "predicate" | "evidence" | "relationship";
export type SourceHandle = { kind: SourceKind; id: string; version: number };
export type OntologySource = SourceHandle & { text: string; truncated: boolean; owner_id?: string };
export type OntologyDefinition = { key: string; label: string; description: string; aliases?: string[]; parent_id?: string; base_entity_kind?: string };
export type OntologyRecord = {
  id: string; version: number; kind: OntologyKind; retired: boolean; current: boolean; stale_reason?: string;
  definition?: OntologyDefinition;
  assignment?: { source: SourceHandle; definition_id: string };
  group?: { members: SourceHandle[]; assessment_id?: string };
  override?: { action: "pin_definition" | "set_classification" | "group_together" | "keep_separate"; target_id?: string; definition_id?: string; members?: SourceHandle[] };
};
export type OntologyEdit = {
  action: "define" | "classify" | "group_together" | "keep_separate" | "pin_definition" | "remove_override";
  record_id?: string; expected_version: number; kind?: OntologyKind; definition?: OntologyDefinition;
  definition_id?: string; source?: SourceHandle; members?: SourceHandle[];
};
export type OntologyPublication = {
  id: string; revision: number; existing: boolean; records: Array<{ id: string; version: number }>;
  origin?: string; actor_id?: string; reason?: string; created_at?: string; rollback_of?: string; record_views?: OntologyRecord[];
};
export type OntologyDraft = {
  id: string; version: number; state: "open" | "published" | "discarded"; base_revision: number;
  reason: string; edits: OntologyEdit[]; publication?: OntologyPublication; updated_at: string;
};
export type SaveOntologyDraft = { operation_key: string; expected_version: number; expected_revision: number; reason: string; edits: OntologyEdit[] };
export type OntologyDraftCommand = { operation_key: string; expected_version: number; expected_revision: number; reason: string };
export type OntologyPreview = {
  draft_id: string; draft_version: number; revision: number; changed_records: number; affected_records: number;
  affected_sources: number; requires_reassessment: boolean; examples: OntologySource[]; examples_truncated: boolean; complete: boolean;
};
export type OntologyCoverage = {
  observed_at: string; revision: number; discovery_complete: boolean; coverage_complete: boolean; pending_markers: number;
  counts: { eligible: number; organized: number; pending: number; ambiguous: number; failed: number; budget_deferred: number };
};
export type OntologyRecordPage = { records: OntologyRecord[]; revision: number; next_cursor?: string };
export type OntologySourcePage = { sources: OntologySource[]; revision: number; next_cursor?: string };
export type OntologyDraftPage = { drafts: OntologyDraft[]; next_cursor?: string };
export type OntologyPublicationPage = { publications: OntologyPublication[]; next_revision?: number };
