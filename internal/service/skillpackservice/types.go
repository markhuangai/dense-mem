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
