package repository

import (
	"context"

	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// These test-only forwarders keep the registry's existing repository-package
// integration sources buildable while their transaction-owned helpers migrate
// to the knowledge PostgreSQL owner. They intentionally contain no SQL or
// policy.
func enqueueConflictDerivedEvidenceTasks(
	ctx context.Context,
	tx *gorm.DB,
	resolutionPlanID string,
	targets []ConflictDerivedEvidenceTarget,
) ([]ConflictDerivedEvidenceTarget, error) {
	return knowledgepostgres.EnqueueConflictDerivedEvidenceTasks(ctx, knowledgepostgres.LegacyTransaction(tx), resolutionPlanID, targets)
}

func loadRelationshipConflictPlacement(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	source *RelationshipRecord,
) (*knowledgepostgres.ConflictPlacement, error) {
	return knowledgepostgres.LoadRelationshipConflictPlacement(ctx, knowledgepostgres.LegacyTransaction(tx), teamID, toKnowledgeRelationshipRecord(source))
}

func upsertRelationshipConflictCase(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	placement *knowledgepostgres.ConflictPlacement,
	config ConflictRuntimeConfig,
) error {
	return knowledgepostgres.UpsertRelationshipConflictCase(ctx, knowledgepostgres.LegacyTransaction(tx), teamID, placement, config)
}

func resolveRememberExactEvidenceInTx(
	ctx context.Context,
	tx *gorm.DB,
	input RememberDuplicateCandidateInput,
	evidence EvidenceInput,
) (string, bool, error) {
	return knowledgepostgres.ResolveRememberExactEvidenceInTx(ctx, knowledgepostgres.LegacyTransaction(tx), input, evidence)
}

func lockEvidenceLifecycleTarget(ctx context.Context, tx *gorm.DB, teamID, fragmentID string) error {
	return knowledgepostgres.LockEvidenceLifecycleTarget(ctx, knowledgepostgres.LegacyTransaction(tx), teamID, fragmentID)
}

const evidenceLifecycleCompleted = knowledgepostgres.EvidenceLifecycleCompleted

func validateSelectedCorrectionEntities(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	candidates []RelationshipCorrectionCandidate,
	selection RelationshipCorrectionSelection,
) error {
	return knowledgepostgres.ValidateSelectedCorrectionEntities(
		ctx,
		knowledgepostgres.LegacyTransaction(tx),
		teamID,
		toKnowledgeRelationshipCorrectionCandidates(candidates),
		toKnowledgeRelationshipCorrectionSelection(selection),
	)
}

var errRelationshipCorrectionSelectionUnavailable = knowledgepostgres.ErrRelationshipCorrectionSelectionUnavailable
