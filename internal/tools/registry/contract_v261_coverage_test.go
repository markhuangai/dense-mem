package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestWithTerminalRememberCompositionAndInvocation(t *testing.T) {
	service := &configurableV261RememberService{rememberFn: func(_ context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
		return &rememberapp.RememberResult{Terminal: terminalRememberResultForRequest(req, string(rememberapp.TerminalProcessingCompleted))}, nil
	}}

	_, err := WithTerminalRemember(nil, service)
	require.ErrorContains(t, err, "active catalog")
	_, err = WithTerminalRemember(New(), nil)
	require.ErrorContains(t, err, "service")
	missingRemember := New()
	require.NoError(t, missingRemember.Register(Tool{Name: ToolGetSubmissionStatus}))
	_, err = WithTerminalRemember(missingRemember, service)
	require.ErrorContains(t, err, ToolRemember)

	active := newV261ActiveRegistry(t)
	selected, err := WithTerminalRemember(active, service)
	require.NoError(t, err)
	remember, ok := selected.Get(ToolRemember)
	require.True(t, ok)
	require.Equal(t, domain.ContractVersion, remember.OutputSchema["properties"].(map[string]any)["contract_version"].(map[string]any)["enum"].([]string)[0])
	output, err := remember.Invoke(contractInvokeContext("write"), "team-1", validFlatRelationshipSubmission())
	require.NoError(t, err)
	require.Equal(t, domain.ContractVersion, output["contract_version"])
	require.NotContains(t, output, "status_tool")
	require.Equal(t, "completed", output["processing_state"])
}

func TestBuildContractV261CompositionValidationAndTerminalSuccess(t *testing.T) {
	service := &configurableV261RememberService{rememberFn: func(_ context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
		return &rememberapp.RememberResult{Terminal: terminalRememberResultForRequest(req, string(rememberapp.TerminalProcessingCompleted))}, nil
	}}
	_, err := BuildContractV261(nil, service)
	require.ErrorContains(t, err, "active catalog")
	_, err = BuildContractV261(New(), nil)
	require.ErrorContains(t, err, "Remember service")

	selected, err := BuildContractV261(newV261ActiveRegistry(t), service)
	require.NoError(t, err)
	remember, ok := selected.Get(ToolRemember)
	require.True(t, ok)
	output, err := remember.Invoke(contractInvokeContext("write"), "team-1", validFlatRelationshipSubmission())
	require.NoError(t, err)
	require.Equal(t, ContractVersionV261, output["contract_version"])
	require.Equal(t, "completed", output["processing_state"])
	require.NoError(t, ValidateInput(Tool{InputSchema: remember.OutputSchema}, output))
}

func TestTerminalRememberInvokerTerminalFailuresAndFallbacks(t *testing.T) {
	input := validFlatRelationshipSubmission()

	failedService := &configurableV261RememberService{rememberFn: func(_ context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
		result := terminalRememberResultForRequest(req, string(rememberapp.TerminalProcessingRejected))
		return nil, rememberapp.TerminalResultWithError(result, rememberapp.TerminalErrorNoSupportedMemory)
	}}
	selected, err := BuildContractV261(newV261ActiveRegistry(t), failedService)
	require.NoError(t, err)
	remember, _ := selected.Get(ToolRemember)
	_, err = remember.Invoke(contractInvokeContext("write"), "team-1", input)
	structured, ok := ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, ContractVersionV261, structured.Result["contract_version"])
	require.Equal(t, "rejected", structured.Result["processing_state"])
	require.NotEmpty(t, structured.Result["errors"])

	fallbackService := &configurableV261RememberService{rememberFn: func(context.Context, rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
		return nil, errors.New("provider response must stay private")
	}}
	selected, err = BuildContractV261(newV261ActiveRegistry(t), fallbackService)
	require.NoError(t, err)
	remember, _ = selected.Get(ToolRemember)
	_, err = remember.Invoke(contractInvokeContext("write"), "team-1", input)
	structured, ok = ToolResultFromError(err)
	require.True(t, ok)
	item := structured.Result["errors"].([]any)[0].(map[string]any)
	require.Equal(t, string(rememberapp.TerminalErrorInternalFailure), item["code"])

	invalidService := &configurableV261RememberService{rememberFn: func(context.Context, rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
		return &rememberapp.RememberResult{Terminal: &rememberapp.TerminalRememberResult{}}, nil
	}}
	selected, err = BuildContractV261(newV261ActiveRegistry(t), invalidService)
	require.NoError(t, err)
	remember, _ = selected.Get(ToolRemember)
	_, err = remember.Invoke(contractInvokeContext("write"), "team-1", input)
	structured, ok = ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, string(rememberapp.TerminalErrorInternalFailure), structured.Result["errors"].([]any)[0].(map[string]any)["code"])

	legacyOnly := New()
	require.NoError(t, legacyOnly.Register(Tool{Name: ToolRemember, InputSchema: rememberInputSchema(), RequiredScopes: []string{"write"}}))
	legacy, err := WithTerminalRemember(legacyOnly, fallbackService)
	require.NoError(t, err)
	legacyRemember, _ := legacy.Get(ToolRemember)
	_, err = legacyRemember.Invoke(contractInvokeContext("write"), "team-1", input)
	require.ErrorContains(t, err, "provider response must stay private")
}

