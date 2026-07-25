package skillpackservice

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	SchemaVersion       = "dense-mem.memory_pack.v1"
	LegacySchemaVersion = "dense-mem.skill_pack.v1"

	SourceKindFact           = "source_fact"
	SourceKindValidatedClaim = "source_validated_claim"
	SourceKindManual         = "manual"

	ModeReview  = "review"
	ModeTrusted = "trusted"

	DecisionImportAnyway    = "import_anyway"
	DecisionSkip            = "skip"
	DecisionSupersedeLocal  = "supersede_local"
	DecisionDemoteToClaim   = "demote_to_claim"
	defaultHistoryRetention = 30 * 24 * time.Hour
)

// Service is the tool-facing memory-pack workflow.
type Service interface {
	FindCandidates(ctx context.Context, profileID string, req FindCandidatesRequest) (*FindCandidatesResult, error)
	Export(ctx context.Context, profileID string, req ExportRequest) (*ExportResult, error)
	Inspect(ctx context.Context, profileID string, req InspectRequest) (*InspectResult, error)
	Import(ctx context.Context, profileID string, req ImportRequest) (*ImportResult, error)
	Rollback(ctx context.Context, profileID string, req RollbackRequest) (*RollbackResult, error)
}

type ImportLedger interface {
	CreateImport(ctx context.Context, record domain.SkillPackImport) error
	UpdateImportStatus(ctx context.Context, teamID, importID, status string, appliedCount, skippedCount int, summary map[string]any) error
	MarkRolledBack(ctx context.Context, teamID, importID string) error
	GetImport(ctx context.Context, teamID, importID string) (*domain.SkillPackImport, error)
	AppendChange(ctx context.Context, change domain.SkillPackImportChange) error
	ListChanges(ctx context.Context, teamID, importID string) ([]domain.SkillPackImportChange, error)
}

type Dependencies struct {
	Ledger      ImportLedger
	HistoryDays int
	HTTPClient  ArtifactHTTPClient
}

type ConflictDecider interface {
	Decide(ctx context.Context, req ConflictDecisionRequest) (ConflictDecisionResult, error)
}

type SkillPack struct {
	SchemaVersion string            `json:"schema_version"`
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	ExportedAt    *time.Time        `json:"exported_at,omitempty"`
	Items         []SkillPackItem   `json:"items"`
	Support       *SkillPackSupport `json:"support,omitempty"`
}

type SkillPackItem struct {
	Subject            string   `json:"subject"`
	Predicate          string   `json:"predicate"`
	Object             string   `json:"object"`
	SourceKind         string   `json:"source_kind"`
	SourceID           string   `json:"source_id,omitempty"`
	SupportClaimIDs    []string `json:"support_claim_ids,omitempty"`
	SupportFragmentIDs []string `json:"support_fragment_ids,omitempty"`
}

type SkillPackSupport struct {
	Claims    []SkillPackSupportClaim    `json:"claims,omitempty"`
	Fragments []SkillPackSupportFragment `json:"fragments,omitempty"`
}

type SkillPackSupportClaim struct {
	ClaimID     string   `json:"claim_id"`
	Subject     string   `json:"subject"`
	Predicate   string   `json:"predicate"`
	Object      string   `json:"object"`
	SupportedBy []string `json:"supported_by,omitempty"`
}

