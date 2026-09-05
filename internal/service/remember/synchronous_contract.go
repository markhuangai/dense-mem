package remember

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// ResultKind identifies a terminal result for internal transport composition.
type ResultKind string

const (
	ResultKindTerminal ResultKind = "terminal"
)

// SynchronousProcessor is the request-owned boundary for terminal Remember.
type SynchronousProcessor interface {
	ProcessRemember(context.Context, RememberProcessRequest) (*SubmissionStatusResult, error)
}

// RememberProcessError preserves a bounded terminal result alongside an
// operational cause, allowing transports to return correlated structured
// errors without inventing a submission identifier.
type RememberProcessError struct {
	Status *SubmissionStatusResult
	Result *TerminalRememberResult
	Err    error
}

func (e *RememberProcessError) Error() string {
	return "remember: processor failed"
}

func (e *RememberProcessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type RememberProcessRequest struct {
	TeamID                   string
	OwnerProfileID           string
	SpaceID                  string
	SpaceGeneration          int64
	IdempotencyKey           string
	RequestHash              string
	SourceSummary            string
	Proposal                 map[string]any
	Metadata                 map[string]any
	Evidence                 []EvidenceInput
	SecuritySignals          []SubmissionSecurityBatchSignal
	SecuritySignalsTruncated bool
	SecurityRejected         bool
	SecurityRejectionAudit   *SecurityRejectionAuditInput
}

type TerminalRememberResult struct {
	ContractVersion     string                         `json:"contract_version"`
	SubmissionID        string                         `json:"submission_id"`
	SubmissionKind      string                         `json:"submission_kind"`
	ProcessingState     string                         `json:"processing_state"`
	SearchState         string                         `json:"search_state"`
	CorrelationID       string                         `json:"correlation_id"`
	Evidence            []TerminalEvidenceResult       `json:"evidence"`
	RelationshipResults []SubmissionRelationshipResult `json:"relationship_results"`
	Errors              []SubmissionStatusError        `json:"errors"`
	Warnings            []string                       `json:"warnings,omitempty"`
	Kind                ResultKind                     `json:"-"`
}

type TerminalEvidenceResult struct {
	Disposition           string   `json:"disposition"`
	EvidenceID            string   `json:"evidence_id,omitempty"`
	ContentHash           string   `json:"content_hash,omitempty"`
	EvidenceIndex         int      `json:"evidence_index"`
	SupersededEvidenceIDs []string `json:"superseded_evidence_ids"`
	SearchState           string   `json:"search_state"`
	Reason                string   `json:"reason,omitempty"`
}

type TerminalProcessingState string

const (
	TerminalProcessingCompleted TerminalProcessingState = "completed"
	TerminalProcessingFailed    TerminalProcessingState = "failed"
)

type TerminalSearchState string

const (
	TerminalSearchCurrent     TerminalSearchState = "current"
	TerminalSearchNotRequired TerminalSearchState = "not_required"
)

type TerminalErrorCode string

const (
	TerminalErrorPolicyRejected           TerminalErrorCode = "submission_policy_rejected"
	TerminalErrorStaleInput               TerminalErrorCode = "stale_input"
	TerminalErrorProviderUnavailable      TerminalErrorCode = "provider_unavailable"
	TerminalErrorProviderResponseInvalid  TerminalErrorCode = "provider_response_invalid"
	TerminalErrorInputBudgetExceeded      TerminalErrorCode = "input_budget_exceeded"
	TerminalErrorConfigurationInvalid     TerminalErrorCode = "configuration_invalid"
	TerminalErrorIdempotencyConflict      TerminalErrorCode = "idempotency_conflict"
	TerminalErrorEmbeddingUnavailable     TerminalErrorCode = "embedding_unavailable"
	TerminalErrorEmbeddingResponseInvalid TerminalErrorCode = "embedding_response_invalid"
	TerminalErrorCommitConflict           TerminalErrorCode = "commit_conflict"
	TerminalErrorDatabaseFailure          TerminalErrorCode = "database_failure"
	TerminalErrorRequestTimeout           TerminalErrorCode = "request_timeout"
	TerminalErrorRequestCancelled         TerminalErrorCode = "request_cancelled"
	TerminalErrorInternalFailure          TerminalErrorCode = "internal_failure"
)

var terminalErrorCodes = []TerminalErrorCode{
	TerminalErrorPolicyRejected,
	TerminalErrorStaleInput,
	TerminalErrorProviderUnavailable,
	TerminalErrorProviderResponseInvalid,
	TerminalErrorInputBudgetExceeded,
	TerminalErrorConfigurationInvalid,
	TerminalErrorIdempotencyConflict,
	TerminalErrorEmbeddingUnavailable,
	TerminalErrorEmbeddingResponseInvalid,
	TerminalErrorCommitConflict,
	TerminalErrorDatabaseFailure,
	TerminalErrorRequestTimeout,
	TerminalErrorRequestCancelled,
	TerminalErrorInternalFailure,
}

const (
	maxTerminalErrors             = 50
	maxTerminalRelationshipSplits = 50
	maxTerminalSupersededEvidence = 50
	maxTerminalCorrelationIDRunes = 128
	maxTerminalRemediationRunes   = 512
)

// NormalizeTerminalCorrelationID returns a bounded correlation identifier for
// terminal Remember and correction results.
func NormalizeTerminalCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxTerminalCorrelationIDRunes {
		return uuid.NewString()
	}
	return value
}

