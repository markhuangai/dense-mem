package memoryservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func TestSubmissionAssessmentWorkerConstructorUsesSafeDefaults(t *testing.T) {
	service := NewSubmissionAssessmentPlacementWorkerService(SubmissionAssessmentPlacementWorkerDependencies{
		TeamID:   " team ",
		WorkerID: " worker ",
	})

	worker := service.(*submissionAssessmentPlacementWorkerService)
	assert.Equal(t, time.Minute, worker.lease)
	assert.NotNil(t, worker.now)
	assert.NotNil(t, worker.metrics)
	assert.Equal(t, "team", worker.teamID)
	assert.Equal(t, "worker", worker.workerID)
}

func TestSubmissionAssessmentTrustedProposalParsingIsStrict(t *testing.T) {
	require.Equal(t, verifier.DefaultSemanticAssessmentLimits().Tokenizer, assessmentTokenizer(verifier.SemanticAssessmentLimits{}))
	require.Empty(t, cloneAssessmentProposal(nil))
	require.Empty(t, cloneAssessmentProposal(map[string]any{"invalid": make(chan int)}))

	failureClass, attempts := semanticAssessmentMalformedFailure(errors.New("provider failed"))
	require.Equal(t, "malformed_response", failureClass)
	require.Zero(t, attempts)
	failureClass, attempts = semanticAssessmentMalformedFailure(&verifier.MalformedResponseError{Attempts: 3})
	require.Equal(t, "malformed_response", failureClass)
	require.Equal(t, 3, attempts)

	groups := []verifier.SemanticAssessmentEntityCandidateGroup{
		{EvidenceID: "evidence-a", Start: 1, End: 4},
		{EvidenceID: "evidence-b", Start: 5, End: 9},
	}
	indexed := assessmentGroupsBySpan(groups)
	require.Same(t, &groups[0], indexed["evidence-a:1:4"])
	require.Same(t, &groups[1], indexed["evidence-b:5:9"])

	direct := []map[string]any{{"ref": "direct"}}
	require.Equal(t, direct, placementProposalObjectArray(map[string]any{"relationships": direct}, "missing", "relationships"))
	require.Equal(t, []map[string]any{{"ref": "mixed"}}, placementProposalObjectArray(map[string]any{
		"relationships": []any{"invalid", map[string]any{"ref": "mixed"}},
	}, "relationships"))
	require.Nil(t, placementProposalObjectArray(map[string]any{"relationships": "invalid"}, "relationships"))
	require.Empty(t, proposalString(nil, "ref"))

	for _, test := range []struct {
		value any
		want  int
		ok    bool
	}{
		{value: 1, want: 1, ok: true},
		{value: int64(2), want: 2, ok: true},
		{value: float64(3), want: 3, ok: true},
		{value: float64(3.5), ok: false},
		{value: "4", ok: false},
	} {
		got, ok := proposalInt(map[string]any{"version": test.value}, "version")
		require.Equal(t, test.ok, ok)
		require.Equal(t, test.want, got)
	}
	_, ok := proposalInt(nil, "version")
	require.False(t, ok)

	instant := time.Date(2026, time.August, 23, 12, 30, 0, 0, time.FixedZone("test", 3600))
	for _, test := range []struct {
		name    string
		fields  map[string]any
		wantNil bool
		wantErr bool
	}{
		{name: "nil fields", fields: nil, wantNil: true},
		{name: "missing", fields: map[string]any{}, wantNil: true},
		{name: "explicit nil", fields: map[string]any{"valid_from": nil}, wantNil: true},
		{name: "time", fields: map[string]any{"valid_from": instant}},
		{name: "time pointer", fields: map[string]any{"valid_from": &instant}},
		{name: "nil time pointer", fields: map[string]any{"valid_from": (*time.Time)(nil)}, wantNil: true},
		{name: "timestamp", fields: map[string]any{"valid_from": "2026-08-23T12:30:00+01:00"}},
		{name: "blank", fields: map[string]any{"valid_from": " "}, wantNil: true},
		{name: "invalid timestamp", fields: map[string]any{"valid_from": "not-a-time"}, wantErr: true},
		{name: "invalid type", fields: map[string]any{"valid_from": 1}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := proposalOptionalTime(test.fields, "valid_from")
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if test.wantNil {
				require.Nil(t, got)
				return
			}
			require.Equal(t, instant.UTC(), *got)
		})
	}

	correction, ok := placementProposalCorrectionTarget(map[string]any{"correction_target": map[string]any{
		"relationship_id": " relationship-id ", "expected_version": int64(2),
	}})
	require.True(t, ok)
	require.Equal(t, "relationship-id", correction.RelationshipID)
	require.Equal(t, 2, correction.ExpectedVersion)
	_, ok = placementProposalCorrectionTarget(map[string]any{"correction_target": map[string]any{"expected_version": 2}})
	require.False(t, ok)

	conflict, ok := placementProposalConflictContext(map[string]any{"conflict_context": map[string]any{
		"conflict_id": " conflict-id ", "expected_version": float64(3),
	}})
	require.True(t, ok)
	require.Equal(t, "conflict-id", conflict.ConflictID)
	require.Equal(t, 3, conflict.ExpectedVersion)
	_, ok = placementProposalConflictContext(map[string]any{"conflict_context": "invalid"})
	require.False(t, ok)

	original := map[string]any{"relationship_hints": []map[string]any{{
		"ref": "relationship", "correction_target": map[string]any{"relationship_id": "relationship-id"},
		"conflict_context": map[string]any{"conflict_id": "conflict-id"},
	}}}
	providerProposal := assessmentClientProposalWithoutTrustedContext(original)
	providerRelationship := placementProposalObjectArray(providerProposal, "relationship_hints")[0]
	require.NotContains(t, providerRelationship, "correction_target")
	require.NotContains(t, providerRelationship, "conflict_context")
	require.Contains(t, original["relationship_hints"].([]map[string]any)[0], "correction_target")

	longRelationshipRef := "relationship-" + strings.Repeat("r", 140)
	longRef := submissionAssessmentObservationRef(longRelationshipRef, 1, 2)
	require.LessOrEqual(t, len(longRef), 128)
	require.Equal(t, longRef, submissionAssessmentObservationRef(longRelationshipRef, 1, 2))
}

