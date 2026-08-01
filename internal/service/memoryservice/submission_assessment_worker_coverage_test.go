package memoryservice

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSubmissionAssessmentWorkerValidatesDependenciesAndOutcomeHelpers(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*submissionAssessmentWorkerService)
		want   string
	}{
		{"missing catalog", func(service *submissionAssessmentWorkerService) { service.catalog = nil }, "semantic catalog"},
		{"missing provider", func(service *submissionAssessmentWorkerService) { service.provider = nil }, "assessor provider"},
		{"invalid team", func(service *submissionAssessmentWorkerService) { service.teamID = "invalid" }, "team_id is required"},
		{"missing worker", func(service *submissionAssessmentWorkerService) { service.workerID = "" }, "worker_id is required"},
		{"invalid threshold", func(service *submissionAssessmentWorkerService) { service.globalConfidenceThreshold = 1.1 }, "confidence threshold"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, _, worker := submissionAssessmentWorkerFixture(t)
			service := worker.(*submissionAssessmentWorkerService)
			testCase.mutate(service)
			require.ErrorContains(t, service.validateDependencies(), testCase.want)
		})
	}

	worker := NewSubmissionAssessmentWorkerService(SubmissionAssessmentWorkerDependencies{Lease: time.Millisecond})
	implementation := worker.(*submissionAssessmentWorkerService)
	require.Equal(t, time.Minute, implementation.lease)
	require.NotNil(t, implementation.now)
	require.NotNil(t, implementation.metrics)

	require.Empty(t, submissionEvidenceOutcomes(nil, "rejected", "policy", "not_required"))
	outcomes := submissionEvidenceOutcomes(&repository.Submission{Evidence: []repository.SubmissionEvidence{{EvidenceIndex: 3}}}, "rejected", "policy", "not_required")
	require.Equal(t, []repository.SubmissionEvidenceStatus{{EvidenceIndex: 3, Status: "rejected", ReasonCode: "policy", SearchState: "not_required"}}, outcomes)
	rejected := submissionRejectedRelationshipOutcomes(submissionAssessmentProposal{Relationships: []submissionAssessmentRelationshipProposal{{ProposalID: "rel-1"}}}, "policy")
	require.Equal(t, "rel-1", rejected[0].ProposalID)
	require.Equal(t, string(domain.SubmissionRejected), rejected[0].Status)
	require.Equal(t, 3, maxSubmissionProviderTurns(3))
	require.Equal(t, 1, maxSubmissionProviderTurns(0))
}