func TerminalErrorCodes() []string {
	result := make([]string, 0, len(terminalErrorCodes))
	for _, code := range terminalErrorCodes {
		result = append(result, string(code))
	}
	return result
}

type TerminalNextAction string

const (
	TerminalNextActionRetrySameRequest   TerminalNextAction = "retry_same_request"
	TerminalNextActionResubmitRemember   TerminalNextAction = "resubmit_remember"
	TerminalNextActionRetryDreamFeedback TerminalNextAction = "retry_dream_feedback"
	TerminalNextActionRetryCorrection    TerminalNextAction = "retry_correction"
	TerminalNextActionContactOperator    TerminalNextAction = "contact_operator"
	TerminalNextActionNone               TerminalNextAction = "none"
)

var terminalNextActions = []TerminalNextAction{
	TerminalNextActionRetrySameRequest,
	TerminalNextActionResubmitRemember,
	TerminalNextActionRetryDreamFeedback,
	TerminalNextActionRetryCorrection,
	TerminalNextActionContactOperator,
	TerminalNextActionNone,
}

func TerminalNextActions() []string {
	result := make([]string, 0, len(terminalNextActions))
	for _, action := range terminalNextActions {
		result = append(result, string(action))
	}
	return result
}

func TerminalStatusError(code TerminalErrorCode) SubmissionStatusError {
	code = normalizeTerminalErrorCode(code)
	retryable, action := terminalErrorGuidance(code)
	return SubmissionStatusError{
		Code:        string(code),
		Message:     terminalErrorMessage(code),
		Retryable:   retryable,
		NextAction:  string(action),
		Remediation: terminalErrorRemediation(action),
	}
}

// TerminalStatusErrorWithDetails preserves canonical code guidance, except
// that a server-owned input budget directs the caller to an operator, while
// attaching bounded, safe cause information from the owning service.
func TerminalStatusErrorWithDetails(code TerminalErrorCode, reasonCode string, details map[string]any) SubmissionStatusError {
	base := TerminalStatusError(code)
	base.ReasonCode = boundedStatusErrorText(reasonCode, 128)
	base.Details = boundedStatusErrorDetails(details)
	applyServerOwnedInputBudgetGuidance(base.Code, &base)
	return base
}

func normalizeTerminalErrorCode(code TerminalErrorCode) TerminalErrorCode {
	for _, known := range terminalErrorCodes {
		if code == known {
			return code
		}
	}
	return TerminalErrorInternalFailure
}

// IsTerminalErrorCode reports whether code belongs to the closed terminal
// Remember error vocabulary.
func IsTerminalErrorCode(code TerminalErrorCode) bool {
	return normalizeTerminalErrorCode(code) == code
}

