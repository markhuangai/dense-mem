package evaluation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

func TestListKnowledgeRefsUsesActorTeamAndStripsOnlyCopy(t *testing.T) {
	teamID := uuid.NewString()
	repository := &evaluationRepositoryStub{page: &dreamcontract.EvaluationPage{
		Items:      []map[string]any{{"type": "evidence", "id": "evidence-1", "content": "secret"}},
		NextCursor: "next",
		HasMore:    true,
	}}
	audit := &evaluationAuditStub{}
	svc := New(Dependencies{Repository: repository, Audit: audit})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.MustParse(teamID)})

	result, err := svc.ListKnowledgeRefs(ctx, "fallback-team", "evidence", 6, "cursor", "active", true)
	require.NoError(t, err)
	require.Equal(t, teamID, repository.input.TeamID)
	require.Equal(t, "evidence", repository.input.Type)
	require.Equal(t, "cursor", repository.input.Cursor)
	require.Equal(t, "active", repository.input.Status)
	require.Equal(t, true, result["has_more"])
	items := result["items"].([]map[string]any)
	_, present := items[0]["content"]
	require.False(t, present)
	_, present = repository.page.Items[0]["content"]
	require.True(t, present, "metadata filtering must not mutate the repository result")
	require.Len(t, audit.entries, 1)
}

func TestListKnowledgeRefsRejectsInvalidFallbackWithoutActor(t *testing.T) {
	svc := New(Dependencies{Repository: &evaluationRepositoryStub{page: &dreamcontract.EvaluationPage{}}, Audit: &evaluationAuditStub{}})
	_, err := svc.ListKnowledgeRefs(context.Background(), "not-a-team", "evidence", 1, "", "", false)
	require.EqualError(t, err, "evaluation tool requires authenticated team context")
}

func TestEvaluationSmallHelpersNormalizeAndCopySafely(t *testing.T) {
	if normalizeListType(" fragment ") != "evidence" || normalizeListType(" relationship ") != "relationship" || normalizeListType("entity") != "entity" {
		t.Fatal("normalizeListType returned an unexpected value")
	}
	if normalizePageLimit(0) != DefaultPageSize || normalizePageLimit(MaxPageSize+1) != MaxPageSize {
		t.Fatal("normalizePageLimit did not enforce bounds")
	}
	if parsed, err := parseOptionalTime("2026-01-01T00:00:00Z"); err != nil || parsed.IsZero() {
		t.Fatalf("parseOptionalTime valid = %v, %v", parsed, err)
	}
	if _, err := parseOptionalTime("bad"); err == nil {
		t.Fatal("malformed optional time was accepted")
	}
	original := map[string]any{"content": "secret", "nested": map[string]any{"value": 1}}
	copied, err := structMap(original)
	if err != nil || copied["content"] != "secret" {
		t.Fatalf("structMap = %#v, %v", copied, err)
	}
	stripped := copyItem(original)
	stripContent("evidence", stripped)
	if _, ok := stripped["content"]; ok {
		t.Fatal("evidence content was not stripped")
	}
	if _, ok := original["content"]; !ok {
		t.Fatal("copyItem mutated original")
	}
}

func TestRunRecallCaseMapsRefsAndRequiresAudit(t *testing.T) {
	recall := &recallServiceStub{result: &recallcontract.RecallResult{
		RecallID:    "recall-1",
		SearchState: "current",
		Results:     []recallcontract.RecallResultItem{{EvidenceID: "evidence-1", Rank: 1, RelationshipIDs: []string{"relationship-1"}}},
	}}
	audit := &evaluationAuditStub{}
	svc := New(Dependencies{Recall: recall, Audit: audit})
	result, err := svc.RunRecallCase(context.Background(), " case-1 ", recallcontract.Request{Query: "query", Limit: 2}, "", "", true)
	require.NoError(t, err)
	require.Equal(t, " case-1 ", result["case_id"])
	require.Equal(t, "recall-1", result["recall_id"])
	require.Equal(t, "current", result["search_state"])
	require.Len(t, result["ranked_refs"], 1)
	require.Len(t, result["context_evidence_refs"], 1)
	require.Len(t, audit.entries, 1)
	require.Equal(t, "query", recall.request.Query)
}

