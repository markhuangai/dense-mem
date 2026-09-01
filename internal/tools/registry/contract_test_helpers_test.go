package registry

import (
	"context"
	"encoding/json"
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
	credentialID := uuid.New()
	return requestctx.WithActor(context.Background(), requestctx.Actor{
		IdentityID: credentialID, CredentialID: &credentialID,
		AuthMethod: "api_key", Role: "member", Grants: scopes,
	})
}

type stubRememberService struct {
	req memoryservice.RememberRequest
	err error
}

func (s *stubRememberService) Remember(_ context.Context, req memoryservice.RememberRequest) (*memoryservice.RememberResult, error) {
	s.req = req
	if s.err != nil {
		return nil, s.err
	}
	return &memoryservice.RememberResult{
		ContractVersion:     domain.ContractVersion,
		SubmissionID:        "ingest-canonical",
		SubmissionKind:      "remember",
		ProcessingState:     "completed",
		SearchState:         string(domain.SearchProjectionCurrent),
		Evidence:            []memoryservice.SubmissionEvidenceStatus{},
		Errors:              []memoryservice.SubmissionStatusError{},
		RelationshipResults: []memoryservice.SubmissionRelationshipResult{},
		CorrelationID:       "corr-canonical",
		Kind:                "terminal",
		IngestID:            "ingest-canonical",
	}, nil
}

type stubRecallService struct {
	req    memoryservice.RecallRequest
	result *memoryservice.RecallResult
}

func (s *stubRecallService) Recall(_ context.Context, req memoryservice.RecallRequest) (*memoryservice.RecallResult, error) {
	s.req = req
	if s.result != nil {
		return s.result, nil
	}
	return &memoryservice.RecallResult{
		RecallID: "rec-canonical",
		Results: []memoryservice.RecallResultItem{{
			EvidenceID:      "evidence-canonical",
			RelationshipIDs: []string{"relationship-canonical"},
			Rank:            1,
			Context:         "Dense-Mem uses PostgreSQL.",
			SpaceKind:       string(domain.MemorySpaceTeamShared),
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
			StoppedReason: "max_edges",
			Truncated:     true,
		},
	}, nil
}
