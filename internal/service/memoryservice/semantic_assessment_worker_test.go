package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSemanticAssessmentWorkerPersistsOnceAndReusesAcrossClaimRetry(t *testing.T) {
	ledger, assessments, commit, catalog, provider, worker := semanticAssessmentWorkerFixture(t)

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	require.Equal(t, 1, provider.calls)
	require.Equal(t, 1, assessments.persistCalls)
	require.Len(t, commit.commits, 1)
	assert.Equal(t, false, commit.commits[0].Payload["assessment_reused"])
	require.NotNil(t, assessments.assessment)
	require.NotEmpty(t, assessments.assessment.AssessmentID)

	processed, err = worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, 1, provider.calls, "a persisted assessment must prevent another provider conversation")
	assert.Equal(t, 1, assessments.persistCalls)
	require.Len(t, commit.commits, 2)
	assert.Equal(t, true, commit.commits[1].Payload["assessment_reused"])
	assert.Equal(t, ledger.run.PlacementRunID, commit.commits[1].PlacementRunID)
	assert.Empty(t, catalog.entityMatches.Matches)
}

func TestSemanticAssessmentWorkerRecordsFirstDispositionOnlyForRemember(t *testing.T) {
	for _, test := range []struct {
		name       string
		isRemember bool
	}{
		{name: "remember", isRemember: true},
		{name: "internal ingest", isRemember: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			metrics := observability.NewPrometheusMetrics()
			ledger, _, _, _, _, worker := semanticAssessmentWorkerFixtureWithMetrics(t, metrics)
			implementation, ok := worker.(*semanticAssessmentPlacementWorkerService)
			require.True(t, ok)
			implementation.recordFirstDisposition(context.Background(), *ledger.run, &repository.PlacementFirstDisposition{
				Status:      "completed",
				CreatedAt:   time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
				CompletedAt: time.Date(2026, 7, 31, 12, 0, 1, 0, time.UTC),
				IsRemember:  test.isRemember,
			})

			recorder := httptest.NewRecorder()
			metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			gotMetric := strings.Contains(recorder.Body.String(), "densemem_remember_first_disposition_total{")
			assert.Equal(t, test.isRemember, gotMetric)
		})
	}
}

func TestSemanticAssessmentWorkerReusesPersistedAssessmentAfterCommitFailure(t *testing.T) {
	ledger, assessments, commit, _, provider, worker := semanticAssessmentWorkerFixture(t)
	commit.commitErr = errors.New("semantic transaction failed")

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.ErrorContains(t, err, "semantic transaction failed")
	require.Equal(t, 1, provider.calls)
	require.Equal(t, 1, assessments.persistCalls)
	require.Len(t, commit.requeues, 1)

	ledger.run.Attempts++
	commit.commitErr = nil
	processed, err = worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.NoError(t, err)
	assert.Equal(t, 1, provider.calls, "a semantic transaction retry must reuse the persisted assessment")
	assert.Equal(t, 1, assessments.persistCalls)
	require.Len(t, commit.commits, 2)
	assert.Equal(t, true, commit.commits[1].Payload["assessment_reused"])
}

func TestSemanticAssessmentWorkerTerminalizesInvalidProviderResponse(t *testing.T) {
	_, assessments, commit, _, provider, worker := semanticAssessmentWorkerFixture(t)
	provider.response = func(req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
		return verifier.SemanticAssessmentResponse{
			RequestID:           req.RequestID,
			SecuritySignals:     []verifier.SemanticSecuritySignal{},
			RelationshipResults: []verifier.SemanticAssessmentRelationshipResult{},
		}, nil
	}

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid complete response")
	require.Equal(t, 1, provider.calls)
	require.Equal(t, 0, assessments.persistCalls)
	assert.Empty(t, commit.requeues)
	require.Len(t, commit.completions, 1)
	assert.Equal(t, "assessment", commit.completions[0].Payload["failure_stage"])
	assert.Equal(t, "malformed_response", commit.completions[0].Payload["failure_class"])
	assert.Equal(t, 1, commit.completions[0].Payload["assessor_turns"])
	assert.True(t, assessments.reserved, "malformed exhaustion must not release a new conversation")
}

func TestSemanticAssessmentWorkerReleasesProviderFailureForLaterClaim(t *testing.T) {
	_, assessments, commit, _, provider, worker := semanticAssessmentWorkerFixture(t)
	provider.response = func(verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
		return verifier.SemanticAssessmentResponse{}, &verifier.ProviderError{
			Provider:     "stub",
			Message:      "provider returned HTTP 503",
			FailureClass: verifier.ProviderFailureClassHTTPServer,
			StatusCode:   503,
		}
	}

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.Error(t, err)
	assert.Equal(t, 1, provider.calls)
	assert.Zero(t, assessments.persistCalls)
	require.Len(t, commit.requeues, 1)
	assert.True(t, commit.requeues[0].ReleaseAssessorAttempt)
	assert.Equal(t, verifier.ProviderFailureClassHTTPServer, commit.requeues[0].Payload["failure_class"])
	assert.Equal(t, 503, commit.requeues[0].Payload["provider_status"])
}

