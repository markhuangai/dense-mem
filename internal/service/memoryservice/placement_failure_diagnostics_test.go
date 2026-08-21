package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestPlacementFailureDiagnosticDoesNotClassifyRepositoryErrorsAsProviderFailures(t *testing.T) {
	diagnostic := placementFailureDiagnosticFor("assessment", errors.New("database unavailable"))

	require.Equal(t, "assessment", diagnostic.Stage)
	require.Equal(t, "internal", diagnostic.Class)
	require.Equal(t, "unknown_internal_failure", diagnostic.ReasonCode)
	require.Zero(t, diagnostic.ProviderStatus)
}

func TestPlacementFailureDiagnosticRetainsBoundedProviderMetadata(t *testing.T) {
	diagnostic := placementFailureDiagnosticFor("assessment", &verifier.ProviderError{
		Provider:     "provider",
		FailureClass: verifier.ProviderFailureClassHTTPServer,
		StatusCode:   503,
	})

	require.Equal(t, "http_5xx", diagnostic.Class)
	require.Equal(t, "assessor_provider_failed", diagnostic.ReasonCode)
	require.Equal(t, 503, diagnostic.ProviderStatus)
}

func TestPlacementWorkerFailureExposesOnlyBoundedContext(t *testing.T) {
	err := newPlacementWorkerError(" team ", " submission ", "assessment", errors.New("database secret"))
	failure, ok := PlacementWorkerFailureFromError(err)
	require.True(t, ok)
	require.Equal(t, "team", failure.TeamID)
	require.Equal(t, "submission", failure.SubmissionID)
	require.Equal(t, "assessment", failure.Stage)
	require.Equal(t, "repository_persistence_failed", failure.ReasonCode)
	require.Equal(t, "repository", failure.Class)
	require.NotContains(t, err.Error(), "database secret")
}

func TestPlacementWorkerFailurePreservesSpecificRepositoryCauseClassification(t *testing.T) {
	for _, test := range []struct {
		name   string
		cause  error
		class  string
		reason string
	}{
		{name: "lease lost", cause: repository.ErrPlacementLeaseLost, class: "lease_lost", reason: "lease_lost"},
		{name: "canceled", cause: context.Canceled, class: "canceled", reason: "unknown_internal_failure"},
		{name: "deadline", cause: context.DeadlineExceeded, class: "deadline", reason: "unknown_internal_failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := newPlacementWorkerError("team", "submission", "assessment", test.cause)
			failure, ok := PlacementWorkerFailureFromError(err)
			require.True(t, ok)
			require.Equal(t, test.class, failure.Class)
			require.Equal(t, test.reason, failure.ReasonCode)
		})
	}
}

func TestPlacementFailureDiagnosticCapturesMalformedValidationAndMeasurement(t *testing.T) {
	malformed := &verifier.MalformedResponseError{
		FailureClass:            "malformed_exhausted",
		Attempts:                3,
		ValidationStage:         "response_contract",
		ValidationFieldFamilies: []string{"response", "response", "relationship_results.ref"},
		Measurement:             &verifier.FailureMeasurement{Unit: "tokens", Observed: 120, ObservedAtLeast: true, Limit: 100},
	}
	diagnostic := placementFailureDiagnosticFor("assessment", malformed)
	require.Equal(t, "assessment", diagnostic.Stage)
	require.Equal(t, "malformed_exhausted", diagnostic.Class)
	require.Equal(t, "assessor_response_invalid", diagnostic.ReasonCode)
	require.Equal(t, "response_contract", diagnostic.ValidationStage)
	require.Equal(t, []string{"response", "relationship_results.ref"}, diagnostic.ValidationFieldFamilies)
	require.Equal(t, 3, diagnostic.AssessorTurns)
	require.Equal(t, &verifier.FailureMeasurement{Unit: "tokens", Observed: 120, ObservedAtLeast: true, Limit: 100}, diagnostic.Measurement)

	payload := diagnostic.payload(true)
	require.Equal(t, "assessment", payload["failure_stage"])
	require.Equal(t, "malformed_exhausted", payload["failure_class"])
	require.Equal(t, true, payload["assessor_provider_attempted"])
	require.Equal(t, "response_contract", payload["validation_stage"])
	require.Equal(t, 3, payload["assessor_turns"])
	require.Equal(t, map[string]any{"unit": "tokens", "observed_at_least": 120, "limit": 100}, payload["failure_measurement"])
}