func TestAuditFailurePreventsRecallExecution(t *testing.T) {
	sentinel := errors.New("audit unavailable")
	recall := &recallServiceStub{}
	svc := New(Dependencies{Recall: recall, Audit: failingAudit{err: sentinel}})
	_, err := svc.RunRecallCase(context.Background(), "case-1", recallcontract.Request{Query: "query"}, "", "", false)
	require.ErrorIs(t, err, sentinel)
	require.False(t, recall.called)
}

func TestRunRecallCaseAuditsBeforeParsingTimes(t *testing.T) {
	sentinel := errors.New("audit unavailable")
	recall := &recallServiceStub{}
	svc := New(Dependencies{Recall: recall, Audit: failingAudit{err: sentinel}})
	_, err := svc.RunRecallCase(context.Background(), "case-1", recallcontract.Request{Query: "query"}, "not-a-time", "", false)
	require.ErrorIs(t, err, sentinel)
	require.False(t, recall.called)
}

func TestRunRecallCaseRejectsMalformedTimeAfterAudit(t *testing.T) {
	for _, raw := range []string{"not-a-time", " ", " 2024-01-01T00:00:00Z "} {
		t.Run(raw, func(t *testing.T) {
			recall := &recallServiceStub{}
			audit := &evaluationAuditStub{}
			svc := New(Dependencies{Recall: recall, Audit: audit})
			_, err := svc.RunRecallCase(context.Background(), "case-1", recallcontract.Request{Query: "query"}, raw, "", false)
			require.EqualError(t, err, "time value must be RFC3339")
			require.Len(t, audit.entries, 1)
			require.False(t, recall.called)
		})
	}
}

func TestEvaluationServiceAppliesWorkflowDefaultsAndCaps(t *testing.T) {
	teamID := uuid.NewString()
	repository := &evaluationRepositoryStub{page: &dreamcontract.EvaluationPage{}}
	recall := &recallServiceStub{}
	svc := New(Dependencies{
		Repository: repository,
		Recall:     recall,
		Audit:      &evaluationAuditStub{},
	})

	_, err := svc.ListKnowledgeRefs(context.Background(), teamID, "evidence", 0, "", "", false)
	require.NoError(t, err)
	require.Equal(t, DefaultPageSize, repository.input.Limit)
	_, err = svc.ListKnowledgeRefs(context.Background(), teamID, "evidence", MaxPageSize+1, "", "", false)
	require.NoError(t, err)
	require.Equal(t, MaxPageSize, repository.input.Limit)

	_, err = svc.RunRecallCase(context.Background(), "case-1", recallcontract.Request{Query: "query"}, "", "", false)
	require.NoError(t, err)
	require.Equal(t, DefaultRecallCaseSize, recall.request.Limit)

}

func TestStripContentAndReferenceMappingPolicies(t *testing.T) {
	for kind, fields := range map[string][]string{
		"evidence": {"content"},
		"entity":   {"canonical_name", "identity_context"},
		"value":    {"canonical_value", "display"},
	} {
		item := make(map[string]any, len(fields))
		for _, field := range fields {
			item[field] = "private"
		}
		stripContent(kind, item)
		require.Empty(t, item, kind)
	}

	require.Empty(t, resultRefs(nil))
}

type evaluationRepositoryStub struct {
	input dreamcontract.EvaluationListInput
	page  *dreamcontract.EvaluationPage
}

func (s *evaluationRepositoryStub) ListEvaluationRefs(_ context.Context, input dreamcontract.EvaluationListInput) (*dreamcontract.EvaluationPage, error) {
	s.input = input
	return s.page, nil
}

func (s *evaluationRepositoryStub) GetEvaluationItem(context.Context, dreamcontract.EvaluationGetInput) (map[string]any, error) {
	return nil, errors.New("not implemented")
}

type evaluationAuditStub struct {
	entries []accessservice.AuditLogEntry
}

func (s *evaluationAuditStub) Append(_ context.Context, entry accessservice.AuditLogEntry) error {
	s.entries = append(s.entries, entry)
	return nil
}

type failingAudit struct{ err error }

func (s failingAudit) Append(context.Context, accessservice.AuditLogEntry) error { return s.err }

type recallServiceStub struct {
	result  *recallcontract.RecallResult
	request recallcontract.Request
	called  bool
}

func (s *recallServiceStub) Recall(_ context.Context, request recallcontract.Request) (*recallcontract.RecallResult, error) {
	s.called = true
	s.request = request
	return s.result, nil
}