func TestSemanticAssessmentWorkerUsesRateLimitRetryAfter(t *testing.T) {
	_, _, commit, _, provider, worker := semanticAssessmentWorkerFixture(t)
	provider.response = func(verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
		return verifier.SemanticAssessmentResponse{}, &verifier.RateLimitError{
			Provider:   "stub",
			Message:    "provider returned HTTP 429",
			RetryAfter: 120,
		}
	}

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.Error(t, err)
	require.Len(t, commit.requeues, 1)
	assert.Equal(t, 2*time.Minute, commit.requeues[0].RetryAfter)
	assert.Equal(t, verifier.ProviderFailureClassRateLimited, commit.requeues[0].Payload["failure_class"])
	assert.Equal(t, 429, commit.requeues[0].Payload["provider_status"])
	assert.NotContains(t, commit.requeues[0].Payload, "provider_message")
}

func TestSemanticAssessmentWorkerTerminalizesProviderFailureAtMaxAttempts(t *testing.T) {
	ledger, _, commit, _, provider, worker := semanticAssessmentWorkerFixture(t)
	ledger.run.MaxAttempts = ledger.run.Attempts
	provider.response = func(verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
		return verifier.SemanticAssessmentResponse{}, &verifier.ProviderError{
			Provider:     "stub",
			Message:      "provider returned HTTP 401",
			FailureClass: verifier.ProviderFailureClassHTTPClient,
			StatusCode:   401,
		}
	}

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.Error(t, err)
	assert.Empty(t, commit.requeues)
	require.Len(t, commit.completions, 1)
	assert.Equal(t, verifier.ProviderFailureClassHTTPClient, commit.completions[0].Payload["failure_class"])
	assert.Equal(t, 401, commit.completions[0].Payload["provider_status"])
}

func TestSemanticAssessmentMalformedFailureUsesSafeFallbacks(t *testing.T) {
	failureClass, turns := semanticAssessmentMalformedFailure(errors.New("untyped"))
	assert.Equal(t, "malformed_response", failureClass)
	assert.Zero(t, turns)

	failureClass, turns = semanticAssessmentMalformedFailure(&verifier.MalformedResponseError{
		Provider: "stub",
		Attempts: 2,
	})
	assert.Equal(t, "malformed_response", failureClass)
	assert.Equal(t, 2, turns)
}

func TestSemanticAssessmentWorkerRetriesCatalogPreflightFailure(t *testing.T) {
	_, assessments, commit, catalog, provider, worker := semanticAssessmentWorkerFixture(t)
	catalog.entityMatchesErr = errors.New("catalog unavailable")

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.ErrorContains(t, err, "catalog unavailable")
	assert.Zero(t, provider.calls)
	assert.Zero(t, assessments.persistCalls)
	require.Len(t, commit.requeues, 1)
	assert.Empty(t, commit.completions)
	assert.False(t, commit.requeues[0].ReleaseAssessorAttempt)
}

func TestSemanticAssessmentWorkerDoesNotCallProviderAfterDurableReservationWithoutAssessment(t *testing.T) {
	_, assessments, commit, _, provider, worker := semanticAssessmentWorkerFixture(t)
	assessments.reserved = true

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.True(t, processed)
	require.NoError(t, err)
	assert.Zero(t, provider.calls, "a consumed claim must not start a second provider conversation")
	require.Len(t, commit.completions, 1)
	assert.Equal(t, "assessment_attempt_consumed", commit.completions[0].Payload["failure_stage"])
}

func TestSemanticAssessmentWorkerRecordsBoundedAssessorMetrics(t *testing.T) {
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	_, assessments, _, catalog, provider, worker := semanticAssessmentWorkerFixtureWithMetrics(t, metrics)
	catalog.entityMatches.Truncated = true

	processed, err := worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	require.Equal(t, 1, provider.calls)
	require.Equal(t, 1, assessments.persistCalls)
	calls := metrics.AssessorCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "ok", calls[0].Outcome)
	assert.Greater(t, calls[0].InputTokens, 0)
	assert.Equal(t, 1, metrics.AssessorCandidateTruncationCount())
	assert.Equal(t, 1, metrics.AssessorPersistenceCount("persisted"))

	processed, err = worker.ProcessNextSemanticAssessmentPlacement(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, 1, provider.calls)
	assert.Equal(t, 1, metrics.AssessorPersistenceCount("reused"))
	assert.Equal(t, 1, metrics.AssessorDuplicatePreventionCount("post_persist"))
}