// IsTerminalNextAction reports whether action belongs to the closed terminal
// Remember remediation vocabulary.
func IsTerminalNextAction(action TerminalNextAction) bool {
	for _, known := range terminalNextActions {
		if action == known {
			return true
		}
	}
	return false
}

// ValidateTerminalStatusError checks the public code, action, and bounded
// message fields before they cross a synchronous transport boundary.
func ValidateTerminalStatusError(value SubmissionStatusError) error {
	code := TerminalErrorCode(strings.TrimSpace(value.Code))
	if value.Code != string(code) || !IsTerminalErrorCode(code) {
		return fmt.Errorf("remember: terminal error code %q is not allowed", value.Code)
	}
	if !IsTerminalNextAction(TerminalNextAction(value.NextAction)) {
		return fmt.Errorf("remember: terminal next action %q is not allowed", value.NextAction)
	}
	retryable, action := terminalErrorGuidance(code)
	serverOwnedInputBudget := code == TerminalErrorInputBudgetExceeded && statusErrorServerOwned(value.Details)
	if serverOwnedInputBudget {
		action = TerminalNextActionContactOperator
	}
	if value.NextAction == string(TerminalNextActionRetryDreamFeedback) {
		if !retryable || action != TerminalNextActionResubmitRemember || !value.Retryable ||
			!validDreamFeedbackRetryRemediation(value.Remediation) {
			return fmt.Errorf("remember: terminal error guidance for %q is inconsistent", code)
		}
	} else if value.Retryable != retryable || value.NextAction != string(action) {
		return fmt.Errorf("remember: terminal error guidance for %q is inconsistent", code)
	}
	canonical := TerminalStatusError(code)
	if value.Message != canonical.Message {
		return fmt.Errorf("remember: terminal error message for %q is not canonical", code)
	}
	if value.NextAction != string(TerminalNextActionRetryDreamFeedback) {
		if serverOwnedInputBudget {
			if value.Remediation != serverOwnedInputBudgetRemediation {
				return fmt.Errorf("remember: terminal error remediation for %q is not server-owned budget guidance", code)
			}
		} else if value.Remediation != canonical.Remediation {
			return fmt.Errorf("remember: terminal error remediation for %q is not canonical", code)
		}
	}
	if value.ReasonCode != boundedStatusErrorText(value.ReasonCode, 128) {
		return fmt.Errorf("remember: terminal error reason_code is not bounded")
	}
	if value.ReasonCode != "" {
		if value.Details == nil {
			return fmt.Errorf("remember: terminal error details are required with reason_code")
		}
		if !reflect.DeepEqual(value.Details, boundedStatusErrorDetails(value.Details)) {
			return fmt.Errorf("remember: terminal error details are not bounded")
		}
	}
	return nil
}

const terminalDreamFeedbackRetryRemediationPrefix = "Retry resolve_dream_feedback with corrected evidence and idempotency_key "

func DreamFeedbackRetryRemediation(idempotencyKey string) string {
	return fmt.Sprintf("%s%q.", terminalDreamFeedbackRetryRemediationPrefix, idempotencyKey)
}

func validDreamFeedbackRetryRemediation(value string) bool {
	if utf8.RuneCountInString(value) > maxTerminalRemediationRunes ||
		!strings.HasPrefix(value, terminalDreamFeedbackRetryRemediationPrefix) ||
		!strings.HasSuffix(value, ".") {
		return false
	}
	quotedKey := strings.TrimSuffix(strings.TrimPrefix(value, terminalDreamFeedbackRetryRemediationPrefix), ".")
	key, err := strconv.Unquote(quotedKey)
	return err == nil && strings.TrimSpace(key) != "" &&
		utf8.RuneCountInString(key) <= maxTerminalCorrelationIDRunes &&
		DreamFeedbackRetryRemediation(key) == value
}

