package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSubmissionAssessmentWorkerAssessesWholeRunAndCommitsAtomically(t *testing.T) {
	ledger, assessments, catalog, provider, worker := submissionAssessmentWorkerFixture(t)

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	require.Equal(t, 1, provider.calls)
	require.NotNil(t, provider.request)
	assert.Len(t, provider.request.Evidence, 2)
	assert.Len(t, provider.request.SubmittedRelationships, 3)
	assert.Len(t, provider.request.SubmittedEntities, 6)
	assert.Equal(t, "evidence:0", provider.request.SubmittedRelationships[0].Evidence[0].EvidenceID)
	sharedEvidenceRelationships := 0
	for _, relationship := range provider.request.SubmittedRelationships {
		if relationship.Evidence[0].EvidenceID == "evidence:0" {
			sharedEvidenceRelationships++
		}
	}
	assert.Equal(t, 2, sharedEvidenceRelationships)
	assert.Equal(t, 1, assessments.persistCalls)
	require.Len(t, assessments.commits, 1)
	commit := assessments.commits[0]
	assert.Len(t, commit.Items, 2)
	assert.Len(t, commit.EntityResolutions, 6)
	assert.Len(t, commit.RelationshipObservations, 3)
	assert.Empty(t, commit.PredicateRegistrations)
	assert.Empty(t, assessments.completions)
	assert.Empty(t, assessments.requeues)
	assert.Equal(t, ledger.run.PlacementRunID, commit.PlacementRunID)
	assert.Equal(t, false, commit.Payload["assessment_reused"])
	assert.Len(t, catalog.entityInputs, 1)
	assert.Len(t, catalog.predicateInputs, 1)
}

func TestSubmissionAssessmentWorkerTerminalizesIncompleteCatalogBeforeProvider(t *testing.T) {
	_, assessments, catalog, provider, worker := submissionAssessmentWorkerFixture(t)
	catalog.predicateComplete = false

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.Error(t, err)
	assert.Zero(t, provider.calls)
	assert.Zero(t, assessments.persistCalls)
	assert.Empty(t, assessments.commits)
	require.Len(t, assessments.completions, 1)
	assert.Equal(t, string(domain.SemanticReviewTerminalFailure), assessments.completions[0].Status)
	assert.Equal(t, "catalog_context_overflow", assessments.completions[0].Payload["failure_stage"])
}

func TestSubmissionAssessmentWorkerHoldsWholeRunWhenOneRelationshipIsNonPromotable(t *testing.T) {
	_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	provider.response = func(req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
		response := submissionAssessmentValidResponse(req, false)
		response.RelationshipResults[1].Confidence = 0.4
		return response, nil
	}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 1, assessments.persistCalls)
	assert.Empty(t, assessments.commits)
	require.Len(t, assessments.completions, 1)
	assert.Equal(t, string(domain.SemanticReviewReviewRequired), assessments.completions[0].Status)
	assert.Equal(t, "policy_review", assessments.completions[0].Payload["failure_stage"])
}

func TestSubmissionAssessmentWorkerPassesControlledRegistrationToAtomicCommit(t *testing.T) {
	_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	provider.response = func(req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
		return submissionAssessmentValidResponse(req, true), nil
	}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, assessments.commits, 1)
	registrations := assessments.commits[0].PredicateRegistrations
	require.Len(t, registrations, 1)
	assert.Equal(t, "r:supports", registrations[0].RelationshipRef)
	assert.Equal(t, "supports", registrations[0].PredicateKey)
	assert.Equal(t, "concept", registrations[0].SubjectKind)
	assert.Equal(t, "concept", registrations[0].ObjectKind)
	for _, observation := range assessments.commits[0].RelationshipObservations {
		if observation.Observation.Ref == "r:supports" {
			assert.Empty(t, observation.Observation.PredicateKey)
			assert.Zero(t, observation.Observation.PredicateVersion)
		}
	}
}

