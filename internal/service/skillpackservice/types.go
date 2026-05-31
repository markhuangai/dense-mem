package skillpackservice

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
)

const (
	SchemaVersion = "dense-mem.skill_pack.v1"

	SourceKindFact           = "source_fact"
	SourceKindValidatedClaim = "source_validated_claim"
	SourceKindManual         = "manual"

	ModeReview  = "review"
	ModeTrusted = "trusted"

	DecisionImportAnyway    = "import_anyway"
	DecisionSkip            = "skip"
	DecisionSupersedeLocal  = "supersede_local"
	defaultHistoryRetention = 30 * 24 * time.Hour
)

// Service is the tool-facing skill-pack workflow.
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
	FragmentCreate fragmentservice.CreateFragmentService
	ClaimCreate    claimservice.CreateClaimService
	ClaimGet       claimservice.GetClaimService
	ClaimList      claimservice.ListClaimsService
	FactPromote    factservice.PromoteClaimService
	FactGet        factservice.GetFactService
	FactList       factservice.ListFactsService
	Graph          GraphStore
	Ledger         ImportLedger
	HistoryDays    int
	HTTPClient     ArtifactHTTPClient
}

type SkillPack struct {
	SchemaVersion string          `json:"schema_version"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Items         []SkillPackItem `json:"items"`
}

type SkillPackItem struct {
	Subject    string `json:"subject"`
	Predicate  string `json:"predicate"`
	Object     string `json:"object"`
	SourceKind string `json:"source_kind"`
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
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	FactIDs     []string        `json:"fact_ids,omitempty"`
	ClaimIDs    []string        `json:"claim_ids,omitempty"`
	ManualItems []SkillPackItem `json:"manual_items,omitempty"`
}

type ExportResult struct {
	Artifact      SkillPack `json:"artifact"`
	CanonicalJSON string    `json:"canonical_json"`
	SHA256        string    `json:"sha256"`
	ItemCount     int       `json:"item_count"`
}

type InspectRequest struct {
	Artifact       *SkillPack `json:"artifact,omitempty"`
	ArtifactJSON   string     `json:"artifact_json,omitempty"`
	URL            string     `json:"url,omitempty"`
	ExpectedSHA256 string     `json:"expected_sha256,omitempty"`
}

type InspectResult struct {
	ArtifactHash      string           `json:"artifact_hash"`
	Name              string           `json:"name"`
	Description       string           `json:"description,omitempty"`
	ItemCount         int              `json:"item_count"`
	Items             []InspectItem    `json:"items"`
	DecisionsRequired []ConflictPrompt `json:"decisions_required,omitempty"`
	SourceURL         string           `json:"source_url,omitempty"`
}

type InspectItem struct {
	Index            int            `json:"index"`
	Item             SkillPackItem  `json:"item"`
	Status           string         `json:"status"`
	Severity         string         `json:"severity,omitempty"`
	MatchingFacts    []FactSummary  `json:"matching_facts,omitempty"`
	ConflictingFacts []FactSummary  `json:"conflicting_facts,omitempty"`
	MatchingClaims   []ClaimSummary `json:"matching_claims,omitempty"`
	Message          string         `json:"message,omitempty"`
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
	Index          int      `json:"index"`
	Reason         string   `json:"reason"`
	FactIDs        []string `json:"fact_ids,omitempty"`
	AllowedActions []string `json:"allowed_actions"`
}

type ImportRequest struct {
	Artifact          *SkillPack         `json:"artifact,omitempty"`
	ArtifactJSON      string             `json:"artifact_json,omitempty"`
	URL               string             `json:"url,omitempty"`
	ExpectedSHA256    string             `json:"expected_sha256,omitempty"`
	Mode              string             `json:"mode"`
	SelectedItems     []int              `json:"selected_items,omitempty"`
	ConflictDecisions []ConflictDecision `json:"conflict_decisions,omitempty"`
}

type ConflictDecision struct {
	Index  int    `json:"index"`
	Action string `json:"action"`
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
