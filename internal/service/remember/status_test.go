package remember

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestStatusProjectionCoversStatesAndBoundedFailures(t *testing.T) {
	states := map[string]string{
		string(domain.PlacementRunQueued):      "queued",
		string(domain.PlacementRunGuarded):     "queued",
		string(domain.PlacementRunProcessing):  "processing",
		string(domain.PlacementRunCompleted):   "completed",
		string(domain.PlacementRunQuarantined): "quarantined",
		string(domain.PlacementRunFailed):      "failed",
		"unexpected":                           "failed",
	}
	for status, want := range states {
		result := ProjectSubmissionStatus(&StageResult{
			SubmissionID: "submission-1", Status: status,
			Evidence: []EvidenceFragment{{FragmentID: "evidence-1", SupersededEvidenceIDs: nil}},
			Items:    []PlacementItem{{FragmentID: "evidence-1", EvidenceIndex: 2, Result: map[string]any{"search_document_states": []any{"current"}}}},
		})
		require.Equal(t, want, result.ProcessingState)
		require.Equal(t, string(domain.SearchProjectionCurrent), result.SearchState)
		require.Equal(t, []string{}, result.Evidence[0].SupersededEvidenceIDs)
		if status == string(domain.PlacementRunFailed) || status == string(domain.PlacementRunQuarantined) || status == "unexpected" {
			require.Len(t, result.Errors, 1)
		} else {
			require.Empty(t, result.Errors)
		}
	}

	failedSearch := ProjectSubmissionStatus(&StageResult{
		SubmissionID: "submission-2", Status: string(domain.PlacementRunCompleted),
		Items: []PlacementItem{{FragmentID: "evidence-1", Result: map[string]any{"search_document_states": []any{"failed"}}}},
	})
	require.Equal(t, string(domain.SearchProjectionFailed), failedSearch.SearchState)
	require.Empty(t, failedSearch.Errors)
	require.Nil(t, failedSearch.Evidence[0].Error)
	require.Len(t, failedSearch.Degradations, 1)
	require.Equal(t, "search_indexing_delayed", failedSearch.Degradations[0].Code)
	require.Equal(t, "search", failedSearch.Degradations[0].Frontier)
	require.True(t, failedSearch.Degradations[0].Optional)

	failed := ProjectSubmissionStatus(&StageResult{
		SubmissionID: "submission-3", Status: string(domain.PlacementRunFailed),
		Items: []PlacementItem{
			{FragmentID: "e1", Status: string(domain.PlacementRunFailed), Result: map[string]any{"failure_class": "timeout"}},
			{FragmentID: "e2", Status: string(domain.PlacementRunFailed), Result: map[string]any{"failure_class": "timeout"}},
		},
	})
	require.Len(t, failed.Errors, 1)
	require.Equal(t, string(SubmissionErrorAssessorUnavailable), failed.Errors[0].Code)
	require.Equal(t, string(SubmissionNextActionContactOperator), failed.Errors[0].NextAction)
	require.Equal(t, failed.Errors[0].Code, failed.Evidence[1].Error.Code)

	rejected := ProjectSubmissionStatus(&StageResult{
		SubmissionID: "submission-4", Status: "rejected",
		Items: []PlacementItem{{FragmentID: "e1", Status: "rejected", Result: map[string]any{
			"failure_code": string(SubmissionErrorNoSupportedMemory),
		}}},
	})
	require.Len(t, rejected.Errors, 1)
	require.Equal(t, string(SubmissionErrorNoSupportedMemory), rejected.Errors[0].Code)

	stale := ProjectSubmissionStatus(&StageResult{
		SubmissionID: "submission-5", Status: "rejected",
		Items: []PlacementItem{{FragmentID: "e1", Status: "rejected", Result: map[string]any{
			"failure_code": string(SubmissionErrorStaleInput),
		}}},
	})
	require.Len(t, stale.Errors, 1)
	require.Equal(t, string(SubmissionErrorStaleInput), stale.Errors[0].Code)

	rich := ProjectSubmissionStatus(&StageResult{
		SubmissionID: "submission-6", Status: string(domain.PlacementRunCompleted), Attempts: 2, MaxAttempts: 5,
		Evidence: []EvidenceFragment{{FragmentID: "evidence-1", SupersededEvidenceIDs: []string{"evidence-old"}}},
		Items: []PlacementItem{{FragmentID: "evidence-1", EvidenceIndex: 3, Result: map[string]any{
			"search_document_ids": []string{"document-1"},
		}}},
		RelationshipResults: []SubmissionRelationshipResult{{
			RelationshipRef: "relationship-a", Disposition: "stored",
			Splits: []SubmissionRelationshipSplit{{SplitIndex: 0, RelationshipID: "relationship-id", RelationshipVersion: 4, Status: "active"}},
		}},
	})
	require.Equal(t, domain.ContractVersion, rich.ContractVersion)
	require.Equal(t, 2, *rich.Attempts)
	require.Equal(t, 5, *rich.MaxAttempts)
	require.Equal(t, []string{"evidence-old"}, rich.Evidence[0].SupersededEvidenceIDs)
	require.Equal(t, "stored", rich.RelationshipResults[0].Disposition)
	require.Equal(t, 4, rich.RelationshipResults[0].Splits[0].RelationshipVersion)

	rejectedWithoutItems := ProjectSubmissionStatus(&StageResult{Status: string(domain.PlacementRunRejected)})
	require.Equal(t, string(SubmissionErrorNoSupportedMemory), rejectedWithoutItems.Errors[0].Code)

	require.Empty(t, ProjectSubmissionStatus(nil).Evidence)
	require.Empty(t, ProjectSubmissionStatus(nil).Errors)
	require.Empty(t, ProjectSubmissionStatus(nil).Degradations)
}