func TestSubmissionAssessmentRejectionPolicyRejectsUnsafeAssessorResults(t *testing.T) {
	repo, _, provider, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentWorkerService)
	request, proposal, err := service.buildRequest(context.Background(), repo.staged)
	require.NoError(t, err)

	for _, testCase := range []struct {
		name   string
		mutate func(*verifier.SubmissionAssessmentResponse)
		want   string
	}{
		{"missing entity", func(response *verifier.SubmissionAssessmentResponse) {
			response.EntityResults = response.EntityResults[:1]
		}, "assessor_entity_correspondence_rejected"},
		{"unknown entity", func(response *verifier.SubmissionAssessmentResponse) { response.EntityResults[0].Ref = "unknown" }, "assessor_entity_correspondence_rejected"},
		{"ambiguous entity", func(response *verifier.SubmissionAssessmentResponse) {
			response.EntityResults[0].Action = string(domain.EntityResolutionAmbiguous)
		}, "assessor_entity_confidence_rejected"},
		{"low entity confidence", func(response *verifier.SubmissionAssessmentResponse) { response.EntityResults[0].Confidence = 0.1 }, "assessor_entity_confidence_rejected"},
		{"missing relationship", func(response *verifier.SubmissionAssessmentResponse) { response.RelationshipResults = nil }, "assessor_relationship_correspondence_rejected"},
		{"changed relationship", func(response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].SubjectRef = "unknown"
		}, "assessor_relationship_correspondence_rejected"},
		{"unsupported semantic verdict", func(response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].EvidenceVerdict = string(domain.VerificationContradicted)
		}, "assessor_semantic_rejected"},
		{"incomplete resolved predicate", func(response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].PredicateKey = nil
		}, "assessor_predicate_rejected"},
		{"unsupplied resolved predicate", func(response *verifier.SubmissionAssessmentResponse) {
			key := "other"
			response.RelationshipResults[0].PredicateKey = &key
		}, "assessor_predicate_rejected"},
		{"missing novel predicate candidate", func(response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].PredicateStatus = "needs_review"
			response.RelationshipResults[0].PredicateKey = nil
			response.RelationshipResults[0].PredicateVersion = nil
		}, "assessor_predicate_rejected"},
		{"unsupported predicate state", func(response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].PredicateStatus = "other"
		}, "assessor_predicate_rejected"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := submissionAssessmentWorkerValidResponse(request, provider.subjectID, provider.objectID)
			testCase.mutate(&response)
			require.Equal(t, testCase.want, submissionAssessmentRejectionReason(request, proposal, response, 0.7))
		})
	}

	accepted := submissionAssessmentWorkerValidResponse(request, provider.subjectID, provider.objectID)
	accepted.RelationshipResults[0].PredicateStatus = "needs_review"
	accepted.RelationshipResults[0].PredicateKey = nil
	accepted.RelationshipResults[0].PredicateVersion = nil
	accepted.RelationshipResults[0].PredicateCandidate = &verifier.SubmissionPredicateCandidate{PredicateKey: "novel_relation", RelationshipKind: "state"}
	require.Empty(t, submissionAssessmentRejectionReason(request, proposal, accepted, 0.7))
}

func TestSubmissionAssessmentSupportAndPromotionHelpersFailClosed(t *testing.T) {
	valueExpected := submissionAssessmentRelationshipProposal{ObjectValue: map[string]any{"type": "string"}}
	require.True(t, submissionRelationshipObjectMatches(valueExpected, verifier.SemanticAssessmentRelationshipResult{ObjectValue: &verifier.SemanticAssessmentValue{ValueType: "string"}}))
	wrongObject := "entity"
	require.False(t, submissionRelationshipObjectMatches(valueExpected, verifier.SemanticAssessmentRelationshipResult{ObjectRef: &wrongObject}))
	require.True(t, submissionRelationshipSpansMatch(
		[]submissionProposalSpan{{EvidenceIndex: 0, Start: 0, End: 1}},
		[]verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 0, End: 1}},
	))
	require.False(t, submissionRelationshipSpansMatch(
		[]submissionProposalSpan{{EvidenceIndex: 0, Start: 0, End: 1}},
		[]verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 1, End: 2}},
	))

	fragments := map[string]repository.EvidenceFragment{
		"evidence:0": {FragmentID: "fragment-1", Content: "Dense-Mem uses PostgreSQL.", Authority: string(domain.AuthorityPrimary)},
	}
	supports, err := submissionAssessmentSupport(fragments, "assessment-1", []verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 0, End: 9}})
	require.NoError(t, err)
	require.Equal(t, "Dense-Mem", supports[0].Quote)
	_, err = submissionAssessmentSupport(fragments, "assessment-1", nil)
	require.ErrorContains(t, err, "no evidence support")
	_, err = submissionAssessmentSupport(fragments, "assessment-1", []verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "missing", Start: 0, End: 1}})
	require.ErrorContains(t, err, "unknown evidence")
	_, err = submissionAssessmentSupport(map[string]repository.EvidenceFragment{"evidence:0": {FragmentID: "fragment", Content: "text", Authority: "invalid"}}, "assessment-1", []verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 0, End: 1}})
	require.Error(t, err)
	_, err = submissionAssessmentSupport(fragments, "assessment-1", []verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 0, End: 999}})
	require.Error(t, err)

	repo, _, provider, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentWorkerService)
	request, proposal, err := service.buildRequest(context.Background(), repo.staged)
	require.NoError(t, err)
	response := submissionAssessmentWorkerValidResponse(request, provider.subjectID, provider.objectID)
	assessment := &repository.SubmissionAssessment{AssessmentID: "assessment-1", RequestID: request.RequestID, Model: "model", ResponseHash: "sha256:response"}
	promotion, err := submissionPromotionInput(repo.claim, repo.staged, proposal, request, response, assessment, 0.7, "worker", time.Minute)
	require.NoError(t, err)
	require.Len(t, promotion.Commits, 1)
	require.Equal(t, "assessment-1", promotion.Commits[0].EntityResolutions[0].SubmissionAssessmentID)
	_, err = submissionPromotionInput(repo.claim, nil, proposal, request, response, assessment, 0.7, "worker", time.Minute)
	require.ErrorContains(t, err, "staged submission and assessment are required")

	unknownEvidence := submissionAssessmentWorkerValidResponse(request, provider.subjectID, provider.objectID)
	unknownEvidence.EntityResults[0].EvidenceID = "missing"
	_, err = submissionPromotionInput(repo.claim, repo.staged, proposal, request, unknownEvidence, assessment, 0.7, "worker", time.Minute)
	require.ErrorContains(t, err, "unknown evidence")
}

