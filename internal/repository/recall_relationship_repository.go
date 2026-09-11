package repository

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	"gorm.io/gorm"
)

const relationshipForegroundRecallGenerationMetadataKey = recallpostgres.RelationshipForegroundRecallGenerationMetadataKey

func loadRecallOpenConflictRecords(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	knownAt *time.Time,
	results []RecallEvidenceHit,
) ([]RelationshipConflictCaseRecord, error) {
	relationshipIDs := []string{}
	seenRelationships := map[string]struct{}{}
	for _, hit := range results {
		for _, id := range hit.RelationshipIDs {
			if _, ok := seenRelationships[id]; ok {
				continue
			}
			seenRelationships[id] = struct{}{}
			relationshipIDs = append(relationshipIDs, id)
		}
	}
	conflicts, err := loadRelationshipConflictRecords(ctx, tx, teamID, relationshipIDs, knownAt)
	if err != nil {
		return nil, err
	}
	out := make([]RelationshipConflictCaseRecord, 0, len(conflicts))
	seenConflicts := map[string]struct{}{}
	for _, conflict := range conflicts {
		if conflict.Status != string(domain.RelationshipConflictOpen) &&
			conflict.Status != string(domain.RelationshipConflictOverdue) &&
			conflict.Status != string(domain.RelationshipConflictResolved) {
			continue
		}
		if _, ok := seenConflicts[conflict.ConflictID]; ok {
			continue
		}
		seenConflicts[conflict.ConflictID] = struct{}{}
		out = append(out, conflict)
	}
	return out, nil
}

// RecallRelationships forwards the historical repository surface to the
// native Recall PostgreSQL adapter.
func (r *SearchRepositoryImpl) RecallRelationships(ctx context.Context, input RecallRelationshipsInput) (*RecallRelationshipsResult, error) {
	owner, err := r.recallReadOwner()
	if err != nil {
		return nil, err
	}
	return owner.RecallRelationships(ctx, input)
}

var _ interface {
	RecallRelationships(context.Context, recallcontract.RecallRelationshipsInput) (*recallcontract.RecallRelationshipsResult, error)
} = (*SearchRepositoryImpl)(nil)