func TestTerminalRememberRequestAndSupersessionValidation(t *testing.T) {
	request, err := terminalRememberRequest(validFlatRelationshipSubmission())
	require.NoError(t, err)
	require.Len(t, request.Evidence, 1)
	require.Len(t, request.RelationshipHints, 1)

	_, err = terminalRememberRequest(map[string]any{"evidence": func() {}})
	require.Error(t, err)
	_, err = terminalRememberRequest(map[string]any{"evidence": "not-an-array"})
	require.Error(t, err)

	require.NoError(t, validateTerminalRememberSupersessions(map[string]any{"evidence": []any{
		map[string]any{"content": "one"},
	}}))
	target := uuid.NewString()
	err = validateTerminalRememberSupersessions(map[string]any{"evidence": []any{
		map[string]any{"supersedes_evidence_ids": []any{"not-a-uuid"}},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "evidence[0].supersedes_evidence_ids[0]")
	err = validateTerminalRememberSupersessions(map[string]any{"evidence": []any{
		map[string]any{"supersedes_evidence_ids": []any{strings.ToUpper(target)}},
		map[string]any{"supersedes_evidence_ids": []any{target}},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicates target")

	require.Equal(t, []string{"first", ""}, terminalRelationshipRefs(map[string]any{
		"relationships": []any{map[string]any{"ref": " first "}, map[string]any{}},
	}))
	require.Empty(t, terminalRelationshipRefs(map[string]any{}))
}

func TestTerminalRememberResultAndCorrectionMaps(t *testing.T) {
	req := rememberapp.RememberRequest{
		Evidence:          []rememberapp.RememberEvidenceInput{{Content: "evidence"}},
		RelationshipHints: []map[string]any{{"ref": "r-1"}},
	}
	result := terminalRememberResultForRequest(req, string(rememberapp.TerminalProcessingCompleted))
	output, err := terminalRememberResultMap(result, ContractVersionV261)
	require.NoError(t, err)
	require.Equal(t, ContractVersionV261, output["contract_version"])
	require.NoError(t, ValidateInput(Tool{InputSchema: terminalRememberOutputSchema(ContractVersionV261)}, output))

	receipt := map[string]any{"submission_id": "receipt-id", "processing_state": "awaiting_confirmation", "correlation_id": "receipt-correlation"}
	status := map[string]any{
		"submission_id": "status-id", "processing_state": "awaiting_confirmation", "search_state": "pending",
		"awaiting_confirmation": map[string]any{"confirmation_token": "token"}, "errors": []rememberapp.SubmissionStatusError{},
	}
	correction, err := terminalCorrectionResultMap(receipt, status, ContractVersionV261)
	require.NoError(t, err)
	require.Equal(t, "status-id", correction["submission_id"])
	require.Equal(t, "receipt-correlation", correction["correlation_id"])
	require.NotNil(t, correction["awaiting_confirmation"])
	require.NotContains(t, correction, "correction_result")

	correction, err = terminalCorrectionResultMap(map[string]any{"processing_state": "completed"}, map[string]any{"correction_result": map[string]any{}, "errors": []map[string]any{{"code": "x"}}}, ContractVersionV261)
	require.NoError(t, err)
	require.Equal(t, string(domain.SearchProjectionNotRequired), correction["search_state"])
	require.NotNil(t, correction["correction_result"])
	var typed rememberapp.SubmissionStatusError
	require.Equal(t, []any{}, normalizeArray(nil))
	require.Equal(t, []any{"value"}, normalizeArray([]any{"value"}))
	require.Len(t, normalizeArray([]map[string]any{{"value": "map"}}), 1)
	require.Len(t, normalizeArray([]rememberapp.SubmissionStatusError{typed}), 1)
	require.Empty(t, normalizeArray("unsupported"))
}

func TestValidateTerminalCorrectionOutputRequiresStateDetails(t *testing.T) {
	base := map[string]any{
		"contract_version": ContractVersionV261, "submission_id": uuid.NewString(),
		"submission_kind": "relationship_correction", "search_state": "not_required",
		"correlation_id": "corr", "errors": []any{},
	}
	awaiting := cloneMap(base)
	awaiting["processing_state"] = "awaiting_confirmation"
	err := validateTerminalCorrectionOutput(awaiting, ContractVersionV261)
	require.ErrorContains(t, err, "awaiting_confirmation result")
	completed := cloneMap(base)
	completed["processing_state"] = "completed"
	err = validateTerminalCorrectionOutput(completed, ContractVersionV261)
	require.ErrorContains(t, err, "completed correction result")
	rejected := cloneMap(base)
	rejected["processing_state"] = "rejected"
	err = validateTerminalCorrectionOutput(rejected, ContractVersionV261)
	require.ErrorContains(t, err, "rejected correction result")
	require.NoError(t, validateTerminalCorrectionOutput(map[string]any{
		"contract_version": ContractVersionV261, "submission_id": uuid.NewString(),
		"submission_kind": "relationship_correction", "processing_state": "failed", "search_state": "not_required",
		"correlation_id": "corr", "errors": []any{map[string]any{
			"code": string(rememberapp.TerminalErrorDatabaseFailure), "message": rememberapp.TerminalStatusError(rememberapp.TerminalErrorDatabaseFailure).Message,
			"retryable": true, "next_action": string(rememberapp.TerminalNextActionRetrySameRequest),
			"remediation": rememberapp.TerminalStatusError(rememberapp.TerminalErrorDatabaseFailure).Remediation,
		}},
	}, ContractVersionV261))
}

func TestTerminalRememberFailureBaseAndErrorMappings(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "failure-correlation")
	base, code := terminalRememberFailureBase(ctx, errors.New("internal"), 1, []string{"rel-1"})
	require.Equal(t, rememberapp.TerminalErrorInternalFailure, code)
	require.Equal(t, string(rememberapp.TerminalProcessingFailed), base.ProcessingState)
	require.Len(t, base.Evidence, 1)
	require.Len(t, base.RelationshipResults, 1)
	require.NoError(t, rememberapp.ValidateTerminalRememberResult(base, 1, []string{"rel-1"}))

	status := &rememberapp.SubmissionStatusResult{
		SubmissionID: "status-submission", CorrelationID: "status-correlation",
		ProcessingState: string(rememberapp.TerminalProcessingRejected),
		Errors:          []rememberapp.SubmissionStatusError{{Code: string(rememberapp.SubmissionErrorStaleInput)}},
	}
	base, code = terminalRememberFailureBase(ctx, &rememberapp.RememberProcessError{Status: status, Err: errors.New("wrapped")}, 0, nil)
	require.Equal(t, rememberapp.TerminalErrorStaleInput, code)
	require.Equal(t, "status-submission", base.SubmissionID)
	require.Equal(t, "status-correlation", base.CorrelationID)

	partial := &rememberapp.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionKind: "remember", ProcessingState: string(rememberapp.TerminalProcessingFailed),
		SearchState: string(rememberapp.TerminalSearchNotRequired), CorrelationID: "", SubmissionID: "", Kind: rememberapp.ResultKindTerminal,
		Evidence:            []rememberapp.TerminalEvidenceResult{{EvidenceIndex: 0, Disposition: "not_stored", SupersededEvidenceIDs: []string{}, SearchState: string(rememberapp.TerminalSearchNotRequired), Reason: "internal_failure"}},
		RelationshipResults: []rememberapp.SubmissionRelationshipResult{}, Errors: []rememberapp.SubmissionStatusError{rememberapp.TerminalStatusError(rememberapp.TerminalErrorInternalFailure)},
	}
	base, _ = terminalRememberFailureBase(context.Background(), &rememberapp.RememberProcessError{Result: partial, Err: errors.New("wrapped")}, 1, nil)
	require.NotEmpty(t, base.SubmissionID)
	require.NotEmpty(t, base.CorrelationID)
}

func TestTerminalErrorCodeMappings(t *testing.T) {
	for _, test := range []struct {
		raw   string
		state string
		want  rememberapp.TerminalErrorCode
	}{
		{string(rememberapp.TerminalErrorProviderUnavailable), "", rememberapp.TerminalErrorProviderUnavailable},
		{string(rememberapp.SubmissionErrorNoSupportedMemory), "", rememberapp.TerminalErrorNoSupportedMemory},
		{string(rememberapp.SubmissionErrorStaleInput), "", rememberapp.TerminalErrorStaleInput},
		{string(rememberapp.SubmissionErrorProviderUnavailable), "", rememberapp.TerminalErrorProviderUnavailable},
		{string(rememberapp.SubmissionErrorProviderResponseInvalid), "", rememberapp.TerminalErrorProviderResponseInvalid},
		{string(rememberapp.SubmissionErrorInputBudgetExceeded), "", rememberapp.TerminalErrorInputBudgetExceeded},
		{string(rememberapp.SubmissionErrorConfigurationInvalid), "", rememberapp.TerminalErrorConfigurationInvalid},
		{"idempotency_conflict", "", rememberapp.TerminalErrorIdempotencyConflict},
		{"embedding_unavailable", "", rememberapp.TerminalErrorEmbeddingUnavailable},
		{"embedding_response_invalid", "", rememberapp.TerminalErrorEmbeddingResponseInvalid},
		{"commit_conflict", "", rememberapp.TerminalErrorCommitConflict},
		{string(rememberapp.SubmissionErrorDatabaseFailure), "", rememberapp.TerminalErrorDatabaseFailure},
		{"request_timeout", "", rememberapp.TerminalErrorRequestTimeout},
		{"request_cancelled", "", rememberapp.TerminalErrorRequestCancelled},
		{string(rememberapp.SubmissionErrorQuarantined), "", rememberapp.TerminalErrorQuarantined},
		{"unknown", string(rememberapp.TerminalProcessingRejected), rememberapp.TerminalErrorNoSupportedMemory},
		{"unknown", string(rememberapp.TerminalProcessingQuarantined), rememberapp.TerminalErrorQuarantined},
		{"unknown", string(rememberapp.TerminalProcessingFailed), rememberapp.TerminalErrorInternalFailure},
	} {
		require.Equal(t, test.want, terminalErrorCodeFromStatus(test.raw, test.state), test.raw)
	}
	for _, test := range []struct {
		err  error
		want rememberapp.TerminalErrorCode
	}{
		{memoryservice.ErrRememberConflict, rememberapp.TerminalErrorIdempotencyConflict},
		{rememberapp.ErrRememberConflict, rememberapp.TerminalErrorIdempotencyConflict},
		{context.DeadlineExceeded, rememberapp.TerminalErrorRequestTimeout},
		{context.Canceled, rememberapp.TerminalErrorRequestCancelled},
		{rememberapp.ErrEvidenceSecurityRejected, rememberapp.TerminalErrorQuarantined},
		{rememberapp.ErrRememberPersistence, rememberapp.TerminalErrorDatabaseFailure},
		{errors.New("unknown"), rememberapp.TerminalErrorInternalFailure},
	} {
		require.Equal(t, test.want, terminalRememberErrorCode(test.err))
	}
}

func TestTerminalCorrectionToolResultErrorMapsBoundedFailures(t *testing.T) {
	ctx := correlation.WithID(context.Background(), "correction-correlation")
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"not found", httperr.New(httperr.NOT_FOUND, "hidden"), string(rememberapp.SubmissionErrorEntityNotFound)},
		{"conflict", httperr.New(httperr.CONFLICT, "hidden"), string(rememberapp.SubmissionErrorRelationshipChanged)},
		{"idempotency conflict", httperr.NewWithDetails(httperr.CONFLICT, "hidden", []httperr.ErrorDetail{{Field: "reason", Message: "idempotency_conflict"}}), string(rememberapp.TerminalErrorIdempotencyConflict)},
		{"confirmation expired", httperr.NewWithDetails(httperr.CONFLICT, "hidden", []httperr.ErrorDetail{{Field: "reason", Message: string(rememberapp.SubmissionErrorConfirmationExpired)}}), string(rememberapp.SubmissionErrorConfirmationExpired)},
		{"embedding unavailable", httperr.New(httperr.ErrEmbeddingUnavailable, "hidden"), string(rememberapp.TerminalErrorEmbeddingUnavailable)},
		{"embedding response invalid", httperr.New(httperr.ErrEmbeddingResponseInvalid, "hidden"), string(rememberapp.TerminalErrorEmbeddingResponseInvalid)},
		{"embedding timeout", httperr.New(httperr.ErrEmbeddingTimeout, "hidden"), string(rememberapp.TerminalErrorRequestTimeout)},
		{"deadline", context.DeadlineExceeded, string(rememberapp.TerminalErrorRequestTimeout)},
		{"cancelled", context.Canceled, string(rememberapp.TerminalErrorRequestCancelled)},
		{"generic", errors.New("database details"), string(rememberapp.TerminalErrorDatabaseFailure)},
	} {
		t.Run(test.name, func(t *testing.T) {
			structured, ok := ToolResultFromError(terminalCorrectionToolResultError(ctx, "submission-1", test.err, ContractVersionV261))
			require.True(t, ok)
			require.Equal(t, test.want, structured.Result["errors"].([]any)[0].(map[string]any)["code"])
			require.Equal(t, ContractVersionV261, structured.Result["contract_version"])
			if test.name == "confirmation expired" {
				require.Equal(t, string(rememberapp.TerminalProcessingRejected), structured.Result["processing_state"])
			}
			if test.name == "generic" {
				errorItem := structured.Result["errors"].([]any)[0].(map[string]any)
				require.Equal(t, string(rememberapp.TerminalNextActionRetryCorrection), errorItem["next_action"])
			}
		})
	}
}

func TestContractCorrectionStatusLookupDoesNotRequireReadScope(t *testing.T) {
	active, err := BuildActive(Dependencies{Lifecycle: &v261LifecycleService{}})
	require.NoError(t, err)
	selected, err := BuildContractV261(active, &v261RememberService{})
	require.NoError(t, err)
	correct, ok := selected.Get(ToolCorrectRelationship)
	require.True(t, ok)
	output, err := correct.Invoke(contractInvokeContext("write"), "team-1", map[string]any{
		"action": "submit", "relationship_id": "relationship-1", "expected_version": 1,
		"patch":    map[string]any{"predicate": map[string]any{"key": "uses"}},
		"supports": []any{map[string]any{"evidence_id": "evidence-1", "start": 0, "end": 4}},
		"reason":   "predicate was resolved incorrectly", "idempotency_key": "correction-write-only",
	})
	require.NoError(t, err)
	require.Equal(t, ContractVersionV261, output["contract_version"])
	require.Equal(t, "completed", output["processing_state"])
	require.NotNil(t, output["correction_result"])
}

func newV261ActiveRegistry(t *testing.T) Registry {
	t.Helper()
	active := New()
	for _, tool := range ContractTools() {
		tool.Visibility = "active"
		tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"marker": "legacy"}, nil
		}
		require.NoError(t, active.Register(tool))
	}
	return active
}