func submissionAssessmentWorkerFixture(t *testing.T) (*submissionAssessmentWorkerLedgerStub, *submissionAssessmentWorkerAssessmentStub, *submissionAssessmentWorkerCatalogStub, *submissionAssessmentWorkerProviderStub, SubmissionAssessmentPlacementWorkerService) {
	t.Helper()
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	run := &repository.PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       uuid.NewString(),
		PlacementRunID: uuid.NewString(),
		Status:         string(domain.PlacementRunProcessing),
		Attempts:       1,
		MaxAttempts:    3,
	}
	placement := submissionAssessmentFixturePlacement(t, run)
	ledger := &submissionAssessmentWorkerLedgerStub{run: run, placement: placement}
	assessments := &submissionAssessmentWorkerAssessmentStub{policy: repository.AutoWriteConfidencePolicy{
		Threshold:     0.7,
		ConfigVersion: 1,
		Version:       repository.AssessmentPolicyVersion,
	}}
	catalog := &submissionAssessmentWorkerCatalogStub{
		predicateOptions: []repository.SemanticReviewPredicateCandidate{
			{PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"concept"}, AllowedObjectKinds: []string{"concept"}, RelationshipKind: "state", CurrentCardinality: "many", LifecycleState: "active"},
			{PredicateKey: "depends_on", Version: 1, AllowedSubjectKinds: []string{"concept"}, AllowedObjectKinds: []string{"concept"}, RelationshipKind: "state", CurrentCardinality: "many", LifecycleState: "active"},
			{PredicateKey: "supports", Version: 1, AllowedSubjectKinds: []string{"concept"}, AllowedObjectKinds: []string{"concept"}, RelationshipKind: "state", CurrentCardinality: "many", LifecycleState: "active"},
		},
		predicateComplete: true,
	}
	provider := &submissionAssessmentWorkerProviderStub{}
	worker := NewSubmissionAssessmentPlacementWorkerService(SubmissionAssessmentPlacementWorkerDependencies{
		Ledger:                    ledger,
		Assessments:               assessments,
		Catalog:                   catalog,
		Provider:                  provider,
		Limits:                    verifier.DefaultSemanticAssessmentLimits(),
		GlobalConfidenceThreshold: 0.7,
		TeamID:                    teamID,
		WorkerID:                  "submission-assessment-worker",
		Now:                       func() time.Time { return time.Unix(0, 0).UTC() },
	})
	return ledger, assessments, catalog, provider, worker
}

func submissionAssessmentFixturePlacement(t *testing.T, run *repository.PlacementRun) *repository.CreateIngestResult {
	t.Helper()
	first := "Alpha uses Beta. Alpha depends on Gamma."
	second := "Gamma supports Delta."
	firstFragment := repository.EvidenceFragment{FragmentID: uuid.NewString(), EvidenceIndex: 0, Content: first}
	secondFragment := repository.EvidenceFragment{FragmentID: uuid.NewString(), EvidenceIndex: 1, Content: second}
	return &repository.CreateIngestResult{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		IngestID:       run.IngestID,
		PlacementRunID: run.PlacementRunID,
		Proposal: map[string]any{"relationship_hints": []any{
			submissionAssessmentRelationship("r:uses", 0, "Alpha", 0, 5, "uses", 6, 10, "Beta", 11, 15, "uses", 0, 16),
			submissionAssessmentRelationship("r:depends", 0, "Alpha", 17, 22, "depends", 23, 30, "Gamma", 34, 39, "depends_on", 17, 40),
			submissionAssessmentRelationship("r:supports", 1, "Gamma", 0, 5, "supports", 6, 14, "Delta", 15, 20, "supports", 0, 21),
		}},
		Evidence: []repository.EvidenceFragment{firstFragment, secondFragment},
		Items: []repository.PlacementItem{
			{PlacementItemID: uuid.NewString(), FragmentID: firstFragment.FragmentID, ClaimKey: uuid.NewString(), EvidenceIndex: 0, Status: string(domain.PlacementRunQueued)},
			{PlacementItemID: uuid.NewString(), FragmentID: secondFragment.FragmentID, ClaimKey: uuid.NewString(), EvidenceIndex: 1, Status: string(domain.PlacementRunQueued)},
		},
	}
}

func submissionAssessmentRelationship(ref string, evidenceIndex int, subject string, subjectStart, subjectEnd int, predicate string, predicateStart, predicateEnd int, object string, objectStart, objectEnd int, proposedKey string, supportStart, supportEnd int) map[string]any {
	span := func(start, end int) map[string]any {
		return map[string]any{"evidence_index": evidenceIndex, "start": start, "end": end}
	}
	return map[string]any{
		"ref":       ref,
		"subject":   map[string]any{"name": subject, "entity_kind": "concept", "span": span(subjectStart, subjectEnd)},
		"predicate": map[string]any{"proposed_key": proposedKey, "surface": predicate, "span": span(predicateStart, predicateEnd)},
		"object":    map[string]any{"entity": map[string]any{"name": object, "entity_kind": "concept", "span": span(objectStart, objectEnd)}},
		"polarity":  "+",
		"modality":  "statement",
		"supports":  []any{span(supportStart, supportEnd)},
	}
}