func TestSubmissionAssessmentWorkerClassifiesClaimAndPreflightFailures(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*submissionAssessmentWorkerLedgerStub, *submissionAssessmentWorkerCatalogStub)
		wantErr    bool
		wantStatus string
		wantStage  string
	}{
		{
			name: "claim error",
			mutate: func(ledger *submissionAssessmentWorkerLedgerStub, _ *submissionAssessmentWorkerCatalogStub) {
				ledger.claimErr = errors.New("claim failed")
			},
			wantErr: true,
		},
		{
			name: "entity catalog provider error",
			mutate: func(_ *submissionAssessmentWorkerLedgerStub, catalog *submissionAssessmentWorkerCatalogStub) {
				catalog.entityErr = errors.New("catalog unavailable")
			},
			wantStage: "candidate_prefetch",
		},
		{
			name: "predicate catalog provider error",
			mutate: func(_ *submissionAssessmentWorkerLedgerStub, catalog *submissionAssessmentWorkerCatalogStub) {
				catalog.predicateResolutionErr = errors.New("predicate catalog unavailable")
			},
			wantStage: "candidate_prefetch",
		},
		{
			name: "predicate option provider error",
			mutate: func(_ *submissionAssessmentWorkerLedgerStub, catalog *submissionAssessmentWorkerCatalogStub) {
				catalog.predicateOptionsErr = errors.New("predicate options unavailable")
			},
			wantStage: "candidate_prefetch",
		},
		{
			name: "contract mismatch",
			mutate: func(ledger *submissionAssessmentWorkerLedgerStub, _ *submissionAssessmentWorkerCatalogStub) {
				ledger.placement.ContractVersion = "v2.5"
			},
			wantStatus: string(domain.SemanticReviewTerminalFailure),
			wantStage:  "contract_superseded",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger, assessments, catalog, _, worker := submissionAssessmentWorkerFixture(t)
			test.mutate(ledger, catalog)

			processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
			if test.wantErr {
				assert.False(t, processed)
				require.Error(t, err)
				return
			}
			assert.True(t, processed)
			require.NoError(t, err)
			if test.wantStatus != "" {
				require.Len(t, assessments.completions, 1)
				assert.Equal(t, test.wantStatus, assessments.completions[0].Status)
				assert.Equal(t, test.wantStage, assessments.completions[0].Payload["failure_stage"])
				return
			}
			require.Len(t, assessments.requeues, 1)
			assert.Equal(t, test.wantStage, assessments.requeues[0].Payload["failure_stage"])
		})
	}
}