func TestPlacementFailureDiagnosticBoundsBranchesAndRetryErrors(t *testing.T) {
	fields := make([]string, 0, 34)
	for i := 0; i < 34; i++ {
		fields = append(fields, fmt.Sprintf("field_%02d", i))
	}
	fields = append(fields, strings.Repeat("x", 65), "", "field_00")
	require.Len(t, boundedValidationFieldFamilies(fields), 32)
	require.Equal(t, "", boundedValidationStage("unknown"))
	require.Equal(t, "internal", boundedPlacementFailureClass("unknown"))
	require.Nil(t, cloneFailureMeasurement(nil))
	require.Nil(t, cloneFailureMeasurement(&verifier.FailureMeasurement{Unit: "seconds", Observed: 1, Limit: 2}))
	require.Nil(t, cloneFailureMeasurement(&verifier.FailureMeasurement{Unit: "tokens", Observed: -1, Limit: 2}))
	require.Nil(t, cloneFailureMeasurement(&verifier.FailureMeasurement{Unit: "tokens", Observed: 1, Limit: -1}))
	require.Nil(t, failureMeasurementPayload(nil))
	require.Equal(t, 0, boundedPositive(-1))
	require.Equal(t, 1000, boundedPositive(1001))
	require.Equal(t, 0, boundedProviderStatus(99))
	require.Equal(t, 0, boundedProviderStatus(600))

	for _, test := range []struct {
		stage, class, reason string
	}{
		{"entity_catalog", "", "entity_catalog_candidate_limit_exceeded"},
		{"catalog_context", "", "candidate_context_token_limit_exceeded"},
		{"assessment_input", "", "assessment_input_token_limit_exceeded"},
		{"predicate_options_overflow", "", "predicate_option_limit_exceeded"},
		{"contract_superseded", "", "contract_superseded"},
		{"replacement_conflict", "", "replacement_conflict"},
		{"assessment_attempt_consumed", "", "assessor_attempt_consumed"},
		{"deterministic_security_scan", "", "security_quarantine"},
		{"semantic_commit", "", "semantic_commit_failed"},
		{"placement_load", "", "placement_load_failed"},
		{"", "timeout", "assessor_provider_failed"},
		{"", "lease_lost", "lease_lost"},
		{"", "internal", "unknown_internal_failure"},
	} {
		require.Equal(t, test.reason, placementFailureReasonCode(test.stage, test.class))
	}

	providerDiagnostic := placementFailureDiagnosticForProvider("assessment", verifier.ProviderFailureMetadata{Class: "http_5xx", StatusCode: 503})
	require.Equal(t, "http_5xx", providerDiagnostic.Class)
	require.Equal(t, 503, providerDiagnostic.ProviderStatus)

	for _, test := range []struct {
		cause error
		class string
	}{
		{repository.ErrPlacementLeaseLost, "lease_lost"},
		{context.DeadlineExceeded, "deadline"},
		{context.Canceled, "canceled"},
	} {
		require.Equal(t, test.class, placementFailureDiagnosticFor("assessment", test.cause).Class)
	}

	require.False(t, isVerifierProviderFailure(errors.New("repository error")))
	_, workerOK := placementWorkerFailureFromError(errors.New("not a worker error"))
	require.False(t, workerOK)
	require.Error(t, retryAfterError(errors.New("original"), func() error { return errors.New("persist") }))
	require.NoError(t, retryAfterError(errors.New("original"), func() error { return nil }))
	require.Nil(t, firstError(nil))
	require.EqualError(t, firstError([]error{errors.New("first"), errors.New("second")}), "first")

	var workerErr *placementWorkerError
	cause := errors.New("cause")
	err := newPlacementWorkerDiagnosticError("team", "submission", placementFailureDiagnostic{Stage: "assessment", Class: "internal", ReasonCode: "unknown_internal_failure"}, cause)
	require.True(t, errors.As(err, &workerErr))
	require.ErrorIs(t, err, cause)
	require.Equal(t, cause, workerErr.Unwrap())
}
