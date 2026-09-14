package postgres

import (
	"context"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
	"gorm.io/gorm"
)

type graphSemanticFixtureStore struct {
	*knowledgepostgres.Store
	graphStore *Store
	traceStore *tracepostgres.Store
}

func newGraphSemanticFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *graphSemanticFixtureStore {
	graphStore := NewStore(db, rls)
	traceStore := tracepostgres.New(db, rls, func(ctx context.Context, tx *gorm.DB, input graphcontract.Query, spaceID string) (*graphcontract.Snapshot, error) {
		rows, err := LoadLocalRows(ctx, tx, input, spaceID)
		if err != nil {
			return nil, err
		}
		return Snapshot(input, rows), nil
	}, nil)
	return &graphSemanticFixtureStore{Store: knowledgepostgres.NewStore(db, rls, knowledgepostgres.ConflictRuntimeConfig{}), graphStore: graphStore, traceStore: traceStore}
}

func (s *graphSemanticFixtureStore) SemanticGraph(ctx context.Context, input graphcontract.Query) (*graphcontract.Snapshot, error) {
	return s.graphStore.SemanticGraph(ctx, input)
}
func (s *graphSemanticFixtureStore) SemanticGraphNodeDetail(ctx context.Context, input graphcontract.NodeDetailInput) (*graphcontract.Node, error) {
	return s.graphStore.SemanticGraphNodeDetail(ctx, input)
}
func (s *graphSemanticFixtureStore) TraceRelationship(ctx context.Context, input tracepostgres.TraceRelationshipInput) (*tracepostgres.RelationshipTraceResult, error) {
	return s.traceStore.TraceRelationship(ctx, input)
}