func submissionAssessmentValidResponse(req verifier.SemanticAssessmentRequest, registerSupports bool) verifier.SemanticAssessmentResponse {
	entities := make([]verifier.SemanticAssessmentEntityResult, 0, len(req.SubmittedEntities))
	for _, entity := range req.SubmittedEntities {
		entities = append(entities, verifier.SemanticAssessmentEntityResult{
			Ref: entity.Ref, Surface: entity.Surface, Kind: entity.Kind, EvidenceID: entity.EvidenceID,
			Start: entity.Start, End: entity.End, Action: "create", Confidence: 0.99, Rationale: "No matching entity is in the complete candidate catalog.",
		})
	}
	relationships := make([]verifier.SemanticAssessmentRelationshipResult, 0, len(req.SubmittedRelationships))
	for _, relationship := range req.SubmittedRelationships {
		result := verifier.SemanticAssessmentRelationshipResult{
			Ref: relationship.Ref, SubjectRef: relationship.SubjectRef, OriginalPredicate: relationship.OriginalPredicate,
			ObjectRef: relationship.ObjectRef, ObjectValue: relationship.ObjectValue, Polarity: relationship.Polarity, Modality: relationship.Modality,
			Evidence: append([]verifier.SemanticAssessmentEvidenceSpan(nil), relationship.Evidence...), ScopeStatus: "absent", EvidenceVerdict: "entailed", TemporalVerdict: "absent", Confidence: 0.95,
			Rationale: "The exact submitted evidence supports the relationship.",
		}
		if registerSupports && relationship.Ref == "r:supports" {
			result.PredicateStatus = "registration_required"
		} else {
			key := map[string]string{"r:uses": "uses", "r:depends": "depends_on", "r:supports": "supports"}[relationship.Ref]
			version := 1
			result.PredicateStatus = "resolved"
			result.PredicateKey = &key
			result.PredicateVersion = &version
		}
		relationships = append(relationships, result)
	}
	return verifier.SemanticAssessmentResponse{
		RequestID:           req.RequestID,
		SecuritySignals:     []verifier.SemanticSecuritySignal{},
		EntityResults:       entities,
		RelationshipResults: relationships,
	}
}

type submissionAssessmentWorkerLedgerStub struct {
	run       *repository.PlacementRun
	placement *repository.CreateIngestResult
}

func (*submissionAssessmentWorkerLedgerStub) CreateIngest(context.Context, repository.CreateIngestInput) (*repository.CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *submissionAssessmentWorkerLedgerStub) GetPlacementRun(context.Context, repository.GetPlacementRunInput) (*repository.CreateIngestResult, error) {
	return s.placement, nil
}

