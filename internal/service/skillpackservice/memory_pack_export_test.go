package skillpackservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

type exportSemanticStub struct {
	trace *tracecontract.RelationshipTraceResult
}

func (s *exportSemanticStub) TraceRelationship(_ context.Context, _ tracecontract.Input) (*tracecontract.RelationshipTraceResult, error) {
	return s.trace, nil
}

func TestMemoryPackExportRejectsMissingInputsAndUnavailableRelationships(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID})
	svc := NewMemoryPackService(MemoryPackDependencies{Semantic: &exportSemanticStub{trace: &tracecontract.RelationshipTraceResult{
		Relationship: &tracecontract.RelationshipTraceRecord{
			RelationshipID: "rel-1",
			Status:         string(domain.RelationshipStatusSuperseded),
		},
	}}})

	if _, err := svc.Export(ctx, ExportRequest{Name: "pack", RelationshipIDs: []string{"rel-1"}}); !errors.Is(err, ErrMemoryPackRelationshipNotActive) {
		t.Fatalf("Export error = %v, want ErrMemoryPackRelationshipNotActive", err)
	}
}
