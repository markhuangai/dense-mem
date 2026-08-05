package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSemanticReviewRetriesCompleteResponseAndDoesNotPersistPartialInvalidAttempt(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	provider := &semanticReviewProviderStub{
		responses: []verifier.SemanticReviewResponse{
			semanticReviewResponse(request.RequestID, false, true),
			semanticReviewResponse(request.RequestID, false, false),
		},
	}
	ledger := &semanticReviewLedgerStub{}
	svc := NewSemanticReviewService(SemanticReviewDependencies{
		Provider: provider,
		Ledger:   ledger,
	})

	result, err := svc.ReviewSemantic(context.Background(), SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         request,
		MaxAttempts:     2,
	})
	if err != nil {
		t.Fatalf("ReviewSemantic returned error: %v", err)
	}
	if result.Status != string(domain.SemanticReviewAccepted) || result.Attempts != 2 {
		t.Fatalf("result = %#v", result)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	if got := provider.requests[0].EntityMentions[0].Candidates; len(got) != 1 || got[0].EntityID != "ent-mark" {
		t.Fatalf("provider saw unauthorized candidates: %#v", got)
	}
	if len(provider.requests[1].ValidationFeedback) == 0 || !strings.Contains(provider.requests[1].ValidationFeedback[0], "missing result for rel_1") {
		t.Fatalf("second request feedback = %#v", provider.requests[1].ValidationFeedback)
	}
	if provider.requests[1].PreviousResponseHash == "" {
		t.Fatal("second request did not carry previous response hash")
	}
	if len(ledger.outcomes) != 3 {
		t.Fatalf("outcomes = %#v", ledger.outcomes)
	}
	if ledger.outcomes[0].Status != "invalid" || ledger.outcomes[1].Status != "valid" || ledger.outcomes[2].Status != string(domain.SemanticReviewAccepted) {
		t.Fatalf("outcome statuses = %#v", ledger.outcomes)
	}
	combinedPayload := ledger.combinedPayloadJSON(t)
	if strings.Contains(combinedPayload, "Mark works") || strings.Contains(combinedPayload, "The evidence states") {
		t.Fatalf("audit payload leaked evidence or rationale: %s", combinedPayload)
	}
}

func TestSemanticReviewRetriesMalformedProviderResponse(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	provider := &semanticReviewProviderStub{
		errs: []error{
			&verifier.MalformedResponseError{Provider: "stub", Message: "bad structured output"},
			nil,
		},
		responses: []verifier.SemanticReviewResponse{
			semanticReviewResponse(request.RequestID, false, false),
		},
	}
	ledger := &semanticReviewLedgerStub{}
	svc := NewSemanticReviewService(SemanticReviewDependencies{
		Provider: provider,
		Ledger:   ledger,
	})

	result, err := svc.ReviewSemantic(context.Background(), SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         request,
		MaxAttempts:     5,
	})
	if err != nil {
		t.Fatalf("ReviewSemantic returned error: %v", err)
	}
	if result.Status != string(domain.SemanticReviewAccepted) || result.Attempts != 2 {
		t.Fatalf("result = %#v", result)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	if provider.requests[1].RequestID != request.RequestID || provider.requests[1].Attempt != 2 {
		t.Fatalf("second request scope = %#v", provider.requests[1])
	}
	if len(provider.requests[1].ValidationFeedback) != 1 ||
		!strings.Contains(provider.requests[1].ValidationFeedback[0], "provider_response") ||
		!strings.Contains(provider.requests[1].ValidationFeedback[0], "malformed structured response") {
		t.Fatalf("second request feedback = %#v", provider.requests[1].ValidationFeedback)
	}
	if len(ledger.outcomes) != 3 {
		t.Fatalf("outcomes = %#v", ledger.outcomes)
	}
	if ledger.outcomes[0].Status != "invalid" || ledger.outcomes[1].Status != "valid" || ledger.outcomes[2].Status != string(domain.SemanticReviewAccepted) {
		t.Fatalf("outcome statuses = %#v", ledger.outcomes)
	}
}

