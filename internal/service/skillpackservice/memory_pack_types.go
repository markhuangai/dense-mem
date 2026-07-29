package skillpackservice

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

const (
	MemoryPackFormat    = "dense-mem.memory-pack.v2.4"
	memoryPackV23Format = "dense-mem.memory-pack.v2.3"

	MemoryPackSourceType = "memory_pack"
	MemoryPackLabel      = "memory_pack_import"
)

var ErrMemoryPackAuthContext = errors.New("memory pack: authenticated actor context is required")

type MemoryPackService interface {
	FindCandidates(ctx context.Context, req FindCandidatesRequest) (*FindCandidatesResult, error)
	Export(ctx context.Context, req ExportRequest) (*ExportResult, error)
	Inspect(ctx context.Context, req InspectRequest) (*InspectResult, error)
	Import(ctx context.Context, req ImportRequest) (*ImportResult, error)
	Rollback(ctx context.Context, req RollbackRequest) (*RollbackResult, error)
}

type MemoryPackDependencies struct {
	Semantic    MemoryPackSemanticReader
	Remember    memoryservice.RememberService
	Ledger      ImportLedger
	HistoryDays int
	HTTPClient  ArtifactHTTPClient
	Now         func() time.Time
}

type MemoryPackSemanticReader interface {
	SemanticGraph(ctx context.Context, input repository.SemanticGraphQuery) (*repository.SemanticGraphSnapshot, error)
	TraceRelationship(ctx context.Context, input repository.TraceRelationshipInput) (*repository.RelationshipTraceResult, error)
}

type memoryPackService struct {
	deps   MemoryPackDependencies
	retain time.Duration
	now    func() time.Time
}

type FindCandidatesRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type FindCandidatesResult struct {
	Candidates []MemoryPackCandidate `json:"candidates"`
}

type MemoryPackCandidate struct {
	RelationshipID   string `json:"relationship_id"`
	PredicateKey     string `json:"predicate_key"`
	SubjectEntityID  string `json:"subject_entity_id,omitempty"`
	Subject          string `json:"subject"`
	ObjectEntityID   string `json:"object_entity_id,omitempty"`
	ObjectValueID    string `json:"object_value_id,omitempty"`
	ObjectValueType  string `json:"object_value_type,omitempty"`
	Object           string `json:"object"`
	Polarity         string `json:"polarity,omitempty"`
	SupportCount     int    `json:"support_count"`
	SourceGroupCount int    `json:"source_group_count"`
}

type ExportRequest struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	RelationshipIDs []string `json:"relationship_ids,omitempty"`
	IncludeSupport  *bool    `json:"include_evidence,omitempty"`
}

type ExportResult struct {
	Artifact      MemoryPackArtifact `json:"artifact"`
	CanonicalJSON string             `json:"canonical_json"`
	SHA256        string             `json:"sha256"`
	ItemCount     int                `json:"item_count"`
	Filename      string             `json:"filename"`
	ContentType   string             `json:"content_type"`
	Omissions     []string           `json:"omissions,omitempty"`
}

type InspectRequest struct {
	ArtifactJSON       string `json:"artifact_json,omitempty"`
	URL                string `json:"url,omitempty"`
	ExpectedSHA256     string `json:"expected_sha256,omitempty"`
	Mode               string `json:"mode,omitempty"`
	RecommendDecisions bool   `json:"recommend_decisions,omitempty"`
}

type InspectResult struct {
	ArtifactHash      string           `json:"artifact_hash"`
	Format            string           `json:"format"`
	Name              string           `json:"name"`
	Description       string           `json:"description,omitempty"`
	ItemCount         int              `json:"item_count"`
	SelectedCount     int              `json:"selected_count,omitempty"`
	SupportSummary    SupportSummary   `json:"support_summary"`
	Items             []InspectItem    `json:"items"`
	DecisionsRequired []ConflictPrompt `json:"decisions_required,omitempty"`
	SourceURL         string           `json:"source_url,omitempty"`
	Metadata          map[string]any   `json:"metadata,omitempty"`
}

type SupportSummary struct {
	EvidenceCount int `json:"evidence_count"`
	SupportCount  int `json:"support_count"`
}