func terminalErrorGuidance(code TerminalErrorCode) (bool, TerminalNextAction) {
	switch code {
	case TerminalErrorProviderUnavailable, TerminalErrorProviderResponseInvalid,
		TerminalErrorEmbeddingUnavailable, TerminalErrorEmbeddingResponseInvalid,
		TerminalErrorCommitConflict, TerminalErrorDatabaseFailure,
		TerminalErrorRequestTimeout, TerminalErrorRequestCancelled,
		TerminalErrorInternalFailure:
		return true, TerminalNextActionRetrySameRequest
	case TerminalErrorPolicyRejected, TerminalErrorStaleInput,
		TerminalErrorIdempotencyConflict, TerminalErrorInputBudgetExceeded:
		return false, TerminalNextActionResubmitRemember
	case TerminalErrorConfigurationInvalid:
		return false, TerminalNextActionContactOperator
	default:
		return false, TerminalNextActionContactOperator
	}
}

func terminalErrorMessage(code TerminalErrorCode) string {
	switch code {
	case TerminalErrorPolicyRejected:
		return "submission was rejected by semantic policy"
	case TerminalErrorStaleInput:
		return "an exact client-owned input changed before commit"
	case TerminalErrorProviderUnavailable:
		return "the semantic assessor was unavailable"
	case TerminalErrorProviderResponseInvalid:
		return "the semantic assessor returned an invalid response"
	case TerminalErrorInputBudgetExceeded:
		return "the semantic assessor input exceeded the configured budget"
	case TerminalErrorConfigurationInvalid:
		return "Dense-Mem provider or search configuration is invalid"
	case TerminalErrorIdempotencyConflict:
		return "the idempotency key is already bound to a different request"
	case TerminalErrorEmbeddingUnavailable:
		return "the embedding provider was unavailable"
	case TerminalErrorEmbeddingResponseInvalid:
		return "the embedding provider returned an invalid response"
	case TerminalErrorCommitConflict:
		return "server-owned state changed before commit"
	case TerminalErrorDatabaseFailure:
		return "Dense-Mem could not persist the submission"
	case TerminalErrorRequestTimeout:
		return "the bounded Remember request deadline was reached"
	case TerminalErrorRequestCancelled:
		return "the Remember request was cancelled before commit"
	default:
		return "Dense-Mem could not complete the submission"
	}
}

func terminalErrorRemediation(action TerminalNextAction) string {
	switch action {
	case TerminalNextActionRetrySameRequest:
		return "Retry the same request with the same idempotency_key after the transient failure clears."
	case TerminalNextActionResubmitRemember:
		return "Submit the complete batch again with remember and a new idempotency_key after correcting the input."
	case TerminalNextActionRetryCorrection:
		return "Retry correct_relationship with current relationship state and a new idempotency_key."
	case TerminalNextActionNone:
		return "No action is required."
	default:
		return "Contact an operator with submission_id and correlation_id."
	}
}

