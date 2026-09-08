package repository

import (
	"context"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
	"gorm.io/gorm"
)

var (
	ErrTraceRelationshipNotFound  = tracecontract.ErrRelationshipNotFound
	ErrTraceRelationshipIDInvalid = tracecontract.ErrRelationshipIDInvalid
)

// TraceRelationship preserves the legacy repository entry point while the
// trace capability owns validation, hydration, and bounded assembly.
func (r *SemanticRepositoryImpl) TraceRelationship(
	ctx context.Context,
	input TraceRelationshipInput,
) (*RelationshipTraceResult, error) {
	return newTraceStore(r).TraceRelationship(ctx, input)
}

func newTraceStore(r *SemanticRepositoryImpl) *tracepostgres.Store {
	if r == nil {
		return tracepostgres.New(nil, nil, nil, nil)
	}
	return tracepostgres.New(r.db, r.rls, tracepostgres.GraphLoader(loadTraceGraphSnapshot), tracepostgres.ConflictLoader(loadTraceConflicts))
}

func loadTraceGraphSnapshot(
	ctx context.Context,
	tx *gorm.DB,
	input graphcontract.Query,
	spaceID string,
) (*graphcontract.Snapshot, error) {
	rows, err := loadSemanticLocalGraphRows(ctx, tx, semanticGraphExecutionQuery{
		SemanticGraphQuery: SemanticGraphQuery(input),
		spaceID:            spaceID,
	})
	if err != nil {
		return nil, err
	}
	return semanticGraphSnapshot(SemanticGraphQuery(input), rows), nil
}

func loadTraceConflicts(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	spaceID string,
) ([]tracecontract.RelationshipConflictCaseRecord, error) {
	return loadRelationshipConflictRecordsInSpace(ctx, tx, teamID, []string{relationshipID}, nil, spaceID)
}