type InspectItem struct {
	ItemID               string   `json:"item_id"`
	SourceRelationshipID string   `json:"source_relationship_id,omitempty"`
	Status               string   `json:"status"`
	Severity             string   `json:"severity,omitempty"`
	Message              string   `json:"message,omitempty"`
	PredicateKey         string   `json:"predicate_key,omitempty"`
	Subject              string   `json:"subject,omitempty"`
	Object               string   `json:"object,omitempty"`
	SupportEvidenceIDs   []string `json:"support_evidence_ids,omitempty"`
}

type ConflictPrompt struct {
	ItemID         string   `json:"item_id"`
	Reason         string   `json:"reason"`
	AllowedActions []string `json:"allowed_actions"`
}

type ImportRequest struct {
	ArtifactJSON      string               `json:"artifact_json,omitempty"`
	URL               string               `json:"url,omitempty"`
	ExpectedSHA256    string               `json:"expected_sha256,omitempty"`
	Mode              string               `json:"mode"`
	SelectedItemIDs   []string             `json:"selected_item_ids,omitempty"`
	ConflictDecisions []ImportItemDecision `json:"conflict_decisions,omitempty"`
}

type ImportItemDecision struct {
	ItemID string `json:"item_id"`
	Action string `json:"decision"`
}

type ImportResult struct {
	ImportID          string             `json:"import_id,omitempty"`
	ArtifactHash      string             `json:"artifact_hash"`
	Mode              string             `json:"mode"`
	Status            string             `json:"status"`
	IngestID          string             `json:"ingest_id,omitempty"`
	CheckAfterSeconds int                `json:"check_after_seconds,omitempty"`
	StatusTool        string             `json:"status_tool,omitempty"`
	AppliedCount      int                `json:"applied_count"`
	SkippedCount      int                `json:"skipped_count"`
	Error             string             `json:"error,omitempty"`
	Items             []ImportItemResult `json:"items,omitempty"`
	DecisionsRequired []ConflictPrompt   `json:"decisions_required,omitempty"`
}

type ImportItemResult struct {
	ItemID               string `json:"item_id"`
	SourceRelationshipID string `json:"source_relationship_id,omitempty"`
	Status               string `json:"status"`
	PlacementItemID      string `json:"placement_item_id,omitempty"`
	EvidenceIndex        int    `json:"evidence_index"`
	Decision             string `json:"decision,omitempty"`
	Error                string `json:"error,omitempty"`
}

