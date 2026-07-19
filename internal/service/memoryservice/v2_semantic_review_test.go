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

func TestV2SemanticReviewRetriesCompleteResponseAndDoesNotPersistPartialInvalidAttempt(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	provider := &v2SemanticReviewProviderStub{
		responses: []verifier.V2SemanticReviewResponse{
			v2SemanticReviewResponse(request.RequestID, false, true),
			v2SemanticReviewResponse(request.RequestID, false, false),
		},
	}
	ledger := &v2SemanticReviewLedgerStub{}
	svc := NewV2SemanticReviewService(V2SemanticReviewDependencies{
		Provider: provider,
		Ledger:   ledger,
	})

	result, err := svc.ReviewV2Semantic(context.Background(), V2SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         request,
		MaxAttempts:     2,
	})
	if err != nil {
		t.Fatalf("ReviewV2Semantic returned error: %v", err)
	}
	if result.Status != string(domain.V2SemanticReviewAccepted) || result.Attempts != 2 {
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
	if ledger.outcomes[0].Status != "invalid" || ledger.outcomes[1].Status != "valid" || ledger.outcomes[2].Status != string(domain.V2SemanticReviewAccepted) {
		t.Fatalf("outcome statuses = %#v", ledger.outcomes)
	}
	combinedPayload := ledger.combinedPayloadJSON(t)
	if strings.Contains(combinedPayload, "Mark works") || strings.Contains(combinedPayload, "The evidence states") {
		t.Fatalf("audit payload leaked evidence or rationale: %s", combinedPayload)
	}
}

func TestV2SemanticReviewForcesJobIdentityOntoProviderRequest(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	request.TeamID = uuid.NewString()
	request.OwnerProfileID = uuid.NewString()
	provider := &v2SemanticReviewProviderStub{
		responses: []verifier.V2SemanticReviewResponse{
			v2SemanticReviewResponse(request.RequestID, false, false),
		},
	}
	ledger := &v2SemanticReviewLedgerStub{}
	svc := NewV2SemanticReviewService(V2SemanticReviewDependencies{Provider: provider, Ledger: ledger})

	result, err := svc.ReviewV2Semantic(context.Background(), V2SemanticReviewJob{
		TeamID:          " " + teamID + " ",
		OwnerProfileID:  " " + ownerID + " ",
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         request,
	})
	if err != nil {
		t.Fatalf("ReviewV2Semantic returned error: %v", err)
	}
	if result.Status != string(domain.V2SemanticReviewAccepted) {
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

func TestV2SemanticReviewQuarantinesSecuritySignalsWithoutSemanticDecisions(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	content := "Renée works on Dense-Mem."
	request.Evidence[0].Content = content
	request.EntityMentions[0].Surface = "Renée"
	request.EntityMentions[0].Start = 0
	request.EntityMentions[0].End = 5
	projectStart := v2SemanticReviewTestRuneIndex(content, "Dense-Mem")
	request.EntityMentions[1].Start = projectStart
	request.EntityMentions[1].End = projectStart + len([]rune("Dense-Mem"))
	request.RelationshipObservations[0].Quote = content
	request.RelationshipObservations[0].Start = 0
	request.RelationshipObservations[0].End = len([]rune(content))
	response := v2SemanticReviewResponse(request.RequestID, false, false)
	response.SecuritySignals = []verifier.V2SemanticSecuritySignal{{
		EvidenceID: "ev_1",
		Kind:       "prompt_secret_extraction",
		Start:      0,
		End:        5,
	}}
	provider := &v2SemanticReviewProviderStub{responses: []verifier.V2SemanticReviewResponse{response}}
	ledger := &v2SemanticReviewLedgerStub{}
	svc := NewV2SemanticReviewService(V2SemanticReviewDependencies{Provider: provider, Ledger: ledger})
	placementItemID := uuid.NewString()

	result, err := svc.ReviewV2Semantic(context.Background(), V2SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: placementItemID,
		Request:         request,
	})
	if err != nil {
		t.Fatalf("ReviewV2Semantic returned error: %v", err)
	}
	if result.Status != string(domain.V2SemanticReviewQuarantined) {
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
	last := ledger.outcomes[len(ledger.outcomes)-1]
	if last.UpdateItemStatus != "quarantined" || last.UpdateItemCategory != "quarantined" {
		t.Fatalf("final outcome did not quarantine item: %#v", last)
	}
}

func TestV2SemanticReviewStopsAtRegenerationLimit(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	request := v2SemanticReviewServiceRequest(teamID, ownerID)
	provider := &v2SemanticReviewProviderStub{
		responses: []verifier.V2SemanticReviewResponse{
			v2SemanticReviewResponse(request.RequestID, false, true),
		},
	}
	ledger := &v2SemanticReviewLedgerStub{}
	svc := NewV2SemanticReviewService(V2SemanticReviewDependencies{Provider: provider, Ledger: ledger})

	result, err := svc.ReviewV2Semantic(context.Background(), V2SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         request,
		MaxAttempts:     1,
	})
	if err != nil {
		t.Fatalf("ReviewV2Semantic returned error: %v", err)
	}
	if result.Status != string(domain.V2SemanticReviewTerminalFailure) || result.Attempts != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls = %d", len(provider.requests))
	}
	last := ledger.outcomes[len(ledger.outcomes)-1]
	if last.UpdateItemStatus != "failed" || last.UpdateItemCategory != "failed" {
		t.Fatalf("terminal failure did not mark item failed: %#v", last)
	}
}