func TestSemanticAssessmentCommitInputAppliesInclusiveConfidenceGate(t *testing.T) {
	run, item, fragment, request, response, assessment := semanticAssessmentConfidenceFixture(t)

	response.RelationshipResults[0].Confidence = 0.7
	commit, err := semanticAssessmentCommitInput(run, item, fragment, request, response, assessment, repository.AutoWriteConfidencePolicy{
		Threshold:     0.7,
		ConfigVersion: 4,
		Version:       repository.AssessmentPolicyVersion,
	}, repository.PlacementAssessmentReviewOverrides{}, nil)
	require.NoError(t, err)
	require.Len(t, commit.RelationshipObservations, 1)
	assert.Equal(t, "meets_write_threshold", commit.RelationshipObservations[0].GateResult)
	assert.False(t, commit.RelationshipObservations[0].SuppressSupport)
	assert.Empty(t, commit.RelationshipObservations[0].SemanticReviewKind)
	assert.Empty(t, commit.RelationshipObservations[0].ReviewQuestion)
	assert.Empty(t, commit.RelationshipObservations[0].ReviewOptions)
	assert.Empty(t, commit.RelationshipObservations[0].ReviewGuidance)

	response.RelationshipResults[0].Confidence = 0.699
	commit, err = semanticAssessmentCommitInput(run, item, fragment, request, response, assessment, repository.AutoWriteConfidencePolicy{
		Threshold:     0.7,
		ConfigVersion: 4,
		Version:       repository.AssessmentPolicyVersion,
	}, repository.PlacementAssessmentReviewOverrides{}, nil)
	require.NoError(t, err)
	require.Len(t, commit.RelationshipObservations, 1)
	assert.Equal(t, "below_write_threshold", commit.RelationshipObservations[0].GateResult)
	assert.True(t, commit.RelationshipObservations[0].SuppressSupport)
	assert.Equal(t, "support_confidence", commit.RelationshipObservations[0].SemanticReviewKind)
	assert.NotEmpty(t, commit.RelationshipObservations[0].ReviewQuestion)
	assert.NotEmpty(t, commit.RelationshipObservations[0].ReviewOptions)
	assert.NotEmpty(t, commit.RelationshipObservations[0].ReviewGuidance)
}

func TestSemanticAssessmentRequestKeepsTrustedRelationshipContextServerSide(t *testing.T) {
	_, _, _, _, _, worker := semanticAssessmentWorkerFixture(t)
	service := worker.(*semanticAssessmentPlacementWorkerService)
	ledger := service.ledger.(*semanticAssessmentWorkerLedgerStub)
	fragment := ledger.placement.Evidence[0]
	targetID := uuid.NewString()
	conflictID := uuid.NewString()
	proposalID := "proposal:durable"
	relationship := map[string]any{
		"proposal_id": proposalID,
		"evidence": []any{map[string]any{
			"evidence_index": fragment.EvidenceIndex,
			"start":          0,
			"end":            len([]rune(fragment.Content)),
		}},
		"correction_target": map[string]any{
			"relationship_id":  targetID,
			"expected_version": 2,
		},
		"conflict_context": map[string]any{
			"conflict_id":      conflictID,
			"expected_version": 3,
		},
	}
	proposal := map[string]any{"relationship_hints": []any{relationship}}

	request, err := service.buildRequest(context.Background(), *ledger.run, ledger.placement.Items[0], fragment, proposal)
	require.NoError(t, err)
	require.Len(t, request.RequiredRelationshipRefs, 1)
	assert.Equal(t, proposalID, request.RequiredRelationshipRefs[0].ProposalID)
	assert.Equal(t, []verifier.SemanticAssessmentEvidenceSpan{{
		EvidenceID: "evidence:0",
		Start:      0,
		End:        len([]rune(fragment.Content)),
	}}, request.RequiredRelationshipRefs[0].Evidence)

	providerRelationship := placementReviewObjectArray(request.ClientProposal, "relationship_hints")
	require.Len(t, providerRelationship, 1)
	assert.Equal(t, proposalID, providerRelationship[0]["proposal_id"])
	assert.NotContains(t, providerRelationship[0], "correction_target")
	assert.NotContains(t, providerRelationship[0], "conflict_context")
	payload, err := json.Marshal(request)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), targetID)
	assert.NotContains(t, string(payload), conflictID)
	assert.Contains(t, relationship, "correction_target", "the immutable stored proposal must remain intact")
	assert.Contains(t, relationship, "conflict_context", "the immutable stored proposal must remain intact")
}