func TestSubmissionAssessmentWorkerHandlesSessionAndPersistenceRaces(t *testing.T) {
	t.Run("provider start error is retryable", func(t *testing.T) {
		_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
		provider.startErr = errors.New("assessor unavailable")

		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		assert.True(t, processed)
		require.Len(t, assessments.requeues, 1)
		assert.Equal(t, "assessment", assessments.requeues[0].Payload["failure_stage"])
		assert.True(t, assessments.requeues[0].ReleaseAssessorAttempt)
	})

	t.Run("repair error is retryable in the same session", func(t *testing.T) {
		_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
		provider.responseForTurn = func(req verifier.SemanticAssessmentRequest, _ int) (verifier.SemanticAssessmentResponse, error) {
			response := submissionAssessmentValidResponse(req, false)
			response.EntityResults[0].GroundingRef = nil
			return response, nil
		}
		provider.repairErr = errors.New("repair unavailable")

		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		assert.True(t, processed)
		assert.Equal(t, 2, provider.calls)
		require.Len(t, assessments.requeues, 1)
		assert.Equal(t, "assessment", assessments.requeues[0].Payload["failure_stage"])
	})

	t.Run("reservation loser reuses assessment loaded after reservation", func(t *testing.T) {
		_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		require.True(t, processed)
		stored := assessments.assessment
		require.NotNil(t, stored)

		assessments.loadNotFoundCall = assessments.loadCalls + 1
		assessments.loadAfterReservation = stored
		processed, err = worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		assert.True(t, processed)
		assert.Equal(t, 1, provider.calls)
		assert.Len(t, assessments.commits, 2)
	})

	t.Run("persist race decodes existing assessment", func(t *testing.T) {
		_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
		assessments.persistExisting = true

		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		assert.True(t, processed)
		assert.Equal(t, 1, provider.calls)
		require.Len(t, assessments.commits, 1)
		assert.True(t, assessments.commits[0].Payload["assessment_reused"].(bool))
	})

	t.Run("reservation race with invalid stored assessment regenerates", func(t *testing.T) {
		ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		require.True(t, processed)
		stored := *assessments.assessment
		stored.NormalizedResponse = []byte("not-json")
		assessments.loadNotFoundCall = assessments.loadCalls + 1
		assessments.loadAfterReservation = &stored
		assessments.reserved = true

		processed, err = worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		assert.True(t, processed)
		assert.Equal(t, 2, provider.calls)
		require.Len(t, assessments.revisionInputs, 1)
		assert.Equal(t, 2, assessments.revisionInputs[0].ProviderTurns)
		assert.Empty(t, assessments.requeues)
		assert.Len(t, assessments.commits, 2)
		_ = ledger
	})

	t.Run("persist race with invalid stored assessment regenerates", func(t *testing.T) {
		_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
		assessments.persistExisting = true
		assessments.persistNormalizedResponse = []byte("not-json")

		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		assert.True(t, processed)
		assert.Equal(t, 2, provider.calls)
		require.Len(t, assessments.revisionInputs, 1)
		assert.Equal(t, 2, assessments.revisionInputs[0].ProviderTurns)
		assert.Empty(t, assessments.requeues)
		assert.Len(t, assessments.commits, 1)
	})

	t.Run("candidate drift regenerates an invalid stored assessment", func(t *testing.T) {
		_, assessments, catalog, provider, worker := submissionAssessmentWorkerFixture(t)
		provider.response = func(req verifier.SemanticAssessmentRequest) (verifier.SemanticAssessmentResponse, error) {
			response := submissionAssessmentValidResponse(req, false)
			for index := range response.EntityResults {
				groundingRef := response.EntityResults[index].GroundingRef
				if groundingRef == nil {
					continue
				}
				for _, group := range req.EntityCandidateGroups {
					if group.GroundingRef != *groundingRef || len(group.Candidates) != 1 {
						continue
					}
					candidateID := group.Candidates[0].EntityID
					response.EntityResults[index].Action = string(domain.EntityResolutionReuse)
					response.EntityResults[index].CandidateEntityID = &candidateID
				}
			}
			return response, nil
		}

		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		require.True(t, processed)
		require.NotEmpty(t, catalog.entityInputs)
		firstTarget := catalog.entityInputs[0].Entities[0]
		catalog.entityCandidates = map[string][]repository.SemanticReviewEntityCandidate{
			firstTarget.Ref: {{
				EntityID: uuid.NewString(), EntityKind: firstTarget.EntityKind, CanonicalName: firstTarget.Surface,
				ActiveNames: []string{firstTarget.Surface}, IdentityContext: map[string]any{}, Status: "active",
			}},
		}

		processed, err = worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		require.True(t, processed)
		assert.Equal(t, 2, provider.calls)
		require.Len(t, assessments.revisionInputs, 1)
		assert.Equal(t, 2, assessments.revisionInputs[0].ProviderTurns)
		assert.Empty(t, assessments.requeues)
		require.Len(t, assessments.commits, 2)
		assert.Equal(t, false, assessments.commits[1].Payload["assessment_reused"])
	})

	t.Run("invalid stored assessment respects the total turn bound", func(t *testing.T) {
		_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
		processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		require.True(t, processed)
		require.NotNil(t, assessments.assessment)
		assessments.assessment.ProviderTurns = SemanticPlacementMaxAssessorTurns
		assessments.assessment.NormalizedResponse = []byte("not-json")

		processed, err = worker.ProcessNextSubmissionAssessmentPlacement(context.Background())
		require.NoError(t, err)
		require.True(t, processed)
		assert.Equal(t, 1, provider.calls)
		assert.Empty(t, assessments.revisionInputs)
		assert.Empty(t, assessments.requeues)
		require.Len(t, assessments.completions, 1)
		assert.Equal(t, "malformed_exhausted", assessments.completions[0].Payload["failure_class"])
		assert.Equal(t, string(SubmissionErrorProviderResponseInvalid), assessments.completions[0].Payload["failure_code"])
	})
}