func TestStatusPolicyAdaptersAndSearchHelpers(t *testing.T) {
	require.Contains(t, SubmissionErrorCodes(), string(SubmissionErrorProcessingFailed))
	require.Contains(t, SubmissionErrorCodes(), string(SubmissionErrorQuarantined))
	require.Contains(t, SubmissionErrorCodes(), string(SubmissionErrorPolicyRejected))
	require.NotContains(t, SubmissionErrorCodes(), "contract_superseded")
	require.NotEmpty(t, SubmissionNextActions())
	require.Equal(t, "custom", StatusErrorWithMessage(SubmissionErrorProcessingFailed, "custom").Message)
	require.Equal(t, string(SubmissionErrorNoSupportedMemory), StatusErrorForCode("unknown", "rejected").Code)
	require.Equal(t, string(SubmissionErrorInternalFailure), StatusErrorForCode("unknown", "failed").Code)

	for _, code := range []SubmissionErrorCode{
		SubmissionErrorSearchIndexingDelayed,
		SubmissionErrorPolicyRejected, SubmissionErrorAssessorUnavailable,
		SubmissionErrorRelationshipVersionStale,
		SubmissionErrorNoChange, SubmissionErrorAssessorInvalid,
		SubmissionErrorProcessingFailed, SubmissionErrorQuarantined,
	} {
		errorValue := StatusError(code)
		require.NotEmpty(t, errorValue.Code)
		require.NotEmpty(t, errorValue.Remediation)
	}

	for _, test := range []struct {
		stage, class string
		want         SubmissionErrorCode
	}{
		{"contract_superseded", "", SubmissionErrorInternalFailure},
		{"normalization_failed", "", SubmissionErrorInternalFailure},
		{"assessment", "malformed_exhausted", SubmissionErrorProviderResponseInvalid},
		{"normalizer_unavailable", "", SubmissionErrorInternalFailure},
		{"", "malformed_response", SubmissionErrorProviderResponseInvalid},
		{"", "timeout", SubmissionErrorProviderUnavailable},
		{"policy_validation", "", SubmissionErrorInternalFailure},
		{"", "", SubmissionErrorInternalFailure},
		{"entity_catalog", "", SubmissionErrorInputBudgetExceeded},
		{"catalog_context", "", SubmissionErrorInputBudgetExceeded},
		{"predicate_options_overflow", "", SubmissionErrorInputBudgetExceeded},
		{"assessment_input", "", SubmissionErrorInputBudgetExceeded},
	} {
		require.Equal(t, test.want, FailureCode(test.stage, test.class))
	}

	for _, state := range []string{"current", "pending", "failed", "ignored"} {
		result := placementSearchStateFromStates([]any{state})
		if state == "ignored" {
			require.Empty(t, result)
		} else {
			require.Equal(t, state, result)
		}
	}
	require.Equal(t, string(domain.SearchProjectionFailed), placementCombinedSearchState("pending", "failed"))
	require.Equal(t, string(domain.SearchProjectionPending), placementCombinedSearchState("current", "pending"))
	require.Equal(t, string(domain.SearchProjectionCurrent), placementCombinedSearchState("none", "current"))
	require.Equal(t, string(domain.SearchProjectionNotRequired), placementCombinedSearchState("", ""))
	for _, values := range []any{[]any{"x"}, []string{"x"}, []map[string]any{{"x": 1}}, "invalid"} {
		result := resultArray(map[string]any{"values": values}, "values")
		if values == "invalid" {
			require.Nil(t, result)
		} else {
			require.Len(t, result, 1)
		}
	}
}