func TestSemanticAssessmentCommitInputReattachesTrustedRelationshipContext(t *testing.T) {
	run, item, fragment, request, response, assessment := semanticAssessmentConfidenceFixture(t)
	targetID := uuid.NewString()
	conflictID := uuid.NewString()
	proposalID := "proposal:works-on"
	response.RelationshipResults[0].Ref = proposalID
	request.RequiredRelationshipRefs = []verifier.SemanticAssessmentRequiredRelationshipRef{{
		ProposalID: proposalID,
		Evidence:   append([]verifier.SemanticAssessmentEvidenceSpan(nil), response.RelationshipResults[0].Evidence...),
	}}
	proposal := map[string]any{"relationship_hints": []any{map[string]any{
		"proposal_id": proposalID,
		"evidence": []any{map[string]any{
			"evidence_index": fragment.EvidenceIndex,
			"start":          0,
			"end":            len([]rune(fragment.Content)),
		}},
		"correction_target": map[string]any{
			"relationship_id":  targetID,
			"expected_version": 2,
		},
		"conflict_context": map[string]any{
			"conflict_id":      conflictID,
			"expected_version": 3,
		},
	}}}
	policy := repository.AutoWriteConfidencePolicy{
		Threshold:     0.7,
		ConfigVersion: 4,
		Version:       repository.AssessmentPolicyVersion,
	}

	response.RelationshipResults[0].PredicateStatus = "needs_review"
	response.RelationshipResults[0].PredicateKey = nil
	response.RelationshipResults[0].PredicateVersion = nil
	reviewCommit, err := semanticAssessmentCommitInput(
		run,
		item,
		fragment,
		request,
		response,
		assessment,
		policy,
		repository.PlacementAssessmentReviewOverrides{},
		proposal,
	)
	require.NoError(t, err)
	assert.Empty(t, reviewCommit.RelationshipObservations)
	require.Len(t, reviewCommit.RelationshipReviews, 1)
	review := reviewCommit.RelationshipReviews[0]
	require.NotNil(t, review.CorrectionTarget)
	assert.Equal(t, targetID, review.CorrectionTarget.RelationshipID)
	assert.Equal(t, 2, review.CorrectionTarget.ExpectedVersion)
	require.NotNil(t, review.ConflictContext)
	assert.Equal(t, conflictID, review.ConflictContext.ConflictID)
	assert.Equal(t, 3, review.ConflictContext.ExpectedVersion)

	response = applySemanticAssessmentReviewOverrides(response, repository.PlacementAssessmentReviewOverrides{
		PredicateSelections: map[string]repository.PlacementAssessmentPredicateOverride{
			proposalID: {PredicateKey: "works_on", PredicateVersion: 1},
		},
	})
	commit, err := semanticAssessmentCommitInput(run, item, fragment, request, response, assessment, policy, repository.PlacementAssessmentReviewOverrides{}, proposal)
	require.NoError(t, err)
	require.Len(t, commit.RelationshipObservations, 1)
	observation := commit.RelationshipObservations[0]
	require.NotNil(t, observation.CorrectionTarget)
	assert.Equal(t, targetID, observation.CorrectionTarget.RelationshipID)
	assert.Equal(t, 2, observation.CorrectionTarget.ExpectedVersion)
	require.NotNil(t, observation.ConflictContext)
	assert.Equal(t, conflictID, observation.ConflictContext.ConflictID)
	assert.Equal(t, 3, observation.ConflictContext.ExpectedVersion)

	response.RelationshipResults[0].Ref = "unrelated"
	_, err = semanticAssessmentCommitInput(run, item, fragment, request, response, assessment, policy, repository.PlacementAssessmentReviewOverrides{}, proposal)
	require.ErrorContains(t, err, "trusted relationship correspondence")
}

func TestSemanticAssessmentCommitInputRoutesUnmatchedPredicateToReview(t *testing.T) {
	run, item, fragment, request, response, assessment := semanticAssessmentConfidenceFixture(t)
	response.RelationshipResults[0].PredicateStatus = "needs_review"
	response.RelationshipResults[0].PredicateKey = nil
	response.RelationshipResults[0].PredicateVersion = nil

	commit, err := semanticAssessmentCommitInput(run, item, fragment, request, response, assessment, repository.AutoWriteConfidencePolicy{
		Threshold:     0.7,
		ConfigVersion: 4,
		Version:       repository.AssessmentPolicyVersion,
	}, repository.PlacementAssessmentReviewOverrides{}, nil)
	require.NoError(t, err)
	assert.Empty(t, commit.RelationshipObservations)
	require.Len(t, commit.RelationshipReviews, 1)
	assert.Equal(t, "predicate", commit.RelationshipReviews[0].SemanticReviewKind)
}

func TestSemanticAssessmentCommitInputReopensIncompatiblePredicateSelection(t *testing.T) {
	run, item, fragment, request, response, assessment := semanticAssessmentConfidenceFixture(t)
	request.PredicateOptions[0].AllowedSubjectKinds = []string{"project"}

	commit, err := semanticAssessmentCommitInput(run, item, fragment, request, response, assessment, repository.AutoWriteConfidencePolicy{
		Threshold:     0.7,
		ConfigVersion: 4,
		Version:       repository.AssessmentPolicyVersion,
	}, repository.PlacementAssessmentReviewOverrides{}, nil)
	require.NoError(t, err)
	assert.Empty(t, commit.RelationshipObservations)
	require.Len(t, commit.RelationshipReviews, 1)
	assert.Equal(t, "predicate", commit.RelationshipReviews[0].SemanticReviewKind)
	assert.Equal(t, []map[string]any{{"action": "submit_new_evidence"}}, commit.RelationshipReviews[0].ReviewOptions)
}

