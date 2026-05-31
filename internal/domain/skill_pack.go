package domain

import "time"

const (
	SkillPackImportStatusInspecting  = "inspecting"
	SkillPackImportStatusApplied     = "applied"
	SkillPackImportStatusRolledBack  = "rolled_back"
	SkillPackImportStatusNeedsReview = "needs_review"

	SkillPackChangeActionCreated    = "created"
	SkillPackChangeActionUpdated    = "updated"
	SkillPackChangeActionSuperseded = "superseded"
	SkillPackChangeActionLinked     = "linked"
)

// SkillPackImport records one import batch for audit and rollback.
type SkillPackImport struct {
	ImportID           string         `json:"import_id"`
	TeamID             string         `json:"team_id"`
	ArtifactHash       string         `json:"artifact_hash"`
	SourceURL          string         `json:"source_url,omitempty"`
	SchemaVersion      string         `json:"schema_version"`
	Name               string         `json:"name"`
	Mode               string         `json:"mode"`
	Status             string         `json:"status"`
	ItemCount          int            `json:"item_count"`
	AppliedCount       int            `json:"applied_count"`
	SkippedCount       int            `json:"skipped_count"`
	Summary            map[string]any `json:"summary,omitempty"`
	RetentionExpiresAt time.Time      `json:"retention_expires_at"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	CompletedAt        *time.Time     `json:"completed_at,omitempty"`
	RolledBackAt       *time.Time     `json:"rolled_back_at,omitempty"`
}

// SkillPackImportChange records a graph mutation caused by one import batch.
type SkillPackImportChange struct {
	ChangeID    string         `json:"change_id"`
	ImportID    string         `json:"import_id"`
	TeamID      string         `json:"team_id"`
	EntityType  string         `json:"entity_type"`
	EntityID    string         `json:"entity_id"`
	Action      string         `json:"action"`
	BeforeState map[string]any `json:"before_state,omitempty"`
	AfterState  map[string]any `json:"after_state,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}