func TestSemanticReviewReturnsRetryableMalformedProviderResponseAtDefaultLimit(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	provider := &semanticReviewProviderStub{
		err: &verifier.MalformedResponseError{Provider: "stub", Message: "bad structured output"},
	}
	ledger := &semanticReviewLedgerStub{}
	svc := NewSemanticReviewService(SemanticReviewDependencies{Provider: provider, Ledger: ledger})

	result, err := svc.ReviewSemantic(context.Background(), SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         semanticReviewServiceRequest(teamID, ownerID),
	})
	if err != nil {
		t.Fatalf("ReviewSemantic returned error: %v", err)
	}
	if result.Status != string(domain.SemanticReviewRetryable) || result.Attempts != 5 {
		t.Fatalf("result = %#v", result)
	}
	if len(provider.requests) != 5 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	if len(result.ValidationErrors) != 1 || result.ValidationErrors[0].Field != "provider_response" {
		t.Fatalf("validation errors = %#v", result.ValidationErrors)
	}
	if len(ledger.outcomes) != 6 || ledger.outcomes[len(ledger.outcomes)-1].Status != string(domain.SemanticReviewRetryable) {
		t.Fatalf("outcomes = %#v", ledger.outcomes)
	}
}

func TestSemanticReviewForcesJobIdentityOntoProviderRequest(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	request.TeamID = uuid.NewString()
	request.OwnerProfileID = uuid.NewString()
	provider := &semanticReviewProviderStub{
		responses: []verifier.SemanticReviewResponse{
			semanticReviewResponse(request.RequestID, false, false),
		},
	}
	ledger := &semanticReviewLedgerStub{}
	svc := NewSemanticReviewService(SemanticReviewDependencies{Provider: provider, Ledger: ledger})

	result, err := svc.ReviewSemantic(context.Background(), SemanticReviewJob{
		TeamID:          " " + teamID + " ",
		OwnerProfileID:  " " + ownerID + " ",
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         request,
	})
	if err != nil {
		t.Fatalf("ReviewSemantic returned error: %v", err)
	}
	if result.Status != string(domain.SemanticReviewAccepted) {
		t.Fatalf("status = %q", result.Status)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	sent := provider.requests[0]
	if sent.TeamID != teamID || sent.OwnerProfileID != ownerID {
		t.Fatalf("provider request identity = %s/%s, want %s/%s", sent.TeamID, sent.OwnerProfileID, teamID, ownerID)
	}
	if got := sent.EntityMentions[0].Candidates; len(got) != 1 || got[0].TeamID != teamID {
		t.Fatalf("provider candidates = %#v", got)
	}
}

func TestSemanticReviewQuarantinesSecuritySignalsWithoutSemanticDecisions(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	content := "Renée works on Dense-Mem."
	request.Evidence[0].Content = content
	request.EntityMentions[0].Surface = "Renée"
	request.EntityMentions[0].Start = 0
	request.EntityMentions[0].End = 5
	projectStart := semanticReviewTestRuneIndex(content, "Dense-Mem")
	request.EntityMentions[1].Start = projectStart
	request.EntityMentions[1].End = projectStart + len([]rune("Dense-Mem"))
	request.RelationshipObservations[0].Quote = content
	request.RelationshipObservations[0].Start = 0
	request.RelationshipObservations[0].End = len([]rune(content))
	response := semanticReviewResponse(request.RequestID, false, false)
	response.SecuritySignals = []verifier.SemanticSecuritySignal{{
		EvidenceID: "ev_1",
		Kind:       "prompt_secret_extraction",
		Start:      0,
		End:        5,
	}}
	provider := &semanticReviewProviderStub{responses: []verifier.SemanticReviewResponse{response}}
	ledger := &semanticReviewLedgerStub{}
	svc := NewSemanticReviewService(SemanticReviewDependencies{Provider: provider, Ledger: ledger})
	placementItemID := uuid.NewString()

	result, err := svc.ReviewSemantic(context.Background(), SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: placementItemID,
		Request:         request,
	})
	if err != nil {
		t.Fatalf("ReviewSemantic returned error: %v", err)
	}
	if result.Status != string(domain.SemanticReviewQuarantined) {
		t.Fatalf("status = %q", result.Status)
	}
	if len(result.RelationshipResults) != 0 || len(result.EntityResults) != 0 {
		t.Fatalf("quarantined result kept semantic decisions: %#v", result)
	}
	if len(ledger.securityEvents) != 1 {
		t.Fatalf("security events = %#v", ledger.securityEvents)
	}
	if got := ledger.securityEvents[0].Signals[0].Quote; got != "Renée" {
		t.Fatalf("security quote = %q", got)
	}
	if got := ledger.securityEvents[0].ScanPolicyHash; got != semanticReviewSecurityPolicyHash {
		t.Fatalf("security policy hash = %q", got)
	}
	last := ledger.outcomes[len(ledger.outcomes)-1]
	if last.UpdateItemStatus != "" || last.UpdateItemCategory != "" {
		t.Fatalf("final review outcome mutated placement item before commit: %#v", last)
	}
}

