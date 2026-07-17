package skillpackservice

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

const (
	V2MemoryPackFormat = "dense-mem.memory-pack.v2"

	v2MemoryPackSourceType = "memory_pack"
	v2MemoryPackLabel      = "memory_pack_import"
)

var ErrV2MemoryPackAuthContext = errors.New("v2 memory pack: authenticated actor context is required")

type V2Service interface {
	FindCandidatesV2(ctx context.Context, req V2FindCandidatesRequest) (*V2FindCandidatesResult, error)
	ExportV2(ctx context.Context, req V2ExportRequest) (*V2ExportResult, error)
	InspectV2(ctx context.Context, req V2InspectRequest) (*V2InspectResult, error)
	ImportV2(ctx context.Context, req V2ImportRequest) (*V2ImportResult, error)
	RollbackV2(ctx context.Context, req V2RollbackRequest) (*V2RollbackResult, error)
}

type V2Dependencies struct {
	Semantic    V2SemanticReader
	Remember    memoryservice.V2RememberService
	Ledger      ImportLedger
	HistoryDays int
	HTTPClient  ArtifactHTTPClient
	Now         func() time.Time
}

type V2SemanticReader interface {
	SemanticGraph(ctx context.Context, input repository.V2SemanticGraphQuery) (*repository.V2SemanticGraphSnapshot, error)
	TraceRelationship(ctx context.Context, input repository.V2TraceRelationshipInput) (*repository.V2RelationshipTraceResult, error)
}

type v2Service struct {
	deps   V2Dependencies
	retain time.Duration
	now    func() time.Time
}

type V2FindCandidatesRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type V2FindCandidatesResult struct {
	Candidates []V2MemoryPackCandidate `json:"candidates"`
}

type V2MemoryPackCandidate struct {
	RelationshipID   string `json:"relationship_id"`
	PredicateKey     string `json:"predicate_key"`
	Subject          string `json:"subject"`
	Object           string `json:"object"`
	Tier             string `json:"tier"`
	SupportCount     int    `json:"support_count"`
	SourceGroupCount int    `json:"source_group_count"`
}

type V2ExportRequest struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	RelationshipIDs []string `json:"relationship_ids,omitempty"`
	IncludeSupport  *bool    `json:"include_support,omitempty"`
}

type V2ExportResult struct {
	Artifact      V2MemoryPackArtifact `json:"artifact"`
	CanonicalJSON string               `json:"canonical_json"`
	SHA256        string               `json:"sha256"`
	ItemCount     int                  `json:"item_count"`
	Filename      string               `json:"filename"`
	ContentType   string               `json:"content_type"`
	Omissions     []string             `json:"omissions,omitempty"`
}

type V2InspectRequest struct {
	ArtifactJSON       string `json:"artifact_json,omitempty"`
	URL                string `json:"url,omitempty"`
	ExpectedSHA256     string `json:"expected_sha256,omitempty"`
	RecommendDecisions bool   `json:"recommend_decisions,omitempty"`
}

type V2InspectResult struct {
	ArtifactHash      string             `json:"artifact_hash"`
	Format            string             `json:"format"`
	Name              string             `json:"name"`
	Description       string             `json:"description,omitempty"`
	ItemCount         int                `json:"item_count"`
	SelectedCount     int                `json:"selected_count,omitempty"`
	SupportSummary    V2SupportSummary   `json:"support_summary"`
	Items             []V2InspectItem    `json:"items"`
	DecisionsRequired []V2ConflictPrompt `json:"decisions_required,omitempty"`
	SourceURL         string             `json:"source_url,omitempty"`
	Metadata          map[string]any     `json:"metadata,omitempty"`
}

type V2SupportSummary struct {
	FragmentCount int `json:"fragment_count"`
	SupportCount  int `json:"support_count"`
}

type V2InspectItem struct {
	ItemID               string   `json:"item_id"`
	SourceRelationshipID string   `json:"source_relationship_id,omitempty"`
	Status               string   `json:"status"`
	Severity             string   `json:"severity,omitempty"`
	Message              string   `json:"message,omitempty"`
	PredicateKey         string   `json:"predicate_key,omitempty"`
	Subject              string   `json:"subject,omitempty"`
	Object               string   `json:"object,omitempty"`
	SupportFragmentIDs   []string `json:"support_fragment_ids,omitempty"`
}

type V2ConflictPrompt struct {
	ItemID         string   `json:"item_id"`
	Reason         string   `json:"reason"`
	AllowedActions []string `json:"allowed_actions"`
}

type V2ImportRequest struct {
	ArtifactJSON      string                 `json:"artifact_json,omitempty"`
	URL               string                 `json:"url,omitempty"`
	ExpectedSHA256    string                 `json:"expected_sha256,omitempty"`
	Mode              string                 `json:"mode"`
	SelectedItemIDs   []string               `json:"selected_item_ids,omitempty"`
	ConflictDecisions []V2ImportItemDecision `json:"conflict_decisions,omitempty"`
}