func TestSubmissionAssessmentWorkerHandlesTerminalAndSecurityPersistenceFailures(t *testing.T) {
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	scope := submissionAssessmentRunScope(*ledger.run, "submission-assessment-worker")

	assessments.completeNil = true
	err := service.completeTerminalWithFailure(context.Background(), scope, "assessment", "provider_error", 503, 2)
	require.Error(t, err)
	assessments.completeNil = false
	assessments.completeErr = errors.New("terminal write failed")
	err = service.completeTerminalWithFailure(context.Background(), scope, "assessment", "provider_error", 503, 2)
	require.Error(t, err)

	assessments.completeErr = nil
	err = service.completeTerminal(context.Background(), scope, string(domain.SemanticReviewSuperseded), "superseded", "superseded")
	require.NoError(t, err)

	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)
	assessments.completeErr = nil
	err = service.completeDeterministicSecurityQuarantine(context.Background(), scope, plan, SubmissionSecurityBatchScan{
		Signals: []SubmissionSecurityBatchSignal{{EvidenceIndex: 99, Source: submissionSecuritySourceEvidence}},
	}, "deterministic_security_scan")
	require.Error(t, err)

	assessments.completeNil = true
	err = service.completeDeterministicSecurityQuarantine(context.Background(), scope, plan, SubmissionSecurityBatchScan{}, "deterministic_security_scan")
	require.Error(t, err)
	assessments.completeNil = false
	assessments.completeErr = errors.New("quarantine write failed")
	err = service.completeDeterministicSecurityQuarantine(context.Background(), scope, plan, SubmissionSecurityBatchScan{}, "deterministic_security_scan")
	require.Error(t, err)
}