func TestSemanticReviewStopsAtRegenerationLimit(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := semanticReviewServiceRequest(teamID, ownerID)
	provider := &semanticReviewProviderStub{
		responses: []verifier.SemanticReviewResponse{
			semanticReviewResponse(request.RequestID, false, true),
		},
	}
	ledger := &semanticReviewLedgerStub{}
	svc := NewSemanticReviewService(SemanticReviewDependencies{Provider: provider, Ledger: ledger})

	result, err := svc.ReviewSemantic(context.Background(), SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         request,
		MaxAttempts:     1,
	})
	if err != nil {
		t.Fatalf("ReviewSemantic returned error: %v", err)
	}
	if result.Status != string(domain.SemanticReviewRetryable) || result.Attempts != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	last := ledger.outcomes[len(ledger.outcomes)-1]
	if last.UpdateItemStatus != "" || last.UpdateItemCategory != "" {
		t.Fatalf("retryable review outcome mutated placement item before commit: %#v", last)
	}
}

func TestSemanticReviewReturnsRetryableProviderFailure(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	provider := &semanticReviewProviderStub{err: &verifier.TimeoutError{Provider: "stub", Message: "secret host timed out"}}
	ledger := &semanticReviewLedgerStub{}
	svc := NewSemanticReviewService(SemanticReviewDependencies{Provider: provider, Ledger: ledger})

	result, err := svc.ReviewSemantic(context.Background(), SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         semanticReviewServiceRequest(teamID, ownerID),
	})
	if err != nil {
		t.Fatalf("ReviewSemantic returned error: %v", err)
	}
	if result.Status != string(domain.SemanticReviewRetryable) {
		t.Fatalf("status = %q", result.Status)
	}
	if result.FailureStage != semanticFailureStageVerification || result.FailureClass != semanticFailureClassTimeout {
		t.Fatalf("failure = %s/%s", result.FailureStage, result.FailureClass)
	}
	last := ledger.outcomes[len(ledger.outcomes)-1]
	if last.Payload["failure_stage"] != semanticFailureStageVerification || last.Payload["failure_class"] != semanticFailureClassTimeout {
		t.Fatalf("final payload failure = %#v", last.Payload)
	}
	if strings.Contains(ledger.combinedPayloadJSON(t), "secret host") {
		t.Fatalf("provider error message leaked into audit payload: %s", ledger.combinedPayloadJSON(t))
	}
}

func semanticReviewServiceRequest(teamID string, ownerID string) verifier.SemanticReviewRequest {
	content := "Mark works on Dense-Mem."
	return verifier.SemanticReviewRequest{
		RequestID:      "verify-service-1",
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []verifier.SemanticReviewEvidence{{
			EvidenceID:    "ev_1",
			FragmentID:    uuid.NewString(),
			EvidenceIndex: 0,
			Content:       content,
		}},
		EntityMentions: []verifier.SemanticEntityMention{
			{
				Ref:        "person_1",
				Surface:    "Mark",
				Kind:       "person",
				EvidenceID: "ev_1",
				Start:      0,
				End:        4,
				Candidates: []verifier.SemanticEntityCandidate{
					{EntityID: "ent-mark", CanonicalName: "Mark Huang", Kind: "person", TeamID: teamID, Status: "active"},
					{EntityID: "ent-cross-profile", CanonicalName: "Other Mark", Kind: "person", TeamID: uuid.NewString(), Status: "active"},
				},
			},
			{
				Ref:        "project_1",
				Surface:    "Dense-Mem",
				Kind:       "project",
				EvidenceID: "ev_1",
				Start:      strings.Index(content, "Dense-Mem"),
				End:        strings.Index(content, "Dense-Mem") + len("Dense-Mem"),
				Candidates: []verifier.SemanticEntityCandidate{{
					EntityID:      "ent-dense-mem",
					CanonicalName: "Dense-Mem",
					Kind:          "project",
					TeamID:        teamID,
					Status:        "active",
				}},
			},
		},
		RelationshipObservations: []verifier.SemanticRelationshipObservation{{
			Ref:               "rel_1",
			SubjectRef:        "person_1",
			OriginalPredicate: "works on",
			ObjectRef:         "project_1",
			EvidenceID:        "ev_1",
			Quote:             "Mark works on Dense-Mem.",
			Start:             0,
			End:               len(content),
			PredicateCandidates: []verifier.SemanticPredicateCandidate{{
				PredicateKey:        "works_on",
				Version:             1,
				AllowedSubjectKinds: []string{"person"},
				AllowedObjectKinds:  []string{"project"},
				RelationshipKind:    "state",
				CurrentCardinality:  "many",
				LifecycleState:      "active",
			}},
		}},
	}
}