func (*submissionAssessmentWorkerLedgerStub) AdvanceSourceRevision(context.Context, repository.AdvanceSourceRevisionInput) (*repository.SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (*submissionAssessmentWorkerLedgerStub) AppendSecurityEvent(context.Context, repository.SecurityEventInput) (string, error) {
	return "", errors.New("unexpected AppendSecurityEvent")
}

func (*submissionAssessmentWorkerLedgerStub) AppendPlacementOutcome(context.Context, repository.PlacementOutcomeInput) (string, error) {
	return "", errors.New("unexpected AppendPlacementOutcome")
}

func (s *submissionAssessmentWorkerLedgerStub) ClaimNextPlacementRun(context.Context, string, string, time.Duration) (*repository.PlacementRun, error) {
	return s.run, nil
}

func (*submissionAssessmentWorkerLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) (*repository.PlacementFirstDisposition, error) {
	return nil, errors.New("unexpected FinishPlacementRun")
}

type submissionAssessmentWorkerAssessmentStub struct {
	assessment   *repository.SubmissionAssessment
	persistCalls int
	reserved     bool
	policy       repository.AutoWriteConfidencePolicy
	commits      []repository.CommitSubmissionAssessmentInput
	completions  []repository.CompleteSubmissionAssessmentInput
	requeues     []repository.RequeueSubmissionAssessmentInput
}

func (s *submissionAssessmentWorkerAssessmentStub) LoadSubmissionAssessment(context.Context, repository.LoadSubmissionAssessmentInput) (*repository.SubmissionAssessment, error) {
	if s.assessment == nil {
		return nil, repository.ErrSubmissionAssessmentNotFound
	}
	return s.assessment, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) ReserveSubmissionAssessorAttempt(context.Context, repository.ReserveSubmissionAssessorAttemptInput) (bool, error) {
	if s.reserved {
		return false, nil
	}
	s.reserved = true
	return true, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) PersistSubmissionAssessment(_ context.Context, input repository.PersistSubmissionAssessmentInput) (*repository.SubmissionAssessment, bool, error) {
	s.persistCalls++
	s.assessment = &repository.SubmissionAssessment{
		TeamID: input.TeamID, AssessmentID: uuid.NewString(), OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID, PlacementRunID: input.PlacementRunID,
		RequestID: input.RequestID, AssessorContractVersion: input.AssessorContractVersion, Model: input.Model, Tokenizer: input.Tokenizer,
		InputTokens: input.InputTokens, OutputTokens: input.OutputTokens, CandidateContextTokens: input.CandidateContextTokens,
		CandidateContextTruncated: input.CandidateContextTruncated, NormalizedResponse: append(json.RawMessage(nil), input.NormalizedResponse...), ResponseHash: input.ResponseHash, ValidatedAt: input.ValidatedAt,
	}
	return s.assessment, false, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) LoadAutoWriteConfidencePolicy(context.Context, repository.LoadAutoWriteConfidencePolicyInput) (repository.AutoWriteConfidencePolicy, error) {
	return s.policy, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) CommitSubmissionAssessment(_ context.Context, input repository.CommitSubmissionAssessmentInput) (*repository.CommitSubmissionAssessmentResult, error) {
	s.commits = append(s.commits, input)
	return &repository.CommitSubmissionAssessmentResult{Status: string(domain.SemanticReviewAccepted)}, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) CompleteSubmissionAssessment(_ context.Context, input repository.CompleteSubmissionAssessmentInput) (*repository.CompleteSubmissionAssessmentResult, error) {
	s.completions = append(s.completions, input)
	return &repository.CompleteSubmissionAssessmentResult{Status: input.Status}, nil
}

func (s *submissionAssessmentWorkerAssessmentStub) RequeueSubmissionAssessment(_ context.Context, input repository.RequeueSubmissionAssessmentInput) (*repository.RequeueSubmissionAssessmentResult, error) {
	s.requeues = append(s.requeues, input)
	return &repository.RequeueSubmissionAssessmentResult{Status: string(domain.SemanticReviewRetryable)}, nil
}

type submissionAssessmentWorkerCatalogStub struct {
	entityInputs      []repository.SubmissionAssessmentEntityCatalogInput
	predicateInputs   []repository.SubmissionAssessmentPredicateCatalogInput
	predicateOptions  []repository.SemanticReviewPredicateCandidate
	predicateComplete bool
}

func (s *submissionAssessmentWorkerCatalogStub) ListSubmissionAssessmentEntityCatalog(_ context.Context, input repository.SubmissionAssessmentEntityCatalogInput) (repository.SubmissionAssessmentEntityCatalogResult, error) {
	s.entityInputs = append(s.entityInputs, input)
	groups := make([]repository.SubmissionAssessmentEntityCatalogGroup, 0, len(input.Entities))
	for _, entity := range input.Entities {
		groups = append(groups, repository.SubmissionAssessmentEntityCatalogGroup{Ref: entity.Ref, Candidates: []repository.SemanticReviewEntityCandidate{}, Complete: true})
	}
	return repository.SubmissionAssessmentEntityCatalogResult{Groups: groups, Complete: true}, nil
}

func (s *submissionAssessmentWorkerCatalogStub) ListSubmissionAssessmentPredicateCatalog(_ context.Context, input repository.SubmissionAssessmentPredicateCatalogInput) (repository.SubmissionAssessmentPredicateCatalogResult, error) {
	s.predicateInputs = append(s.predicateInputs, input)
	return repository.SubmissionAssessmentPredicateCatalogResult{Options: append([]repository.SemanticReviewPredicateCandidate(nil), s.predicateOptions...), Complete: s.predicateComplete}, nil
}

type submissionAssessmentWorkerProviderStub struct {
	calls    int
	request  *verifier.SemanticAssessmentRequest
	response func(verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error)
}

func (s *submissionAssessmentWorkerProviderStub) AssessSemantic(_ context.Context, req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
	s.calls++
	s.request = &req
	if s.response != nil {
		return s.response(req)
	}
	return submissionAssessmentValidResponse(req, false), nil
}

func (*submissionAssessmentWorkerProviderStub) ModelName() string {
	return "submission-assessment-model"
}