func TestSubmissionAssessmentWorkerHandlesProviderSecurityFailures(t *testing.T) {
	ledger, assessments, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	scope := submissionAssessmentRunScope(*ledger.run, "submission-assessment-worker")
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)

	err = service.completeProviderSecurityQuarantine(context.Background(), scope, plan, verifier.SemanticAssessmentResponse{
		SecuritySignals: []verifier.SemanticAssessmentSecuritySignal{{EvidenceID: "missing", Start: 0, End: 1}},
	}, "security_signal")
	require.Error(t, err)
	err = service.completeProviderSecurityQuarantine(context.Background(), scope, plan, verifier.SemanticAssessmentResponse{
		SecuritySignals: []verifier.SemanticAssessmentSecuritySignal{{EvidenceID: "evidence:0", Start: -1, End: 1}},
	}, "security_signal")
	require.Error(t, err)
	err = service.completeProviderSecurityQuarantine(context.Background(), scope, plan, verifier.SemanticAssessmentResponse{}, "security_signal")
	require.Error(t, err)

	valid := verifier.SemanticAssessmentResponse{SecuritySignals: []verifier.SemanticAssessmentSecuritySignal{{EvidenceID: "evidence:0", Kind: "instruction_override", Start: 0, End: 5}}}
	assessments.completeNil = true
	err = service.completeProviderSecurityQuarantine(context.Background(), scope, plan, valid, "security_signal")
	require.Error(t, err)
	assessments.completeNil = false
	assessments.completeErr = errors.New("provider quarantine write failed")
	err = service.completeProviderSecurityQuarantine(context.Background(), scope, plan, valid, "security_signal")
	require.Error(t, err)
}

func TestSubmissionAssessmentWorkerHandlesSessionRefreshAndRetryPersistenceFailures(t *testing.T) {
	ledger, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)
	request, err := service.buildRequest(context.Background(), *ledger.run, plan, ledger.placement.Proposal)
	require.NoError(t, err)
	provider.responseForTurn = func(req verifier.SemanticAssessmentRequest, _ int) (verifier.SemanticAssessmentResponse, error) {
		response := submissionAssessmentValidResponse(req, false)
		response.EntityResults[0].GroundingRef = nil
		return response, nil
	}
	_, _, _, err = service.assessRememberSession(context.Background(), request, func(context.Context) (verifier.SemanticAssessmentRequest, error) {
		return verifier.SemanticAssessmentRequest{}, errors.New("candidate refresh failed")
	}, 0)
	require.Error(t, err)

	scope := submissionAssessmentRunScope(*ledger.run, "submission-assessment-worker")
	assessments.requeueNil = true
	err = service.retryProviderFailure(context.Background(), *ledger.run, scope, "assessment", true, verifier.ProviderFailureMetadata{Class: verifier.ProviderFailureClassHTTPServer})
	require.Error(t, err)
	assessments.requeueNil = false
	assessments.requeueErr = errors.New("requeue failed")
	err = service.retryProviderFailure(context.Background(), *ledger.run, scope, "assessment", true, verifier.ProviderFailureMetadata{Class: verifier.ProviderFailureClassHTTPServer})
	require.Error(t, err)

	badSpace := repository.PlacementRun{SpaceID: "not-a-uuid"}
	assert.Equal(t, context.Background(), withPlacementRunSpace(context.Background(), badSpace))
}

func TestSubmissionAssessmentWorkerHelperDefaults(t *testing.T) {
	assert.Equal(t, "response_contract", assessmentValidationStage(""))
	assert.Equal(t, []string{"other"}, semanticAssessmentValidationFieldFamiliesForService([]verifier.SemanticValidationError{{}}))
	assert.Equal(t, "fallback", relationshipObjectKind(verifier.SemanticAssessmentRelationshipSplit{}, map[string]string{}, "fallback"))
}