func TestSubmissionPromotionInputRejectsIncompleteAssessmentResults(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*repository.Submission, *verifier.SubmissionAssessmentResponse)
		want   string
	}{
		{"invalid staged submission", func(staged *repository.Submission, _ *verifier.SubmissionAssessmentResponse) { staged.Proposal = nil }, "staged proposal.entities is required"},
		{"reuse without candidate", func(_ *repository.Submission, response *verifier.SubmissionAssessmentResponse) {
			response.EntityResults[0].CandidateEntityID = nil
		}, "reuse result has no candidate"},
		{"unresolved entity", func(_ *repository.Submission, response *verifier.SubmissionAssessmentResponse) {
			response.EntityResults[0].Action = string(domain.EntityResolutionAmbiguous)
		}, "unresolved entity result"},
		{"relationship support absent", func(_ *repository.Submission, response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].Evidence = nil
		}, "no evidence support"},
		{"resolved predicate incomplete", func(_ *repository.Submission, response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].PredicateKey = nil
		}, "resolved predicate is incomplete"},
		{"resolved predicate not supplied", func(_ *repository.Submission, response *verifier.SubmissionAssessmentResponse) {
			key := "other"
			response.RelationshipResults[0].PredicateKey = &key
		}, "resolved predicate was not supplied"},
		{"novel predicate missing candidate", func(_ *repository.Submission, response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].PredicateStatus = "needs_review"
			response.RelationshipResults[0].PredicateKey = nil
			response.RelationshipResults[0].PredicateVersion = nil
		}, "novel predicate candidate is required"},
		{"unsupported predicate status", func(_ *repository.Submission, response *verifier.SubmissionAssessmentResponse) {
			response.RelationshipResults[0].PredicateStatus = "unsupported"
		}, "unsupported predicate status"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo, _, provider, worker := submissionAssessmentWorkerFixture(t)
			service := worker.(*submissionAssessmentWorkerService)
			request, proposal, err := service.buildRequest(context.Background(), repo.staged)
			require.NoError(t, err)
			response := submissionAssessmentWorkerValidResponse(request, provider.subjectID, provider.objectID)
			testCase.mutate(repo.staged, &response)
			_, err = submissionPromotionInput(
				repo.claim,
				repo.staged,
				proposal,
				request,
				response,
				&repository.SubmissionAssessment{AssessmentID: "assessment-1", RequestID: request.RequestID, Model: "model", ResponseHash: "sha256:response"},
				0.7,
				"worker",
				time.Minute,
			)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}