func TestV2SemanticReviewReturnsRetryableProviderFailure(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	provider := &v2SemanticReviewProviderStub{err: errors.New("provider unavailable")}
	ledger := &v2SemanticReviewLedgerStub{}
	svc := NewV2SemanticReviewService(V2SemanticReviewDependencies{Provider: provider, Ledger: ledger})

	result, err := svc.ReviewV2Semantic(context.Background(), V2SemanticReviewJob{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        uuid.NewString(),
		PlacementRunID:  uuid.NewString(),
		PlacementItemID: uuid.NewString(),
		Request:         v2SemanticReviewServiceRequest(teamID, ownerID),
	})
	if err != nil {
		t.Fatalf("ReviewV2Semantic returned error: %v", err)
	}
	if result.Status != string(domain.V2SemanticReviewRetryable) {
		t.Fatalf("status = %q", result.Status)
	}
	if strings.Contains(ledger.combinedPayloadJSON(t), "provider unavailable") {
		t.Fatalf("provider error message leaked into audit payload: %s", ledger.combinedPayloadJSON(t))
	}
}

func v2SemanticReviewServiceRequest(teamID string, ownerID string) verifier.V2SemanticReviewRequest {
	content := "Mark works on Dense-Mem."
	return verifier.V2SemanticReviewRequest{
		RequestID:      "verify-service-1",
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []verifier.V2SemanticReviewEvidence{{
			EvidenceID:    "ev_1",
			FragmentID:    uuid.NewString(),
			EvidenceIndex: 0,
			Content:       content,
		}},
		EntityMentions: []verifier.V2SemanticEntityMention{
			{
				Ref:        "person_1",
				Surface:    "Mark",
				Kind:       "person",
				EvidenceID: "ev_1",
				Start:      0,
				End:        4,
				Candidates: []verifier.V2SemanticEntityCandidate{
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
				Candidates: []verifier.V2SemanticEntityCandidate{{
					EntityID:      "ent-dense-mem",
					CanonicalName: "Dense-Mem",
					Kind:          "project",
					TeamID:        teamID,
					Status:        "active",
				}},
			},
		},
		RelationshipObservations: []verifier.V2SemanticRelationshipObservation{{
			Ref:               "rel_1",
			SubjectRef:        "person_1",
			OriginalPredicate: "works on",
			ObjectRef:         "project_1",
			EvidenceID:        "ev_1",
			Quote:             "Mark works on Dense-Mem.",
			Start:             0,
			End:               len(content),
			PredicateCandidates: []verifier.V2SemanticPredicateCandidate{{
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

func v2SemanticReviewTestRuneIndex(content string, substring string) int {
	index := strings.Index(content, substring)
	if index < 0 {
		return -1
	}
	return len([]rune(content[:index]))
}

func v2SemanticReviewResponse(requestID string, quarantined bool, omitRelationship bool) verifier.V2SemanticReviewResponse {
	markID := "ent-mark"
	projectID := "ent-dense-mem"
	predicate := "works_on"
	resp := verifier.V2SemanticReviewResponse{
		RequestID:       requestID,
		SecuritySignals: []verifier.V2SemanticSecuritySignal{},
		EntityResults: []verifier.V2SemanticEntityResult{
			{Ref: "person_1", Action: "reuse", CandidateEntityID: &markID, Confidence: 0.95, Rationale: "The evidence states the person."},
			{Ref: "project_1", Action: "reuse", CandidateEntityID: &projectID, Confidence: 0.95, Rationale: "The evidence states the project."},
		},
		RelationshipResults: []verifier.V2SemanticRelationshipReviewResult{
			{Ref: "rel_1", PredicateStatus: "resolved", PredicateKey: &predicate, EvidenceVerdict: "entailed", Confidence: 0.94, Rationale: "The evidence states the relationship."},
		},
	}
	if omitRelationship {
		resp.RelationshipResults = []verifier.V2SemanticRelationshipReviewResult{}
	}
	if quarantined {
		resp.SecuritySignals = []verifier.V2SemanticSecuritySignal{{EvidenceID: "ev_1", Kind: "prompt_secret_extraction", Start: 0, End: 4}}
	}
	return resp
}

type v2SemanticReviewProviderStub struct {
	requests  []verifier.V2SemanticReviewRequest
	responses []verifier.V2SemanticReviewResponse
	err       error
}

func (s *v2SemanticReviewProviderStub) ReviewV2Semantic(_ context.Context, req verifier.V2SemanticReviewRequest) (verifier.V2SemanticReviewResponse, error) {
	s.requests = append(s.requests, req)
	if s.err != nil {
		return verifier.V2SemanticReviewResponse{}, s.err
	}
	if len(s.responses) == 0 {
		return verifier.V2SemanticReviewResponse{}, errors.New("missing stub response")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func (s *v2SemanticReviewProviderStub) ModelName() string {
	return "stub-semantic-reviewer"
}

type v2SemanticReviewLedgerStub struct {
	outcomes       []repository.V2PlacementOutcomeInput
	securityEvents []repository.V2SecurityEventInput
}

func (s *v2SemanticReviewLedgerStub) CreateIngest(context.Context, repository.V2CreateIngestInput) (*repository.V2CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *v2SemanticReviewLedgerStub) AdvanceSourceRevision(context.Context, repository.V2AdvanceSourceRevisionInput) (*repository.V2SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (s *v2SemanticReviewLedgerStub) AppendSecurityEvent(_ context.Context, input repository.V2SecurityEventInput) (string, error) {
	s.securityEvents = append(s.securityEvents, input)
	return uuid.NewString(), nil
}

func (s *v2SemanticReviewLedgerStub) AppendPlacementOutcome(_ context.Context, input repository.V2PlacementOutcomeInput) (string, error) {
	s.outcomes = append(s.outcomes, input)
	return uuid.NewString(), nil
}

func (s *v2SemanticReviewLedgerStub) ClaimNextPlacementRun(context.Context, string, string, time.Duration) (*repository.V2PlacementRun, error) {
	return nil, errors.New("unexpected ClaimNextPlacementRun")
}

func (s *v2SemanticReviewLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) error {
	return errors.New("unexpected FinishPlacementRun")
}

func (s *v2SemanticReviewLedgerStub) combinedPayloadJSON(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(s.outcomes)
	if err != nil {
		t.Fatalf("marshal outcomes: %v", err)
	}
	return string(data)
}