func TestRememberConversionAndValidationBranches(t *testing.T) {
	content := []RememberEvidenceInput{
		{Content: "a", SourceType: "", Authority: "", SourceKey: "doc", SourceRevision: "rev", PreviousSourceRevision: "old", SourceGroup: "group", SupersedesEvidenceIDs: []string{"old-a"}, Metadata: map[string]any{"k": "v"}},
		{Content: "b", SourceType: "document", Authority: "authoritative", SourceKey: "doc", SourceRevision: "rev", PreviousSourceRevision: "old", SupersedesEvidenceIDs: []string{"old-b"}},
	}
	converted := repositoryEvidenceInputs(content)
	require.Len(t, converted, 2)
	require.Equal(t, "conversation", converted[0].SourceType)
	require.Equal(t, string(domain.AuthorityPrimary), converted[0].Authority)
	require.Equal(t, "authoritative", converted[1].Authority)
	require.Equal(t, converted[0].SourceRevisionContentHash, converted[1].SourceRevisionContentHash)
	require.Equal(t, "group", converted[0].Metadata["contract_source_group"])
	require.NotNil(t, converted[0].InitialEvent)
	require.Equal(t, "deterministic_scan", converted[0].InitialEvent.EventKind)
	require.Equal(t, "doc", sourceSummary(content))
	require.NotEmpty(t, sourceRevisionBatchHash([]string{"a"}))
	require.Empty(t, sourceRevisionBatchKey(RememberEvidenceInput{}))

	for _, raw := range []any{[]any{0}, []map[string]any{{"x": 1}}, []string{"0"}, "invalid"} {
		values := rememberArrayValues(raw)
		if raw == "invalid" {
			require.Nil(t, values)
		} else {
			require.Len(t, values, 1)
		}
	}
	for _, raw := range []any{int(1), int8(2), int16(3), int32(4), int64(5), uint(6), uint8(7), uint16(8), uint32(9), uint64(10), float64(11), float32(12), json.Number("13"), "14"} {
		_, ok := rememberEvidenceIndex(raw)
		require.True(t, ok)
	}
	for _, raw := range []any{float64(1.5), float32(2.5), json.Number("bad"), "bad", struct{}{}} {
		_, ok := rememberEvidenceIndex(raw)
		require.False(t, ok)
	}
	require.Empty(t, rememberSpaceID(domain.MemorySpaceAccess{}))
	require.EqualError(t, validateRememberRelationshipCoverage(2, nil), "remember: relationship evidence_indices must cover every evidence item; missing evidence indexes: [0 1]")
	require.NoError(t, validateRememberRelationshipCoverage(1, []map[string]any{{"evidence_indices": []any{"0"}}}))
	require.Equal(t, "failed", publicSubmissionProcessingState("unknown"))
	require.Equal(t, "completed", publicSubmissionProcessingState(string(domain.PlacementRunCompleted)))
}

