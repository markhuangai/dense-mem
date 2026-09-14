package postgres

import (
	"context"

	"gorm.io/gorm"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	graphpostgres "github.com/markhuangai/dense-mem/internal/graph/postgres"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
)

type recallTraceFixtureStore struct {
	*knowledgepostgres.Store
	trace *tracepostgres.Store
}

func newRecallTraceFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *recallTraceFixtureStore {
	trace := tracepostgres.New(db, rls, func(ctx context.Context, tx *gorm.DB, input graphcontract.Query, spaceID string) (*graphcontract.Snapshot, error) {
		rows, err := graphpostgres.LoadLocalRows(ctx, tx, input, spaceID)
		if err != nil {
			return nil, err
		}
		return graphpostgres.Snapshot(input, rows), nil
	}, nil)
	return &recallTraceFixtureStore{Store: knowledgepostgres.NewStore(db, rls, knowledgepostgres.ConflictRuntimeConfig{}), trace: trace}
}

func (s *recallTraceFixtureStore) TraceRelationship(ctx context.Context, input tracepostgres.TraceRelationshipInput) (*tracepostgres.RelationshipTraceResult, error) {
	return s.trace.TraceRelationship(ctx, input)
}
