package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// ContractVersionV261 is the prepared terminal contract version. Production
// remains on domain.ContractVersion until the stopped-service cutover.
const ContractVersionV261 = "dense-mem.v2.6.1"

var contractV261ToolNames = []string{
	ToolRemember,
	ToolRetractEvidence,
	ToolCorrectRelationship,
	ToolRecallMemory,
	ToolTraceMemory,
	ToolSubmitRecallSessionFeedback,
	ToolListDreams,
	ToolGetDream,
	ToolResolveDreamFeedback,
	ToolExportMemoryPack,
}

// ContractV261ToolNames returns the frozen ten-tool target catalog names.
func ContractV261ToolNames() []string {
	return append([]string(nil), contractV261ToolNames...)
}

// ContractV261Tools returns metadata for the inactive terminal catalog. The
// returned tools have no invokers until an explicit test-only composition seam
// installs them.
func ContractV261Tools() []Tool {
	byName := make(map[string]Tool, len(contractToolNames))
	for _, tool := range ContractTools() {
		byName[tool.Name] = tool
	}
	tools := make([]Tool, 0, len(contractV261ToolNames))
	for _, name := range contractV261ToolNames {
		tool, ok := byName[name]
		if !ok {
			continue
		}
		tool.Visibility = domain.ToolVisibility
		switch name {
		case ToolRemember:
			tool.Description = "Submit exact evidence and relationship proposals for one synchronous atomic semantic commit; every submitted evidence item must be cited by evidence_index. The server and assessor own exact grounding. Use exactly one object shape: {\"object\":{\"entity\":{\"name\":\"PostgreSQL\",\"entity_kind\":\"product\"}}} or {\"object\":{\"value\":{\"type\":\"string\",\"value\":\"PostgreSQL\"}}}."
			tool.OutputSchema = terminalRememberOutputSchema(ContractVersionV261)
		case ToolCorrectRelationship:
			tool.OutputSchema = terminalCorrectionOutputSchema(ContractVersionV261)
		}
		tools = append(tools, tool)
	}
	return tools
}

// WithTerminalRemember replaces only Remember in an active catalog. It is the
// transitional E2E seam used by T02 and retains the v2.6 status tool.
func WithTerminalRemember(active Registry, remember rememberapp.Service) (Registry, error) {
	if active == nil {
		return nil, errors.New("terminal Remember registry requires an active catalog")
	}
	if remember == nil {
		return nil, errors.New("terminal Remember registry requires a service")
	}
	legacy, ok := active.Get(ToolRemember)
	if !ok {
		return nil, fmt.Errorf("terminal Remember registry has no %q tool", ToolRemember)
	}
	replacement := legacy
	replacement.OutputSchema = terminalRememberOutputSchema(domain.ContractVersion)
	replacement.Invoke = terminalRememberInvoker(legacy, remember, domain.ContractVersion, false)
	return cloneContractRegistry(active, replacement, false)
}

// BuildContractV261 installs the inactive ten-tool terminal catalog over an
// active registry. It is intentionally not called by production BuildActive.
func BuildContractV261(active Registry, remember rememberapp.Service) (Registry, error) {
	if active == nil {
		return nil, errors.New("v2.6.1 registry requires an active catalog")
	}
	if remember == nil {
		return nil, errors.New("v2.6.1 registry requires a Remember service")
	}
	if _, ok := active.Get(ToolGetSubmissionStatus); !ok {
		return nil, fmt.Errorf("v2.6.1 registry requires the legacy %q invoker", ToolGetSubmissionStatus)
	}
	metadata := make(map[string]Tool, len(contractV261ToolNames))
	for _, tool := range ContractV261Tools() {
		metadata[tool.Name] = tool
	}
	for _, name := range contractV261ToolNames {
		if _, ok := active.Get(name); !ok {
			return nil, fmt.Errorf("v2.6.1 registry is missing %q", name)
		}
	}

	status, _ := active.Get(ToolGetSubmissionStatus)
	cloned := New()
	for _, name := range contractV261ToolNames {
		source, ok := active.Get(name)
		if !ok {
			return nil, fmt.Errorf("v2.6.1 registry is missing %q", name)
		}
		target := metadata[name]
		target.Invoke = source.Invoke
		target.Visibility = source.Visibility
		switch name {
		case ToolRemember:
			target.Invoke = terminalRememberInvoker(source, remember, ContractVersionV261, true)
		case ToolCorrectRelationship:
			target.Invoke = terminalCorrectionInvoker(source, status, ContractVersionV261)
		}
		if err := cloned.Register(target); err != nil {
			return nil, fmt.Errorf("v2.6.1 registry: %w", err)
		}
	}
	return cloned, nil
}

