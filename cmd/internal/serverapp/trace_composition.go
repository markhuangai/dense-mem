package serverapp

import (
	"context"

	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	graphpostgres "github.com/markhuangai/dense-mem/internal/graph/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	trace "github.com/markhuangai/dense-mem/internal/trace"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
	"gorm.io/gorm"
)

func buildContextApplication(store trace.SemanticTraceStore) trace.Service {
	return trace.NewSemantic(store)
}

func buildTraceStore(db *gorm.DB, rls storagepostgres.RLSHelper) trace.SemanticTraceStore {
	return tracepostgres.New(db, rls,
		func(ctx context.Context, tx *gorm.DB, input graphcontract.Query, spaceID string) (*graphcontract.Snapshot, error) {
			rows, err := graphpostgres.LoadLocalRows(ctx, tx, input, spaceID)
			if err != nil {
				return nil, err
			}
			return graphpostgres.Snapshot(input, rows), nil
		},
		func(ctx context.Context, tx *gorm.DB, teamID, relationshipID, spaceID string) ([]tracepostgres.RelationshipConflictCaseRecord, error) {
			return conflictpostgres.LoadRelationshipConflictRecordsInSpace(ctx, tx, teamID, []string{relationshipID}, nil, spaceID)
		},
	)
}
