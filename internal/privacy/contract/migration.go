package contract

import (
	"context"
	"errors"
)

const PrivateMemoryPlannerVersion = "private-memory.v2.7.0.1"

var ErrPrivateMemoryMigrationBlocked = errors.New("private memory migration preflight found blockers")

type PrivateMemoryMigrationReport struct {
	Status             string                        `json:"status"`
	PlannerVersion     string                        `json:"planner_version"`
	SourceFingerprint  string                        `json:"source_fingerprint"`
	Tables             []PrivateMemoryMigrationTable `json:"tables"`
	Mappings           []PrivateMemoryWriterMapping  `json:"ownership_mappings"`
	MappingCount       int64                         `json:"ownership_mapping_count"`
	MappingsTruncated  bool                          `json:"ownership_mappings_truncated"`
	SupportCopyCount   int64                         `json:"support_copy_requirements"`
	HandleMappingCount int64                         `json:"handle_mapping_requirements"`
	Blockers           []string                      `json:"blockers"`
}

type PrivateMemoryMigrationTable struct {
	Name        string `json:"name"`
	PrivateRows int64  `json:"private_rows"`
}

type PrivateMemoryWriterMapping struct {
	TeamID            string `json:"team_id"`
	SourceSpaceID     string `json:"source_space_id"`
	SourceGeneration  int64  `json:"source_generation"`
	OwnerID           string `json:"original_owner_id"`
	OwnerMembershipID string `json:"owner_membership_id"`
}

type PrivateMemoryMigrationInspector interface {
	InspectPrivateMemoryMigration(context.Context) (*PrivateMemoryMigrationReport, error)
}

type PrivateMemoryMigrationExecutor interface {
	MigratePrivateMemory(context.Context, string) error
}