func cloneContractRegistry(active Registry, replacement Tool, omitStatus bool) (Registry, error) {
	cloned := New()
	replaced := false
	for _, tool := range active.List() {
		if omitStatus && tool.Name == ToolGetSubmissionStatus {
			continue
		}
		if tool.Name == replacement.Name {
			tool = replacement
			replaced = true
		}
		if err := cloned.Register(tool); err != nil {
			return nil, fmt.Errorf("clone contract registry: %w", err)
		}
	}
	if !replaced {
		return nil, fmt.Errorf("clone contract registry: tool %q is not registered", replacement.Name)
	}
	return cloned, nil
}

func terminalRememberInvoker(legacy Tool, remember rememberapp.Service, version string, structuredFallback bool) ToolInvoker {
	return func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		if err := ValidateContractInput(legacy, input, authenticatedScopes(ctx)); err != nil {
			return nil, fmt.Errorf("remember: invalid input: %w", err)
		}
		if err := validateTerminalRememberSupersessions(input); err != nil {
			return nil, err
		}
		req, err := terminalRememberRequest(input)
		if err != nil {
			return nil, fmt.Errorf("remember: invalid input: %w", err)
		}
		result, err := remember.Remember(ctx, req)
		if err != nil {
			var processErr *rememberapp.RememberProcessError
			if errors.As(err, &processErr) && processErr.Result != nil {
				result = &rememberapp.RememberResult{Terminal: processErr.Result}
			} else if structuredFallback {
				return nil, terminalRememberToolResultError(ctx, input, err, version)
			} else {
				return nil, err
			}
		}
		if result == nil || result.Terminal == nil {
			if structuredFallback {
				return nil, terminalRememberToolResultError(ctx, input, errors.New("remember: terminal result is required"), version)
			}
			return nil, errors.New("remember: terminal result is required")
		}
		if err := rememberapp.ValidateTerminalRememberResult(result.Terminal, len(req.Evidence), terminalRelationshipRefs(input)); err != nil {
			if structuredFallback {
				return nil, terminalRememberToolResultError(ctx, input, err, version)
			}
			return nil, fmt.Errorf("remember: invalid terminal result")
		}
		output, err := terminalRememberResultMap(result.Terminal, version)
		if err != nil {
			if structuredFallback {
				return nil, terminalRememberToolResultError(ctx, input, err, version)
			}
			return nil, err
		}
		if result.Terminal.ProcessingState != string(rememberapp.TerminalProcessingCompleted) {
			return nil, NewToolResultError(output)
		}
		return output, nil
	}
}

func terminalCorrectionInvoker(legacy Tool, status Tool, version string) ToolInvoker {
	return func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
		if err := ValidateContractInput(legacy, input, authenticatedScopes(ctx)); err != nil {
			return nil, fmt.Errorf("correct_relationship: invalid input: %w", err)
		}
		if legacy.Invoke == nil || status.Invoke == nil {
			return nil, terminalCorrectionToolResultError(ctx, stringInput(input["submission_id"]), errors.New("correction invoker is unavailable"), version)
		}
		receipt, err := legacy.Invoke(ctx, teamID, input)
		if err != nil {
			return nil, terminalCorrectionToolResultError(ctx, stringInput(input["submission_id"]), err, version)
		}
		submissionID := strings.TrimSpace(stringInput(receipt["submission_id"]))
		if submissionID == "" {
			return nil, terminalCorrectionToolResultError(ctx, "", errors.New("correction receipt missing submission_id"), version)
		}
		statusOutput, err := status.Invoke(withInternalSubmissionStatusLookup(ctx), teamID, map[string]any{"submission_id": submissionID})
		if err != nil {
			return nil, terminalCorrectionStatusReadError(ctx, submissionID, version)
		}
		output, err := terminalCorrectionResultMap(receipt, statusOutput, version)
		if err != nil {
			return nil, terminalCorrectionToolResultError(ctx, submissionID, err, version)
		}
		if err := validateTerminalCorrectionOutput(output, version); err != nil {
			return nil, terminalCorrectionToolResultError(ctx, submissionID, err, version)
		}
		state := stringInput(output["processing_state"])
		if state == "rejected" || state == "failed" {
			return nil, NewToolResultError(output)
		}
		return output, nil
	}
}