type SkillPackSupportFragment struct {
	FragmentID    string   `json:"fragment_id"`
	Content       string   `json:"content"`
	Source        string   `json:"source,omitempty"`
	SourceType    string   `json:"source_type,omitempty"`
	Authority     string   `json:"authority,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	SourceQuality *float64 `json:"source_quality,omitempty"`
}

type Candidate struct {
	ID         string        `json:"id"`
	Type       string        `json:"type"`
	Item       SkillPackItem `json:"item"`
	Snippet    string        `json:"snippet,omitempty"`
	RecordedAt time.Time     `json:"recorded_at,omitempty"`
}

type FindCandidatesRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type FindCandidatesResult struct {
	Candidates []Candidate `json:"candidates"`
}

type ExportRequest struct {
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	FactIDs        []string        `json:"fact_ids,omitempty"`
	ClaimIDs       []string        `json:"claim_ids,omitempty"`
	ManualItems    []SkillPackItem `json:"manual_items,omitempty"`
	IncludeSupport *bool           `json:"include_support,omitempty"`
}

type ExportResult struct {
	Artifact      SkillPack `json:"artifact"`
	CanonicalJSON string    `json:"canonical_json"`
	SHA256        string    `json:"sha256"`
	ItemCount     int       `json:"item_count"`
	Filename      string    `json:"filename"`
	ContentType   string    `json:"content_type"`
}

type InspectRequest struct {
	Artifact           *SkillPack `json:"artifact,omitempty"`
	ArtifactJSON       string     `json:"artifact_json,omitempty"`
	URL                string     `json:"url,omitempty"`
	ExpectedSHA256     string     `json:"expected_sha256,omitempty"`
	RecommendDecisions bool       `json:"recommend_decisions,omitempty"`
}

type InspectResult struct {
	ArtifactHash      string           `json:"artifact_hash"`
	Name              string           `json:"name"`
	Description       string           `json:"description,omitempty"`
	ItemCount         int              `json:"item_count"`
	SupportSummary    *SupportSummary  `json:"support_summary,omitempty"`
	Items             []InspectItem    `json:"items"`
	DecisionsRequired []ConflictPrompt `json:"decisions_required,omitempty"`
	SourceURL         string           `json:"source_url,omitempty"`
}

type SupportSummary struct {
	ClaimCount    int `json:"claim_count"`
	FragmentCount int `json:"fragment_count"`
}

type InspectItem struct {
	Index             int            `json:"index"`
	Item              SkillPackItem  `json:"item"`
	Status            string         `json:"status"`
	Severity          string         `json:"severity,omitempty"`
	MatchingFacts     []FactSummary  `json:"matching_facts,omitempty"`
	ConflictingFacts  []FactSummary  `json:"conflicting_facts,omitempty"`
	SupersededMatches []FactSummary  `json:"superseded_matches,omitempty"`
	MatchingClaims    []ClaimSummary `json:"matching_claims,omitempty"`
	Message           string         `json:"message,omitempty"`
}

type FactSummary struct {
	FactID    string `json:"fact_id"`
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
	Status    string `json:"status"`
}

type ClaimSummary struct {
	ClaimID   string `json:"claim_id"`
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
	Status    string `json:"status"`
}

type ConflictPrompt struct {
	Index             int                     `json:"index"`
	Reason            string                  `json:"reason"`
	FactIDs           []string                `json:"fact_ids,omitempty"`
	SupersededFactIDs []string                `json:"superseded_fact_ids,omitempty"`
	AllowedActions    []string                `json:"allowed_actions"`
	Recommendation    *DecisionRecommendation `json:"recommendation,omitempty"`
}

type ImportRequest struct {
	Artifact            *SkillPack         `json:"artifact,omitempty"`
	ArtifactJSON        string             `json:"artifact_json,omitempty"`
	URL                 string             `json:"url,omitempty"`
	ExpectedSHA256      string             `json:"expected_sha256,omitempty"`
	Mode                string             `json:"mode"`
	SelectedItems       []int              `json:"selected_items,omitempty"`
	ConflictDecisions   []ConflictDecision `json:"conflict_decisions,omitempty"`
	AutoDecideConflicts bool               `json:"auto_decide_conflicts,omitempty"`
}

type ConflictDecision struct {
	Index  int    `json:"index"`
	Action string `json:"action"`
}

type ConflictDecisionRequest struct {
	ProfileID    string         `json:"profile_id"`
	ArtifactHash string         `json:"artifact_hash"`
	Mode         string         `json:"mode"`
	SourceURL    string         `json:"source_url,omitempty"`
	Item         SkillPackItem  `json:"item"`
	Inspection   InspectItem    `json:"inspection"`
	Prompt       ConflictPrompt `json:"prompt"`
}

type ConflictDecisionResult struct {
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
	Model      string  `json:"model,omitempty"`
	RawJSON    string  `json:"raw_json,omitempty"`
}

type DecisionRecommendation struct {
	Action     string  `json:"action,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Rationale  string  `json:"rationale,omitempty"`
	Model      string  `json:"model,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type ImportItemResult struct {
	Index     int           `json:"index"`
	Item      SkillPackItem `json:"item"`
	Status    string        `json:"status"`
	ClaimID   string        `json:"claim_id,omitempty"`
	FactID    string        `json:"fact_id,omitempty"`
	Error     string        `json:"error,omitempty"`
	Decision  string        `json:"decision,omitempty"`
	Conflicts []FactSummary `json:"conflicts,omitempty"`
}

type ImportResult struct {
	ImportID          string             `json:"import_id,omitempty"`
	ArtifactHash      string             `json:"artifact_hash"`
	Mode              string             `json:"mode"`
	Status            string             `json:"status"`
	AppliedCount      int                `json:"applied_count"`
	SkippedCount      int                `json:"skipped_count"`
	Error             string             `json:"error,omitempty"`
	Items             []ImportItemResult `json:"items,omitempty"`
	DecisionsRequired []ConflictPrompt   `json:"decisions_required,omitempty"`
}

type RollbackRequest struct {
	ImportID string `json:"import_id"`
}

type RollbackResult struct {
	ImportID      string   `json:"import_id"`
	Status        string   `json:"status"`
	RevertedCount int      `json:"reverted_count"`
	Conflicts     []string `json:"conflicts,omitempty"`
}