func TestSemanticAssessmentCommitInputFiltersPredicateReviewOptionsByEndpoints(t *testing.T) {
	run, item, fragment, request, response, assessment := semanticAssessmentConfidenceFixture(t)
	request.PredicateOptions = append(request.PredicateOptions, verifier.SemanticAssessmentPredicateOption{
		PredicateKey: "manages", Version: 1, Aliases: []string{"manages"},
		AllowedSubjectKinds: []string{"project"}, AllowedObjectKinds: []string{"product"},
		RelationshipKind: "state", CurrentCardinality: "many",
	})
	response.RelationshipResults[0].PredicateStatus = "needs_review"
	response.RelationshipResults[0].PredicateKey = nil
	response.RelationshipResults[0].PredicateVersion = nil

	commit, err := semanticAssessmentCommitInput(run, item, fragment, request, response, assessment, repository.AutoWriteConfidencePolicy{
		Threshold:     0.7,
		ConfigVersion: 4,
		Version:       repository.AssessmentPolicyVersion,
	}, repository.PlacementAssessmentReviewOverrides{}, nil)
	require.NoError(t, err)
	require.Len(t, commit.RelationshipReviews, 1)
	require.Len(t, commit.RelationshipReviews[0].ReviewOptions, 1)
	assert.Equal(t, "works_on", commit.RelationshipReviews[0].ReviewOptions[0]["predicate_key"])
}

func TestApplySemanticAssessmentReviewOverridesUsesPersistedSelections(t *testing.T) {
	selectedEntityID := uuid.NewString()
	response := verifier.SemanticAssessmentResponse{
		EntityResults: []verifier.SemanticAssessmentEntityResult{{
			Ref: "subject", Action: "needs_review",
		}},
		RelationshipResults: []verifier.SemanticAssessmentRelationshipResult{{
			Ref: "works-on", PredicateStatus: "unknown",
		}},
	}

	updated := applySemanticAssessmentReviewOverrides(response, repository.PlacementAssessmentReviewOverrides{
		EntitySelections: map[string]string{
			"subject": selectedEntityID,
		},
		PredicateSelections: map[string]repository.PlacementAssessmentPredicateOverride{
			"works-on": {
				RelationshipRef:  "works-on",
				PredicateKey:     "works_on",
				PredicateVersion: 1,
			},
		},
	})

	require.Len(t, updated.EntityResults, 1)
	assert.Equal(t, "reuse", updated.EntityResults[0].Action)
	require.NotNil(t, updated.EntityResults[0].CandidateEntityID)
	assert.Equal(t, selectedEntityID, *updated.EntityResults[0].CandidateEntityID)
	require.Len(t, updated.RelationshipResults, 1)
	assert.Equal(t, "resolved", updated.RelationshipResults[0].PredicateStatus)
	require.NotNil(t, updated.RelationshipResults[0].PredicateKey)
	assert.Equal(t, "works_on", *updated.RelationshipResults[0].PredicateKey)
	require.NotNil(t, updated.RelationshipResults[0].PredicateVersion)
	assert.Equal(t, 1, *updated.RelationshipResults[0].PredicateVersion)
}

func TestExactTokenSpansExcludesSubstringAndIdentifierMatches(t *testing.T) {
	spans := exactTokenSpans("Mark met Markus. MARK meets mark_2.", "mark")
	require.Len(t, spans, 2)
	assert.Equal(t, assessmentTextSpan{start: 0, end: 4, surface: "Mark"}, spans[0])
	assert.Equal(t, assessmentTextSpan{start: 17, end: 21, surface: "MARK"}, spans[1])

	spans = exactTokenSpans("Renée met RENÉE.", "renée")
	require.Len(t, spans, 2)
	assert.Equal(t, assessmentTextSpan{start: 0, end: 5, surface: "Renée"}, spans[0])
	assert.Equal(t, assessmentTextSpan{start: 10, end: 15, surface: "RENÉE"}, spans[1])
}

func semanticAssessmentWorkerFixture(t *testing.T) (*semanticAssessmentWorkerLedgerStub, *semanticAssessmentWorkerAssessmentStub, *semanticAssessmentWorkerCommitStub, *semanticAssessmentWorkerCatalogStub, *semanticAssessmentWorkerProviderStub, SemanticAssessmentPlacementWorkerService) {
	return semanticAssessmentWorkerFixtureWithMetrics(t, nil)
}

