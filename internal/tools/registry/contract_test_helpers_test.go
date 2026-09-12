package registry

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/recall"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
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
	req            rememberapp.RememberRequest
	err            error
	inspectContext func(context.Context)
}

func (s *stubRememberService) Remember(ctx context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	s.req = req
	if s.inspectContext != nil {
		s.inspectContext(ctx)
	}
	if s.err != nil {
		return nil, s.err
	}
	return &rememberapp.RememberResult{
		ContractVersion:     domain.ContractVersion,
		SubmissionID:        "ingest-canonical",
		SubmissionKind:      "remember",
		ProcessingState:     "completed",
		SearchState:         string(domain.SearchProjectionCurrent),
		Evidence:            []rememberapp.SubmissionEvidenceStatus{},
		Errors:              []rememberapp.SubmissionStatusError{},
		RelationshipResults: []rememberapp.SubmissionRelationshipResult{},
		CorrelationID:       "corr-canonical",
		Kind:                "terminal",
		IngestID:            "ingest-canonical",
	}, nil
}

type stubRecallService struct {
	req    recall.RecallRequest
	result *recall.RecallResult
}

func (s *stubRecallService) Recall(_ context.Context, req recall.RecallRequest) (*recall.RecallResult, error) {
	s.req = req
	if s.result != nil {
		return s.result, nil
	}
	return &recall.RecallResult{
		RecallID: "rec-canonical",
		Results: []recall.RecallResultItem{{
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
	req traceapp.TraceRequest
}

func (s *stubTraceContext) Trace(_ context.Context, _ string, req traceapp.TraceRequest) (*traceapp.TraceResult, error) {
	s.req = req
	return &traceapp.TraceResult{
		Semantic: &traceapp.SemanticTrace{
			Relationship: &tracecontract.RelationshipTraceRecord{
				RelationshipID:   "relationship-canonical",
				TeamID:           "team-canonical",
				SemanticGroupKey: "group-canonical",
				PredicateKey:     "works_on",
				Status:           string(domain.RelationshipStatusActive),
			},
			EvidenceSupports: []tracecontract.RelationshipEvidenceSupportRecord{{
				SupportID:      "support-canonical",
				RelationshipID: "relationship-canonical",
				FragmentID:     "fragment-canonical",
				SpanStart:      0,
				SpanEnd:        12,
			}},
			SearchDocuments: []tracecontract.TraceSearchDocument{{
				SearchDocumentID: "search-doc-canonical",
			}},
			StoppedReason: "max_edges",
			Truncated:     true,
		},
	}, nil
}
