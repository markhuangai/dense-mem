package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	graphpostgres "github.com/markhuangai/dense-mem/internal/graph/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres/graphread"
	"gorm.io/gorm"
)

// GraphStore is the narrow construction seam used by graph-owned composition.
// It returns the graph contract and keeps database handles private to the
// legacy repository compatibility boundary.
func (r *SemanticRepositoryImpl) GraphStore() graphcontract.Store {
	if r == nil || r.db == nil || r.rls == nil {
		return nil
	}
	return graphpostgres.NewStore(r.db, r.rls)
}

func (r *SemanticRepositoryImpl) graphOwner() (*graphpostgres.Store, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("semantic: database is required")
	}
	if r.rls == nil {
		return nil, errors.New("semantic: rls helper is required")
	}
	store := r.GraphStore()
	owner, ok := store.(*graphpostgres.Store)
	if !ok || owner == nil {
		return nil, errors.New("semantic: graph owner is required")
	}
	return owner, nil
}

func (r *SemanticRepositoryImpl) SemanticGraph(ctx context.Context, input SemanticGraphQuery) (*SemanticGraphSnapshot, error) {
	input = graphpostgres.NormalizeQuery(input)
	if err := graphpostgres.ValidateQuery(input); err != nil {
		return nil, err
	}
	owner, err := r.graphOwner()
	if err != nil {
		return nil, fmt.Errorf("semantic graph: %w", err)
	}
	return owner.SemanticGraph(ctx, input)
}

func (r *SemanticRepositoryImpl) SemanticGraphNodeDetail(ctx context.Context, input SemanticGraphNodeDetailInput) (*SemanticGraphNode, error) {
	input = graphpostgres.NormalizeNodeDetailInput(input)
	if err := graphpostgres.ValidateNodeDetailInput(input); err != nil {
		return nil, err
	}
	owner, err := r.graphOwner()
	if err != nil {
		return nil, fmt.Errorf("semantic graph node: %w", err)
	}
	return owner.SemanticGraphNodeDetail(ctx, input)
}

// These narrow helpers keep the legacy trace adapter on the same graph read
// owner while its public compatibility contract remains in repository aliases.
type semanticGraphEdgeRow = graphread.Row

type semanticGraphExecutionQuery struct {
	SemanticGraphQuery
	spaceID string
}

func loadSemanticLocalGraphRows(ctx context.Context, tx *gorm.DB, input semanticGraphExecutionQuery) ([]semanticGraphEdgeRow, error) {
	return graphpostgres.LoadLocalRows(ctx, tx, input.SemanticGraphQuery, input.spaceID)
}

func semanticGraphSnapshot(input SemanticGraphQuery, rows []semanticGraphEdgeRow) *SemanticGraphSnapshot {
	return graphread.Snapshot(input, rows)
}

const (
	defaultSemanticGraphLimit = 80
	defaultSemanticGraphDepth = 2
	maxSemanticGraphDepth     = 5
)

func normalizeSemanticGraphQuery(input SemanticGraphQuery) SemanticGraphQuery {
	return graphpostgres.NormalizeQuery(input)
}

func normalizeSemanticGraphNodeDetailInput(input SemanticGraphNodeDetailInput) SemanticGraphNodeDetailInput {
	return graphpostgres.NormalizeNodeDetailInput(input)
}

func normalizeSemanticGraphTypes(values []string) []string {
	return graphread.NormalizeTypes(values)
}

func semanticGraphTypeSet(values []string) map[string]bool {
	return graphread.TypeSet(values)
}

func normalizeSemanticGraphNodeType(raw string) string {
	return graphread.NormalizeNodeType(raw)
}

func semanticGraphNodeKey(nodeType, id string) string {
	nodeType = normalizeSemanticGraphNodeType(nodeType)
	id = strings.TrimSpace(id)
	if nodeType == "" || id == "" {
		return ""
	}
	return nodeType + ":" + id
}