func semanticAssessmentWorkerFixtureWithMetrics(t *testing.T, metrics observability.DiscoverabilityMetrics) (*semanticAssessmentWorkerLedgerStub, *semanticAssessmentWorkerAssessmentStub, *semanticAssessmentWorkerCommitStub, *semanticAssessmentWorkerCatalogStub, *semanticAssessmentWorkerProviderStub, SemanticAssessmentPlacementWorkerService) {
	t.Helper()
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	run := &repository.PlacementRun{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       uuid.NewString(),
		PlacementRunID: uuid.NewString(),
		Status:         "processing",
		Attempts:       1,
		MaxAttempts:    3,
	}
	placement := &repository.CreateIngestResult{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IngestID:       run.IngestID,
		PlacementRunID: run.PlacementRunID,
		Proposal:       map[string]any{},
		Evidence: []repository.EvidenceFragment{{
			FragmentID:       uuid.NewString(),
			EvidenceIndex:    0,
			Content:          "Evidence is durable.",
			SourceID:         uuid.NewString(),
			SourceRevisionID: uuid.NewString(),
		}},
		Items: []repository.PlacementItem{{
			PlacementItemID: uuid.NewString(),
			ClaimKey:        uuid.NewString(),
			EvidenceIndex:   0,
			Status:          "processing",
			Version:         1,
		}},
	}
	ledger := &semanticAssessmentWorkerLedgerStub{run: run, placement: placement}
	assessments := &semanticAssessmentWorkerAssessmentStub{policy: repository.AutoWriteConfidencePolicy{
		Threshold:     0.7,
		ConfigVersion: 1,
		Version:       repository.AssessmentPolicyVersion,
	}}
	commit := &semanticAssessmentWorkerCommitStub{}
	catalog := &semanticAssessmentWorkerCatalogStub{}
	provider := &semanticAssessmentWorkerProviderStub{}
	worker := NewSemanticAssessmentPlacementWorkerService(SemanticAssessmentPlacementWorkerDependencies{
		Ledger:                    ledger,
		Assessments:               assessments,
		Commit:                    commit,
		Catalog:                   catalog,
		Provider:                  provider,
		Limits:                    verifier.DefaultSemanticAssessmentLimits(),
		GlobalConfidenceThreshold: 0.7,
		TeamID:                    teamID,
		WorkerID:                  "assessment-worker",
		Now:                       func() time.Time { return time.Unix(0, 0).UTC() },
		Metrics:                   metrics,
	})
	return ledger, assessments, commit, catalog, provider, worker
}

func semanticAssessmentConfidenceFixture(t *testing.T) (repository.PlacementRun, repository.PlacementItem, repository.EvidenceFragment, verifier.SemanticAssessmentRequest, verifier.SemanticAssessmentResponse, *repository.PlacementAssessment) {
	t.Helper()
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	sourceRevisionID := uuid.NewString()
	content := "Mark works on Dense-Mem."
	markID := uuid.NewString()
	denseMemID := uuid.NewString()
	predicateKey := "works_on"
	predicateVersion := 1
	request := verifier.SemanticAssessmentRequest{
		RequestID:      "semantic-assessment:" + uuid.NewString(),
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []verifier.SemanticReviewEvidence{{
			EvidenceID:              "evidence:0",
			FragmentID:              uuid.NewString(),
			EvidenceIndex:           0,
			Content:                 content,
			SourceID:                uuid.NewString(),
			SourceRevisionID:        sourceRevisionID,
			CurrentSourceRevisionID: sourceRevisionID,
		}},
		EntityCandidateGroups: []verifier.SemanticAssessmentEntityCandidateGroup{
			{
				Surface: "Mark", EvidenceID: "evidence:0", Start: 0, End: 4,
				Candidates: []verifier.SemanticAssessmentEntityCandidate{{EntityID: markID, CanonicalName: "Mark", Kind: "person", IdentityContext: map[string]any{}}},
			},
			{
				Surface: "Dense-Mem", EvidenceID: "evidence:0", Start: 14, End: 23,
				Candidates: []verifier.SemanticAssessmentEntityCandidate{{EntityID: denseMemID, CanonicalName: "Dense-Mem", Kind: "product", IdentityContext: map[string]any{}}},
			},
		},
		PredicateOptions: []verifier.SemanticAssessmentPredicateOption{{
			PredicateKey: predicateKey, Version: predicateVersion, Aliases: []string{"works on"},
			AllowedSubjectKinds: []string{"person"}, AllowedObjectKinds: []string{"product"},
			RelationshipKind: "state", CurrentCardinality: "many",
		}},
	}
	prepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(request, verifier.DefaultSemanticAssessmentLimits())
	require.Empty(t, validationErrors)
	response := verifier.SemanticAssessmentResponse{
		RequestID:       prepared.RequestID,
		SecuritySignals: []verifier.SemanticSecuritySignal{},
		EntityResults: []verifier.SemanticAssessmentEntityResult{
			{Ref: "mark", Surface: "Mark", Kind: "person", EvidenceID: "evidence:0", Start: 0, End: 4, Action: "reuse", CandidateEntityID: testStringPointer(markID), Confidence: 1, Rationale: "Exact candidate."},
			{Ref: "dense-mem", Surface: "Dense-Mem", Kind: "product", EvidenceID: "evidence:0", Start: 14, End: 23, Action: "reuse", CandidateEntityID: testStringPointer(denseMemID), Confidence: 1, Rationale: "Exact candidate."},
		},
		RelationshipResults: []verifier.SemanticAssessmentRelationshipResult{{
			Ref: "works-on", SubjectRef: "mark", OriginalPredicate: "works on", PredicateStatus: "resolved",
			PredicateKey: testStringPointer(predicateKey), PredicateVersion: testIntPointer(predicateVersion), ObjectRef: testStringPointer("dense-mem"),
			Polarity: "+", Modality: "statement", Evidence: []verifier.SemanticAssessmentEvidenceSpan{{EvidenceID: "evidence:0", Start: 0, End: len([]rune(content))}},
			ScopeStatus: "absent", EvidenceVerdict: "entailed", TemporalVerdict: "absent", Confidence: 0.7, Rationale: "The evidence states the relationship.",
		}},
	}
	normalized, validationErrors := verifier.PrepareSemanticAssessmentResponse(prepared, response, verifier.DefaultSemanticAssessmentLimits())
	require.Empty(t, validationErrors)
	normalizedJSON, err := json.Marshal(normalized)
	require.NoError(t, err)
	fragment := repository.EvidenceFragment{
		FragmentID:       prepared.Evidence[0].FragmentID,
		EvidenceIndex:    0,
		Content:          content,
		SourceID:         prepared.Evidence[0].SourceID,
		SourceRevisionID: prepared.Evidence[0].SourceRevisionID,
	}
	return repository.PlacementRun{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString(), PlacementRunID: uuid.NewString(), Attempts: 1},
		repository.PlacementItem{PlacementItemID: uuid.NewString(), ClaimKey: uuid.NewString()}, fragment, prepared, normalized,
		&repository.PlacementAssessment{AssessmentID: uuid.NewString(), RequestID: prepared.RequestID, Model: "assessment-model", ResponseHash: semanticAssessmentHash(normalizedJSON)}
}

