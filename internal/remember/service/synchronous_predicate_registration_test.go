package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

func TestSynchronousAssessmentRepairsPredicateCatalogConflict(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.catalog.registrationValidate = func(input repository.SubmissionPredicateRegistrationValidationInput) ([]repository.SubmissionPredicateRegistrationIssue, error) {
		if input.Registrations[0].PredicateKey == "retired_key" {
			fixture.catalog.predicateOptions = []repository.SemanticReviewPredicateCandidate{{
				PredicateKey: "late_option", Version: 1, AllowedSubjectKinds: []string{"concept"},
				AllowedObjectKinds: []string{"concept"}, RelationshipKind: "state", CurrentCardinality: "many", LifecycleState: "active",
			}}
			return []repository.SubmissionPredicateRegistrationIssue{{RegistrationIndex: 0, Field: "subject_kind", Message: "is incompatible with the existing predicate"}}, nil
		}
		return nil, nil
	}
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, turn int) assessor.SemanticAssessmentResponse {
		key := "fresh_key"
		if turn == 1 {
			key = "retired_key"
		}
		return synchronousResponseWithRegistration(request, key)
	}

	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)
	require.Equal(t, 1, fixture.provider.repairCalls)
	require.Equal(t, 2, prepared.Response.ProviderTurns)
	require.Len(t, fixture.catalog.registrationInputs, 2)
	require.Equal(t, fixture.input.Scope.TeamID, fixture.catalog.registrationInputs[0].TeamID)
	require.Equal(t, fixture.input.Scope.OwnerProfileID, fixture.catalog.registrationInputs[0].OwnerProfileID)
	require.Equal(t, "retired_key", fixture.catalog.registrationInputs[0].Registrations[0].PredicateKey)
	require.Equal(t, "fresh_key", fixture.catalog.registrationInputs[1].Registrations[0].PredicateKey)
	require.Equal(t, "late_option", fixture.catalog.predicateOptions[0].PredicateKey)
	require.Equal(t, fixture.provider.assessRequest, fixture.provider.repairRequest)
	require.Equal(t, "relationship_results[0].splits[0].predicate_registration.predicate_key", fixture.provider.repairErrors[0].Field)
	for _, option := range prepared.Request.PredicateOptions {
		require.NotEqual(t, "retired_key", option.PredicateKey)
	}
	commit, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{Assessment: prepared})
	require.NoError(t, err)
	require.Len(t, commit.Commit.PredicateRegistrations, 1)
	require.Equal(t, fixture.catalog.registrationInputs[1].Registrations[0], commit.Commit.PredicateRegistrations[0])
}

func TestSynchronousAssessmentPredicateCatalogConflictExhaustsCompleteRepair(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.catalog.registrationIssues = []repository.SubmissionPredicateRegistrationIssue{{RegistrationIndex: 0, Field: "predicate_key", Message: "resolves to a predicate that is not active"}}
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		return synchronousResponseWithRegistration(request, "retired_key")
	}

	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.Nil(t, prepared)
	require.ErrorIs(t, err, ErrRememberProviderResponseInvalid)
	require.Equal(t, SemanticMaxAssessorTurns, SynchronousAssessmentProviderTurns(err))
	require.Equal(t, SemanticMaxAssessorTurns-1, fixture.provider.repairCalls)
	require.Len(t, fixture.catalog.registrationInputs, SemanticMaxAssessorTurns)
	diagnostics := SynchronousAssessmentValidationDiagnostics(err)
	require.NotNil(t, diagnostics)
}

func TestSynchronousAssessmentPredicateCatalogOperationalFailure(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.catalog.registrationErr = errors.New("catalog unavailable")
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		return synchronousResponseWithRegistration(request, "fresh_key")
	}

	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.Nil(t, prepared)
	require.ErrorIs(t, err, ErrRememberDatabaseFailure)
	require.Equal(t, 1, SynchronousAssessmentProviderTurns(err))
	require.Zero(t, fixture.provider.repairCalls)
}

func TestSynchronousAssessmentPredicateCatalogCancellation(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.catalog.registrationErr = context.Canceled
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		return synchronousResponseWithRegistration(request, "fresh_key")
	}

	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.Nil(t, prepared)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, SynchronousAssessmentProviderTurns(err))
	require.Zero(t, fixture.provider.repairCalls)
}

func synchronousResponseWithRegistration(request assessor.SemanticAssessmentRequest, key string) assessor.SemanticAssessmentResponse {
	response := validSynchronousAssessmentResponse(request)
	split := &response.RelationshipResults[0].Splits[0]
	split.PredicateStatus = "registration_required"
	split.PredicateKey = nil
	split.PredicateVersion = nil
	split.PredicateRegistration = &assessor.SemanticAssessmentPredicateRegistration{
		PredicateKey: key, RelationshipKind: "state", CurrentCardinality: "many",
	}
	return response
}