type V2ImportItemDecision struct {
	ItemID string `json:"item_id"`
	Action string `json:"action"`
}

type V2ImportResult struct {
	ImportID          string               `json:"import_id,omitempty"`
	ArtifactHash      string               `json:"artifact_hash"`
	Mode              string               `json:"mode"`
	Status            string               `json:"status"`
	IngestID          string               `json:"ingest_id,omitempty"`
	CheckAfterSeconds int                  `json:"check_after_seconds,omitempty"`
	StatusTool        string               `json:"status_tool,omitempty"`
	AppliedCount      int                  `json:"applied_count"`
	SkippedCount      int                  `json:"skipped_count"`
	Error             string               `json:"error,omitempty"`
	Items             []V2ImportItemResult `json:"items,omitempty"`
	DecisionsRequired []V2ConflictPrompt   `json:"decisions_required,omitempty"`
}

type V2ImportItemResult struct {
	ItemID               string `json:"item_id"`
	SourceRelationshipID string `json:"source_relationship_id,omitempty"`
	Status               string `json:"status"`
	PlacementItemID      string `json:"placement_item_id,omitempty"`
	EvidenceIndex        int    `json:"evidence_index"`
	Decision             string `json:"decision,omitempty"`
	Error                string `json:"error,omitempty"`
}

type V2RollbackRequest struct {
	ImportID string `json:"import_id"`
	DryRun   bool   `json:"dry_run,omitempty"`
	Confirm  bool   `json:"confirm,omitempty"`
}

type V2RollbackResult struct {
	ImportID      string   `json:"import_id"`
	Status        string   `json:"status"`
	DryRun        bool     `json:"dry_run"`
	RevertedCount int      `json:"reverted_count"`
	Conflicts     []string `json:"conflicts,omitempty"`
	ImpactSummary string   `json:"impact_summary,omitempty"`
}

type V2MemoryPackArtifact struct {
	Format              string                         `json:"format"`
	PackID              string                         `json:"pack_id"`
	Name                string                         `json:"name"`
	Description         string                         `json:"description,omitempty"`
	CreatedAt           string                         `json:"created_at"`
	Source              V2MemoryPackSource             `json:"source"`
	Relationships       []V2MemoryPackRelationship     `json:"relationships"`
	EvidenceFragments   []V2MemoryPackEvidenceFragment `json:"evidence_fragments,omitempty"`
	EvidenceSupports    []V2MemoryPackEvidenceSupport  `json:"evidence_supports,omitempty"`
	Extensions          map[string]any                 `json:"extensions,omitempty"`
	ContentSHA256       string                         `json:"content_sha256,omitempty"`
	LegacySchemaVersion string                         `json:"legacy_schema_version,omitempty"`
}

type V2MemoryPackSource struct {
	InstallationID string `json:"installation_id,omitempty"`
	TeamID         string `json:"team_id,omitempty"`
	ExportedBy     string `json:"exported_by,omitempty"`
}

type V2MemoryPackRelationship struct {
	ItemID                    string               `json:"item_id"`
	SourceRelationshipID      string               `json:"source_relationship_id,omitempty"`
	SourceRelationshipVersion int                  `json:"source_relationship_version,omitempty"`
	SourceOwnerProfileID      string               `json:"source_owner_profile_id,omitempty"`
	Subject                   V2MemoryPackEndpoint `json:"subject"`
	PredicateKey              string               `json:"predicate_key"`
	PredicateVersion          int                  `json:"predicate_version"`
	Object                    V2MemoryPackEndpoint `json:"object"`
	Polarity                  string               `json:"polarity,omitempty"`
	ScopeKey                  string               `json:"scope_key,omitempty"`
	Tier                      string               `json:"tier,omitempty"`
	Status                    string               `json:"status,omitempty"`
	SupportFragmentIDs        []string             `json:"support_fragment_ids,omitempty"`
	Metadata                  map[string]any       `json:"metadata,omitempty"`
}

type V2MemoryPackEndpoint struct {
	Ref         string `json:"ref"`
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	ValueType   string `json:"value_type,omitempty"`
	Value       string `json:"value,omitempty"`
}

type V2MemoryPackEvidenceFragment struct {
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

type V2MemoryPackEvidenceSupport struct {
	RelationshipItemID string         `json:"relationship_item_id"`
	FragmentID         string         `json:"fragment_id"`
	Quote              string         `json:"quote,omitempty"`
	SpanStart          int            `json:"span_start,omitempty"`
	SpanEnd            int            `json:"span_end,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

type v2LoadedArtifact struct {
	artifact V2MemoryPackArtifact
	hash     string
	source   string
	legacy   bool
}