func testStringPointer(value string) *string { return &value }

func testIntPointer(value int) *int { return &value }

type semanticAssessmentWorkerLedgerStub struct {
	run               *repository.PlacementRun
	placement         *repository.CreateIngestResult
	appendSecurity    []repository.SecurityEventInput
	appendSecurityErr error
	finishCalls       int
	finishErr         error
}

func (s *semanticAssessmentWorkerLedgerStub) CreateIngest(context.Context, repository.CreateIngestInput) (*repository.CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *semanticAssessmentWorkerLedgerStub) GetPlacementRun(context.Context, repository.GetPlacementRunInput) (*repository.CreateIngestResult, error) {
	return s.placement, nil
}

func (s *semanticAssessmentWorkerLedgerStub) AdvanceSourceRevision(context.Context, repository.AdvanceSourceRevisionInput) (*repository.SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (s *semanticAssessmentWorkerLedgerStub) AppendSecurityEvent(_ context.Context, input repository.SecurityEventInput) (string, error) {
	s.appendSecurity = append(s.appendSecurity, input)
	if s.appendSecurityErr != nil {
		return "", s.appendSecurityErr
	}
	return uuid.NewString(), nil
}

func (s *semanticAssessmentWorkerLedgerStub) AppendPlacementOutcome(context.Context, repository.PlacementOutcomeInput) (string, error) {
	return "", errors.New("unexpected AppendPlacementOutcome")
}

func (s *semanticAssessmentWorkerLedgerStub) ClaimNextPlacementRun(context.Context, string, string, time.Duration) (*repository.PlacementRun, error) {
	return s.run, nil
}

func (s *semanticAssessmentWorkerLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) (*repository.PlacementFirstDisposition, error) {
	s.finishCalls++
	return nil, s.finishErr
}

type semanticAssessmentWorkerAssessmentStub struct {
	assessment   *repository.PlacementAssessment
	persistCalls int
	reserved     bool
	policy       repository.AutoWriteConfidencePolicy
}

func (s *semanticAssessmentWorkerAssessmentStub) LoadPlacementAssessment(context.Context, repository.LoadPlacementAssessmentInput) (*repository.PlacementAssessment, error) {
	if s.assessment == nil {
		return nil, repository.ErrPlacementAssessmentNotFound
	}
	return s.assessment, nil
}

func (s *semanticAssessmentWorkerAssessmentStub) ReservePlacementAssessmentProviderAttempt(context.Context, repository.ReservePlacementAssessmentProviderAttemptInput) (bool, error) {
	if s.reserved {
		return false, nil
	}
	s.reserved = true
	return true, nil
}

func (s *semanticAssessmentWorkerAssessmentStub) PersistPlacementAssessment(_ context.Context, input repository.PersistPlacementAssessmentInput) (*repository.PlacementAssessment, bool, error) {
	s.persistCalls++
	s.assessment = &repository.PlacementAssessment{
		TeamID: input.TeamID, AssessmentID: uuid.NewString(), OwnerProfileID: input.OwnerProfileID,
		PlacementItemID: input.PlacementItemID, ClaimKey: input.ClaimKey, RequestID: input.RequestID,
		AssessorContractVersion: input.AssessorContractVersion, Model: input.Model,
		Tokenizer: input.Tokenizer, InputTokens: input.InputTokens, OutputTokens: input.OutputTokens,
		CandidateContextTokens: input.CandidateContextTokens, CandidateContextTruncated: input.CandidateContextTruncated,
		NormalizedResponse: append(json.RawMessage(nil), input.NormalizedResponse...), ResponseHash: input.ResponseHash,
		ValidatedAt: input.ValidatedAt,
	}
	return s.assessment, false, nil
}

func (s *semanticAssessmentWorkerAssessmentStub) LoadAutoWriteConfidencePolicy(context.Context, repository.LoadAutoWriteConfidencePolicyInput) (repository.AutoWriteConfidencePolicy, error) {
	return s.policy, nil
}

func (s *semanticAssessmentWorkerAssessmentStub) LoadPlacementAssessmentReviewOverrides(context.Context, repository.LoadPlacementAssessmentReviewOverridesInput) (repository.PlacementAssessmentReviewOverrides, error) {
	return repository.PlacementAssessmentReviewOverrides{EntitySelections: map[string]string{}, PredicateSelections: map[string]repository.PlacementAssessmentPredicateOverride{}}, nil
}

func (s *semanticAssessmentWorkerAssessmentStub) ExpirePlacementAssessmentReviews(context.Context, repository.ExpirePlacementAssessmentReviewsInput) (int64, error) {
	return 0, nil
}

type semanticAssessmentWorkerCommitStub struct {
	commits     []repository.CommitPlacementSemanticInput
	requeues    []repository.RequeuePlacementReviewInput
	completions []repository.CompletePlacementReviewInput
	commitErr   error
}

func (s *semanticAssessmentWorkerCommitStub) CommitPlacementSemanticResult(_ context.Context, input repository.CommitPlacementSemanticInput) (*repository.CommitPlacementSemanticResult, error) {
	s.commits = append(s.commits, input)
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	return &repository.CommitPlacementSemanticResult{Status: input.Status}, nil
}

func (s *semanticAssessmentWorkerCommitStub) CompletePlacementReviewResult(_ context.Context, input repository.CompletePlacementReviewInput) (*repository.CompletePlacementReviewResult, error) {
	s.completions = append(s.completions, input)
	return &repository.CompletePlacementReviewResult{Status: input.Status}, nil
}

func (s *semanticAssessmentWorkerCommitStub) RequeuePlacementReviewResult(_ context.Context, input repository.RequeuePlacementReviewInput) (*repository.RequeuePlacementReviewResult, error) {
	s.requeues = append(s.requeues, input)
	return &repository.RequeuePlacementReviewResult{Status: "retryable"}, nil
}

type semanticAssessmentWorkerCatalogStub struct {
	entityMatches       repository.SemanticAssessmentEntityMatchResult
	entityMatchesErr    error
	knownCandidates     map[string][]repository.SemanticReviewEntityCandidate
	knownCandidateErr   error
	knownCandidateCalls int
	knownCandidateIDs   []string
	predicateOptions    []repository.SemanticReviewPredicateCandidate
	predicateOptionsErr error
}

func (s *semanticAssessmentWorkerCatalogStub) ListSemanticAssessmentEntityMatches(context.Context, repository.SemanticAssessmentEntityMatchInput) (repository.SemanticAssessmentEntityMatchResult, error) {
	return s.entityMatches, s.entityMatchesErr
}

func (s *semanticAssessmentWorkerCatalogStub) ListSemanticAssessmentKnownEntities(_ context.Context, input repository.SemanticAssessmentKnownEntityInput) ([]repository.SemanticReviewEntityCandidate, error) {
	s.knownCandidateCalls++
	s.knownCandidateIDs = append([]string(nil), input.EntityIDs...)
	if s.knownCandidateErr != nil {
		return nil, s.knownCandidateErr
	}
	candidates := []repository.SemanticReviewEntityCandidate{}
	for _, entityID := range input.EntityIDs {
		candidates = append(candidates, s.knownCandidates[entityID]...)
	}
	return candidates, nil
}

func (s *semanticAssessmentWorkerCatalogStub) ListSemanticAssessmentPredicateOptions(context.Context, repository.SemanticAssessmentPredicateOptionsInput) ([]repository.SemanticReviewPredicateCandidate, error) {
	if s.predicateOptionsErr != nil {
		return nil, s.predicateOptionsErr
	}
	return append([]repository.SemanticReviewPredicateCandidate(nil), s.predicateOptions...), nil
}

type semanticAssessmentWorkerProviderStub struct {
	calls    int
	response func(verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error)
}

func (s *semanticAssessmentWorkerProviderStub) AssessSemantic(_ context.Context, req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
	s.calls++
	if s.response != nil {
		return s.response(req)
	}
	return verifier.SemanticAssessmentResponse{
		RequestID:           req.RequestID,
		SecuritySignals:     []verifier.SemanticSecuritySignal{},
		EntityResults:       []verifier.SemanticAssessmentEntityResult{},
		RelationshipResults: []verifier.SemanticAssessmentRelationshipResult{},
	}, nil
}

func (*semanticAssessmentWorkerProviderStub) ModelName() string { return "assessment-model" }