func semanticReviewTestRuneIndex(content string, substring string) int {
	index := strings.Index(content, substring)
	if index < 0 {
		return -1
	}
	return len([]rune(content[:index]))
}

func semanticReviewResponse(requestID string, quarantined bool, omitRelationship bool) verifier.SemanticReviewResponse {
	markID := "ent-mark"
	projectID := "ent-dense-mem"
	predicate := "works_on"
	resp := verifier.SemanticReviewResponse{
		RequestID:       requestID,
		SecuritySignals: []verifier.SemanticSecuritySignal{},
		EntityResults: []verifier.SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &markID, Confidence: 0.95, Rationale: "The evidence states the person."},
			{Ref: "project_1", Action: "reuse", CandidateEntityID: &projectID, Confidence: 0.95, Rationale: "The evidence states the project."},
		},
		RelationshipResults: []verifier.SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: &predicate, EvidenceVerdict: "entailed", Confidence: 0.94, Rationale: "The evidence states the relationship."},
		},
	}
	if omitRelationship {
		resp.RelationshipResults = []verifier.SemanticRelationshipReviewResult{}
	}
	if quarantined {
		resp.SecuritySignals = []verifier.SemanticSecuritySignal{{EvidenceID: "ev_1", Kind: "prompt_secret_extraction", Start: 0, End: 4}}
	}
	return resp
}

type semanticReviewProviderStub struct {
	requests  []verifier.SemanticReviewRequest
	responses []verifier.SemanticReviewResponse
	errs      []error
	err       error
}

func (s *semanticReviewProviderStub) ReviewSemantic(_ context.Context, req verifier.SemanticReviewRequest) (verifier.SemanticReviewResponse, error) {
	s.requests = append(s.requests, req)
	if len(s.errs) > 0 {
		index := len(s.requests) - 1
		if index >= len(s.errs) {
			index = len(s.errs) - 1
		}
		if s.errs[index] != nil {
			return verifier.SemanticReviewResponse{}, s.errs[index]
		}
	}
	if s.err != nil {
		return verifier.SemanticReviewResponse{}, s.err
	}
	if len(s.responses) == 0 {
		return verifier.SemanticReviewResponse{}, errors.New("missing stub response")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func (s *semanticReviewProviderStub) ModelName() string {
	return "stub-semantic-reviewer"
}

type semanticReviewLedgerStub struct {
	outcomes       []repository.PlacementOutcomeInput
	securityEvents []repository.SecurityEventInput
}

func (s *semanticReviewLedgerStub) CreateIngest(context.Context, repository.CreateIngestInput) (*repository.CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *semanticReviewLedgerStub) GetPlacementRun(context.Context, repository.GetPlacementRunInput) (*repository.CreateIngestResult, error) {
	return nil, errors.New("unexpected GetPlacementRun")
}

func (s *semanticReviewLedgerStub) AdvanceSourceRevision(context.Context, repository.AdvanceSourceRevisionInput) (*repository.SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (s *semanticReviewLedgerStub) AppendSecurityEvent(_ context.Context, input repository.SecurityEventInput) (string, error) {
	s.securityEvents = append(s.securityEvents, input)
	return uuid.NewString(), nil
}

func (s *semanticReviewLedgerStub) AppendPlacementOutcome(_ context.Context, input repository.PlacementOutcomeInput) (string, error) {
	s.outcomes = append(s.outcomes, input)
	return uuid.NewString(), nil
}

func (s *semanticReviewLedgerStub) ClaimNextPlacementRun(context.Context, string, string, time.Duration) (*repository.PlacementRun, error) {
	return nil, errors.New("unexpected ClaimNextPlacementRun")
}

func (s *semanticReviewLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) (*repository.PlacementFirstDisposition, error) {
	return nil, errors.New("unexpected FinishPlacementRun")
}

func (s *semanticReviewLedgerStub) combinedPayloadJSON(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(s.outcomes)
	if err != nil {
		t.Fatalf("marshal outcomes: %v", err)
	}
	return string(data)
}