func terminalRememberRequest(input map[string]any) (rememberapp.RememberRequest, error) {
	copyInput := make(map[string]any, len(input)+1)
	for key, value := range input {
		copyInput[key] = value
	}
	copyInput["relationship_hints"] = input["relationships"]
	encoded, err := json.Marshal(copyInput)
	if err != nil {
		return rememberapp.RememberRequest{}, err
	}
	var request rememberapp.RememberRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return rememberapp.RememberRequest{}, err
	}
	return request, nil
}

func terminalRelationshipRefs(input map[string]any) []string {
	items, _ := input["relationships"].([]any)
	refs := make([]string, 0, len(items))
	for _, value := range items {
		fields, _ := value.(map[string]any)
		refs = append(refs, strings.TrimSpace(stringInput(fields["ref"])))
	}
	return refs
}

func terminalRememberResultMap(result *rememberapp.TerminalRememberResult, version string) (map[string]any, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	output := map[string]any{}
	if err := json.Unmarshal(encoded, &output); err != nil {
		return nil, err
	}
	output["contract_version"] = version
	if err := ValidateInput(Tool{InputSchema: terminalRememberOutputSchema(version)}, output); err != nil {
		return nil, fmt.Errorf("remember: terminal output validation failed: %w", err)
	}
	return output, nil
}

func terminalCorrectionResultMap(receipt, status map[string]any, version string) (map[string]any, error) {
	submissionID := firstNonEmptyString(stringInput(status["submission_id"]), stringInput(receipt["submission_id"]))
	correlationID := firstNonEmptyString(stringInput(receipt["correlation_id"]), stringInput(status["correlation_id"]))
	searchState := firstNonEmptyString(stringInput(status["search_state"]), string(domain.SearchProjectionNotRequired))
	processingState := firstNonEmptyString(stringInput(status["processing_state"]), stringInput(receipt["processing_state"]))
	output := map[string]any{
		"contract_version": version,
		"submission_id":    submissionID,
		"submission_kind":  "relationship_correction",
		"processing_state": processingState,
		"search_state":     searchState,
		"correlation_id":   correlationID,
		"errors":           normalizeArray(status["errors"]),
	}
	if value, ok := status["awaiting_confirmation"]; ok && value != nil {
		output["awaiting_confirmation"] = value
	}
	if value, ok := status["correction_result"]; ok && value != nil {
		output["correction_result"] = value
	}
	return output, nil
}

func validateTerminalCorrectionOutput(output map[string]any, version string) error {
	if err := ValidateInput(Tool{InputSchema: terminalCorrectionOutputSchema(version)}, output); err != nil {
		return err
	}
	state := stringInput(output["processing_state"])
	if state == "awaiting_confirmation" && output["awaiting_confirmation"] == nil {
		return errors.New("correction awaiting_confirmation result is missing confirmation details")
	}
	if state == "completed" && output["correction_result"] == nil {
		return errors.New("completed correction result is missing correction details")
	}
	if (state == "rejected" || state == "failed") && len(normalizeArray(output["errors"])) == 0 {
		return errors.New("rejected correction result is missing errors")
	}
	return nil
}