func terminalRememberResultForRequest(req rememberapp.RememberRequest, state string) *rememberapp.TerminalRememberResult {
	evidence := make([]rememberapp.TerminalEvidenceResult, len(req.Evidence))
	for index := range evidence {
		evidence[index] = rememberapp.TerminalEvidenceResult{
			Disposition: "stored", EvidenceID: uuid.NewString(), EvidenceIndex: index,
			SupersededEvidenceIDs: []string{}, SearchState: string(rememberapp.TerminalSearchCurrent),
		}
	}
	relationships := make([]rememberapp.SubmissionRelationshipResult, len(req.RelationshipHints))
	for index, hint := range req.RelationshipHints {
		ref, _ := hint["ref"].(string)
		relationships[index] = rememberapp.SubmissionRelationshipResult{
			RelationshipRef: ref, Disposition: "stored",
			Splits: []rememberapp.SubmissionRelationshipSplit{{SplitIndex: 0, RelationshipID: uuid.NewString(), RelationshipVersion: 1, Status: "active"}},
		}
	}
	result := &rememberapp.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "remember",
		ProcessingState: state, SearchState: string(rememberapp.TerminalSearchCurrent), CorrelationID: "terminal-correlation",
		Evidence: evidence, RelationshipResults: relationships, Errors: []rememberapp.SubmissionStatusError{}, Kind: rememberapp.ResultKindTerminal,
	}
	if state != string(rememberapp.TerminalProcessingCompleted) {
		code := rememberapp.TerminalErrorNoSupportedMemory
		if state == string(rememberapp.TerminalProcessingQuarantined) {
			code = rememberapp.TerminalErrorQuarantined
		}
		result = rememberapp.TerminalResultWithError(result, code).Result
	}
	return result
}

