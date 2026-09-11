package repository

import (
	"context"
	"time"

	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"gorm.io/gorm"
)

type ConflictRuntimeConfig = conflictcontract.ConflictRuntimeConfig
type ConflictReviewRunInput = conflictcontract.ConflictReviewRunInput
type ConflictReviewRunCompleteInput = conflictcontract.ConflictReviewRunCompleteInput
type ConflictReviewRunRecord = conflictcontract.ConflictReviewRunRecord
type ClaimRelationshipConflictCasesInput = conflictcontract.ClaimRelationshipConflictCasesInput
type ReleaseRelationshipConflictCaseClaimInput = conflictcontract.ReleaseRelationshipConflictCaseClaimInput
type ReviewRelationshipConflictCaseInput = conflictcontract.ReviewRelationshipConflictCaseInput
type ReviewRelationshipConflictCaseResult = conflictcontract.ReviewRelationshipConflictCaseResult
type RelationshipConflictResolutionInput = conflictcontract.RelationshipConflictResolutionInput
type RelationshipConflictResolutionDocument = conflictcontract.RelationshipConflictResolutionDocument
type RelationshipConflictResolutionFence = conflictcontract.RelationshipConflictResolutionFence
type RelationshipConflictResolutionPlan = conflictcontract.RelationshipConflictResolutionPlan
type RelationshipConflictResolutionEmbedding = conflictcontract.RelationshipConflictResolutionEmbedding
type CommitRelationshipConflictResolutionInput = conflictcontract.CommitRelationshipConflictResolutionInput
type RelationshipConflictCaseRecord = conflictcontract.RelationshipConflictCaseRecord
type ValidateRelationshipConflictContextInput = conflictcontract.ValidateRelationshipConflictContextInput

func loadRelationshipConflictRecords(ctx context.Context, tx *gorm.DB, teamID string, relationshipIDs []string, knownAt *time.Time) ([]RelationshipConflictCaseRecord, error) {
	return conflictpostgres.LoadRelationshipConflictRecordsInSpace(ctx, tx, teamID, relationshipIDs, knownAt, "")
}

func loadRelationshipConflictRecordsInSpace(ctx context.Context, tx *gorm.DB, teamID string, relationshipIDs []string, knownAt *time.Time, spaceID string) ([]RelationshipConflictCaseRecord, error) {
	return conflictpostgres.LoadRelationshipConflictRecordsInSpace(ctx, tx, teamID, relationshipIDs, knownAt, spaceID)
}

func loadRelationshipConflictRecordsByID(ctx context.Context, tx *gorm.DB, teamID string, conflictIDs []string, knownAt *time.Time) ([]RelationshipConflictCaseRecord, error) {
	return conflictpostgres.LoadRelationshipConflictRecordsByID(ctx, tx, teamID, conflictIDs, knownAt)
}

func applyConflictKnownAt(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	conflictpostgres.ApplyConflictKnownAt(record, knownAt)
}

func applyConflictPositionKnownAtDispositions(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	conflictpostgres.ApplyConflictPositionKnownAtDispositions(record, knownAt)
}