func TestSubmissionAssessmentCommitInputRejectsOutOfContractResults(t *testing.T) {
	ledger, _, _, _, worker := submissionAssessmentWorkerFixture(t)
	service := worker.(*submissionAssessmentPlacementWorkerService)
	plan, err := buildSubmissionAssessmentPlan(ledger.placement)
	require.NoError(t, err)
	request, err := service.buildRequest(context.Background(), *ledger.run, plan, ledger.placement.Proposal)
	require.NoError(t, err)
	base := submissionAssessmentValidResponse(request, false)
	base, validationErrors := verifier.PrepareSemanticAssessmentResponse(request, base, service.limits)
	require.Empty(t, validationErrors)
	assessment := &repository.SubmissionAssessment{AssessmentID: "assessment", Model: "model", ResponseHash: "hash", RequestID: request.RequestID}
	scope := submissionAssessmentRunScope(*ledger.run, "submission-assessment-worker")
	clone := func() verifier.SemanticAssessmentResponse {
		response := base
		response.EntityResults = append([]verifier.SemanticAssessmentEntityResult(nil), base.EntityResults...)
		response.RelationshipResults = append([]verifier.SemanticAssessmentRelationshipResult(nil), base.RelationshipResults...)
		return response
	}
	assertError := func(response verifier.SemanticAssessmentResponse) {
		_, commitErr := submissionAssessmentCommitInput(*ledger.run, scope, plan, response, assessment, false)
		require.Error(t, commitErr)
	}

	t.Run("unknown entity result", func(t *testing.T) {
		response := clone()
		response.EntityResults = append(response.EntityResults, verifier.SemanticAssessmentEntityResult{Ref: "unknown"})
		assertError(response)
	})
	t.Run("entity result outside evidence", func(t *testing.T) {
		response := clone()
		response.EntityResults[0].EvidenceID = "missing"
		assertError(response)
	})
	t.Run("unsupported entity action", func(t *testing.T) {
		response := clone()
		response.EntityResults[0].Action = "unsupported"
		assertError(response)
	})
	t.Run("conflicting duplicate grounding", func(t *testing.T) {
		response := clone()
		duplicate := response.EntityResults[0]
		duplicate.Ref = response.EntityResults[1].Ref
		duplicate.Action = "unsupported"
		response.EntityResults = append(response.EntityResults, duplicate)
		assertError(response)
	})
	t.Run("omitted entity result", func(t *testing.T) {
		response := clone()
		response.EntityResults = response.EntityResults[:len(response.EntityResults)-1]
		assertError(response)
	})
	t.Run("unknown relationship result", func(t *testing.T) {
		response := clone()
		response.RelationshipResults = append(response.RelationshipResults, verifier.SemanticAssessmentRelationshipResult{Ref: "unknown"})
		assertError(response)
	})
	t.Run("duplicate relationship result", func(t *testing.T) {
		response := clone()
		response.RelationshipResults = append(response.RelationshipResults, response.RelationshipResults[0])
		assertError(response)
	})
	t.Run("unsupported predicate status", func(t *testing.T) {
		response := clone()
		response.RelationshipResults[0].Splits[0].PredicateStatus = "invalid"
		assertError(response)
	})
	t.Run("invalid validity", func(t *testing.T) {
		response := clone()
		invalid := "not-a-time"
		response.RelationshipResults[0].Splits[0].ValidFrom = &invalid
		assertError(response)
	})
	t.Run("missing support", func(t *testing.T) {
		response := clone()
		response.RelationshipResults[0].Splits[0].SupportRanges = nil
		response.RelationshipResults[0].Splits[0].Evidence = nil
		assertError(response)
	})
	t.Run("missing object", func(t *testing.T) {
		response := clone()
		response.RelationshipResults[0].Splits[0].ObjectRef = nil
		response.RelationshipResults[0].Splits[0].ObjectValue = nil
		assertError(response)
	})
	t.Run("resolved predicate incomplete", func(t *testing.T) {
		response := clone()
		response.RelationshipResults[0].Splits[0].PredicateKey = nil
		assertError(response)
	})
}