type configurableV261RememberService struct {
	rememberFn func(context.Context, rememberapp.RememberRequest) (*rememberapp.RememberResult, error)
	statusFn   func(context.Context, rememberapp.GetSubmissionStatusRequest) (*rememberapp.SubmissionStatusResult, error)
}

type v261LifecycleService struct{}

func (v261LifecycleService) CorrectRelationship(context.Context, memoryservice.CorrectRelationshipRequest) (*memoryservice.CorrectRelationshipReceipt, error) {
	return &memoryservice.CorrectRelationshipReceipt{
		ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "relationship_correction",
		ProcessingState: "completed", StatusTool: ToolGetSubmissionStatus, CorrelationID: "correction-correlation",
	}, nil
}

func (v261LifecycleService) GetRelationshipCorrectionStatus(_ context.Context, req memoryservice.GetSubmissionStatusRequest) (*memoryservice.SubmissionStatusResult, error) {
	return &memoryservice.SubmissionStatusResult{
		ContractVersion: domain.ContractVersion, SubmissionID: req.SubmissionID, SubmissionKind: "relationship_correction",
		ProcessingState: "completed", SearchState: "current", Errors: []memoryservice.SubmissionStatusError{},
		Degradations: []memoryservice.SubmissionStatusDegradation{}, Evidence: []memoryservice.SubmissionEvidenceStatus{},
		CorrectionResult: &repository.RelationshipCorrectionResult{
			OriginalRelationshipID: uuid.NewString(), OriginalVersion: 1,
			SuccessorRelationshipID: uuid.NewString(), SuccessorVersion: 1, ReusedSuccessor: false,
		},
	}, nil
}

func (v261LifecycleService) RetractEvidence(context.Context, memoryservice.RetractEvidenceRequest) (*memoryservice.RetractEvidenceResult, error) {
	return nil, errors.New("unused")
}

func (s *configurableV261RememberService) Remember(ctx context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	if s.rememberFn == nil {
		return nil, errors.New("remember function is not configured")
	}
	return s.rememberFn(ctx, req)
}

func (s *configurableV261RememberService) GetSubmissionStatus(ctx context.Context, req rememberapp.GetSubmissionStatusRequest) (*rememberapp.SubmissionStatusResult, error) {
	if s.statusFn == nil {
		return nil, errors.New("status function is not configured")
	}
	return s.statusFn(ctx, req)
}