func TestRememberSecurityEventAdaptersAndScannerWrappers(t *testing.T) {
	proposal := map[string]any{"note": "safe proposal"}
	batch, err := ScanSubmissionWithProviderProposal([]string{"safe evidence"}, proposal)
	require.NoError(t, err)
	require.Equal(t, 1, batch.EvidenceCount)
	pass := SubmissionSecurityPassEvent()
	require.Equal(t, "pass", pass.Decision)
	quarantine := SubmissionSecurityBatchQuarantineEvent(SubmissionSecurityBatchScan{
		Signals: []SubmissionSecurityBatchSignal{{EvidenceIndex: 0, Source: SecuritySourceEvidence, SubmissionSecuritySignal: SubmissionSecuritySignal{Kind: "kind", RuleID: "rule", Severity: "high", Start: 1, End: 2}}},
	})
	require.Equal(t, "quarantine", quarantine.Decision)
	require.Len(t, quarantine.Signals, 1)
	require.Equal(t, "evidence", quarantine.Signals[0].Metadata["source"])
	require.Equal(t, "rule", quarantine.Signals[0].Metadata["rule_id"])
	single := submissionSecurityQuarantineEvent(SubmissionSecurityScan{Signals: []SubmissionSecuritySignal{{Kind: "kind", RuleID: "rule", Severity: "high", Start: 1, End: 2}}})
	require.Equal(t, "quarantine", single.Decision)
	public := SubmissionSecurityQuarantineEventForSignals([]SubmissionSecuritySignal{{Kind: "kind", RuleID: "rule", Start: 1, End: 2}}, false, nil)
	require.Equal(t, "quarantine", public.Decision)
	require.NotNil(t, ItemFailureError(PlacementItem{Status: "failed", Result: map[string]any{"failure_class": "timeout"}}, "failed"))
	actor := requestctx.Actor{AllowedSpaces: []domain.MemorySpaceAccess{{Kind: domain.MemorySpaceTeamShared}, {Kind: domain.MemorySpaceCredentialPrivate}}}
	require.Equal(t, domain.MemorySpaceCredentialPrivate, rememberSpace(actor).Kind)
	require.Equal(t, "stored", rememberResultFromLedger(&StageResult{SubmissionID: "id", Status: "completed", Existing: true, CorrelationID: "stored"}, "request").CorrelationID)
	wrapped := fmt.Errorf("wrapped: %w", ErrEncodedEvidenceNotAllowed)
	require.True(t, errors.Is(wrapped, ErrEncodedEvidenceNotAllowed))
}

func TestRememberBoundaryRejectsMissingInputsAndBoundsStorageErrors(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	ctx := rememberTestContext(teamID, ownerID)
	_, err := NewService(Dependencies{}).GetSubmissionStatus(ctx, GetSubmissionStatusRequest{SubmissionID: uuid.NewString()})
	require.ErrorContains(t, err, "intake port is required")

	svc := NewService(Dependencies{Intake: &intakeStub{}})
	_, err = svc.GetSubmissionStatus(context.Background(), GetSubmissionStatusRequest{SubmissionID: uuid.NewString()})
	require.ErrorIs(t, err, ErrRememberAuthContext)
	_, err = svc.GetSubmissionStatus(ctx, GetSubmissionStatusRequest{})
	require.ErrorContains(t, err, "submission_id is required")
	_, err = svc.GetSubmissionStatus(ctx, GetSubmissionStatusRequest{SubmissionID: "not-a-uuid"})
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.NOT_FOUND, apiErr.Code)

	_, err = svc.Remember(ctx, RememberRequest{})
	require.ErrorContains(t, err, "evidence is required")
	for _, storageErr := range []error{ErrTeamInactive, errors.New("database unavailable")} {
		intake := &intakeStub{stageErr: storageErr}
		_, err = NewService(Dependencies{Intake: intake}).Remember(ctx, RememberRequest{
			IdempotencyKey: "status-boundary",
			Evidence:       []RememberEvidenceInput{{Content: "evidence"}}, RelationshipHints: coveredRelationships(1),
		})
		require.Error(t, err)
		if errors.Is(storageErr, ErrTeamInactive) {
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, httperr.NOT_FOUND, apiErr.Code)
		} else {
			require.ErrorIs(t, err, ErrRememberPersistence)
		}
	}
}
