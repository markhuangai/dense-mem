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

func v2ToolMap(t *testing.T) map[string]Tool {
	t.Helper()
	tools := map[string]Tool{}
	for _, tool := range V2ContractTools() {
		tools[tool.Name] = tool
	}
	return tools
}

func readV2ContractFixtures(t *testing.T) []v2ContractFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/v2_contract_fixtures.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixtures []v2ContractFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no V2 contract fixtures")
	}
	return fixtures
}

func v2ContractInvokeContext(scopes ...string) context.Context {
	return requestctx.WithActorCredential(context.Background(), requestctx.ActorCredential{
		KeyID:      uuid.New(),
		AuthMethod: "api_key",
		Role:       "member",
		Scopes:     scopes,
	})
}

type stubRememberService struct {
	req          memoryservice.V2RememberRequest
	placementReq memoryservice.V2GetMemoryPlacementRequest
}

func (s *stubRememberService) Remember(_ context.Context, req memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error) {
	s.req = req
	return &memoryservice.V2RememberResult{
		IngestID:          "ingest-v2",
		ProcessingState:   string(domain.V2PlacementRunQueued),
		CheckAfterSeconds: 60,
		StatusTool:        V2ToolGetMemoryPlacement,
		CorrelationID:     "corr-v2",
	}, nil
}

func (s *stubRememberService) GetMemoryPlacement(
	_ context.Context,
	req memoryservice.V2GetMemoryPlacementRequest,
) (*memoryservice.V2PlacementRunResult, error) {
	s.placementReq = req
	return &memoryservice.V2PlacementRunResult{
		IngestID:        req.IngestID,
		ProcessingState: string(domain.V2PlacementRunCompleted),
		SearchState:     string(domain.V2SearchProjectionNotRequired),
		Items: []memoryservice.V2PlacementItemResult{{
			ItemID:               "item-v2",
			EvidenceID:           "evidence-v2",
			Version:              3,
			EvidenceIndex:        0,
			Category:             string(domain.V2EvidenceProcessed),
			SearchState:          string(domain.V2SearchProjectionNotRequired),
			RelationshipOutcomes: []memoryservice.V2RelationshipOutcomeRef{},
			Errors:               []memoryservice.V2PlacementError{},
		}},
		Errors: []memoryservice.V2PlacementError{},
	}, nil
}

type stubRecallService struct {
	req memoryservice.V2RecallRequest
}

func (s *stubRecallService) Recall(_ context.Context, req memoryservice.V2RecallRequest) (*memoryservice.V2RecallResult, error) {
	s.req = req
	return &memoryservice.V2RecallResult{
		RecallID: "rec-v2",
		Results: []memoryservice.V2RecallResultItem{{
			EvidenceID:      "evidence-v2",
			RelationshipIDs: []string{"relationship-v2"},
			Rank:            1,
			Context:         "Dense-Mem uses PostgreSQL.",
		}},
		SearchState: string(domain.V2SearchProjectionCurrent),
	}, nil
}

type stubV2TraceContext struct {
	req contextservice.TraceRequest
}

func (s *stubV2TraceContext) Trace(_ context.Context, _ string, req contextservice.TraceRequest) (*contextservice.TraceResult, error) {
	s.req = req
	return &contextservice.TraceResult{
		Semantic: &contextservice.SemanticTrace{
			Relationship: &repository.V2RelationshipTraceRecord{
				RelationshipID:   "relationship-v2",
				TeamID:           "team-v2",
				SemanticGroupKey: "group-v2",
				PredicateKey:     "works_on",
				Status:           string(domain.V2RelationshipStatusActive),
			},
			EvidenceSupports: []repository.V2RelationshipEvidenceSupportRecord{{
				SupportID:      "support-v2",
				RelationshipID: "relationship-v2",
				FragmentID:     "fragment-v2",
				SpanStart:      0,
				SpanEnd:        12,
			}},
			SearchDocuments: []repository.V2TraceSearchDocument{{
				SearchDocumentID: "search-doc-v2",
			}},
			EmbeddingJobs: []repository.V2TraceEmbeddingJob{{
				EmbeddingJobID: "embedding-job-v2",
			}},
			StoppedReason: "max_edges",
			Truncated:     true,
		},
	}, nil
}

func (s *stubV2TraceContext) Assemble(context.Context, string, contextservice.AssembleRequest) (*contextservice.AssembleResult, error) {
	return nil, errors.New("not implemented")
}