func ValidateTerminalRememberResult(result *TerminalRememberResult, evidenceCount int, relationshipRefs []string) error {
	if result == nil {
		return errors.New("remember: terminal result is required")
	}
	if result.Kind != ResultKindTerminal {
		return fmt.Errorf("remember: result kind %q is not terminal", result.Kind)
	}
	if !domain.ContractVersionCompatible(strings.TrimSpace(result.ContractVersion)) || result.SubmissionKind != "remember" {
		return fmt.Errorf("remember: terminal result identity is invalid")
	}
	if strings.TrimSpace(result.SubmissionID) == "" || result.CorrelationID == "" || result.CorrelationID != strings.TrimSpace(result.CorrelationID) {
		return errors.New("remember: terminal result identity fields are required")
	}
	if utf8.RuneCountInString(result.CorrelationID) > maxTerminalCorrelationIDRunes {
		return errors.New("remember: terminal result correlation_id exceeds maximum length")
	}
	if _, err := uuid.Parse(result.SubmissionID); err != nil {
		return errors.New("remember: terminal result submission_id is invalid")
	}
	if result.ProcessingState != string(TerminalProcessingCompleted) &&
		result.ProcessingState != string(TerminalProcessingFailed) {
		return fmt.Errorf("remember: invalid terminal processing state %q", result.ProcessingState)
	}
	if result.SearchState != string(TerminalSearchCurrent) && result.SearchState != string(TerminalSearchNotRequired) {
		return fmt.Errorf("remember: invalid terminal search state %q", result.SearchState)
	}
	if result.Errors == nil {
		return errors.New("remember: terminal errors are required")
	}
	if result.Evidence == nil {
		return errors.New("remember: terminal evidence is required")
	}
	if len(result.Errors) > maxTerminalErrors {
		return fmt.Errorf("remember: terminal error count %d exceeds limit %d", len(result.Errors), maxTerminalErrors)
	}
	if len(result.Warnings) > 20 {
		return errors.New("remember: terminal warning count exceeds limit")
	}
	for _, warning := range result.Warnings {
		if strings.TrimSpace(warning) == "" || utf8.RuneCountInString(warning) > 512 {
			return errors.New("remember: terminal warning is empty or exceeds maximum length")
		}
	}
	if len(result.Evidence) != evidenceCount {
		return fmt.Errorf("remember: terminal evidence count %d, expected %d", len(result.Evidence), evidenceCount)
	}
	storedResult := false
	seenStoredEvidenceIDs := make(map[uuid.UUID]struct{}, len(result.Evidence))
	seenSupersededEvidenceIDs := make(map[uuid.UUID]struct{})
	for index, item := range result.Evidence {
		if item.EvidenceIndex != index {
			return fmt.Errorf("remember: terminal evidence index %d is out of order", item.EvidenceIndex)
		}
		if item.Disposition != "stored" && item.Disposition != "not_stored" {
			return fmt.Errorf("remember: terminal evidence disposition %q is invalid", item.Disposition)
		}
		if item.SupersededEvidenceIDs == nil {
			return fmt.Errorf("remember: terminal evidence %d superseded evidence is required", index)
		}
		if len(item.SupersededEvidenceIDs) > maxTerminalSupersededEvidence {
			return fmt.Errorf("remember: terminal evidence %d has too many superseded evidence IDs", index)
		}
		if item.Disposition == "stored" && item.EvidenceID == "" {
			return fmt.Errorf("remember: stored evidence %d has no evidence_id", index)
		}
		if item.Disposition == "stored" {
			evidenceID, err := uuid.Parse(item.EvidenceID)
			if err != nil {
				return fmt.Errorf("remember: stored evidence %d has invalid evidence_id", index)
			}
			if _, ok := seenStoredEvidenceIDs[evidenceID]; ok {
				return fmt.Errorf("remember: stored evidence %d has duplicated evidence_id", index)
			}
			if _, ok := seenSupersededEvidenceIDs[evidenceID]; ok {
				return fmt.Errorf("remember: stored evidence %d supersedes itself", index)
			}
			seenStoredEvidenceIDs[evidenceID] = struct{}{}
			if strings.TrimSpace(item.Reason) != "" {
				return fmt.Errorf("remember: stored evidence %d has a reason", index)
			}
			storedResult = true
		}
		if item.Disposition == "not_stored" && item.EvidenceID != "" {
			return fmt.Errorf("remember: non-stored evidence %d has an evidence_id", index)
		}
		if item.Disposition == "not_stored" && len(item.SupersededEvidenceIDs) > 0 {
			return fmt.Errorf("remember: non-stored evidence %d has superseded evidence", index)
		}
		if item.Disposition == "not_stored" && !terminalNotStoredReasonAllowedForResult(result, item.Reason) {
			return fmt.Errorf("remember: non-stored evidence %d has unsupported reason", index)
		}
		for _, supersededID := range item.SupersededEvidenceIDs {
			supersededUUID, err := uuid.Parse(supersededID)
			if err != nil {
				return fmt.Errorf("remember: terminal evidence %d has invalid superseded evidence_id", index)
			}
			if _, ok := seenSupersededEvidenceIDs[supersededUUID]; ok {
				return fmt.Errorf("remember: terminal evidence %d has duplicated superseded evidence_id", index)
			}
			if _, ok := seenStoredEvidenceIDs[supersededUUID]; ok {
				return fmt.Errorf("remember: terminal evidence %d supersedes a stored evidence_id", index)
			}
			seenSupersededEvidenceIDs[supersededUUID] = struct{}{}
		}
		if item.SearchState != string(TerminalSearchCurrent) && item.SearchState != string(TerminalSearchNotRequired) {
			return fmt.Errorf("remember: terminal evidence %d has invalid search state %q", index, item.SearchState)
		}
		if item.Disposition == "stored" && item.SearchState != string(TerminalSearchCurrent) {
			return fmt.Errorf("remember: stored evidence %d must have current search state", index)
		}
		if item.Disposition == "not_stored" && item.SearchState != string(TerminalSearchNotRequired) {
			return fmt.Errorf("remember: non-stored evidence %d must have not_required search state", index)
		}
	}
	seenErrorCodes := make(map[TerminalErrorCode]struct{}, len(result.Errors))
	for index, item := range result.Errors {
		if err := ValidateTerminalStatusError(item); err != nil {
			return fmt.Errorf("remember: terminal error %d: %w", index, err)
		}
		code := TerminalErrorCode(item.Code)
		if _, ok := seenErrorCodes[code]; ok {
			return fmt.Errorf("remember: terminal error %d duplicates code %q", index, item.Code)
		}
		seenErrorCodes[code] = struct{}{}
		if result.ProcessingState != string(terminalProcessingStateForError(code)) {
			return fmt.Errorf("remember: terminal error %d is inconsistent with processing state", index)
		}
	}
	if result.RelationshipResults == nil {
		return errors.New("remember: terminal relationship results are required")
	}
	if len(result.RelationshipResults) != len(relationshipRefs) {
		return fmt.Errorf("remember: terminal relationship count %d, expected %d", len(result.RelationshipResults), len(relationshipRefs))
	}
	seenRefs := make(map[string]struct{}, len(relationshipRefs))
	for index, item := range result.RelationshipResults {
		if item.RelationshipRef != relationshipRefs[index] {
			return fmt.Errorf("remember: terminal relationship ref %q is out of order", item.RelationshipRef)
		}
		if _, ok := seenRefs[item.RelationshipRef]; ok {
			return fmt.Errorf("remember: terminal relationship ref %q is duplicated", item.RelationshipRef)
		}
		seenRefs[item.RelationshipRef] = struct{}{}
		if item.Disposition != "stored" && item.Disposition != "not_stored" {
			return fmt.Errorf("remember: terminal relationship disposition %q is invalid", item.Disposition)
		}
		if item.Splits == nil {
			return fmt.Errorf("remember: terminal relationship %q splits are required", item.RelationshipRef)
		}
		if len(item.Splits) > maxTerminalRelationshipSplits {
			return fmt.Errorf("remember: terminal relationship %q has too many splits", item.RelationshipRef)
		}
		if item.Disposition == "not_stored" && len(item.Splits) > 0 {
			return fmt.Errorf("remember: non-stored relationship %q has splits", item.RelationshipRef)
		}
		if item.Disposition == "not_stored" && !terminalNotStoredReasonAllowedForResult(result, item.Reason) {
			return fmt.Errorf("remember: non-stored relationship %q has unsupported reason", item.RelationshipRef)
		}
		if item.Disposition == "stored" {
			if len(item.Splits) == 0 {
				return fmt.Errorf("remember: stored relationship %q has no split", item.RelationshipRef)
			}
			if strings.TrimSpace(item.Reason) != "" {
				return fmt.Errorf("remember: stored relationship %q has a reason", item.RelationshipRef)
			}
			for splitIndex, split := range item.Splits {
				if split.SplitIndex != splitIndex {
					return fmt.Errorf("remember: stored relationship %q split index %d is out of order", item.RelationshipRef, split.SplitIndex)
				}
				if _, err := uuid.Parse(split.RelationshipID); err != nil {
					return fmt.Errorf("remember: stored relationship %q split has invalid relationship_id", item.RelationshipRef)
				}
				if split.RelationshipVersion < 1 || split.Status != "active" {
					return fmt.Errorf("remember: stored relationship %q split is not active and versioned", item.RelationshipRef)
				}
			}
			storedResult = true
		}
	}
	switch result.ProcessingState {
	case string(TerminalProcessingCompleted):
		if len(result.Errors) > 0 {
			return errors.New("remember: completed terminal result cannot contain errors")
		}
		if !storedResult {
			return errors.New("remember: completed terminal result has no stored result")
		}
	case string(TerminalProcessingFailed):
		if len(result.Errors) == 0 {
			return fmt.Errorf("remember: %s terminal result requires an error", result.ProcessingState)
		}
		if storedResult {
			return fmt.Errorf("remember: %s terminal result cannot contain stored results", result.ProcessingState)
		}
	}
	expectedSearchState := TerminalSearchNotRequired
	if storedResult {
		expectedSearchState = TerminalSearchCurrent
	}
	if result.SearchState != string(expectedSearchState) {
		return fmt.Errorf("remember: terminal search state %q is inconsistent with stored results", result.SearchState)
	}
	return nil
}