func normalizeArray(value any) []any {
	if value == nil {
		return []any{}
	}
	if values, ok := value.([]any); ok {
		return values
	}
	if values, ok := value.([]map[string]any); ok {
		result := make([]any, 0, len(values))
		for _, item := range values {
			result = append(result, item)
		}
		return result
	}
	if values, ok := value.([]rememberapp.SubmissionStatusError); ok {
		result := make([]any, 0, len(values))
		for _, item := range values {
			result = append(result, map[string]any{
				"code": item.Code, "message": item.Message, "retryable": item.Retryable,
				"next_action": item.NextAction, "remediation": item.Remediation,
			})
		}
		return result
	}
	return []any{}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validateTerminalRememberSupersessions(input map[string]any) error {
	evidence, _ := input["evidence"].([]any)
	seen := make(map[uuid.UUID]int)
	for evidenceIndex, rawEvidence := range evidence {
		item, _ := rawEvidence.(map[string]any)
		targets, _ := item["supersedes_evidence_ids"].([]any)
		for targetIndex, rawTarget := range targets {
			target, _ := rawTarget.(string)
			parsed, err := uuid.Parse(strings.TrimSpace(target))
			if err != nil {
				return &ContractValidationFailure{Result: ContractValidationResult{Issues: []ContractValidationIssue{{
					Path: fmt.Sprintf("/evidence/%d/supersedes_evidence_ids/%d", evidenceIndex, targetIndex), Code: "format",
					Message: fmt.Sprintf("evidence[%d].supersedes_evidence_ids[%d]: target must be a UUID", evidenceIndex, targetIndex),
				}}}}
			}
			if previous, exists := seen[parsed]; exists {
				return &ContractValidationFailure{Result: ContractValidationResult{Issues: []ContractValidationIssue{{
					Path: fmt.Sprintf("/evidence/%d/supersedes_evidence_ids", evidenceIndex), Code: "duplicate",
					Message: fmt.Sprintf("evidence[%d].supersedes_evidence_ids: duplicates target from evidence[%d]", evidenceIndex, previous),
				}}}}
			}
			seen[parsed] = evidenceIndex
		}
	}
	return nil
}

func terminalRememberToolResultError(ctx context.Context, input map[string]any, err error, version string) error {
	refs := terminalRelationshipRefs(input)
	evidenceCount := terminalEvidenceCount(input)
	base, _ := terminalRememberFailureBase(ctx, err, evidenceCount, refs)
	output, mapErr := terminalRememberResultMap(base, version)
	if mapErr != nil {
		return mapErr
	}
	return NewToolResultError(output)
}

func terminalEvidenceCount(input map[string]any) int {
	evidence, _ := input["evidence"].([]any)
	return len(evidence)
}

func terminalRememberFailureBase(ctx context.Context, err error, evidenceCount int, refs []string) (*rememberapp.TerminalRememberResult, rememberapp.TerminalErrorCode) {
	code := terminalRememberErrorCode(err)
	base := &rememberapp.TerminalRememberResult{
		ContractVersion:     domain.ContractVersion,
		SubmissionID:        uuid.NewString(),
		SubmissionKind:      "remember",
		ProcessingState:     string(rememberapp.TerminalProcessingFailed),
		SearchState:         string(rememberapp.TerminalSearchNotRequired),
		CorrelationID:       correlation.FromContext(ctx),
		Evidence:            make([]rememberapp.TerminalEvidenceResult, evidenceCount),
		RelationshipResults: make([]rememberapp.SubmissionRelationshipResult, len(refs)),
		Errors:              []rememberapp.SubmissionStatusError{},
		Kind:                rememberapp.ResultKindTerminal,
	}
	for index := range base.Evidence {
		base.Evidence[index] = rememberapp.TerminalEvidenceResult{
			Disposition: "not_stored", EvidenceIndex: index, SupersededEvidenceIDs: []string{},
			SearchState: string(rememberapp.TerminalSearchNotRequired), Reason: "internal_failure",
		}
	}
	for index, ref := range refs {
		base.RelationshipResults[index] = rememberapp.SubmissionRelationshipResult{
			RelationshipRef: ref, Disposition: "not_stored", Reason: "internal_failure",
			Splits: []rememberapp.SubmissionRelationshipSplit{},
		}
	}

	var processErr *rememberapp.RememberProcessError
	if errors.As(err, &processErr) && processErr != nil {
		if processErr.Result != nil {
			base = processErr.Result
		}
		if processErr.Status != nil {
			status := processErr.Status
			if strings.TrimSpace(status.SubmissionID) != "" {
				base.SubmissionID = strings.TrimSpace(status.SubmissionID)
			}
			if strings.TrimSpace(status.CorrelationID) != "" {
				base.CorrelationID = strings.TrimSpace(status.CorrelationID)
			}
			if status.ProcessingState != "" {
				base.ProcessingState = status.ProcessingState
			}
			if len(status.Errors) > 0 {
				code = terminalErrorCodeFromStatus(status.Errors[0].Code, status.ProcessingState)
			}
		}
	}
	if strings.TrimSpace(base.SubmissionID) == "" {
		base.SubmissionID = uuid.NewString()
	}
	if strings.TrimSpace(base.CorrelationID) == "" {
		base.CorrelationID = firstNonEmptyString(correlation.FromContext(ctx), uuid.NewString())
	}
	base.ContractVersion = domain.ContractVersion
	base.SubmissionKind = "remember"
	base.Kind = rememberapp.ResultKindTerminal
	if base.Evidence == nil || len(base.Evidence) != evidenceCount || base.RelationshipResults == nil || len(base.RelationshipResults) != len(refs) || len(base.Errors) == 0 || rememberapp.ValidateTerminalRememberResult(base, evidenceCount, refs) != nil {
		failure := rememberapp.TerminalResultWithError(base, code)
		base = failure.Result
	}
	return base, code
}

func terminalErrorCodeFromStatus(rawCode, state string) rememberapp.TerminalErrorCode {
	code := rememberapp.TerminalErrorCode(strings.TrimSpace(rawCode))
	if rememberapp.IsTerminalErrorCode(code) {
		return code
	}
	switch strings.TrimSpace(rawCode) {
	case string(rememberapp.SubmissionErrorNoSupportedMemory):
		return rememberapp.TerminalErrorNoSupportedMemory
	case string(rememberapp.SubmissionErrorStaleInput):
		return rememberapp.TerminalErrorStaleInput
	case string(rememberapp.SubmissionErrorProviderUnavailable):
		return rememberapp.TerminalErrorProviderUnavailable
	case string(rememberapp.SubmissionErrorProviderResponseInvalid):
		return rememberapp.TerminalErrorProviderResponseInvalid
	case string(rememberapp.SubmissionErrorInputBudgetExceeded):
		return rememberapp.TerminalErrorInputBudgetExceeded
	case string(rememberapp.SubmissionErrorConfigurationInvalid):
		return rememberapp.TerminalErrorConfigurationInvalid
	case "idempotency_conflict":
		return rememberapp.TerminalErrorIdempotencyConflict
	case "embedding_unavailable":
		return rememberapp.TerminalErrorEmbeddingUnavailable
	case "embedding_response_invalid":
		return rememberapp.TerminalErrorEmbeddingResponseInvalid
	case "commit_conflict":
		return rememberapp.TerminalErrorCommitConflict
	case string(rememberapp.SubmissionErrorDatabaseFailure):
		return rememberapp.TerminalErrorDatabaseFailure
	case "request_timeout":
		return rememberapp.TerminalErrorRequestTimeout
	case "request_cancelled":
		return rememberapp.TerminalErrorRequestCancelled
	case string(rememberapp.SubmissionErrorQuarantined):
		return rememberapp.TerminalErrorQuarantined
	}
	if state == string(rememberapp.TerminalProcessingRejected) {
		return rememberapp.TerminalErrorNoSupportedMemory
	}
	if state == string(rememberapp.TerminalProcessingQuarantined) {
		return rememberapp.TerminalErrorQuarantined
	}
	return rememberapp.TerminalErrorInternalFailure
}

func terminalRememberErrorCode(err error) rememberapp.TerminalErrorCode {
	switch {
	case errors.Is(err, memoryservice.ErrRememberConflict), errors.Is(err, rememberapp.ErrRememberConflict):
		return rememberapp.TerminalErrorIdempotencyConflict
	case errors.Is(err, context.DeadlineExceeded):
		return rememberapp.TerminalErrorRequestTimeout
	case errors.Is(err, context.Canceled):
		return rememberapp.TerminalErrorRequestCancelled
	case errors.Is(err, rememberapp.ErrEvidenceSecurityRejected):
		return rememberapp.TerminalErrorQuarantined
	case errors.Is(err, rememberapp.ErrRememberPersistence):
		return rememberapp.TerminalErrorDatabaseFailure
	default:
		return rememberapp.TerminalErrorInternalFailure
	}
}

func terminalCorrectionToolResultError(ctx context.Context, submissionID string, err error, version string) error {
	return terminalCorrectionToolResultErrorWithOptions(ctx, submissionID, err, version, false)
}

func terminalCorrectionStatusReadError(ctx context.Context, submissionID, version string) error {
	return terminalCorrectionToolResultErrorWithOptions(ctx, submissionID, nil, version, true)
}

func terminalCorrectionToolResultErrorWithOptions(ctx context.Context, submissionID string, err error, version string, retrySameRequest bool) error {
	code := rememberapp.TerminalErrorDatabaseFailure
	processingState := string(rememberapp.TerminalProcessingFailed)
	var statusError rememberapp.SubmissionStatusError
	var apiErr *httperr.APIError
	if !retrySameRequest && errors.As(err, &apiErr) {
		switch apiErr.Code {
		case httperr.NOT_FOUND:
			statusError = rememberapp.StatusError(rememberapp.SubmissionErrorEntityNotFound)
		case httperr.CONFLICT:
			switch {
			case apiErrorDetailEquals(apiErr, "reason", string(rememberapp.TerminalErrorIdempotencyConflict)):
				code = rememberapp.TerminalErrorIdempotencyConflict
				statusError = terminalCorrectionStatusError(code)
			case apiErrorDetailEquals(apiErr, "reason", string(rememberapp.TerminalErrorCommitConflict)):
				code = rememberapp.TerminalErrorCommitConflict
				statusError = rememberapp.TerminalStatusError(code)
			case apiErrorDetailEquals(apiErr, "reason", string(rememberapp.SubmissionErrorConfirmationExpired)):
				statusError = rememberapp.StatusError(rememberapp.SubmissionErrorConfirmationExpired)
				processingState = string(rememberapp.TerminalProcessingRejected)
			default:
				statusError = rememberapp.StatusError(rememberapp.SubmissionErrorRelationshipChanged)
			}
		case httperr.ErrEmbeddingUnavailable:
			code = rememberapp.TerminalErrorEmbeddingUnavailable
		case httperr.ErrEmbeddingResponseInvalid:
			code = rememberapp.TerminalErrorEmbeddingResponseInvalid
		case httperr.ErrEmbeddingTimeout:
			code = rememberapp.TerminalErrorRequestTimeout
		}
	}
	if !retrySameRequest && errors.Is(err, context.DeadlineExceeded) {
		code = rememberapp.TerminalErrorRequestTimeout
		statusError = rememberapp.TerminalStatusError(code)
	} else if !retrySameRequest && errors.Is(err, context.Canceled) {
		code = rememberapp.TerminalErrorRequestCancelled
		statusError = rememberapp.TerminalStatusError(code)
	}
	if statusError.Code == "" {
		statusError = rememberapp.TerminalStatusError(code)
	}
	output := map[string]any{
		"contract_version": version,
		"submission_id":    firstNonEmptyString(submissionID, uuid.NewString()),
		"submission_kind":  "relationship_correction",
		"processing_state": processingState,
		"search_state":     string(rememberapp.TerminalSearchNotRequired),
		"correlation_id":   firstNonEmptyString(correlation.FromContext(ctx), uuid.NewString()),
		"errors": []any{map[string]any{
			"code": statusError.Code, "message": statusError.Message, "retryable": statusError.Retryable,
			"next_action": statusError.NextAction, "remediation": statusError.Remediation,
		}},
	}
	if statusError.Code == string(rememberapp.TerminalErrorDatabaseFailure) && !retrySameRequest {
		output["errors"].([]any)[0].(map[string]any)["next_action"] = string(rememberapp.TerminalNextActionRetryCorrection)
		output["errors"].([]any)[0].(map[string]any)["remediation"] = "Retry correct_relationship with current relationship state and a new idempotency_key."
	}
	return NewToolResultError(output)
}

func terminalCorrectionStatusError(code rememberapp.TerminalErrorCode) rememberapp.SubmissionStatusError {
	status := rememberapp.TerminalStatusError(code)
	if code == rememberapp.TerminalErrorIdempotencyConflict {
		status.NextAction = string(rememberapp.TerminalNextActionRetryCorrection)
		status.Remediation = "Retry correct_relationship with current relationship state and a new idempotency_key."
	}
	return status
}

func apiErrorDetailEquals(apiErr *httperr.APIError, field, value string) bool {
	if apiErr == nil {
		return false
	}
	for _, detail := range apiErr.Details {
		if detail.Field == field && detail.Message == value {
			return true
		}
	}
	return false
}