type RollbackRequest struct {
	ImportID    string `json:"import_id"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Confirm     bool   `json:"confirm,omitempty"`
	ImpactToken string `json:"impact_token,omitempty"`
}

type RollbackResult struct {
	ImportID                string   `json:"import_id"`
	Status                  string   `json:"status"`
	DryRun                  bool     `json:"dry_run"`
	RevertedCount           int      `json:"reverted_count"`
	Conflicts               []string `json:"conflicts,omitempty"`
	ImpactSummary           string   `json:"impact_summary,omitempty"`
	ImpactToken             string   `json:"impact_token,omitempty"`
	AffectedRelationshipIDs []string `json:"affected_relationship_ids,omitempty"`
}

type MemoryPackArtifact struct {
	Format              string                      `json:"format"`
	PackID              string                      `json:"pack_id"`
	Name                string                      `json:"name"`
	Description         string                      `json:"description,omitempty"`
	CreatedAt           string                      `json:"created_at"`
	Source              MemoryPackSource            `json:"source"`
	Relationships       []MemoryPackRelationship    `json:"relationships"`
	Evidence            []MemoryPackEvidence        `json:"evidence,omitempty"`
	EvidenceSupports    []MemoryPackEvidenceSupport `json:"evidence_supports,omitempty"`
	Extensions          map[string]any              `json:"extensions,omitempty"`
	ContentSHA256       string                      `json:"content_sha256,omitempty"`
	LegacySchemaVersion string                      `json:"legacy_schema_version,omitempty"`
}

type MemoryPackSource struct {
	InstallationID string `json:"installation_id,omitempty"`
	TeamID         string `json:"team_id,omitempty"`
	ExportedBy     string `json:"exported_by,omitempty"`
}

type MemoryPackRelationship struct {
	ItemID                    string             `json:"item_id"`
	SourceRelationshipID      string             `json:"source_relationship_id,omitempty"`
	SourceRelationshipVersion int                `json:"source_relationship_version,omitempty"`
	SourceOwnerProfileID      string             `json:"source_owner_profile_id,omitempty"`
	Subject                   MemoryPackEndpoint `json:"subject"`
	PredicateKey              string             `json:"predicate_key"`
	PredicateVersion          int                `json:"predicate_version"`
	Object                    MemoryPackEndpoint `json:"object"`
	Polarity                  string             `json:"polarity,omitempty"`
	ScopeKey                  string             `json:"scope_key,omitempty"`
	Status                    string             `json:"status,omitempty"`
	SupportEvidenceIDs        []string           `json:"support_evidence_ids,omitempty"`
	Metadata                  map[string]any     `json:"metadata,omitempty"`
}

type MemoryPackEndpoint struct {
	Ref         string `json:"ref"`
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	ValueType   string `json:"value_type,omitempty"`
	Value       string `json:"value,omitempty"`
}

type MemoryPackEvidence struct {
	EvidenceID       string         `json:"evidence_id"`
	Content          string         `json:"content"`
	ContentHash      string         `json:"content_hash,omitempty"`
	SourceType       string         `json:"source_type,omitempty"`
	Authority        string         `json:"authority,omitempty"`
	SourceRef        string         `json:"source_ref,omitempty"`
	SourceKey        string         `json:"source_key,omitempty"`
	SourceRevisionID string         `json:"source_revision_id,omitempty"`
	Labels           []string       `json:"labels,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type MemoryPackEvidenceSupport struct {
	RelationshipItemID string         `json:"relationship_item_id"`
	EvidenceID         string         `json:"evidence_id"`
	Quote              string         `json:"quote,omitempty"`
	SpanStart          int            `json:"span_start,omitempty"`
	SpanEnd            int            `json:"span_end,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

type loadedArtifact struct {
	artifact MemoryPackArtifact
	hash     string
	format   string
	source   string
	legacy   bool
}

type memoryPackArtifactV23 struct {
	Format              string                          `json:"format"`
	PackID              string                          `json:"pack_id"`
	Name                string                          `json:"name"`
	Description         string                          `json:"description,omitempty"`
	CreatedAt           string                          `json:"created_at"`
	Source              MemoryPackSource                `json:"source"`
	Relationships       []memoryPackRelationshipV23     `json:"relationships"`
	EvidenceFragments   []memoryPackEvidenceFragmentV23 `json:"evidence_fragments,omitempty"`
	EvidenceSupports    []memoryPackEvidenceSupportV23  `json:"evidence_supports,omitempty"`
	Extensions          map[string]any                  `json:"extensions,omitempty"`
	ContentSHA256       string                          `json:"content_sha256,omitempty"`
	LegacySchemaVersion string                          `json:"legacy_schema_version,omitempty"`
}

type memoryPackRelationshipV23 struct {
	ItemID                    string             `json:"item_id"`
	SourceRelationshipID      string             `json:"source_relationship_id,omitempty"`
	SourceRelationshipVersion int                `json:"source_relationship_version,omitempty"`
	SourceOwnerProfileID      string             `json:"source_owner_profile_id,omitempty"`
	Subject                   MemoryPackEndpoint `json:"subject"`
	PredicateKey              string             `json:"predicate_key"`
	PredicateVersion          int                `json:"predicate_version"`
	Object                    MemoryPackEndpoint `json:"object"`
	Polarity                  string             `json:"polarity,omitempty"`
	ScopeKey                  string             `json:"scope_key,omitempty"`
	Status                    string             `json:"status,omitempty"`
	SupportFragmentIDs        []string           `json:"support_fragment_ids,omitempty"`
	Metadata                  map[string]any     `json:"metadata,omitempty"`
}

type memoryPackEvidenceFragmentV23 struct {
	FragmentID       string         `json:"fragment_id"`
	Content          string         `json:"content"`
	ContentHash      string         `json:"content_hash,omitempty"`
	SourceType       string         `json:"source_type,omitempty"`
	Authority        string         `json:"authority,omitempty"`
	SourceRef        string         `json:"source_ref,omitempty"`
	SourceKey        string         `json:"source_key,omitempty"`
	SourceRevisionID string         `json:"source_revision_id,omitempty"`
	Labels           []string       `json:"labels,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type memoryPackEvidenceSupportV23 struct {
	RelationshipItemID string         `json:"relationship_item_id"`
	FragmentID         string         `json:"fragment_id"`
	Quote              string         `json:"quote,omitempty"`
	SpanStart          int            `json:"span_start,omitempty"`
	SpanEnd            int            `json:"span_end,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}
