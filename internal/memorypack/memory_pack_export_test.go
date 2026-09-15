package memorypack

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestMemoryPackExportBuildsCanonicalArtifactWithOptionalSupport(t *testing.T) {
	teamID, profileID := uuid.New(), uuid.New()
	relationshipID := "00000000-0000-0000-0000-000000000001"
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID})
	service := NewMemoryPackService(MemoryPackDependencies{
		Now: func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) },
		Semantic: &exportSemanticStub{trace: &tracecontract.RelationshipTraceResult{
			Relationship: &tracecontract.RelationshipTraceRecord{
				TeamID: teamID.String(), RelationshipID: relationshipID, OwnerProfileID: profileID.String(),
				SubjectEntityID: "subject", SubjectName: "Subject", PredicateKey: "uses", PredicateVersion: 1,
				ObjectEntityID: "object", ObjectEntityName: "Object", Status: string(domain.RelationshipStatusActive), Version: 2,
			},
			EvidenceSupports: []tracecontract.RelationshipEvidenceSupportRecord{{
				RelationshipID: "", FragmentID: "evidence-1", Quote: "exact quote", SpanStart: 1, SpanEnd: 12,
			}},
			EvidenceFragments: []tracecontract.TraceEvidenceFragment{{
				FragmentID: "evidence-1", Content: "support content", ContentHash: "hash", SourceType: "note",
				Authority: "authoritative", SourceRef: "ref", SourceKey: "key", SourceRevisionID: "revision",
				Labels: []string{"label"}, Metadata: map[string]any{"source": "test"},
			}},
		}},
	})

	result, err := service.Export(ctx, ExportRequest{Name: " Team Pack ", Description: " description ", RelationshipIDs: []string{" " + relationshipID}})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.ItemCount != 1 || result.Artifact.Name != "Team Pack" || result.Artifact.ContentSHA256 != result.SHA256 {
		t.Fatalf("export result = %#v", result)
	}
	if len(result.Artifact.Evidence) != 1 || len(result.Artifact.EvidenceSupports) != 1 || result.Filename != "team-pack.memory-pack.json" {
		t.Fatalf("export evidence/filename = %#v / %q", result.Artifact, result.Filename)
	}
	if len(result.Omissions) != 0 || result.ContentType != "application/json" || result.CanonicalJSON == "" {
		t.Fatalf("export metadata = %#v", result)
	}

	includeSupport := false
	includeNames := false
	result, err = service.Export(ctx, ExportRequest{
		Name: "No support", RelationshipIDs: []string{relationshipID},
		IncludeSupport: &includeSupport, IncludeEntityNames: &includeNames,
	})
	if err != nil {
		t.Fatalf("Export without optional data: %v", err)
	}
	if len(result.Artifact.Evidence) != 0 || len(result.Omissions) != 1 || result.Artifact.Relationships[0].Subject.DisplayName != "" {
		t.Fatalf("optional export = %#v, omissions=%#v", result.Artifact, result.Omissions)
	}
}
