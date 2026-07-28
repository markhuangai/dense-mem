package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func toolMap(t *testing.T) map[string]Tool {
	t.Helper()
	tools := map[string]Tool{}
	for _, tool := range ContractTools() {
		tools[tool.Name] = tool
	}
	return tools
}

func readContractFixtures(t *testing.T) []contractFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/contract_fixtures.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixtures []contractFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no contract fixtures")
	}
	return fixtures
}

func contractInvokeContext(scopes ...string) context.Context {
	return requestctx.WithActorCredential(context.Background(), requestctx.ActorCredential{
		KeyID:      uuid.New(),
		AuthMethod: "api_key",
		Role:       "member",
		Scopes:     scopes,
	})
}

type stubRememberService struct {
	req          memoryservice.RememberRequest
	placementReq memoryservice.GetMemoryPlacementRequest
}

func (s *stubRememberService) Remember(_ context.Context, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	s.req = req
	return &memoryservice.RememberResult{
		IngestID:          "ingest-canonical",
		ProcessingState:   string(domain.PlacementRunQueued),
		CheckAfterSeconds: 60,
		StatusTool:        ToolGetMemoryPlacement,
		CorrelationID:     "corr-canonical",
	}, nil
}

func (s *stubRememberService) GetMemoryPlacement(
	_ context.Context,
	req memoryservice.GetMemoryPlacementRequest,
) (*memoryservice.PlacementRunResult, error) {
	s.placementReq = req
	return &memoryservice.PlacementRunResult{
		IngestID:        req.IngestID,
		ProcessingState: string(domain.PlacementRunCompleted),
		SearchState:     string(domain.SearchProjectionNotRequired),
		Items: []memoryservice.PlacementItemResult{{
			ItemID:               "item-canonical",
			EvidenceID:           "evidence-canonical",
			Version:              3,
			EvidenceIndex:        0,
			Category:             string(domain.EvidenceProcessed),
			SearchState:          string(domain.SearchProjectionNotRequired),
			RelationshipOutcomes: []memoryservice.RelationshipOutcomeRef{},
			ReviewTasks:          []memoryservice.PlacementReviewTaskRef{},
			Errors:               []memoryservice.PlacementError{},
		}},
		Errors: []memoryservice.PlacementError{},
	}, nil
}

type stubRecallService struct {
	req memoryservice.RecallRequest
}

func (s *stubRecallService) Recall(_ context.Context, req memoryservice.RecallRequest) (*memoryservice.RecallResult, error) {
	s.req = req
	return &memoryservice.RecallResult{
		RecallID: "rec-canonical",
		Results: []memoryservice.RecallResultItem{{
			EvidenceID:      "evidence-canonical",
			RelationshipIDs: []string{"relationship-canonical"},
			Rank:            1,
			Context:         "Dense-Mem uses PostgreSQL.",
		}},
		SearchState: string(domain.SearchProjectionCurrent),
	}, nil
}

type stubTraceContext struct {
	req contextservice.TraceRequest
}

func (s *stubTraceContext) Trace(_ context.Context, _ string, req contextservice.TraceRequest) (*contextservice.TraceResult, error) {
	s.req = req
	return &contextservice.TraceResult{
		Semantic: &contextservice.SemanticTrace{
			Relationship: &repository.RelationshipTraceRecord{
				RelationshipID:   "relationship-canonical",
				TeamID:           "team-canonical",
				SemanticGroupKey: "group-canonical",
				PredicateKey:     "works_on",
				Status:           string(domain.RelationshipStatusActive),
			},
			EvidenceSupports: []repository.RelationshipEvidenceSupportRecord{{
				SupportID:      "support-canonical",
				RelationshipID: "relationship-canonical",
				FragmentID:     "fragment-canonical",
				SpanStart:      0,
				SpanEnd:        12,
			}},
			SearchDocuments: []repository.TraceSearchDocument{{
				SearchDocumentID: "search-doc-canonical",
			}},
			EmbeddingJobs: []repository.TraceEmbeddingJob{{
				EmbeddingJobID: "embedding-job-canonical",
			}},
			StoppedReason: "max_edges",
			Truncated:     true,
		},
	}, nil
}

func (s *stubTraceContext) Assemble(context.Context, string, contextservice.AssembleRequest) (*contextservice.AssembleResult, error) {
	return nil, errors.New("not implemented")
}