func terminalNotStoredReasonAllowed(reason string) bool {
	trimmed := strings.TrimSpace(reason)
	if reason != trimmed {
		return false
	}
	switch trimmed {
	case "not_supported_by_evidence", "stale_input", "submission_policy_rejected", "security_quarantine", "internal_failure":
		return true
	default:
		return false
	}
}

func terminalNotStoredReasonAllowedForResult(result *TerminalRememberResult, reason string) bool {
	if result == nil || !terminalNotStoredReasonAllowed(reason) {
		return false
	}
	if result.ProcessingState == string(TerminalProcessingCompleted) {
		return reason == "not_supported_by_evidence"
	}
	for _, item := range result.Errors {
		if reason == terminalNotStoredReasonForError(TerminalErrorCode(item.Code)) {
			return true
		}
	}
	return false
}

// TerminalResultWithError builds a bounded failure projection for transport
// tests and future processor adapters without exposing provider/database text.
func TerminalResultWithError(result *TerminalRememberResult, code TerminalErrorCode) *RememberProcessError {
	if result == nil {
		result = &TerminalRememberResult{Kind: ResultKindTerminal}
	}
	result.Kind = ResultKindTerminal
	result.ProcessingState = string(terminalProcessingStateForError(code))
	result.SearchState = string(TerminalSearchNotRequired)
	notStoredReason := terminalNotStoredReasonForError(code)
	for index := range result.Evidence {
		result.Evidence[index].Disposition = "not_stored"
		result.Evidence[index].EvidenceID = ""
		result.Evidence[index].SupersededEvidenceIDs = []string{}
		result.Evidence[index].SearchState = string(TerminalSearchNotRequired)
		result.Evidence[index].Reason = notStoredReason
	}
	for index := range result.RelationshipResults {
		result.RelationshipResults[index].Disposition = "not_stored"
		result.RelationshipResults[index].Reason = notStoredReason
		result.RelationshipResults[index].Splits = []SubmissionRelationshipSplit{}
	}
	if result.Evidence == nil {
		result.Evidence = []TerminalEvidenceResult{}
	}
	if result.RelationshipResults == nil {
		result.RelationshipResults = []SubmissionRelationshipResult{}
	}
	status := TerminalStatusError(code)
	result.Errors = []SubmissionStatusError{status}
	return &RememberProcessError{Result: result, Err: errors.New(status.Message)}
}

func terminalNotStoredReasonForError(code TerminalErrorCode) string {
	switch normalizeTerminalErrorCode(code) {
	case TerminalErrorPolicyRejected:
		return "submission_policy_rejected"
	case TerminalErrorStaleInput:
		return "stale_input"
	default:
		return "internal_failure"
	}
}

func terminalProcessingStateForError(code TerminalErrorCode) TerminalProcessingState {
	switch normalizeTerminalErrorCode(code) {
	case TerminalErrorPolicyRejected, TerminalErrorStaleInput:
		return TerminalProcessingFailed
	default:
		return TerminalProcessingFailed
	}
}
